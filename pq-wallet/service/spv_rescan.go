package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
		Confirm             string `json:"confirm"`
		Mode                string `json:"mode"`
		RollbackBlockHash   string `json:"rollback_block_hash"`
		RollbackHeight      int64  `json:"rollback_height"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	confirm := strings.TrimSpace(body.Confirm)
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if confirm == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"confirm":"RESCAN"} for full reset or {"confirm":"ROLLBACK"} with rollback_block_hash or rollback_height`})
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
		s.stopSPVNode()
		_ = os.Remove(walletDB)
		_ = os.Remove(headersPath)
		_ = os.Remove(s.spvWatchAddrPath())
		s.startSPVNode(wf)
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":              true,
			"action":          "full_rescan",
			"removed_headers": headersPath,
			"removed_wallet":  walletDB,
			"note":            "SPV will rebuild headers from the bundled checkpoint and rescan watched addresses.",
		})
		return

	case "ROLLBACK":
		hash := strings.ToLower(strings.TrimSpace(body.RollbackBlockHash))
		height := body.RollbackHeight
		var keepHeight int64
		var heightSource string

		if height > 0 {
			if height > 200_000_000 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rollback_height out of range"})
				return
			}
			keepHeight = height
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
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `ROLLBACK requires rollback_block_hash (64 hex) or rollback_height (positive)`})
			return
		}

		if _, err := os.Stat(headersPath); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "headers.db missing — use RESCAN (full) instead"})
			return
		}
		if !isSQLiteDBFile(headersPath) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "headers.db is not a SQLite file; use RESCAN (full) to reset SPV storage"})
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
			"ok":                 true,
			"action":             "rollback_headers",
			"keep_height":        keepHeight,
			"height_source":      heightSource,
			"rollback_block_hash": hash,
			"headers_db":         headersPath,
			"tables_deleted_from": tables,
			"removed_wallet_db":  true,
			"note":               "Headers newer than the chosen block were removed; spv_wallet.db was deleted so the node can rescan filters and UTXOs from the rolled-back chain tip.",
		})
		return

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `unknown confirm value; use "RESCAN" or "ROLLBACK"`})
	}
}
