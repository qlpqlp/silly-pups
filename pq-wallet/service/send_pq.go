package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reAmountDoge = regexp.MustCompile(`^\d+(\.\d+)?$`)

const dogeEconomicFeePerKBKoinu = int64(1_000_000) // 0.01 DOGE/KB (Dogecoin recommendation)

// extraOutputs counts additional vouts beyond recipient (+ optional change), e.g. OP_RETURN (1) or OP_RETURN+P2SH carrier (2).
func estimateFeeKoinu(inputCount int, includeChange bool, extraOutputs int) int64 {
	if inputCount < 1 {
		inputCount = 1
	}
	outs := 1 // destination
	if includeChange {
		outs++
	}
	if extraOutputs < 0 {
		extraOutputs = 0
	}
	outs += extraOutputs
	// Legacy P2PKH rough size model.
	vbytes := (180 * inputCount) + (34 * outs) + 10
	kb := (vbytes + 999) / 1000 // ceil
	if kb < 1 {
		kb = 1
	}
	return int64(kb) * dogeEconomicFeePerKBKoinu
}

type sendPQSafeBody struct {
	ToAddress           string `json:"to_address"`
	AmountDOGE          string `json:"amount_doge"`
	IncludePQCommitment *bool  `json:"include_pq_commitment"`
}

func (s *Server) scriptPubHexForUTXO(wf *WalletFile, u *ExplorerUTXO) (string, error) {
	if hx := strings.TrimSpace(u.ScriptPubHex); hx != "" {
		return hx, nil
	}
	return s.p2pkhScriptPubKeyHexForSign(wf)
}

