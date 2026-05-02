package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type spvSyncPrefs struct {
	UseCheckpoint bool `json:"use_checkpoint"`
	// RestoreCheckpointHint is the bundled checkpoint height last chosen at wallet import (0 = genesis path).
	RestoreCheckpointHint int64 `json:"restore_checkpoint_hint"`
	// PendingRollbackKeepHeight is set when rollback could not trim legacy (non-SQLite) headers.db; once SQLite
	// headers exist with tip above this height, tryApplyPendingRollbackSQLite deletes rows with height > keep.
	PendingRollbackKeepHeight *int64 `json:"pending_rollback_keep_height,omitempty"`
}

// spvOnRestoreOpts is sent with wrapped wallet import JSON as spv_on_restore.
type spvOnRestoreOpts struct {
	Sync   string `json:"sync"`   // "genesis" (default) or "bundled_checkpoints"
	Height int64  `json:"height"` // when sync is bundled_checkpoints: checkpoint height from the bundled table (>0)
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
	return p
}

// applySPVSyncPrefsFromWalletRestore persists SPV sync prefs after a wrapped wallet import.
// nil opts means genesis (full headers from block 0, no spvnode -p).
func (s *Server) applySPVSyncPrefsFromWalletRestore(o *spvOnRestoreOpts, testnet bool) error {
	if o == nil {
		o = &spvOnRestoreOpts{Sync: "genesis"}
	}
	sync := strings.ToLower(strings.TrimSpace(o.Sync))
	prefs := s.readSPVSyncPrefs()
	prefs.PendingRollbackKeepHeight = nil
	switch sync {
	case "bundled_checkpoints", "checkpoints":
		prefs.UseCheckpoint = true
		h := o.Height
		if h > 0 && !bundledCheckpointHeightKnown(testnet, h) {
			h = 0
		}
		prefs.RestoreCheckpointHint = h
	default:
		prefs.UseCheckpoint = false
		prefs.RestoreCheckpointHint = 0
	}
	return s.writeSPVSyncPrefs(prefs)
}

func (s *Server) writeSPVSyncPrefs(p spvSyncPrefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.spvSyncPrefsPath(), b, 0600)
}
