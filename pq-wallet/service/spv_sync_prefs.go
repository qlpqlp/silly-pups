package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type spvSyncPrefs struct {
	UseCheckpoint bool `json:"use_checkpoint"`
	// RestoreCheckpointHint is the bundled checkpoint height last chosen at wallet import (0 = genesis path).
	RestoreCheckpointHint int64 `json:"restore_checkpoint_hint"`
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
	switch sync {
	case "bundled_checkpoints", "checkpoints":
		h := o.Height
		if h > 0 && !bundledCheckpointHeightKnown(testnet, h) {
			h = 0
		}
		prefs.RestoreCheckpointHint = h
		// Without a valid bundled height, do not leave -p on with hint 0 (spvnode would ignore PQ_SPV_CHECKPOINT_HEIGHT).
		prefs.UseCheckpoint = h > 0
	default:
		prefs.UseCheckpoint = false
		prefs.RestoreCheckpointHint = 0
	}
	return s.writeSPVSyncPrefs(prefs)
}

// buildSPVPrefsForCheckpointResync maps a bundled checkpoint height (0 = genesis path) to persisted SPV prefs.
// useCheckpoint overrides UseCheckpoint when non-nil; when keepHeight > 0 and UseCheckpoint is false, RestoreCheckpointHint is cleared.
func buildSPVPrefsForCheckpointResync(testnet bool, keepHeight int64, useCheckpoint *bool, base spvSyncPrefs) (spvSyncPrefs, error) {
	p := base
	if keepHeight < 0 || keepHeight > 200_000_000 {
		return spvSyncPrefs{}, fmt.Errorf("checkpoint height out of range")
	}
	if keepHeight == 0 {
		p.UseCheckpoint = false
		p.RestoreCheckpointHint = 0
		return p, nil
	}
	if !bundledCheckpointHeightKnown(testnet, keepHeight) {
		return spvSyncPrefs{}, fmt.Errorf("height %d is not a bundled libdogecoin checkpoint for this network", keepHeight)
	}
	if useCheckpoint != nil {
		p.UseCheckpoint = *useCheckpoint
	} else {
		p.UseCheckpoint = true
	}
	if !p.UseCheckpoint {
		p.RestoreCheckpointHint = 0
	} else {
		p.RestoreCheckpointHint = keepHeight
	}
	return p, nil
}

func (s *Server) writeSPVSyncPrefs(p spvSyncPrefs) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.spvSyncPrefsPath(), b, 0600)
}
