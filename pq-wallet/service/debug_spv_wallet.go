package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const debugSQLiteMaxRows = 200

func debugSQLiteQueryAllowed(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", errors.New("empty query")
	}
	// Strip a single trailing semicolon (sqlite3 accepts it).
	if strings.HasSuffix(q, ";") {
		q = strings.TrimSpace(strings.TrimSuffix(q, ";"))
	}
	low := strings.ToLower(q)
	if strings.Contains(q, ";") {
		return "", errors.New("multiple statements are not allowed")
	}
	if strings.HasPrefix(low, "pragma ") {
		return q, nil
	}
	if strings.HasPrefix(low, "select ") || strings.HasPrefix(low, "with ") {
		if strings.Contains(low, " into ") {
			return "", errors.New("SELECT INTO is not allowed")
		}
		return q, nil
	}
	return "", errors.New("only SELECT, WITH … SELECT, or PRAGMA queries are allowed")
}

func (s *Server) handleDebugSPVWalletDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadWallet(); err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wallet locked"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	st, err := os.Stat(dbPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "spv_wallet.db not found"})
		return
	}
	if st.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is not a file"})
		return
	}
	op := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("op")))
	if op == "" || op == "meta" {
		if !isSQLiteDatabaseFile(dbPath) {
			writeJSON(w, http.StatusOK, map[string]any{
				"path":   dbPath,
				"format": "libdogecoin_binary_or_non_sqlite",
				"hint":   "Default libdogecoin wallet path uses a binary wallet file. Transaction rows are merged via `such list_unspent`; SQLite tools apply only if this file is actual SQLite.",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path":   dbPath,
			"format": "sqlite3",
		})
		return
	}
	if !isSQLiteDatabaseFile(dbPath) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "spv_wallet.db is not SQLite — query API disabled"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	switch op {
	case "tables":
		rows, err := s.sqliteRows(ctx, dbPath, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tables": rows})
	case "table_info":
		tbl := strings.TrimSpace(r.URL.Query().Get("table"))
		if tbl == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing table"})
			return
		}
		rows, err := s.sqliteRows(ctx, dbPath, "PRAGMA table_info("+sqliteIdent(tbl)+")")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"table": tbl, "table_info": rows})
	case "query":
		q, err := debugSQLiteQueryAllowed(r.URL.Query().Get("q"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		limited := q
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(q)), "pragma") {
			limited = q + " LIMIT " + fmt.Sprintf("%d", debugSQLiteMaxRows+1)
		}
		rows, err := s.sqliteRows(ctx, dbPath, limited)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		truncated := false
		if len(rows) > debugSQLiteMaxRows {
			truncated = true
			rows = rows[:debugSQLiteMaxRows]
		}
		writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "row_count": len(rows), "truncated": truncated})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown op (meta, tables, table_info, query)"})
	}
}