func (s *Server) handleSendPQSafe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body sendPQSafeBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	to := strings.TrimSpace(body.ToAddress)
	amt := strings.TrimSpace(body.AmountDOGE)
	includePQCommitment := true
	if body.IncludePQCommitment != nil {
		includePQCommitment = *body.IncludePQCommitment
	}
	if to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to_address required"})
		return
	}
	if amt == "" || !reAmountDoge.MatchString(amt) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "amount_doge must be numeric only (e.g. 0.001 or 10000000000000000.001), no commas or letters"})
		return
	}
	if len(to) < 26 || len(to) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to_address looks invalid"})
		return
	}

	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	pa := wf.PrimaryAddress()
	if pa == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no primary address"})
		return
	}
	testnet := strings.EqualFold(wf.Network, "testnet")

	carrierKoinu := int64(100_000_000) // 1 DOGE — canonical carrier output (libdogecoin default)
	if v := strings.TrimSpace(os.Getenv("PUP_PQ_CARRIER_KOINU")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= dustLimitKoinu {
			carrierKoinu = n
		}
	}
	txRFeeKoinu := int64(2_000_000) // 0.02 DOGE for TX_R relay
	if v := strings.TrimSpace(os.Getenv("PUP_PQ_TXR_FEE_KOINU")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= minRelayFeeKoinu {
			txRFeeKoinu = n
		}
	}

	sendKoinu, err := dogeAmountStringToKoinu(amt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 165*time.Second)
	defer cancel()

	utxos, err := s.fetchUTXOsFromExplorer(ctx, strings.TrimSpace(pa.P2PKH))
	if err != nil || len(utxos) == 0 {
		// Fallback: spend from all wallet addresses, not only primary.
		seen := map[string]struct{}{}
		all := make([]ExplorerUTXO, 0, 16)
		for _, a := range wf.AllDistinctP2PKHAddresses() {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			list, ferr := s.fetchUTXOsFromExplorer(ctx, a)
			if ferr != nil || len(list) == 0 {
				continue
			}
			for _, u := range list {
				k := strings.ToLower(strings.TrimSpace(u.TxID)) + ":" + fmt.Sprintf("%d", u.Vout)
				if _, ok := seen[k]; ok {
					continue
				}
				seen[k] = struct{}{}
				all = append(all, u)
			}
		}
		if len(all) == 0 {
			msg := "could not load spendable UTXOs from SPV wallet (primary or derived addresses)"
			if err != nil {
				msg += ": " + err.Error()
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
		utxos = all
	}

	carrierEnvDisabled := strings.TrimSpace(os.Getenv("PUP_PQ_DISABLE_CARRIER")) != ""
	useCarrierBudget := includePQCommitment && !carrierEnvDisabled &&
		strings.TrimSpace(wf.PQPublicHex) != "" && strings.TrimSpace(wf.PQPrivateHex) != ""

	var selected []ExplorerUTXO
	var sumIn int64
	var fee int64
	var change int64
	var changeOut int64
	var toScript, changeScript []byte
	var unsignedForSign []byte
	var baseHex string
	var pqCommitment32Hex string
	var pqMode string
	var falconSigHex string
	var carrierFlow bool
	econDowngraded := false
	var errUtx error
	var unsignedBase []byte

	for econPass := 0; econPass < 3; econPass++ {
		extraFeeOutputs := 0
		if includePQCommitment {
			extraFeeOutputs = 1
			if useCarrierBudget {
				extraFeeOutputs = 2
			}
		}

		selected = nil
		sumIn = 0
		for attempt := 0; attempt < 12; attempt++ {
			n := len(selected)
			if n == 0 {
				n = 1
			}
			fee = estimateFeeKoinu(n, true, extraFeeOutputs)
			need := sendKoinu + fee
			if extraFeeOutputs >= 2 {
				need += carrierKoinu
			}
			var errPick error
			selected, sumIn, errPick = selectUTXOs(utxos, need)
			if errPick != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": errPick.Error()})
				return
			}
			fee = estimateFeeKoinu(len(selected), true, extraFeeOutputs)
			if sumIn >= need {
				break
			}
			if attempt == 11 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not cover amount plus fee"})
				return
			}
		}
		fee = estimateFeeKoinu(len(selected), true, extraFeeOutputs)
		change = sumIn - sendKoinu - fee
		if extraFeeOutputs >= 2 {
			change -= carrierKoinu
		}
		if change < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "insufficient balance after fee and PQ carrier reserve"})
			return
		}

		var errScr error
		if econPass == 0 {
			toScript, errScr = dogeP2PKHScriptFromAddress(to, testnet)
			if errScr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("destination: %v", errScr)})
				return
			}
			changeScript, errScr = dogeP2PKHScriptFromAddress(strings.TrimSpace(pa.P2PKH), testnet)
			if errScr != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("change script: %v", errScr)})
				return
			}
		}

		changeOut = change
		if change <= dustLimitKoinu {
			changeOut = 0
		}
		unsignedBase, errUtx = buildUnsignedDogeP2PKH(selected, toScript, sendKoinu, changeScript, changeOut, "")
		if errUtx != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": errUtx.Error()})
			return
		}
		baseHex = hexMsgTx(unsignedBase)

		pqCommitment32Hex = ""
		pqMode = "none"
		falconSigHex = ""
		if includePQCommitment && strings.TrimSpace(wf.PQPublicHex) != "" && strings.TrimSpace(wf.PQPrivateHex) != "" && len(selected) > 0 {
			if scr, err := s.scriptPubHexForUTXO(wf, &selected[0]); err == nil {
				if sighashHex, err2 := s.runSuchTxSighash32(baseHex, scr, 0, 1, testnet); err2 == nil {
					if sigHex, err3 := s.runSuchFalconSign(sighashHex, wf.PQPrivateHex, testnet); err3 == nil {
						falconSigHex = strings.TrimSpace(sigHex)
						pubB, pubErr := hex.DecodeString(strings.TrimSpace(wf.PQPublicHex))
						sigB, sigErr := hex.DecodeString(falconSigHex)
						if pubErr == nil && sigErr == nil && len(pubB) > 0 && len(sigB) > 0 {
							buf := make([]byte, 0, len(pubB)+len(sigB))
							buf = append(buf, pubB...)
							buf = append(buf, sigB...)
							h := sha256.Sum256(buf)
							pqCommitment32Hex = hex.EncodeToString(h[:])
							pqMode = "phase1_canonical_falcon"
						}
					}
				}
			}
		}
		if includePQCommitment && pqCommitment32Hex == "" {
			if pubB, err := hex.DecodeString(strings.TrimSpace(wf.PQPublicHex)); err == nil && len(pubB) > 0 {
				h := sha256.Sum256(pubB)
				pqCommitment32Hex = hex.EncodeToString(h[:])
				pqMode = "legacy_pubkey_hash_fallback"
			}
		}

		unsignedForSign, errUtx = buildUnsignedDogeP2PKH(selected, toScript, sendKoinu, changeScript, changeOut, pqCommitment32Hex)
		if errUtx != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": errUtx.Error()})
			return
		}
		carrierFlow = false
		carrierWish := !carrierEnvDisabled &&
			includePQCommitment && pqCommitment32Hex != "" && falconSigHex != "" &&
			pqMode != "legacy_pubkey_hash_fallback"
		if carrierWish {
			extHex, errC := s.runSuchFalconAddCommitAndCarrierTx(baseHex, pqCommitment32Hex, strings.TrimSpace(wf.PQPublicHex), falconSigHex, carrierKoinu, testnet)
			if errC == nil {
				if b, errH := hex.DecodeString(extHex); errH == nil && len(b) > 80 {
					unsignedForSign = b
					carrierFlow = true
					pqMode = pqMode + "_carrier_txc"
				}
			}
		}

		if useCarrierBudget && !carrierFlow {
			econDowngraded = true
			useCarrierBudget = false
			continue
		}
		break
	}

	rawHex := hexMsgTx(unsignedForSign)
	for i := range selected {
		scr, err := s.scriptPubHexForUTXO(wf, &selected[i])
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		signed, err := s.runSuchSign(rawHex, scr, pa.WIF, i, 1, testnet)
		if err != nil {
			s.appendBroadcastLogLine(fmt.Sprintf("sign input %d failed: %v", i, err))
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("sign input %d: %v", i, err)})
			return
		}
		rawHex = signed
	}

	sendOut, err := s.runSendtx(rawHex, testnet, "")
	sendSummary := summarizeSendtxOutput(sendOut)
	if err != nil {
		s.logBroadcastDetails("send_pq_safe", sendSummary.BroadcastTxID, rawHex, sendOut, err)
		log.Printf("[pq-wallet] send_pq_safe sendtx failed txid=%q signed_hex_len=%d diagnostics=%v err=%v",
			sendSummary.BroadcastTxID, len(rawHex), sendSummary.SendtxDiagnosticLines, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if sendSummary.ConnectedNodes == 0 {
		s.logBroadcastDetails("send_pq_safe", sendSummary.BroadcastTxID, rawHex, sendOut, nil)
		log.Printf("[pq-wallet] send_pq_safe no peers txid=%q signed_hex_len=%d diagnostics=%v sendtx_output=%q",
			sendSummary.BroadcastTxID, len(rawHex), sendSummary.SendtxDiagnosticLines, truncateStr(sendOut, 600))
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":            "sendtx connected to 0 peers; transaction was not propagated",
			"signed_raw_hex":   rawHex,
			"sendtx_output":    sendOut,
			"sendtx_summary":   sendSummary,
			"txid":             sendSummary.BroadcastTxID,
		})
		return
	}
	s.logBroadcastDetails("send_pq_safe", sendSummary.BroadcastTxID, rawHex, sendOut, nil)
	log.Printf("[pq-wallet] send_pq_safe ok txid=%s signed_hex_len=%d informed=%d requested=%d seen_other=%d diagnostics=%v relay_heuristic=%q",
		sendSummary.BroadcastTxID, len(rawHex), sendSummary.InformedNodes, sendSummary.RequestedFromNodes, sendSummary.SeenOnOtherNodes,
		sendSummary.SendtxDiagnosticLines, sendSummary.RelayHeuristicError)

	txCTxid := normalizeTxid(sendSummary.BroadcastTxID)
	if txCTxid == "" {
		if b, errH := hex.DecodeString(strings.TrimSpace(rawHex)); errH == nil {
			txCTxid = dogeLegacyTxidHex(b)
		}
	}

	txRID := ""
	var txRErr string
	pqMkParts := 0
	if carrierFlow && txCTxid != "" && falconSigHex != "" {
		txcBytes, errD := hex.DecodeString(strings.TrimSpace(rawHex))
		if errD != nil || len(txcBytes) < 50 {
			txRErr = "decode TX_C hex failed"
		} else {
			carrierSPKHex, errSpk := s.runSuchPqcCarrierScriptPubkey(testnet)
			if errSpk != nil {
				txRErr = errSpk.Error()
			} else {
				spkBytes, errH := hex.DecodeString(carrierSPKHex)
				if errH != nil || len(spkBytes) == 0 {
					txRErr = "carrier scriptPubKey decode failed"
				} else {
					outs, errP := parseLegacyTxOutputs(txcBytes)
					if errP != nil {
						txRErr = errP.Error()
					} else {
						carrierIdx := findAllPkScriptMatches(outs, spkBytes)
						if len(carrierIdx) == 0 {
							txRErr = "carrier P2SH output not found on TX_C"
						} else {
							scriptSigs, errM := s.collectCarrierScriptSigs("464c4331", strings.TrimSpace(wf.PQPublicHex), falconSigHex, testnet)
							if errM != nil {
								txRErr = errM.Error()
							} else if len(carrierIdx) < len(scriptSigs) {
								txRErr = fmt.Sprintf("TX_C has %d carrier output(s) but PQ payload needs %d mkpart(s); fund a larger carrier layout or split manually", len(carrierIdx), len(scriptSigs))
							} else {
								pqMkParts = len(scriptSigs)
								useIdx := carrierIdx[:len(scriptSigs)]
								var totalIn int64
								vouts := make([]uint32, len(useIdx))
								for i, vi := range useIdx {
									vouts[i] = uint32(vi)
									totalIn += outs[vi].Value
								}
								txRFeeEff := estimateFeeKoinu(len(scriptSigs), false, 0)
								if txRFeeEff < txRFeeKoinu {
									txRFeeEff = txRFeeKoinu
								}
								revealVal := totalIn - txRFeeEff
								if revealVal <= dustLimitKoinu {
									txRErr = "TX_R would leave dust after fee; increase balance or lower PUP_PQ_TXR_FEE_KOINU"
								} else {
									unsignedR, errB := buildUnsignedCarrierRevealTxMulti(txCTxid, vouts, revealVal, changeScript)
									if errB != nil {
										txRErr = errB.Error()
									} else {
										rHex, errSS := s.runSuchSetScriptSigMulti(hexMsgTx(unsignedR), scriptSigs, testnet)
										if errSS != nil {
											txRErr = errSS.Error()
										} else {
											sendOutR, errST := s.runSendtx(rHex, testnet, "")
											sumR := summarizeSendtxOutput(sendOutR)
											if errST != nil {
												s.logBroadcastDetails("send_pq_safe_txr", sumR.BroadcastTxID, rHex, sendOutR, errST)
												txRErr = errST.Error()
											} else if sumR.ConnectedNodes == 0 {
												s.logBroadcastDetails("send_pq_safe_txr", sumR.BroadcastTxID, rHex, sendOutR, nil)
												txRErr = "sendtx TX_R connected to 0 peers"
											} else {
												txRID = normalizeTxid(sumR.BroadcastTxID)
												if txRID == "" {
													if rb, errR := hex.DecodeString(strings.TrimSpace(rHex)); errR == nil {
														txRID = dogeLegacyTxidHex(rb)
													}
												}
												s.logBroadcastDetails("send_pq_safe_txr", txRID, rHex, sendOutR, nil)
												log.Printf("[pq-wallet] send_pq_safe TX_R ok txid=%s parts=%d", txRID, len(scriptSigs))
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	resp := map[string]any{
		"ok":               true,
		"code":             "sent",
		"txid":             sendSummary.BroadcastTxID,
		"signed_raw_hex":   rawHex,
		"sendtx_output":    sendOut,
		"sendtx_summary":   sendSummary,
		"fee_koinu":        fee,
		"change_koinu":     change,
		"inputs_used":      len(selected),
		"pq_commitment":    pqCommitment32Hex != "",
		"pq_commitment_32": pqCommitment32Hex,
		"pq_mode":          pqMode,
		"pq_carrier_flow":                 carrierFlow,
		"carrier_koinu":                   carrierKoinu,
		"pq_carrier_economics_downgraded": econDowngraded,
		"signing_note":                    "ECDSA P2PKH via such -c sign. With libdogecoin liboqs: TX_C adds FLC1 OP_RETURN + canonical P2SH carrier; TX_R reveals Falcon payload via pqc_carrier_mkpart (+ multi-part set_scriptsig when the payload spans several carrier outputs).",
		"transport":        "libdogecoin_sendtx_p2p",
		"transport_note":   "Same model as Dogecoin Wallet (Android): broadcast is wallet-to-network P2P (here libdogecoin sendtx), not JSON-RPC sendrawtransaction to a local Core node.",
	}
	if txRID != "" {
		resp["tx_r_txid"] = txRID
	}
	if pqMkParts > 0 {
		resp["pq_carrier_mkpart_parts"] = pqMkParts
	}
	if txRErr != "" {
		resp["tx_r_error"] = txRErr
	}
	writeJSON(w, http.StatusOK, resp)
}
