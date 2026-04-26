package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *app) withPublicAccess(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.publicEndpointAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next(w, r)
	}
}

func (a *app) coreIndexerStatus() map[string]any {
	if a.cidx == nil {
		return map[string]any{
			"enabled": false,
			"note":    "Core indexer requires PostgreSQL chain backend (QE_POSTGRES_URL) and Core RPC env vars.",
		}
	}
	return a.cidx.status()
}

func (a *app) publicCoreSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 25
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out, err := a.cidx.search(ctx, q, limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, out)
}

func (a *app) publicCoreSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	if a.cidx == nil {
		writeJSON(w, 200, map[string]any{"indexer": a.coreIndexerStatus(), "core_rpc": a.core.snapshot()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"indexer":  a.cidx.summary(ctx),
		"core_rpc": a.core.snapshot(),
	})
}

// pqCarrierTXRole classifies carrier-flow rows for the homepage: TX_C (Phase-1 OP_RETURN commitment)
// vs TX_R (reveal that matched a commitment or carried verified carrier material).
func pqCarrierTXRole(row map[string]any) string {
	if row == nil {
		return ""
	}
	txcLink := strings.TrimSpace(rowString(row, "matched_txc_txid"))
	txrLink := strings.TrimSpace(rowString(row, "matched_txr_txid"))
	pq, _ := row["pq_verification"].(map[string]any)
	var strictOK, carVer bool
	if pq != nil {
		if st, ok := pq["strict"].(map[string]any); ok {
			if v, ok := st["valid"].(bool); ok {
				strictOK = v
			}
		}
		if car, ok := pq["carrier_phase1"].(map[string]any); ok {
			if v, ok := car["verified"].(bool); ok {
				carVer = v
			}
		}
	}
	if txcLink != "" || carVer {
		return "tx_r"
	}
	if txrLink != "" || (strings.EqualFold(rowString(row, "quantum_state"), "quantum") && strictOK) {
		return "tx_c"
	}
	return ""
}

func (a *app) publicCoreRecentTxs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	limit := 100
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	fetchLimit := limit
	if mode == "quantum" {
		fetchLimit = limit * 4
		if fetchLimit > 500 {
			fetchLimit = 500
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rows, err := a.cidx.recentTransactions(ctx, fetchLimit, "")
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	enriched := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		txid := strings.ToLower(strings.TrimSpace(rowString(row, "txid")))
		if len(txid) != 64 || !isHex64String(txid) {
			continue
		}
		rawHex, qState, pqReason, blkH, _, _, _, okRow, err := a.cidx.txRowByID(ctx, txid)
		if err != nil || !okRow {
			continue
		}
		row["quantum_state"] = qState
		row["pq_reason"] = pqReason
		pq := buildPQVerificationDetail(rawHex, net)
		pq = a.enrichDecodeWithPrevouts(ctx, pq)
		pq = a.enrichCarrierVerification(ctx, pq, txid, blkH, rawHex)
		pq = a.enrichReverseCarrierVerification(ctx, pq, txid, blkH)
		row["pq_verification"] = pq
		if car, ok := pq["carrier_phase1"].(map[string]any); ok {
			if fcv, ok := car["falcon_crypto_verify"].(map[string]any); ok {
				row["falcon_status"] = strings.ToLower(strings.TrimSpace(rowString(fcv, "status")))
			}
			row["matched_txc_txid"] = strings.ToLower(strings.TrimSpace(rowString(car, "matched_txc_txid")))
		}
		if rev, ok := pq["carrier_reverse_phase1"].(map[string]any); ok {
			row["matched_txr_txid"] = strings.ToLower(strings.TrimSpace(rowString(rev, "matched_txr_txid")))
		}
		row["pq_carrier_role"] = pqCarrierTXRole(row)
		enriched = append(enriched, row)
	}
	rows = enriched
	if mode == "quantum" {
		filtered := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if rowHasQuantumPQ(row) {
				filtered = append(filtered, row)
				continue
			}
			if strings.EqualFold(rowString(row, "quantum_state"), "quantum") {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, 200, map[string]any{
		"rows":  rows,
		"limit": limit,
		"mode":  mode,
	})
}

func rowString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(m[key]))
}

func (a *app) publicCoreRecentBlocks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	limit := 30
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := a.cidx.recentBlocks(ctx, limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"rows":  rows,
		"limit": limit,
	})
}

func (a *app) adminStartCoreIndexer(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	if err := a.cidx.start(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": a.cidx.status()})
}

func (a *app) adminStopCoreIndexer(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	a.cidx.stop()
	writeJSON(w, 200, map[string]any{"ok": true, "status": a.cidx.status()})
}

func (a *app) adminCoreIndexerStatus(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	writeJSON(w, 200, a.cidx.summary(ctx))
}

// adminCoreIndexerRewind POST: delete indexed Core chain rows from ?from_height= onward and reset last_height (indexer must be stopped).
func (a *app) adminCoreIndexerRewind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	fs := strings.TrimSpace(r.URL.Query().Get("from_height"))
	if fs == "" {
		writeJSON(w, 400, map[string]string{"error": "missing from_height query parameter (block height to delete from, inclusive)"})
		return
	}
	from, err := strconv.ParseInt(fs, 10, 64)
	if err != nil || from < 0 {
		writeJSON(w, 400, map[string]string{"error": "from_height must be a non-negative integer"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	if err := a.cidx.rewindChainFrom(ctx, from); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "indexer": a.cidx.status()})
}
