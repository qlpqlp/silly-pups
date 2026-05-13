package main

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// strictSettingsPeekBlocked is true when strict mode is on, the wallet is sealed,
// and there is no active unlock session (same gate as reading wallet secrets).
func (s *Server) strictSettingsPeekBlocked() bool {
	if !s.readServicePrefs().StrictSettingsAuth {
		return false
	}
	if !s.hasSealedWallet() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.memWallet == nil || time.Now().After(s.unlockUntil)
}

func writeStrictSettingsAuthRequired(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       "settings_auth_required",
		"need_unlock": true,
		"detail":      "Strict settings protection is enabled. Unlock the wallet (PIN or biometric) before loading this data.",
	})
}

func (s *Server) handleSettingsPrefsGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	p := s.readServicePrefs()
	writeJSON(w, http.StatusOK, map[string]any{
		"strict_settings_auth": p.StrictSettingsAuth,
	})
}

func (s *Server) handleSettingsPrefsPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		StrictSettingsAuth *bool  `json:"strict_settings_auth"`
		PIN                string `json:"pin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.StrictSettingsAuth == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send strict_settings_auth boolean"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasSealedWallet() {
		if !s.requireSealedWalletPINForAction(w, body.PIN) {
			return
		}
	}
	cur := s.readServicePrefs()
	cur.StrictSettingsAuth = *body.StrictSettingsAuth
	if err := s.writeServicePrefs(cur); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"strict_settings_auth": cur.StrictSettingsAuth,
	})
}
