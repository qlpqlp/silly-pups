package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var reAmountDoge = regexp.MustCompile(`^\d+(\.\d+)?$`)

type sendPQSafeBody struct {
	ToAddress  string `json:"to_address"`
	AmountDOGE string `json:"amount_doge"`
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
	if err != nil || wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}

	line := "send_pq_safe requested to=" + to + " amount_doge=" + amt + " (automatic PQ tx build not in this build)"
	s.appendBroadcastLogLine(line)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    false,
		"code":  "pq_tx_build_pending",
		"message": "Automatic construction of a post-quantum-safe signed transaction from this wallet's UTXOs is not implemented in this build. Build and sign your transaction with libdogecoin tooling (or another workflow), then use the Manual tab: paste the raw signed hex and broadcast via libdogecoin sendtx (P2P).",
	})
}
