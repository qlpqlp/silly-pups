package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// servicePrefsFileMu serializes reads/writes to service_prefs.json (independent of Server.mu).
var servicePrefsFileMu sync.Mutex

type servicePrefs struct {
	SpvEnabled           bool `json:"spv_enabled"`
	MemetrackerEnabled   bool `json:"memetracker_enabled"`
	StrictSettingsAuth   bool `json:"strict_settings_auth"`
}

func (s *Server) servicePrefsPath() string {
	return filepath.Join(s.storageDir, "service_prefs.json")
}

func (s *Server) readServicePrefs() servicePrefs {
	out := servicePrefs{SpvEnabled: true, MemetrackerEnabled: true}
	servicePrefsFileMu.Lock()
	defer servicePrefsFileMu.Unlock()
	b, err := os.ReadFile(s.servicePrefsPath())
	if err != nil || len(b) == 0 {
		return out
	}
	var p servicePrefs
	if json.Unmarshal(b, &p) != nil {
		return out
	}
	return p
}

func (s *Server) writeServicePrefs(p servicePrefs) error {
	servicePrefsFileMu.Lock()
	defer servicePrefsFileMu.Unlock()
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.servicePrefsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.servicePrefsPath())
}
