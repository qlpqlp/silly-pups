package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// publicDashboard returns a single JSON payload for the homepage: indexer row counts, PQ aggregates,
// Core mempool / hashrate / tip, optional circulation (cached gettxoutsetinfo), and the 10 latest txs.
func (a *app) publicDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	a.mu.RLock()
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	a.mu.RUnlock()

	out := map[string]any{
		"app_version": qeAppVersion,
		"build_hash":  qeAppBuildHash,
		"network":     net,
		"spec_bip":    "https://github.com/edtubbs/libdogecoin/blob/0.1.5-dev-pqc-carrier/doc/spec/bip-post-quantum-signature-commitments.mediawiki",
		"qdv_verify":  "https://suchquantum.com/qdv/",
	}

	if a.cidx != nil {
		sum := a.cidx.summary(ctx)
		out["indexer"] = sum
		out["pq_totals"] = a.cidx.pqAggregates(ctx)
		if txc, txr, err := a.cidx.pqPhase1RoleCounts(ctx); err == nil {
			out["pq_phase1_roles"] = map[string]int64{"committed_tx_c": txc, "revealed_tx_r": txr}
		}
		if txs, err := a.cidx.recentTransactions(ctx, 10, ""); err == nil {
			lite := make([]map[string]any, 0, len(txs))
			for _, row := range txs {
				txid := strings.ToLower(strings.TrimSpace(rowString(row, "txid")))
				rawHex := strings.TrimSpace(rowString(row, "raw_hex"))
				role := ""
				if hasCarrierRevealScriptSigRaw(rawHex) {
					role = "tx_r"
				} else if ok, _, _, _ := verifyPQStrict(rawHex); ok {
					role = "tx_c"
				}
				lite = append(lite, map[string]any{
					"txid":           txid,
					"block_height":   row["block_height"],
					"time_unix":      row["time_unix"],
					"quantum_state":  row["quantum_state"],
					"pq_reason":      row["pq_reason"],
					"value_out_sats": row["value_out_sats"],
					"pq_carrier_role_hint": role,
				})
			}
			out["latest_transactions"] = lite
		}
	} else {
		out["indexer"] = map[string]any{"enabled": false, "note": "PostgreSQL indexer not configured"}
	}

	if a.core != nil && a.core.enabled() {
		live := map[string]any{"rpc": true}
		var mem map[string]any
		if err := a.core.call(ctx, "getmempoolinfo", []any{}, &mem); err == nil {
			live["mempoolinfo"] = mem
		}
		var hps float64
		if err := a.core.call(ctx, "getnetworkhashps", []any{120, -1}, &hps); err == nil {
			live["network_hash_ps"] = hps
		}
		var bc map[string]any
		if err := a.core.call(ctx, "getblockchaininfo", []any{}, &bc); err == nil {
			live["blockchaininfo"] = bc
		}
		out["core_live"] = live
		a.maybeRefreshCirculation()
		out["circulation"] = a.peekCirculationDOGE()
		a.mu.RLock()
		mLim := a.cfg.MempoolRecentLimit
		a.mu.RUnlock()
		if mLim < 1 {
			mLim = 25
		}
		if mLim > 100 {
			mLim = 100
		}
		if rows, err := a.mempoolRecentRows(ctx, mLim, false); err == nil && rows != nil {
			out["mempool_recent"] = rows
		}
	} else {
		out["core_live"] = map[string]any{"rpc": false, "note": "Set QE_CORE_RPC_URL for live mempool, hashrate, and circulation."}
	}

	writeJSON(w, 200, out)
}

func (a *app) publicExplorerConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	bd := cfg.BlockDecodeLimitDefault
	if bd < 1 {
		bd = envIntBounded("QE_BLOCK_DECODE_LIMIT_DEFAULT", 200, 1, 500)
	}
	if bd > 500 {
		bd = 500
	}
	ml := cfg.MempoolRecentLimit
	if ml < 1 {
		ml = envIntBounded("QE_MEMPOOL_RECENT_LIMIT", 25, 1, 100)
	}
	if ml > 100 {
		ml = 100
	}
	writeJSON(w, 200, map[string]any{
		"network":                    cfg.Network,
		"block_decode_limit_default": bd,
		"mempool_recent_limit":       ml,
	})
}

func (a *app) publicAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		addr = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	if addr == "" {
		writeJSON(w, 400, map[string]string{"error": "provide address= or q="})
		return
	}
	offset := 0
	if s := strings.TrimSpace(r.URL.Query().Get("offset")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			offset = n
		}
	}
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 50, 1, 200)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	detail, err := a.cidx.addressDetail(ctx, addr, offset, limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	detail["source"] = "core_index"
	writeJSON(w, 200, detail)
}

func (a *app) publicMempoolRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.core == nil || !a.core.enabled() {
		writeJSON(w, 503, map[string]string{"error": "core rpc not configured"})
		return
	}
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 40, 1, 100)
	decodePQ := strings.TrimSpace(r.URL.Query().Get("decode_pq")) == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	rows, err := a.mempoolRecentRows(ctx, limit, decodePQ)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"source": "core_rpc", "decode_pq": decodePQ, "rows": rows, "limit": limit})
}

