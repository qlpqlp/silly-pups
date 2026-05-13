package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const debugWalletDBMaxRows = 200

func debugVirtualQueryAllowed(q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", errors.New("empty query")
	}
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

func nonSQLiteDebugPayload(path, op string) map[string]any {
	return map[string]any{
		"path":                path,
		"format":              "libdogecoin_wallet_file",
		"requested_operation": op,
		"hint":                "libdogecoin spv_wallet.db is a binary wallet file in this pup. Use virtual tables (addresses, utxos, transactions, chaintip, timestamp) backed by such + SPV REST.",
	}
}

func extractFromClauseTable(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return ""
	}
	parts := strings.Fields(q)
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "from" {
			t := strings.Trim(parts[i+1], "`\"'[]();")
			return t
		}
	}
	return ""
}

func nonSQLiteVirtualTableNames() []string {
	return []string{"addresses", "utxos", "transactions", "chaintip", "timestamp"}
}

func (s *Server) nonSQLiteVirtualRows(wf *WalletFile, table string) ([]map[string]any, error) {
	table = strings.ToLower(strings.TrimSpace(table))
	testnet := false
	if wf != nil {
		testnet = strings.EqualFold(wf.Network, "testnet")
	}
	switch table {
	case "addresses":
		addrs := []map[string]any{}
		if wf != nil {
			for _, a := range wf.AllDistinctP2PKHAddresses() {
				a = strings.TrimSpace(a)
				if a == "" {
					continue
				}
				addrs = append(addrs, map[string]any{"address": a})
			}
		}
		return addrs, nil
	case "utxos":
		rows := []map[string]any{}
		if wf == nil {
			return rows, nil
		}
		for _, addr := range wf.AllDistinctP2PKHAddresses() {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			utxos, err := s.runSuchListUnspent(addr, testnet)
			if err != nil {
				continue
			}
			for _, u := range utxos {
				rows = append(rows, map[string]any{
					"txid":       normalizeTxid(u.TxID),
					"vout":       u.Vout,
					"address":    addr,
					"value_koinu": u.Value,
					"amount_doge": float64(u.Value) / 1e8,
					"script":     u.ScriptPubHex,
				})
			}
		}
		return rows, nil
	case "transactions":
		all := []map[string]any{}
		if raw, err := s.fetchSPVREST("/getUTXOs"); err == nil {
			for _, r := range parseSPVRESTRows(raw, "unknown") {
				dir := strings.TrimSpace(strings.ToLower(r.Direction))
				if dir == "" {
					dir = "unknown"
				}
				all = append(all, map[string]any{
					"txid":          normalizeTxid(r.Txid),
					"address":       r.Address,
					"direction":     dir,
					"amount_doge":   r.AmountDOGE,
					"confirmations": r.Confirmations,
				})
			}
		}
		if raw, err := s.fetchSPVREST("/getTransactions"); err == nil {
			for _, r := range parseSPVRESTRows(raw, "unknown") {
				dir := strings.TrimSpace(strings.ToLower(r.Direction))
				if dir == "" {
					dir = "unknown"
				}
				all = append(all, map[string]any{
					"txid":          normalizeTxid(r.Txid),
					"address":       r.Address,
					"direction":     dir,
					"amount_doge":   r.AmountDOGE,
					"confirmations": r.Confirmations,
				})
			}
		}
		return all, nil
	case "chaintip":
		raw, err := s.fetchSPVREST("/getChaintip")
		if err != nil {
			return nil, err
		}
		h, bh := parseSPVRESTChaintip(raw)
		return []map[string]any{{"height": h, "hash": bh}}, nil
	case "timestamp":
		raw, err := s.fetchSPVREST("/getTimestamp")
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"timestamp_unix": parseSPVRESTTimestamp(raw)}}, nil
	default:
		return nil, fmt.Errorf("unknown virtual table: %s", table)
	}
}

func (s *Server) handleDebugSPVWalletDB(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	if s.strictSettingsPeekBlocked() {
		writeStrictSettingsAuthRequired(w)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
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
		p := nonSQLiteDebugPayload(dbPath, "meta")
		p["virtual_tables"] = nonSQLiteVirtualTableNames()
		writeJSON(w, http.StatusOK, p)
		return
	}
	switch op {
	case "tables":
		t := nonSQLiteVirtualTableNames()
		sort.Strings(t)
		writeJSON(w, http.StatusOK, map[string]any{
			"path":            dbPath,
			"format":          "libdogecoin_wallet_file",
			"virtual_tables":  t,
			"tables":          t,
			"note":            "Virtual tables are derived from libdogecoin such/spv REST data sources.",
		})
		return
	case "table_info":
		tbl := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("table")))
		if tbl == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing table"})
			return
		}
		rows, err := s.nonSQLiteVirtualRows(wf, tbl)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		cols := []string{}
		if len(rows) > 0 {
			for k := range rows[0] {
				cols = append(cols, k)
			}
			sort.Strings(cols)
		}
		writeJSON(w, http.StatusOK, map[string]any{"table": tbl, "columns": cols, "row_count": len(rows), "virtual": true})
		return
	case "query":
		qin := strings.TrimSpace(r.URL.Query().Get("q"))
		if _, err := debugVirtualQueryAllowed(qin); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		tbl := extractFromClauseTable(qin)
		if tbl == "" {
			tbl = strings.ToLower(strings.TrimSpace(qin))
		}
		rows, err := s.nonSQLiteVirtualRows(wf, tbl)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "use virtual table names or SELECT ... FROM <table>; available: addresses, utxos, transactions, chaintip, timestamp"})
			return
		}
		truncated := false
		if len(rows) > debugWalletDBMaxRows {
			truncated = true
			rows = rows[:debugWalletDBMaxRows]
		}
		writeJSON(w, http.StatusOK, map[string]any{"table": tbl, "rows": rows, "row_count": len(rows), "truncated": truncated, "virtual": true})
		return
	default:
		p := nonSQLiteDebugPayload(dbPath, op)
		p["virtual_tables"] = nonSQLiteVirtualTableNames()
		writeJSON(w, http.StatusOK, p)
		return
	}
}
