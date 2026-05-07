package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

type carrierStuckRow struct {
	TxCTxid             string  `json:"tx_c_txid"`
	CarrierOutputs      int     `json:"carrier_outputs"`
	CarrierTotalKoinu   int64   `json:"carrier_total_koinu"`
	CarrierTotalDOGE    float64 `json:"carrier_total_doge"`
	Confirmations       int     `json:"confirmations"`
	PQTag4              string  `json:"pq_tag4,omitempty"`
	Source              string  `json:"source,omitempty"`
	RecoverableHint     string  `json:"recoverable_hint,omitempty"`
}

func carrierTag8HexFromTag4(tag4 string) string {
	switch strings.ToUpper(strings.TrimSpace(tag4)) {
	case "FLC1":
		return "464c433146554c4c" // FLC1FULL
	case "DIL2":
		return "44494c3246554c4c" // DIL2FULL
	case "RCG4":
		return "5243473446554c4c" // RCG4FULL
	default:
		return "464c433146554c4c"
	}
}

func pqCommitTag4FromRawHexLocal(rawHex string) string {
	s := strings.TrimSpace(strings.ToLower(rawHex))
	switch {
	case strings.Contains(s, "6a24464c4331"):
		return "FLC1"
	case strings.Contains(s, "6a2444494c32"):
		return "DIL2"
	case strings.Contains(s, "6a2452434734"):
		return "RCG4"
	default:
		return ""
	}
}

func carrierDummyScriptSigHex(tag8Hex string, partIndex, partTotal int) string {
	if partIndex < 0 {
		partIndex = 0
	}
	if partIndex > 255 {
		partIndex = 255
	}
	if partTotal < 1 {
		partTotal = 1
	}
	if partTotal > 255 {
		partTotal = 255
	}
	tag, err := hex.DecodeString(strings.TrimSpace(tag8Hex))
	if err != nil || len(tag) != 8 {
		tag = []byte("FLC1FULL")
	}
	// HDR8: version(1), part_index, part_total, reserved(0), pk_len(0), full_len(0)
	hdr := []byte{0x01, byte(partIndex), byte(partTotal), 0x00, 0x00, 0x00, 0x00, 0x00}
	redeem, _ := hex.DecodeString("757575757551") // OP_DROP x5, OP_TRUE
	var b []byte
	// Push TAG8
	b = append(b, byte(len(tag)))
	b = append(b, tag...)
	// Push HDR8
	b = append(b, byte(len(hdr)))
	b = append(b, hdr...)
	// CHUNK0/1/2 = OP_0 (empty pushes)
	b = append(b, 0x00, 0x00, 0x00)
	// Push redeemScript
	b = append(b, byte(len(redeem)))
	b = append(b, redeem...)
	return hex.EncodeToString(b)
}

func parseCarrierOutputsFromRaw(rawHex string, carrierSPK []byte) (vouts []uint32, totalIn int64, tag4 string, err error) {
	rawHex = strings.TrimSpace(rawHex)
	if rawHex == "" {
		return nil, 0, "", errors.New("empty TX_C raw hex")
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return nil, 0, "", err
	}
	outs, err := parseLegacyTxOutputs(raw)
	if err != nil {
		return nil, 0, "", err
	}
	idxs := findAllPkScriptMatches(outs, carrierSPK)
	if len(idxs) == 0 {
		return nil, 0, "", errors.New("carrier P2SH output not found on TX_C")
	}
	vouts = make([]uint32, 0, len(idxs))
	for _, vi := range idxs {
		vouts = append(vouts, uint32(vi))
		totalIn += outs[vi].Value
	}
	tag4 = pqCommitTag4FromRawHexLocal(rawHex)
	return vouts, totalIn, tag4, nil
}

func stateSpentPrevoutSet(st *WalletState) map[string]struct{} {
	out := make(map[string]struct{})
	if st == nil {
		return out
	}
	for _, t := range st.Transactions {
		raw := strings.TrimSpace(t.RawHex)
		if raw == "" {
			continue
		}
		rawB, err := hex.DecodeString(strings.ToLower(raw))
		if err != nil {
			continue
		}
		ins, err := parseLegacyTxPrevouts(rawB)
		if err != nil || len(ins) == 0 {
			continue
		}
		for _, in := range ins {
			if in.Txid == "" {
				continue
			}
			out[prevoutIndexKey(in.Txid, in.Vout)] = struct{}{}
		}
	}
	return out
}