func (a *app) mempoolRecentRows(ctx context.Context, limit int, decodePQ bool) ([]map[string]any, error) {
	if a == nil || a.core == nil || !a.core.enabled() {
		return nil, fmt.Errorf("core rpc disabled")
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	type memTx struct {
		id   string
		t    float64
		meta map[string]any
	}
	var verbose map[string]map[string]any
	var txs []memTx
	if err := a.core.call(ctx, "getrawmempool", []any{true}, &verbose); err == nil && len(verbose) > 0 {
		for id, meta := range verbose {
			id = strings.ToLower(strings.TrimSpace(id))
			if len(id) != 64 || !isHex64String(id) {
				continue
			}
			var tv float64
			if meta != nil {
				switch x := meta["time"].(type) {
				case float64:
					tv = x
				case int:
					tv = float64(x)
				case int64:
					tv = float64(x)
				case json.Number:
					if f, e := x.Float64(); e == nil {
						tv = f
					}
				}
			}
			txs = append(txs, memTx{id: id, t: tv, meta: meta})
		}
	}
	if len(txs) == 0 {
		var flat []string
		if err := a.core.call(ctx, "getrawmempool", []any{false}, &flat); err == nil {
			for _, id := range flat {
				id = strings.ToLower(strings.TrimSpace(id))
				if len(id) != 64 || !isHex64String(id) {
					continue
				}
				txs = append(txs, memTx{id: id, t: 0, meta: nil})
			}
		}
	}
	sort.Slice(txs, func(i, j int) bool {
		if txs[i].t != txs[j].t {
			return txs[i].t > txs[j].t
		}
		return txs[i].id > txs[j].id
	})
	if len(txs) > limit {
		txs = txs[:limit]
	}
	out := make([]map[string]any, 0, len(txs))
	for i, x := range txs {
		row := map[string]any{"txid": x.id, "mempool_time": x.t}
		if x.meta != nil {
			row["mempool_entry"] = x.meta
			if s, ok := x.meta["size"].(float64); ok {
				row["size"] = int64(s)
			}
			if s, ok := x.meta["vsize"].(float64); ok {
				row["vsize"] = int64(s)
			}
			if f, ok := x.meta["fee"].(float64); ok {
				row["fee"] = f
			}
		}
		if decodePQ && i < 40 {
			if raw, err := a.core.getRawTransactionHex(ctx, x.id, ""); err == nil && strings.TrimSpace(raw) != "" {
				row["size_bytes"] = len(raw) / 2
				ok, reas, _, _ := verifyPQStrict(raw)
				row["pq_strict_valid"] = ok
				row["pq_strict_reason"] = reas
			}
		}
		out = append(out, row)
	}
	return out, nil
}

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
	lite := strings.TrimSpace(r.URL.Query().Get("lite")) == "1"
	fetchLimit := limit
	if mode == "quantum" {
		fetchLimit = limit * 4
		if fetchLimit > 500 {
			fetchLimit = 500
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	recentMode := ""
	if mode == "quantum" {
		// Primary source for quantum feed: DB-classified quantum rows.
		// This prevents starvation when latest non-quantum volume is high.
		recentMode = "quantum"
	}
	rows, err := a.cidx.recentTransactions(ctx, fetchLimit, recentMode)
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
		if lite {
			rawHex := strings.TrimSpace(rowString(row, "raw_hex"))
			role := strings.TrimSpace(rowString(row, "pq_carrier_role"))
			if role == "" {
				if hasCarrierRevealScriptSigRaw(rawHex) {
					role = "tx_r"
				} else if ok, _, _, _ := verifyPQStrict(rawHex); ok {
					role = "tx_c"
				}
			}
			row["pq_carrier_role"] = role
			row["pq_valid"] = strings.EqualFold(strings.TrimSpace(rowString(row, "quantum_state")), "quantum")
			if role == "tx_r" {
				row["carrier_verified"] = true
			}
			enriched = append(enriched, row)
			continue
		}
		rawHex, qState, pqReason, blkH, _, _, _, okRow, err := a.cidx.txRowByID(ctx, txid)
		if err != nil || !okRow {
			// Preserve fallback rows instead of dropping everything on transient decode/index misses.
			row["pq_carrier_role"] = pqCarrierTXRole(row)
			enriched = append(enriched, row)
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
		// Top-up with carrier-linked rows from the generic recent stream when needed.
		// This keeps TX_R entries visible even when quantum_state is not "quantum".
		if len(rows) < limit {
			extraRows, extraErr := a.cidx.recentTransactions(ctx, fetchLimit, "")
			if extraErr == nil {
				seen := make(map[string]struct{}, len(rows))
				for _, row := range rows {
					txid := strings.ToLower(strings.TrimSpace(rowString(row, "txid")))
					if len(txid) == 64 && isHex64String(txid) {
						seen[txid] = struct{}{}
					}
				}
				for _, row := range extraRows {
					txid := strings.ToLower(strings.TrimSpace(rowString(row, "txid")))
					if len(txid) != 64 || !isHex64String(txid) {
						continue
					}
					if _, ok := seen[txid]; ok {
						continue
					}
					rows = append(rows, row)
					seen[txid] = struct{}{}
				}
			}
		}
		filtered := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if strings.EqualFold(strings.TrimSpace(rowString(row, "pq_carrier_role")), "tx_r") {
				filtered = append(filtered, row)
				continue
			}
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
