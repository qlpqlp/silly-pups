package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
)

func (s *Server) handleServicesControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		SpvEnabled         *bool `json:"spv_enabled"`
		MemetrackerEnabled *bool `json:"memetracker_enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.SpvEnabled == nil && body.MemetrackerEnabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send spv_enabled and/or memetracker_enabled booleans"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}

	cur := s.readServicePrefs()
	if body.SpvEnabled != nil {
		cur.SpvEnabled = *body.SpvEnabled
	}
	if body.MemetrackerEnabled != nil {
		cur.MemetrackerEnabled = *body.MemetrackerEnabled
	}
	if err := s.writeServicePrefs(cur); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if cur.SpvEnabled {
		s.startSPVNode(wf)
	} else {
		s.stopSPVNode()
	}

	if cur.MemetrackerEnabled {
		if _, e := s.ensureMempoolEngine(wf); e != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":              true,
				"service_prefs":   cur,
				"memetracker_err": e.Error(),
			})
			return
		}
	} else {
		s.mempoolMu.Lock()
		if s.mempoolEngine != nil {
			s.mempoolEngine.Stop()
			s.mempoolEngine = nil
		}
		s.mempoolMu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service_prefs": cur})
}

func (s *Server) removeServicePrefsFile() {
	_ = os.Remove(s.servicePrefsPath())
}
