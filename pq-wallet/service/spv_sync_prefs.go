package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type spvSyncPrefs struct {
	UseCheckpoint bool `json:"use_checkpoint"`
}

func (s *Server) spvSyncPrefsPath() string {
	return filepath.Join(s.storageDir, "spv_sync_prefs.json")
}

func (s *Server) readSPVSyncPrefs() spvSyncPrefs {
	out := spvSyncPrefs{UseCheckpoint: true}
	b, err := os.ReadFile(s.spvSyncPrefsPath())
	if err != nil || len(b) == 0 {
		return out
	}
	var p spvSyncPrefs
	if json.Unmarshal(b, &p) != nil {
		return out
	}
	return p
}

func (s *Server) writeSPVSyncPrefs(p spvSyncPrefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.spvSyncPrefsPath(), b, 0600)
}
