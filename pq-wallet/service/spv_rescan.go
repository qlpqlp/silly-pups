package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// repairHeadersDBForSPVStart removes broken headers.db states that make spvnode print
// "Could not load or create headers database" (tiny/corrupt stub, or a directory at the path).
func (s *Server) repairHeadersDBForSPVStart() {
	headersPath := filepath.Join(s.storageDir, "headers.db")
	st, err := os.Stat(headersPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[pq-wallet] headers.db stat: %v", err)
		return
	}
	if st.IsDir() {
		s.stopSPVNode()
		backup := headersPath + ".bad_dir." + strconv.FormatInt(time.Now().Unix(), 10)
		if err := os.Rename(headersPath, backup); err != nil {
			log.Printf("[pq-wallet] could not rename headers.db directory: %v", err)
			return
		}
		walletDB := filepath.Join(s.storageDir, "spv_wallet.db")
		_ = os.Remove(walletDB)
		_ = os.Remove(s.spvWatchAddrPath())
		log.Printf("[pq-wallet] removed invalid headers.db directory (renamed to %s); SPV will recreate headers from checkpoint", backup)
		return
	}
	if st.Size() < 100 {
		s.stopSPVNode()
		_ = os.Remove(headersPath)
		_ = os.Remove(filepath.Join(s.storageDir, "spv_wallet.db"))
		_ = os.Remove(s.spvWatchAddrPath())
		log.Printf("[pq-wallet] removed invalid tiny headers.db; SPV will recreate header store from checkpoint")
	}
}

// migrateLegacyHeadersDB stops spvnode, then if headers.db exists but is not the libdogecoin file layout,
// renames it aside, removes spv_wallet.db and the watch-list file so the next spvnode start creates a fresh headers.db.
func (s *Server) migrateLegacyHeadersDB() (migrated bool, backupPath string, err error) {
	headersPath := filepath.Join(s.storageDir, "headers.db")
	st, statErr := os.Stat(headersPath)
	if statErr != nil {
		return false, "", nil
	}
	if st.IsDir() {
		return false, "", fmt.Errorf("headers.db is a directory")
	}
	// Do not treat a tiny file as "legacy": it may be an incomplete write; renaming would force a full resync.
	if st.Size() < 100 {
		return false, "", nil
	}
	if isLibdogecoinHeadersFileFormat(headersPath) {
		return false, "", nil
	}
	s.stopSPVNode()
	backupPath = headersPath + ".legacy." + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(headersPath, backupPath); err != nil {
		return false, "", fmt.Errorf("backup legacy headers.db: %w", err)
	}
	walletDB := filepath.Join(s.storageDir, "spv_wallet.db")
	_ = os.Remove(walletDB)
	_ = os.Remove(s.spvWatchAddrPath())
	return true, backupPath, nil
}

