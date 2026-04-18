package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
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
		log.Printf("[pq-wallet] removed invalid headers.db directory (renamed to %s); SPV will recreate SQLite store from checkpoint", backup)
		return
	}
	if st.Size() < 100 {
		s.stopSPVNode()
		_ = os.Remove(headersPath)
		_ = os.Remove(filepath.Join(s.storageDir, "spv_wallet.db"))
		_ = os.Remove(s.spvWatchAddrPath())
		log.Printf("[pq-wallet] removed invalid tiny headers.db; SPV will recreate SQLite header store from checkpoint")
	}
}

// migrateLegacyHeadersDB stops spvnode, then if headers.db exists but is not SQLite (older libdogecoin layouts),
// renames it aside, removes spv_wallet.db and the watch-list file so the next spvnode start creates a fresh SQLite header store.
func (s *Server) migrateLegacyHeadersDB() (migrated bool, backupPath string, err error) {
	headersPath := filepath.Join(s.storageDir, "headers.db")
	st, statErr := os.Stat(headersPath)
	if statErr != nil {
		return false, "", nil
	}
	if st.IsDir() {
		return false, "", fmt.Errorf("headers.db is a directory")
	}
	// Do not treat a tiny file as "legacy": SQLite may still be initializing, and
	// renaming it would force a full header resync (often mistaken for "unlock wiped DB").
	if st.Size() < 100 {
		return false, "", nil
	}
	if isSQLiteDBFile(headersPath) {
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

func isSQLiteDBFile(path string) bool {
	b := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	n, err := f.Read(b)
	if err != nil || n < 16 {
		return false
	}
	return string(b[:15]) == "SQLite format 3" && b[15] == 0
}

func sqlite3ListTables(ctx context.Context, sqlite3Bin, dbPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, sqlite3Bin, "-bail", "-batch", dbPath, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

func sqlite3QuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func sqlite3PickHeightColumn(ctx context.Context, sqlite3Bin, dbPath, table string) string {
	q := fmt.Sprintf(`PRAGMA table_info(%s);`, sqlite3QuoteIdent(table))
	cmd := exec.CommandContext(ctx, sqlite3Bin, "-bail", "-batch", dbPath, q)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Lines like: 0|height|INTEGER|0||0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		col := strings.TrimSpace(parts[1])
		switch strings.ToLower(col) {
		case "height", "block_height", "n_height":
			return col
		default:
		}
	}
	return ""
}

// sqlite3DeleteHeadersAbove removes rows with height > keepHeight from every table that has a height-like column.
func sqlite3DeleteHeadersAbove(ctx context.Context, sqlite3Bin, dbPath string, keepHeight int64) ([]string, error) {
	if keepHeight < 0 {
		return nil, errors.New("invalid keep height")
	}
	tables, err := sqlite3ListTables(ctx, sqlite3Bin, dbPath)
	if err != nil {
		return nil, err
	}
	var touched []string
	for _, tbl := range tables {
		col := sqlite3PickHeightColumn(ctx, sqlite3Bin, dbPath, tbl)
		if col == "" {
			continue
		}
		del := fmt.Sprintf(`DELETE FROM %s WHERE %s > %d;`, sqlite3QuoteIdent(tbl), sqlite3QuoteIdent(col), keepHeight)
		cmd := exec.CommandContext(ctx, sqlite3Bin, "-bail", dbPath, del)
		if err := cmd.Run(); err != nil {
			return touched, fmt.Errorf("sqlite delete on %s: %w", tbl, err)
		}
		touched = append(touched, tbl)
	}
	return touched, nil
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

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	switch confirm {
	case "RESCAN":
		if mode != "" && mode != "full" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "full" or omitted`})
			return
		}
		if body.UseCheckpoint != nil {
			_ = s.writeSPVSyncPrefs(spvSyncPrefs{UseCheckpoint: *body.UseCheckpoint})
		}
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
		s.startSPVNode(wf)
		prefs := s.readSPVSyncPrefs()
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
			out["note"] = "Non-SQLite headers.db (legacy install) was renamed aside; SPV will create a new SQLite header store from the bundled checkpoint and rescan watched addresses."
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
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "spv.log not readable; cannot map block hash"})
				return
			}
			h, ok := heightForHeaderHashInSPVLog(logRaw, hash)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rollback_block_hash not found in recent spv.log (hash|height rows). Paste rollback_height from an explorer, or use confirm RESCAN for a full header rebuild."})
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
				"error":        "headers.db missing — rollback needs the SQLite header store on disk",
				"headers_path": headersPath,
				"storage_dir":  s.storageDir,
				"hint":         "Dashboard chain tip can come from spv.log (and metrics) before spvnode creates headers.db, or if SPV never wrote this file. Use Full SPV rescan (RESCAN), or wait until GET /api/spv/status shows headers_db_present true. Headers resume in the same directory: headers.db next to spv_wallet.db and spv.log (PQ_STORAGE_DIR, default /storage/pq-wallet in the pup).",
			})
			return
		}
		if !isSQLiteDBFile(headersPath) {
			migrated, legacyBackup, migErr := s.migrateLegacyHeadersDB()
			if migErr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": migErr.Error()})
				return
			}
			if !migrated {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "headers.db was not SQLite but disappeared before backup; retry or use RESCAN (full)"})
				return
			}
			_ = os.Remove(walletDB)
			s.startSPVNode(wf)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":                        true,
				"action":                    "legacy_headers_migrated",
				"legacy_headers_renamed_to": legacyBackup,
				"requested_keep_height":     keepHeight,
				"height_source":             heightSource,
				"rollback_block_hash":       hash,
				"note":                      "headers.db was not SQLite (legacy install). SPV was stopped, the file renamed aside, spv_wallet.db removed, and spvnode restarted. SQLite rollback was not applied to the old file. After headers sync, run rollback again if you still need to trim the new SQLite header chain.",
			})
			return
		}
		sqlite3Bin, err := exec.LookPath("sqlite3")
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sqlite3 CLI not found on PATH — install sqlite for rollback-from-height, or use RESCAN (full)"})
			return
		}

		s.stopSPVNode()
		tables, err := sqlite3DeleteHeadersAbove(ctx, sqlite3Bin, headersPath, keepHeight)
		if err != nil {
			s.startSPVNode(wf)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		_ = os.Remove(walletDB)
		_ = os.Remove(s.spvWatchAddrPath())
		s.startSPVNode(wf)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":                  true,
			"action":              "rollback_headers",
			"keep_height":         keepHeight,
			"height_source":       heightSource,
			"rollback_block_hash": hash,
			"headers_db":          headersPath,
			"tables_deleted_from": tables,
			"removed_wallet_db":   true,
			"note":                "Headers newer than the chosen block were removed; spv_wallet.db was deleted so the node can rescan filters and UTXOs from the rolled-back chain tip.",
		})
		return

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `unknown confirm value; use "RESCAN" or "ROLLBACK"`})
	}
}
