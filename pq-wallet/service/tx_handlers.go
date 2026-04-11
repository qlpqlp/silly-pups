package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type signBody struct {
	RawHex      string `json:"raw_hex"`
	InputIndex  int    `json:"input_index"`
	SighashType int    `json:"sighash_type"`
}

type broadcastBody struct {
	RawHex string `json:"raw_hex"`
	Peers  string `json:"peers"`
}

func (s *Server) handleTxSign(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body signBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.RawHex) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "raw_hex required"})
		return
	}
	if body.SighashType == 0 {
		body.SighashType = 1
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if err != nil || wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	scriptHex, err := s.p2pkhScriptPubKeyHexForSign(wf)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	signed, err := s.runSuchSign(strings.TrimSpace(body.RawHex), scriptHex, wf.WIFPrivateKey, body.InputIndex, body.SighashType, testnet)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signed_raw_hex": signed})
}

func (s *Server) handleTxBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body broadcastBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.RawHex) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "raw_hex required"})
		return
	}
	s.mu.Lock()
	wf, _ := s.loadWallet()
	s.mu.Unlock()
	testnet := wf != nil && strings.EqualFold(wf.Network, "testnet")
	out, err := s.runSendtx(strings.TrimSpace(body.RawHex), testnet, strings.TrimSpace(body.Peers))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sendtx_output": out})
}

func (s *Server) handleSPVStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	if err == nil && wf != nil {
		s.startSPVNode(wf)
	}
	writeJSON(w, http.StatusOK, s.readSPVStatus())
}