func (s *Server) handleSPVRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct {
		Confirm           string `json:"confirm"`
		Mode              string `json:"mode"`
		RollbackBlockHash string `json:"rollback_block_hash"`
		// RollbackHeight is a pointer so JSON rollback_height: 0 is valid (genesis checkpoint).
		RollbackHeight *int64 `json:"rollback_height"`
		UseCheckpoint  *bool  `json:"use_checkpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	confirm := strings.TrimSpace(body.Confirm)
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if confirm == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"confirm":"RESCAN"} for full reset or {"confirm":"ROLLBACK"} with rollback_block_hash or rollback_height (including 0 for genesis block)`})
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

	headersPath := filepath.Join(s.storageDir, "headers.db")
	walletDB := filepath.Join(s.storageDir, "spv_wallet.db")

	switch confirm {
	case "RESCAN":
		if mode != "" && mode != "full" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "full" or omitted`})
			return
		}
		prefs := s.readSPVSyncPrefs()
		if body.UseCheckpoint != nil {
			prefs.UseCheckpoint = *body.UseCheckpoint
		}
		_ = s.writeSPVSyncPrefs(prefs)
		s.stopSPVNode()
		migrated, legacyBackup, migErr := s.migrateLegacyHeadersDB()
		if migErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": migErr.Error()})
			return
		}
		_ = os.Remove(walletDB)
		if !migrated {
			_ = os.Remove(headersPath)
		}
		_ = os.Remove(s.spvWatchAddrPath())
		s.wipeAuxiliaryWalletRuntimeState()
		s.startSPVNode(wf)
		out := map[string]any{
			"ok":              true,
			"action":          "full_rescan",
			"removed_headers": headersPath,
			"removed_wallet":  walletDB,
			"use_checkpoint":  prefs.UseCheckpoint,
			"note":            "SPV will rescan watched addresses after clearing local header + wallet DB state. With use_checkpoint true (spvnode -p), libdogecoin may seed sync from its embedded checkpoint table; with false, header sync starts from genesis in the block locator.",
		}
		if migrated && legacyBackup != "" {
			out["legacy_headers_renamed_to"] = legacyBackup
			out["note"] = "Unknown-format headers.db (legacy install) was renamed aside; SPV will create a new libdogecoin headers file from the bundled checkpoint and rescan watched addresses."
		}
		writeJSON(w, http.StatusOK, out)
		return

	case "ROLLBACK":
		hash := strings.ToLower(strings.TrimSpace(body.RollbackBlockHash))
		var keepHeight int64
		var heightSource string

		if body.RollbackHeight != nil {
			h := *body.RollbackHeight
			if h < 0 || h > 200_000_000 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rollback_height out of range (use 0 … 200000000)"})
				return
			}
			keepHeight = h
			heightSource = "rollback_height"
		} else if len(hash) == 64 && isHex64(hash) {
			logRaw, err := readFileTail(s.spvLogPath(), 16<<20)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "SPV header hash lookup unavailable; provide rollback_height or run full rescan"})
				return
			}
			h, ok := heightForHeaderHashInSPVLog(logRaw, hash)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rollback_block_hash not found in available SPV header history; paste rollback_height from an explorer, or use confirm RESCAN for a full header rebuild."})
				return
			}
			keepHeight = h
			heightSource = "spv_log"
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `ROLLBACK requires rollback_block_hash (64 hex) or rollback_height (0+)`})
			return
		}

		if _, err := os.Stat(headersPath); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":        "headers.db missing — rollback needs the on-disk header store",
				"headers_path": headersPath,
				"storage_dir":  s.storageDir,
				"hint":         "Dashboard chain tip comes from SPV REST status and metrics. Use Full SPV rescan (RESCAN), or wait until GET /api/spv/status shows headers_db_present true.",
			})
			return
		}

		if isLibdogecoinHeadersFileFormat(headersPath) {
			s.stopSPVNode()
			maxKept, newSz, terr := truncateLibdogecoinHeadersFile(headersPath, keepHeight)
			if terr == nil {
				s.wipeAuxiliaryWalletRuntimeState()
				s.startSPVNode(wf)
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":                  true,
					"action":              "rollback_headers_file",
					"keep_height":         keepHeight,
					"height_source":       heightSource,
					"rollback_block_hash": hash,
					"headers_db":          headersPath,
					"headers_db_format":   "libdogecoin_file_truncated",
					"max_height_kept":     int64(maxKept),
					"headers_file_bytes":  newSz,
					"removed_wallet_db":   true,
					"note": fmt.Sprintf("File-based headers.db (libdogecoin headersdb_file) was truncated after the last header at or below height %d. Local SPV wallet DB, watch files, tx cache, mempool tracker data, and logs were cleared so SPV can resync from the rolled-back chain tip.", keepHeight),
				})
				return
			}
			log.Printf("[pq-wallet] libdogecoin headers file truncate failed: %v; attempting legacy migrate", terr)
			migrated, legacyBackup, migErr := s.migrateLegacyHeadersDB()
			if migErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": migErr.Error()})
				return
			}
			if !migrated {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error":   fmt.Sprintf("rollback truncate failed: %v", terr),
					"headers": headersPath,
					"hint":    "headers.db is libdogecoin file format but could not be truncated; try Full SPV rescan (RESCAN).",
				})
				return
			}
			_ = os.Remove(walletDB)
			s.wipeAuxiliaryWalletRuntimeState()
			s.startSPVNode(wf)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":                        true,
				"action":                    "legacy_headers_migrated_after_truncate_fail",
				"legacy_headers_renamed_to": legacyBackup,
				"requested_keep_height":     keepHeight,
				"height_source":             heightSource,
				"rollback_block_hash":       hash,
				"truncate_error":            terr.Error(),
				"note": "Could not truncate libdogecoin headers.db in place; the file was renamed aside for a fresh header sync. " +
					"The chosen rollback height was not applied to the old file. After the node catches up, run Rollback again or use Full rescan.",
			})
			return
		}

		migrated, legacyBackup, migErr := s.migrateLegacyHeadersDB()
		if migErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": migErr.Error()})
			return
		}
		if !migrated {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "headers.db is not libdogecoin file format and could not be migrated (file missing or too small); retry or use RESCAN (full)"})
			return
		}
		_ = os.Remove(walletDB)
		s.wipeAuxiliaryWalletRuntimeState()
		s.startSPVNode(wf)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                        true,
			"action":                    "legacy_headers_migrated",
			"legacy_headers_renamed_to": legacyBackup,
			"requested_keep_height":     keepHeight,
			"height_source":             heightSource,
			"rollback_block_hash":       hash,
			"note": "Unknown-format headers.db was renamed aside for a fresh libdogecoin header sync. " +
				"The chosen rollback height was not applied to the old file. After headers exist again, run Rollback again or use Full rescan.",
		})
		return

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `unknown confirm value; use "RESCAN" or "ROLLBACK"`})
	}
}