func (s *Server) listStuckCarrierRows(st *WalletState, wf *WalletFile) ([]carrierStuckRow, error) {
	if st == nil || wf == nil {
		return nil, nil
	}
	testnet := strings.EqualFold(strings.TrimSpace(wf.Network), "testnet")
	carrierSPKHex, err := s.runSuchPqcCarrierScriptPubkey(testnet)
	if err != nil {
		return nil, err
	}
	carrierSPK, err := hex.DecodeString(strings.TrimSpace(carrierSPKHex))
	if err != nil || len(carrierSPK) == 0 {
		return nil, fmt.Errorf("carrier scriptPubKey decode failed")
	}
	spent := stateSpentPrevoutSet(st)
	rows := make([]carrierStuckRow, 0)
	for _, t := range st.Transactions {
		txid := normalizeTxid(t.Txid)
		if txid == "" {
			continue
		}
		raw := strings.TrimSpace(t.RawHex)
		if raw == "" {
			continue
		}
		vouts, totalIn, tag4, err := parseCarrierOutputsFromRaw(raw, carrierSPK)
		if err != nil || len(vouts) == 0 || totalIn <= 0 {
			continue
		}
		openCount := 0
		openTotal := int64(0)
		for _, vo := range vouts {
			if _, ok := spent[prevoutIndexKey(txid, vo)]; ok {
				continue
			}
			openCount++
			rawB, err := hex.DecodeString(strings.ToLower(raw))
			if err != nil {
				continue
			}
			outs, err := parseLegacyTxOutputs(rawB)
			if err != nil {
				continue
			}
			if int(vo) >= 0 && int(vo) < len(outs) {
				openTotal += outs[vo].Value
			}
		}
		if openCount == 0 || openTotal <= 0 {
			continue
		}
		rows = append(rows, carrierStuckRow{
			TxCTxid:           txid,
			CarrierOutputs:    openCount,
			CarrierTotalKoinu: openTotal,
			CarrierTotalDOGE:  round2(float64(openTotal) / 1e8),
			Confirmations:     t.Confirmations,
			PQTag4:            tag4,
			Source:            strings.TrimSpace(t.Source),
			RecoverableHint:   "Carrier outputs look unspent in local state. Use Recovery to broadcast TX_R-style spend back to your wallet.",
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Confirmations != rows[j].Confirmations {
			return rows[i].Confirmations > rows[j].Confirmations
		}
		return rows[i].TxCTxid > rows[j].TxCTxid
	})
	return rows, nil
}

func (s *Server) handlePQCarrierStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
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
	s.stateMergeMu.Lock()
	st, err := s.loadState()
	s.stateMergeMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rows, err := s.listStuckCarrierRows(st, wf)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": rows,
		"instructions": "Carrier mode sends TX_C with a temporary P2SH carrier output. Normally TX_R spends it back. If TX_R fails, use Recover to sweep those outputs back to your wallet. If your PQ send is missing from the activity list, you can still recover using the TX_C txid you saved: the server tries local state, then SPV GET /getRawTx, or you can paste the raw transaction hex from any block explorer (must be the commitment transaction you broadcast from this wallet).",
	})
}

// resolvePQCarrierTXCRawHex loads TX_C raw hex: optional pasted hex, else wallet state, else SPV /getRawTx.
func (s *Server) resolvePQCarrierTXCRawHex(st *WalletState, txCTxid, pastedRawHex string) (rawHex string, source string, err error) {
	txCTxid = normalizeTxid(txCTxid)
	pastedRawHex = strings.TrimSpace(pastedRawHex)
	if pastedRawHex != "" {
		if !looksLikeDogecoinRawTxHex(pastedRawHex) {
			return "", "", errors.New("tx_c_raw_hex does not look like hex")
		}
		rawB, derr := hex.DecodeString(strings.ToLower(pastedRawHex))
		if derr != nil {
			return "", "", fmt.Errorf("tx_c_raw_hex decode: %w", derr)
		}
		computed := normalizeTxid(dogeLegacyTxidHex(rawB))
		if computed == "" {
			return "", "", errors.New("could not compute txid from tx_c_raw_hex")
		}
		if txCTxid != "" && !strings.EqualFold(txCTxid, computed) {
			return "", "", fmt.Errorf("tx_c_txid does not match raw hex (expected %s)", computed)
		}
		return strings.ToLower(pastedRawHex), "pasted_hex", nil
	}
	if txCTxid == "" {
		return "", "", errors.New("tx_c_txid or tx_c_raw_hex required")
	}
	for _, t := range st.Transactions {
		if normalizeTxid(t.Txid) == txCTxid && strings.TrimSpace(t.RawHex) != "" {
			return strings.TrimSpace(t.RawHex), "wallet_state", nil
		}
	}
	body, rerr := s.fetchSPVREST("/getRawTx?txid=" + strings.ToLower(txCTxid))
	if rerr == nil {
		line := strings.TrimSpace(strings.ReplaceAll(body, "\r", ""))
		line = strings.TrimSuffix(line, "\n")
		if looksLikeDogecoinRawTxHex(line) {
			rawB, derr := hex.DecodeString(strings.ToLower(line))
			if derr == nil {
				if got := normalizeTxid(dogeLegacyTxidHex(rawB)); got == txCTxid {
					return strings.ToLower(line), "spv_get_raw_tx", nil
				}
			}
		}
	}
	extra := ""
	if rerr != nil {
		extra = rerr.Error()
	}
	return "", "", fmt.Errorf("TX_C raw hex not found in wallet state and SPV /getRawTx failed (%s); paste tx_c_raw_hex from a block explorer", extra)
}

