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

// spvFullResyncClearingHeadersAndWallet stops SPV, writes prefs, removes headers.db (or migrates legacy aside),
// clears SPV wallet DB and related state, and restarts spvnode. Caller must hold s.mu.
func (s *Server) spvFullResyncClearingHeadersAndWallet(wf *WalletFile, prefs spvSyncPrefs) (map[string]any, error) {
	headersPath := filepath.Join(s.storageDir, "headers.db")
	walletDB := filepath.Join(s.storageDir, "spv_wallet.db")
	s.stopSPVNode()
	if err := s.writeSPVSyncPrefs(prefs); err != nil {
		return nil, err
	}
	migrated, legacyBackup, migErr := s.migrateLegacyHeadersDB()
	if migErr != nil {
		return nil, migErr
	}
	if !migrated {
		_ = os.Remove(headersPath)
	}
	_ = os.Remove(walletDB)
	_ = os.Remove(s.spvWatchAddrPath())
	s.wipeAuxiliaryWalletRuntimeState()
	s.startSPVNode(wf)
	out := map[string]any{
		"ok":                      true,
		"action":                  "full_rescan",
		"removed_headers":         headersPath,
		"removed_wallet":          walletDB,
		"use_checkpoint":          prefs.UseCheckpoint,
		"restore_checkpoint_hint": prefs.RestoreCheckpointHint,
		"note": "SPV stopped; local headers.db and SPV wallet state cleared; SPV restarted. With use_checkpoint true and restore_checkpoint_hint > 0, the next run seeds that bundled checkpoint when headers.db is empty.",
	}
	if migrated && legacyBackup != "" {
		out["legacy_headers_renamed_to"] = legacyBackup
		out["note"] = "Unknown-format headers.db (legacy install) was renamed aside; SPV will create a new libdogecoin headers file and rescan watched addresses."
	}
	return out, nil
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
		// Also accepted on RESCAN to set restore_checkpoint_hint when rebuilding headers.
		RollbackHeight *int64 `json:"rollback_height"`
		UseCheckpoint  *bool  `json:"use_checkpoint"`
		PIN            string `json:"pin"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	confirm := strings.TrimSpace(body.Confirm)
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if confirm == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"confirm":"RESCAN"} (optional rollback_height, use_checkpoint) for full reset, or {"confirm":"ROLLBACK"} with rollback_block_hash or rollback_height (including 0 for genesis block)`})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.requireSealedWalletPINForAction(w, body.PIN) {
		return
	}
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
		testnet := strings.EqualFold(wf.Network, "testnet")
		prefs := s.readSPVSyncPrefs()
		var mergeErr error
		if body.RollbackHeight != nil {
			prefs, mergeErr = buildSPVPrefsForCheckpointResync(testnet, *body.RollbackHeight, body.UseCheckpoint, prefs)
		} else if body.UseCheckpoint != nil {
			prefs.UseCheckpoint = *body.UseCheckpoint
		}
		if mergeErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": mergeErr.Error()})
			return
		}
		out, rerr := s.spvFullResyncClearingHeadersAndWallet(wf, prefs)
		if rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": rerr.Error()})
			return
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

		testnet := strings.EqualFold(wf.Network, "testnet")

		_, statErr := os.Stat(headersPath)
		headersMissing := statErr != nil && errors.Is(statErr, os.ErrNotExist)

		tryHeaderRebuild := headersMissing
		if statErr == nil && isLibdogecoinHeadersFileFormat(headersPath) {
			if fh, fhErr := libdogecoinHeadersFileFirstHeight(headersPath); fhErr == nil && int64(fh) > keepHeight {
				tryHeaderRebuild = true
			}
		}

		if tryHeaderRebuild {
			prefs := s.readSPVSyncPrefs()
			var perr error
			if body.RollbackHeight != nil {
				prefs, perr = buildSPVPrefsForCheckpointResync(testnet, keepHeight, body.UseCheckpoint, s.readSPVSyncPrefs())
				if perr != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": perr.Error()})
					return
				}
			}
			out, rerr := s.spvFullResyncClearingHeadersAndWallet(wf, prefs)
			if rerr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": rerr.Error()})
				return
			}
			out["action"] = "rollback_via_header_rebuild"
			out["requested_rollback_height"] = keepHeight
			out["height_source"] = heightSource
			out["rollback_block_hash"] = hash
			if headersMissing {
				out["note"] = "headers.db was not present; SPV prefs were applied and local SPV wallet/header cache cleared so sync can start from your selected bundled checkpoint (when rollback_height was sent) or from existing SPV prefs."
			} else {
				out["note"] = "The stored header chain begins above the height you rolled back to (common when SPV previously synced from a newer bundled checkpoint). SPV was stopped, headers.db removed, prefs updated when you chose a bundled checkpoint height, local SPV wallet state cleared, and SPV restarted."
			}
			writeJSON(w, http.StatusOK, out)
			return
		}

		if statErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":        statErr.Error(),
				"headers_path": headersPath,
				"storage_dir":  s.storageDir,
				"hint":         "Could not read headers.db; check storage permissions.",
			})
			return
		}

		if isLibdogecoinHeadersFileFormat(headersPath) {
			s.stopSPVNode()
			maxKept, newSz, terr := truncateLibdogecoinHeadersFile(headersPath, keepHeight)
			if terr != nil {
				if strings.Contains(terr.Error(), "on-disk header chain starts at height") {
					prefs := s.readSPVSyncPrefs()
					var perr error
					if body.RollbackHeight != nil {
						prefs, perr = buildSPVPrefsForCheckpointResync(testnet, keepHeight, body.UseCheckpoint, prefs)
						if perr != nil {
							s.startSPVNode(wf)
							writeJSON(w, http.StatusConflict, map[string]any{
								"ok":    false,
								"error": terr.Error(),
								"hint":  perr.Error(),
							})
							return
						}
					}
					out, rerr := s.spvFullResyncClearingHeadersAndWallet(wf, prefs)
					if rerr != nil {
						s.startSPVNode(wf)
						writeJSON(w, http.StatusInternalServerError, map[string]string{"error": rerr.Error()})
						return
					}
					out["action"] = "rollback_via_header_rebuild"
					out["requested_rollback_height"] = keepHeight
					out["height_source"] = heightSource
					out["rollback_block_hash"] = hash
					out["note"] = "headers.db could not be truncated to that height; performed a full header store rebuild instead."
					writeJSON(w, http.StatusOK, out)
					return
				}
				s.startSPVNode(wf)
				writeJSON(w, http.StatusConflict, map[string]any{
					"ok":            false,
					"error":         terr.Error(),
					"headers_db":    headersPath,
					"keep_height":   keepHeight,
					"height_source": heightSource,
					"hint":          "Try a different rollback height, or POST {\"confirm\":\"RESCAN\"} for a full header rebuild (optionally with use_checkpoint).",
				})
				return
			}
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
