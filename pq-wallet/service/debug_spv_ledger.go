package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const debugLedgerRESTMax = 256 * 1024

func truncateDebugString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("…(%d bytes total)", len(s))
}

func spvStatusMapForDebug(spv map[string]any, logTailMax int) map[string]any {
	if spv == nil {
		return nil
	}
	out := make(map[string]any, len(spv)+1)
	for k, v := range spv {
		if k == "log_tail" {
			if s, ok := v.(string); ok && logTailMax > 0 && len(s) > logTailMax {
				out[k] = truncateDebugString(s, logTailMax)
				out["log_tail_truncated"] = true
				out["log_tail_original_bytes"] = len(s)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func spvRESTTxRowsSummary(rows []spvRESTTxRow, limit int) []map[string]any {
	if limit <= 0 {
		limit = 200
	}
	var out []map[string]any
	for i, r := range rows {
		if i >= limit {
			break
		}
		out = append(out, map[string]any{
			"txid":          normalizeTxid(r.Txid),
			"direction":     strings.ToLower(strings.TrimSpace(r.Direction)),
			"amount_doge":   r.AmountDOGE,
			"address":       r.Address,
			"confirmations": r.Confirmations,
			"vout":          r.Vout,
			"seen_at_unix": func() int64 {
				if r.SeenAt.IsZero() {
					return 0
				}
				return r.SeenAt.UTC().Unix()
			}(),
		})
	}
	return out
}

// handleDebugSPVLedger returns a JSON snapshot of SPV REST bodies, truncated log tail, headers.db height probe,
// persisted state tx summary, and broadcast.log-derived outgoing txids (for comparing with the UI list).
func (s *Server) handleDebugSPVLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	_, werr := s.loadWallet()
	s.mu.Unlock()
	if werr != nil {
		if errors.Is(werr, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wallet locked"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": werr.Error()})
		return
	}

	spv := s.readSPVStatus()
	out := map[string]any{
		"spv_status": spvStatusMapForDebug(spv, 24*1024),
	}

	rawTip, errTip := s.fetchSPVREST("/getChaintip")
	rawTS, errTS := s.fetchSPVREST("/getTimestamp")
	rawTx, errTx := s.fetchSPVREST("/getTransactions")
	rawUtxo, errUtxo := s.fetchSPVREST("/getUTXOs")

	out["rest"] = map[string]any{
		"getChaintip": map[string]any{
			"error": errString(errTip),
			"raw":   truncateDebugString(rawTip, debugLedgerRESTMax),
		},
		"getTimestamp": map[string]any{
			"error": errString(errTS),
			"raw":   truncateDebugString(rawTS, debugLedgerRESTMax),
		},
		"getTransactions": map[string]any{
			"error": errString(errTx),
			"raw":   truncateDebugString(rawTx, debugLedgerRESTMax),
		},
		"getUTXOs": map[string]any{
			"error": errString(errUtxo),
			"raw":   truncateDebugString(rawUtxo, debugLedgerRESTMax),
		},
	}

	th, bh := parseSPVRESTChaintip(rawTip)
	txRows := parseSPVRESTRows(rawTx, "unknown")
	utxoRows := parseSPVRESTRows(rawUtxo, "in")
	out["parsed"] = map[string]any{
		"chaintip_height": th,
		"chaintip_hash":   bh,
		"timestamp_unix":  parseSPVRESTTimestamp(rawTS),
		"tx_rows":         spvRESTTxRowsSummary(txRows, 300),
		"utxo_rows":       spvRESTTxRowsSummary(utxoRows, 300),
		"tx_row_count":    len(txRows),
		"utxo_row_count":  len(utxoRows),
		"tx_parse_note":   "Rows are parsed heuristically from plain-text REST; compare raw.*.raw when in doubt.",
	}

	hdb := filepath.Join(s.storageDir, "headers.db")
	hmeta := map[string]any{"path": hdb}
	if st, err := os.Stat(hdb); err == nil {
		hmeta["size"] = st.Size()
		hmeta["is_dir"] = st.IsDir()
		hmeta["sqlite"] = isSQLiteDBFile(hdb)
		hmeta["max_height_sqlite_probe"] = s.sqliteHeadersDBMaxHeight()
	} else {
		hmeta["stat_error"] = err.Error()
	}
	out["headers_db"] = hmeta

	s.stateMergeMu.Lock()
	st, _ := s.loadState()
	s.stateMergeMu.Unlock()
	txSample := []map[string]any{}
	n := 0
	if st != nil {
		n = len(st.Transactions)
		for i, t := range st.Transactions {
			if i >= 120 {
				break
			}
			seenAt := ""
			if !t.SeenAt.IsZero() {
				seenAt = t.SeenAt.UTC().Format(time.RFC3339Nano)
			}
			txSample = append(txSample, map[string]any{
				"txid":          t.Txid,
				"direction":     t.Direction,
				"amount_doge":   t.AmountDOGE,
				"confirmations": t.Confirmations,
				"source":        t.Source,
				"seen_at":       seenAt,
			})
		}
	}
	out["state"] = map[string]any{
		"transactions_count":  n,
		"transactions_sample": txSample,
	}

	bcTail, bcErr := readFileTail(s.broadcastLogPath(), 2<<20)
	bcIDs := []string{}
	if bcErr == nil {
		bcIDs = extractBroadcastOutTxidsFromTail(bcTail)
	}
	out["broadcast_log"] = map[string]any{
		"path":              s.broadcastLogPath(),
		"tail_read_error":   errString(bcErr),
		"tail_bytes":        len(bcTail),
		"txids_from_log":    bcIDs,
		"virtual_db_query":  "GET /api/debug/spv-wallet-db?op=query&q=SELECT%20*%20FROM%20transactions — virtual tables when spv_wallet.db is not SQLite",
	}

	writeJSON(w, http.StatusOK, out)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