func (s *Server) handlePQCarrierRecover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		TxCTxid   string `json:"tx_c_txid"`
		TxCRawHex string `json:"tx_c_raw_hex"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pasted := strings.TrimSpace(body.TxCRawHex)
	txCTxidIn := normalizeTxid(strings.TrimSpace(body.TxCTxid))
	if txCTxidIn == "" && pasted == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tx_c_txid or tx_c_raw_hex required"})
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
	s.stateMergeMu.Lock()
	st, err := s.loadState()
	s.stateMergeMu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rawTXC, rawSource, err := s.resolvePQCarrierTXCRawHex(st, txCTxidIn, pasted)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	rawBytes, decErr := hex.DecodeString(strings.ToLower(strings.TrimSpace(rawTXC)))
	if decErr != nil || len(rawBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "resolved TX_C hex is invalid"})
		return
	}
	txCTxid := normalizeTxid(dogeLegacyTxidHex(rawBytes))
	if txCTxid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not compute txid from resolved TX_C"})
		return
	}
	testnet := strings.EqualFold(strings.TrimSpace(wf.Network), "testnet")
	carrierSPKHex, err := s.runSuchPqcCarrierScriptPubkey(testnet)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	carrierSPK, err := hex.DecodeString(strings.TrimSpace(carrierSPKHex))
	if err != nil || len(carrierSPK) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "carrier scriptPubKey decode failed"})
		return
	}
	vouts, totalIn, tag4, err := parseCarrierOutputsFromRaw(rawTXC, carrierSPK)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(vouts) == 0 || totalIn <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no carrier outputs to recover"})
		return
	}
	pa := wf.PrimaryAddress()
	if pa == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no primary address"})
		return
	}
	changeScript, err := dogeP2PKHScriptFromAddress(strings.TrimSpace(pa.P2PKH), testnet)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("change script: %v", err)})
		return
	}
	txRFeeFloorKoinu := minRelayFeeKoinu
	if v := strings.TrimSpace(os.Getenv("PUP_PQ_TXR_FEE_KOINU")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= minRelayFeeKoinu {
			txRFeeFloorKoinu = n
		}
	}
	txRFeeEff := estimateSendTxFeeKoinu(dogeDefaultFeePerKbKoinu, len(vouts), false, 0)
	if txRFeeEff < txRFeeFloorKoinu {
		txRFeeEff = txRFeeFloorKoinu
	}
	revealVal := totalIn - txRFeeEff
	if revealVal <= dustLimitKoinu {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recovery TX_R would leave dust after fee; cannot recover automatically"})
		return
	}
	unsignedR, err := buildUnsignedCarrierRevealTxMulti(txCTxid, vouts, revealVal, changeScript)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tag8 := carrierTag8HexFromTag4(tag4)
	if tag8 == "" {
		tag8 = "464c433146554c4c"
	}
	sigs := make([]string, len(vouts))
	for i := range vouts {
		sigs[i] = carrierDummyScriptSigHex(tag8, i, len(vouts))
	}
	rHex, err := s.runSuchSetScriptSigMulti(hexMsgTx(unsignedR), sigs, testnet)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	sendOut, err := s.runSendtx(rHex, testnet, "")
	sum := summarizeSendtxOutput(sendOut)
	if err != nil {
		s.logBroadcastDetails("pq_carrier_recover_txr", sum.BroadcastTxID, rHex, sendOut, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.logBroadcastDetails("pq_carrier_recover_txr", sum.BroadcastTxID, rHex, sendOut, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"code":              "recovery_broadcasted",
		"tx_c_txid":         txCTxid,
		"tx_c_hex_source":   rawSource,
		"tx_r_txid":         normalizeTxid(sum.BroadcastTxID),
		"sendtx_summary":    sum,
		"sendtx_output":     sendOut,
		"recovered_koinu":   revealVal,
		"recovered_doge":    round2(float64(revealVal) / 1e8),
		"fee_koinu":         txRFeeEff,
		"carrier_total_doge": round2(float64(totalIn) / 1e8),
		"note":              "Emergency carrier recovery spend broadcasted. This path spends carrier outputs back to your wallet even when canonical pqc_carrier_mkpart failed previously.",
	})
}

