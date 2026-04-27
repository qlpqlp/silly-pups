package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var reAmountDoge = regexp.MustCompile(`^\d+(\.\d+)?$`)

const dogeEconomicFeePerKBKoinu = int64(1_000_000) // 0.01 DOGE/KB (Dogecoin recommendation)

func estimateFeeKoinu(inputCount int, includeChange bool, includeCommitment bool) int64 {
	if inputCount < 1 {
		inputCount = 1
	}
	outs := 1 // destination
	if includeChange {
		outs++
	}
	if includeCommitment {
		outs++
	}
	// Legacy P2PKH rough size model.
	vbytes := (180 * inputCount) + (34 * outs) + 10
	kb := (vbytes + 999) / 1000 // ceil
	if kb < 1 {
		kb = 1
	}
	return int64(kb) * dogeEconomicFeePerKBKoinu
}

type sendPQSafeBody struct {
	ToAddress  string `json:"to_address"`
	AmountDOGE string `json:"amount_doge"`
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

	sendKoinu, err := dogeAmountStringToKoinu(amt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
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

	var selected []ExplorerUTXO
	var sumIn int64
	for attempt := 0; attempt < 12; attempt++ {
		n := len(selected)
		if n == 0 {
			n = 1
		}
		fee := estimateFeeKoinu(n, true, true)
		need := sendKoinu + fee
		selected, sumIn, err = selectUTXOs(utxos, need)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		fee = estimateFeeKoinu(len(selected), true, true)
		if sumIn >= sendKoinu+fee {
			break
		}
		if attempt == 11 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not cover amount plus fee"})
			return
		}
	}
	fee := estimateFeeKoinu(len(selected), true, true)
	change := sumIn - sendKoinu - fee
	if change < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "insufficient balance after fee"})
		return
	}

	toScript, err := dogeP2PKHScriptFromAddress(to, testnet)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("destination: %v", err)})
		return
	}
	changeScript, err := dogeP2PKHScriptFromAddress(strings.TrimSpace(pa.P2PKH), testnet)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("change script: %v", err)})
		return
	}

	changeOut := change
	if change <= dustLimitKoinu {
		changeOut = 0
	}
	pqCommitment32Hex := ""
	pqMode := "none"
	// Build canonical Phase-1 commitment when Falcon material is available.
	// Preferred: SHA256(pubkey || signature(sighash32)).
	if strings.TrimSpace(wf.PQPublicHex) != "" && strings.TrimSpace(wf.PQPrivateHex) != "" && len(selected) > 0 {
		if scr, err := s.scriptPubHexForUTXO(wf, &selected[0]); err == nil {
			// Build first without commitment to derive base tx sighash32.
			unsignedBase, err := buildUnsignedDogeP2PKH(selected, toScript, sendKoinu, changeScript, changeOut, "")
			if err == nil {
				baseHex := hexMsgTx(unsignedBase)
				if sighashHex, err := s.runSuchTxSighash32(baseHex, scr, 0, 1, testnet); err == nil {
					if sigHex, err := s.runSuchFalconSign(sighashHex, wf.PQPrivateHex, testnet); err == nil {
						pubB, pubErr := hex.DecodeString(strings.TrimSpace(wf.PQPublicHex))
						sigB, sigErr := hex.DecodeString(strings.TrimSpace(sigHex))
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
	}
	// Safe fallback for compatibility: SHA256(pubkey) if signing material is unavailable.
	if pqCommitment32Hex == "" {
		if pubB, err := hex.DecodeString(strings.TrimSpace(wf.PQPublicHex)); err == nil && len(pubB) > 0 {
			h := sha256.Sum256(pubB)
			pqCommitment32Hex = hex.EncodeToString(h[:])
			pqMode = "legacy_pubkey_hash_fallback"
		}
	}
	unsigned, err := buildUnsignedDogeP2PKH(selected, toScript, sendKoinu, changeScript, changeOut, pqCommitment32Hex)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	rawHex := hexMsgTx(unsigned)
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
	if err != nil {
		s.appendBroadcastLogLine("send_pq_safe broadcast FAIL: " + err.Error())
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	sendSummary := summarizeSendtxOutput(sendOut)
	if sendSummary.ConnectedNodes == 0 {
		s.appendBroadcastLogLine("send_pq_safe broadcast FAIL: no peers connected in sendtx output")
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":          "sendtx connected to 0 peers; transaction was not propagated",
			"sendtx_output":  sendOut,
			"sendtx_summary": sendSummary,
			"txid":           sendSummary.BroadcastTxID,
		})
		return
	}
	s.appendBroadcastLogLine("send_pq_safe broadcast OK: " + truncateStr(sendOut, 400))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"code":             "sent",
		"txid":             sendSummary.BroadcastTxID,
		"sendtx_output":    sendOut,
		"sendtx_summary":   sendSummary,
		"fee_koinu":        fee,
		"change_koinu":     change,
		"inputs_used":      len(selected),
		"pq_commitment":    pqCommitment32Hex != "",
		"pq_commitment_32": pqCommitment32Hex,
		"pq_mode":          pqMode,
		"signing_note":     "ECDSA P2PKH via such -c sign. PQ commitment output uses canonical Phase-1 OP_RETURN tag (FLC1) with 32-byte commitment.",
		"transport":        "libdogecoin_sendtx_p2p",
	})
}
