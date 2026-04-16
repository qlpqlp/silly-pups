package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func (a *app) adminPostgresSQLDB() *sql.DB {
	if pg, ok := a.chain.(*postgresChainBackend); ok && pg != nil {
		return pg.db
	}
	return nil
}

func (a *app) adminDBOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	db := a.adminPostgresSQLDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "PostgreSQL chain backend not active (set QE_POSTGRES_URL)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, "VACUUM (ANALYZE)"); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "VACUUM (ANALYZE) finished for this database."})
}

func (a *app) adminDBExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	if a.adminPostgresSQLDB() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "PostgreSQL chain backend not active (set QE_POSTGRES_URL)"})
		return
	}
	dsn := strings.TrimSpace(os.Getenv("QE_POSTGRES_URL"))
	if dsn == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "QE_POSTGRES_URL not set"})
		return
	}
	pgDump, err := exec.LookPath("pg_dump")
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pg_dump not found on PATH (install PostgreSQL client tools)"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Hour)
	defer cancel()
	tmp, err := os.CreateTemp(a.storageDir, "qe-export-*.dump")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	cmd := exec.CommandContext(ctx, pgDump, "--dbname="+dsn, "-Fc", "--no-owner", "--no-acl", "-f", tmpPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "detail": strings.TrimSpace(string(out))})
		return
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fname := "quantum_explorer_" + time.Now().UTC().Format("20060102_150405") + ".dump"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	http.ServeContent(w, r, fname, st.ModTime(), f)
}

func (a *app) adminDBImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if a.adminPostgresSQLDB() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "PostgreSQL chain backend not active (set QE_POSTGRES_URL)"})
		return
	}
	dsn := strings.TrimSpace(os.Getenv("QE_POSTGRES_URL"))
	if dsn == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "QE_POSTGRES_URL not set"})
		return
	}
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "multipart: " + err.Error()})
		return
	}
	fh, hdr, err := r.FormFile("dump")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing form field \"dump\" (upload file)"})
		return
	}
	defer fh.Close()

	if a.cidx != nil {
		a.cidx.stop()
	}

	tmp, err := os.CreateTemp(a.storageDir, "qe-import-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := io.Copy(tmp, io.LimitReader(fh, 512<<20)); err != nil {
		_ = tmp.Close()
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := tmp.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	head := make([]byte, 5)
	hf, err := os.Open(tmpPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_, _ = hf.Read(head)
	_ = hf.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Hour)
	defer cancel()

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	var cmd *exec.Cmd
	if string(head) == "PGDMP" {
		pgRestore, err := exec.LookPath("pg_restore")
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pg_restore not found on PATH"})
			return
		}
		args := []string{"--dbname=" + dsn, "--no-owner", "--exit-on-error", tmpPath}
		if mode == "clean" {
			args = append([]string{"--clean", "--if-exists"}, args...)
		}
		cmd = exec.CommandContext(ctx, pgRestore, args...)
	} else {
		psql, err := exec.LookPath("psql")
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "psql not found on PATH"})
			return
		}
		cmd = exec.CommandContext(ctx, psql, "--dbname="+dsn, "-v", "ON_ERROR_STOP=1", "-f", tmpPath)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":     false,
			"error":  err.Error(),
			"detail": strings.TrimSpace(string(out)),
			"file":   hdr.Filename,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"file":     hdr.Filename,
		"note":     "Import finished. Restart the explorer process and start the core indexer again if needed.",
		"pg_log":   strings.TrimSpace(string(out)),
		"indexer":  "stopped before import (if indexer was configured)",
	})
}

func adminSQLAllowed(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if low == "" {
		return false
	}
	if strings.Contains(low, "explain") && strings.Contains(low, "analyze") {
		return false
	}
	fields := strings.Fields(low)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "select", "with", "explain", "show", "values", "table":
		return true
	default:
		return false
	}
}

func (a *app) adminDBQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	db := a.adminPostgresSQLDB()
	if db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "PostgreSQL chain backend not active (set QE_POSTGRES_URL)"})
		return
	}
	var body struct {
		SQL   string `json:"sql"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	sqlText := strings.TrimSpace(body.SQL)
	sqlText = strings.TrimSuffix(sqlText, ";")
	if strings.Contains(sqlText, ";") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only one statement; remove internal semicolons"})
		return
	}
	if !adminSQLAllowed(sqlText) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only read-only SELECT/WITH/EXPLAIN/SHOW/VALUES/TABLE queries are allowed"})
		return
	}
	limit := body.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, sqlText)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	outRows := make([][]any, 0, min(limit, 64))
	n := 0
	truncated := false
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		n++
		if n > limit {
			truncated = true
			continue
		}
		row := make([]any, len(cols))
		for i, v := range raw {
			row[i] = adminSQLCellJSON(v)
		}
		outRows = append(outRows, row)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = tx.Rollback()

	writeJSON(w, http.StatusOK, map[string]any{
		"columns":   cols,
		"rows":      outRows,
		"row_count": len(outRows),
		"limit":     limit,
		"truncated": truncated,
	})
}

func adminSQLCellJSON(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []byte:
		s := string(t)
		if strings.ContainsRune(s, 0) {
			return map[string]string{"encoding": "base64", "data": base64.StdEncoding.EncodeToString(t)}
		}
		return s
	case string:
		return t
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	default:
		return t
	}
}
