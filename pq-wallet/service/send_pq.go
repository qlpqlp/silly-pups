package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var reAmountDoge = regexp.MustCompile(`^\d+(\.\d+)?$`)

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
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var selected []ExplorerUTXO
	var sumIn int64
	for attempt := 0; attempt < 12; attempt++ {
		n := len(selected)
		if n == 0 {
			n = 1
		}
		fee := int64(200000) + int64(max(0, n-1))*50000
		need := sendKoinu + fee
		selected, sumIn, err = selectUTXOs(utxos, need)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		fee = int64(200000) + int64(max(0, len(selected)-1))*50000
		if sumIn >= sendKoinu+fee {
			break
		}
		if attempt == 11 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not cover amount plus fee"})
			return
		}
	}
	fee := int64(200000) + int64(max(0, len(selected)-1))*50000
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
	pqHex := strings.TrimSpace(wf.PQPublicHex)
	unsigned, err := buildUnsignedDogeP2PKH(selected, toScript, sendKoinu, changeScript, changeOut, pqHex)
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
	s.appendBroadcastLogLine("send_pq_safe broadcast OK: " + truncateStr(sendOut, 400))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"code":             "sent",
		"sendtx_output":    sendOut,
		"fee_koinu":        fee,
		"change_koinu":     change,
		"inputs_used":      len(selected),
		"pq_commitment":    pqHex != "",
		"signing_note":     "ECDSA P2PKH via such -c sign; optional OP_RETURN commits SHA256(PQ public key) when Falcon/PQ keys exist.",
		"transport":        "libdogecoin_sendtx_p2p",
	})
}
