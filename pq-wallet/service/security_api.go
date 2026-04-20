package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Server) handleSecurityStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed := s.hasSealedWallet()
	unlocked := !sealed || (s.memWallet != nil && time.Now().Before(s.unlockUntil))
	writeJSON(w, http.StatusOK, map[string]any{
		"sealed":               sealed,
		"unlocked":             unlocked,
		"has_plaintext_wallet": fileExists(s.walletPath),
	})
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (s *Server) handleSecurityUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	pin := strings.TrimSpace(body.PIN)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSealedWallet() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "wallet is not sealed"})
		return
	}
	if err := s.unlockSealedWallet(pin); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if s.memWallet != nil {
		s.startSPVNode(s.memWallet)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSecurityLock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockWalletSession()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSecuritySeal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasSealedWallet() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "already sealed"})
		return
	}
	if _, err := os.Stat(s.walletPath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no plaintext wallet to seal"})
		return
	}
	if err := s.sealWalletFromPlaintext(body.PIN); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sealed": true})
}

func (s *Server) handleSecurityUnseal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasSealedWallet() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "wallet is not sealed"})
		return
	}
	if err := s.unlockSealedWallet(strings.TrimSpace(body.PIN)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	wf := s.memWallet
	if wf == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "wallet unlock failed"})
		return
	}
	// Save plaintext wallet and remove sealed file when user explicitly disables encryption.
	s.walletKey = nil
	s.sealSalt = nil
	if err := s.saveWallet(wf); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = os.Remove(s.sealedPath())
	s.memWallet = nil
	s.unlockUntil = time.Time{}
	s.startSPVNode(wf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sealed": false})
}
