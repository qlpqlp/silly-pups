package main

import (
	"encoding/json"
	"log"
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
	path := s.spvSyncPrefsPath()
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		_ = s.writeSPVSyncPrefs(out)
		return out
	}
	var p spvSyncPrefs
	if json.Unmarshal(b, &p) != nil {
		_ = s.writeSPVSyncPrefs(out)
		return out
	}
	// Always prefer libdogecoin checkpoint-assisted sync (-p) for on-disk headers.db; genesis-only is easy to misconfigure.
	if !p.UseCheckpoint {
		p.UseCheckpoint = true
		if err := s.writeSPVSyncPrefs(p); err != nil {
			log.Printf("[pq-wallet] spv_sync_prefs write: %v", err)
		} else {
			log.Printf("[pq-wallet] spv_sync_prefs: use_checkpoint set to true (checkpoint sync)")
		}
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
