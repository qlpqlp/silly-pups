package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// txListRow is one row for /api/transactions (SPV + MemeTracker).
type txListRow struct {
	TxRecord
	Pending bool `json:"pending"`
}

// shouldRunSuchMerge throttles expensive such list_unspent merges.
// Frequent dashboard/transactions polls should not repeatedly spawn such.
func (s *Server) shouldRunSuchMerge(minInterval time.Duration) bool {
	if minInterval <= 0 {
		minInterval = 20 * time.Second
	}
	s.suchMergeMu.Lock()
	defer s.suchMergeMu.Unlock()
	if time.Since(s.lastSuchMerge) < minInterval {
		return false
	}
	s.lastSuchMerge = time.Now()
	return true
}

func (s *Server) latestSuchSpendable(maxAge time.Duration) (float64, bool) {
	if maxAge <= 0 {
		maxAge = 3 * time.Minute
	}
	s.suchMergeMu.Lock()
	defer s.suchMergeMu.Unlock()
	if s.lastSuchSpendableAt.IsZero() {
		return 0, false
	}
	if time.Since(s.lastSuchSpendableAt) > maxAge {
		return 0, false
	}
	return s.lastSuchSpendableDOGE, true
}

// handleDashboard returns balances, SPV header parse, metrics tail, and merged tx summary.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			s.startSPVNodeFromWatchState()
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil, "locked": true, "sealed": true})
			return
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil})
		return
	}
	if wf == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil})
		return
	}
	svcPrefs := s.readServicePrefs()
	s.startSPVNode(wf)
	s.mu.Unlock()

	// SPV REST + merges can take many seconds; never hold s.mu across that work or other tabs
	// (and PQ send) will block on the same mutex and appear frozen.
	spv := s.readSPVStatus()
	logTail, _ := spv["log_tail"].(string)
	hdr := parseSPVLogHeaderInfo(logTail)
	applySPVStatusHeaderInfo(&hdr, spv)
	tipHeight := int64(0)
	switch v := spv["header_height"].(type) {
	case int64:
		tipHeight = v
	case int:
		tipHeight = int64(v)
	case float64:
		tipHeight = int64(v)
	}
	tipUnix := int64(0)
	switch v := spv["header_unix_time"].(type) {
	case int64:
		tipUnix = v
	case int:
		tipUnix = int64(v)
	case float64:
		tipUnix = int64(v)
	}

	var st *WalletState
	var pendingMeme float64
	var memeErr error
	var mtrCount, mtrConn int
	var mtrLive, mtrWorkers []map[string]any

	s.stateMergeMu.Lock()
	st, err = s.loadState()
	if err != nil {
		s.stateMergeMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	seenChanged := s.applySPVSeenTxids(st, logTail)
	rawChanged := s.applySPVRawHex(st, logTail)
	enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
	restChanged := s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
	dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
	suchChanged := false
	if s.shouldRunSuchMerge(20 * time.Second) {
		suchChanged = s.mergeTransactionsFromSuchListUnspent(wf, st)
	}
	if suchChanged && s.enrichSPVTxFromRawHex(st, wf) {
		enrichedChanged = true
	}
	if s.applySPVConfirmations(st, logTail) {
		_ = s.saveState(st)
	} else if seenChanged || rawChanged || enrichedChanged || restChanged || dbChanged || suchChanged {
		_ = s.saveState(st)
	}

	var inSum, outSum float64
	for _, t := range st.Transactions {
		if strings.EqualFold(t.Direction, "in") {
			inSum += t.AmountDOGE
		}
		if strings.EqualFold(t.Direction, "out") {
			outSum += t.AmountDOGE
		}
	}
	spendable := math.Max(0, inSum-outSum)
	// Prefer recent UTXO-derived spendable from such list_unspent when available.
	// This avoids drift when direction-classification history is imperfect.
	if utxoSpendable, ok := s.latestSuchSpendable(3 * time.Minute); ok {
		spendable = math.Max(0, utxoSpendable)
	}

	eng, engErr := s.ensureMempoolEngine(wf)
	if eng != nil && engErr == nil {
		mtrCount, mtrLive, mtrWorkers, mtrConn = eng.DashboardSnapshot()
	}
	if eng != nil && engErr == nil {
		if s.persistMemeTrackerTxs(st, mtrLive) || s.enrichSPVTxFromRawHex(st, wf) {
			_ = s.saveState(st)
		}
	}
	s.stateMergeMu.Unlock()

	// Run mempool pending estimate outside stateMerge lock so dashboard and tx list calls
	// do not block each other for long periods during refresh/poll bursts.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	pendingMeme, memeErr = s.syncMemeTracker(ctx, wf, st)
	cancel()

	s.mu.Lock()
	if changed, err := s.maybeRotateHDReceiveAddress(wf, st); err == nil && changed {
		_ = s.saveWallet(wf)
		s.startSPVNode(wf)
	}
	wf, _ = s.loadWallet()
	s.mu.Unlock()

	if hdr.HeaderHeight == 0 && len(st.Metrics) > 0 {
		for i := len(st.Metrics) - 1; i >= 0; i-- {
			if st.Metrics[i].HeaderHeight > 0 {
				hdr.HeaderHeight = st.Metrics[i].HeaderHeight
				if hdr.BestBlockHash == "" && st.Metrics[i].BestBlockHash != "" {
					hdr.BestBlockHash = st.Metrics[i].BestBlockHash
				}
				break
			}
		}
	}
	running, _ := spv["running"].(bool)

	memeErrStr := ""
	if memeErr != nil {
		memeErrStr = memeErr.Error()
	}
	mempoolDisplay := hdr.MempoolTxCount
	if eng != nil && engErr == nil {
		mempoolDisplay = mtrCount
	}
	mtrEngErr := ""
	if engErr != nil {
		mtrEngErr = engErr.Error()
	}

	mtrP2PActive := eng != nil && engErr == nil && (mtrConn > 0 || mtrCount > 0)

	writeJSON(w, http.StatusOK, map[string]any{
		"wallet": wf,
		"dashboard": map[string]any{
			"services": map[string]any{
				"spv_enabled":                   svcPrefs.SpvEnabled,
				"spv_running":                   running,
				"memetracker_enabled":           svcPrefs.MemetrackerEnabled,
				"memetracker_engine_alive":      eng != nil && engErr == nil,
				"memetracker_p2p_active":        mtrP2PActive,
				"memetracker_workers_connected": mtrConn,
				"memetracker_mempool_tx_count":  mtrCount,
			},
			"spv": map[string]any{
				"running":          running,
				"header_height":    hdr.HeaderHeight,
				"best_block_hash":  hdr.BestBlockHash,
				"header_unix_time": hdr.HeaderUnixTime,
				"sync_lag_seconds": syncLagSeconds(hdr.HeaderUnixTime),
				"sync_lag_label":   syncLagLabel(hdr.HeaderUnixTime),
				"mempool_tx_count": mempoolDisplay,
			},
			"memetracker": map[string]any{
				"mempool_tx_count":     mtrCount,
				"mempool_transactions": mtrLive,
				"worker_peers":         mtrWorkers,
				"workers_connected":    mtrConn,
				"engine_ok":            eng != nil && engErr == nil,
				"engine_error":         mtrEngErr,
			},
			"totals": map[string]any{
				"spendable_hint_doge":  round2(spendable),
				"pending_mempool_doge": round2(pendingMeme),
				"memetracker_error":    memeErrStr,
				"tx_count":             len(st.Transactions),
			},
			"metrics_sample": metricsLast24Hours(st.Metrics),
		},
	})
}

func tailMetrics(m []MetricPoint, n int) []MetricPoint {
	if n <= 0 || len(m) <= n {
		return m
	}
	return m[len(m)-n:]
}

// metricsLast24Hours returns samples from the last 24 hours for dashboard charts (up to ~4000 points cap).
func metricsLast24Hours(m []MetricPoint) []MetricPoint {
	if len(m) == 0 {
		return m
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	out := make([]MetricPoint, 0, len(m))
	for _, p := range m {
		if !p.T.Before(cutoff) {
			out = append(out, p)
		}
	}
	if len(out) > 0 {
		return out
	}
	return tailMetrics(m, 120)
}

func round4(f float64) float64 {
	return math.Round(f*1e4) / 1e4
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func applySPVStatusHeaderInfo(hdr *SPVHeaderInfo, spv map[string]any) {
	if hdr == nil || spv == nil {
		return
	}
	if hdr.HeaderHeight == 0 {
		switch v := spv["header_height"].(type) {
		case int64:
			hdr.HeaderHeight = v
		case int:
			hdr.HeaderHeight = int64(v)
		case float64:
			hdr.HeaderHeight = int64(v)
		}
	}
	if hdr.BestBlockHash == "" {
		if v, ok := spv["best_block_hash"].(string); ok {
			hdr.BestBlockHash = strings.TrimSpace(v)
		}
	}
	if hdr.HeaderUnixTime == 0 {
		switch v := spv["header_unix_time"].(type) {
		case int64:
			hdr.HeaderUnixTime = v
		case int:
			hdr.HeaderUnixTime = int64(v)
		case float64:
			hdr.HeaderUnixTime = int64(v)
		}
	}
}

// handleMetrics returns metric history for charts.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": st.Metrics})
}

// handleTransactions lists cached transactions from local SPV/P2P state.
func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			s.mu.Unlock()
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true, "transactions": []TxRecord{}})
			return
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"transactions": []TxRecord{}})
		return
	}
	if wf == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"transactions": []TxRecord{}})
		return
	}
	s.mu.Unlock()

	engTx, engTxErr := s.ensureMempoolEngine(wf)

	spv := s.readSPVStatus()
	tipHeight := int64(0)
	switch v := spv["header_height"].(type) {
	case int64:
		tipHeight = v
	case int:
		tipHeight = int64(v)
	case float64:
		tipHeight = int64(v)
	}
	tipUnix := int64(0)
	switch v := spv["header_unix_time"].(type) {
	case int64:
		tipUnix = v
	case int:
		tipUnix = int64(v)
	case float64:
		tipUnix = int64(v)
	}

	s.stateMergeMu.Lock()
	st, err := s.loadState()
	if err != nil {
		s.stateMergeMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if engTx != nil && engTxErr == nil {
		_, mtrLive, _, _ := engTx.DashboardSnapshot()
		if s.persistMemeTrackerTxs(st, mtrLive) {
			_ = s.saveState(st)
		}
	}
	if logTail, _ := spv["log_tail"].(string); logTail != "" {
		changed := s.applySPVSeenTxids(st, logTail)
		rawChanged := s.applySPVRawHex(st, logTail)
		enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
		restChanged := s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
		dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
		suchChanged := false
		if s.shouldRunSuchMerge(20 * time.Second) {
			suchChanged = s.mergeTransactionsFromSuchListUnspent(wf, st)
		}
		if suchChanged {
			if s.enrichSPVTxFromRawHex(st, wf) {
				enrichedChanged = true
			}
		}
		if s.applySPVConfirmations(st, logTail) || changed || rawChanged || enrichedChanged || restChanged || dbChanged || suchChanged {
			_ = s.saveState(st)
		}
	}
	s.stateMergeMu.Unlock()

	s.mu.Lock()
	if changed, err := s.maybeRotateHDReceiveAddress(wf, st); err == nil && changed {
		_ = s.saveWallet(wf)
		s.startSPVNode(wf)
	}
	wf, _ = s.loadWallet()
	s.mu.Unlock()

	out := s.mergeTxListWithMemeTracker(wf, st)
	writeJSON(w, http.StatusOK, map[string]any{"transactions": out})
}

func syncLagSeconds(headerUnix int64) int64 {
	if headerUnix <= 0 {
		return -1
	}
	lag := time.Now().UTC().Unix() - headerUnix
	if lag < 0 {
		return 0
	}
	return lag
}

func syncLagLabel(headerUnix int64) string {
	lag := syncLagSeconds(headerUnix)
	if lag < 0 {
		return "Unknown"
	}
	if lag == 0 {
		return "Synced"
	}
	const (
		hour  = int64(3600)
		day   = int64(24 * 3600)
		week  = int64(7 * 24 * 3600)
		month = int64(30 * 24 * 3600)
	)
	switch {
	case lag < 2*day:
		return fmt.Sprintf("%d hours behind", lag/hour)
	case lag < 2*week:
		return fmt.Sprintf("%d days behind", lag/day)
	case lag < 3*month:
		return fmt.Sprintf("%d weeks behind", lag/week)
	default:
		return fmt.Sprintf("%d months behind", lag/month)
	}
}

func truthyAny(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func jsonStringAny(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func floatFromAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

// persistMemeTrackerTxs appends mempool-tracked txs to state so they remain listed after they leave the live mempool.
func (s *Server) persistMemeTrackerTxs(st *WalletState, mtrLive []map[string]any) bool {
	byTxid := make(map[string]int, len(st.Transactions))
	for i := range st.Transactions {
		id := normalizeTxid(st.Transactions[i].Txid)
		if id == "" {
			continue
		}
		st.Transactions[i].Txid = id
		if _, ok := byTxid[id]; !ok {
			byTxid[id] = i
		}
	}
	var incoming []TxRecord
	changed := false
	for _, m := range mtrLive {
		if !truthyAny(m["tracked_match"]) {
			continue
		}
		txid := normalizeTxid(jsonStringAny(m["txid"]))
		if txid == "" {
			continue
		}
		amt := floatFromAny(m["amount_doge"])
		rawHex := strings.TrimSpace(jsonStringAny(m["raw_hex"]))
		if idx, ok := byTxid[txid]; ok {
			row := &st.Transactions[idx]
			if row.AmountDOGE == 0 && amt > 0 {
				row.AmountDOGE = amt
				changed = true
			}
			if row.RawHex == "" && rawHex != "" {
				row.RawHex = rawHex
				changed = true
			}
			if row.Direction == "" || strings.EqualFold(row.Direction, "unknown") {
				row.Direction = "in"
				changed = true
			}
			if row.Address == "" {
				if addr := strings.TrimSpace(jsonStringAny(m["tracked_address"])); addr != "" {
					row.Address = addr
					changed = true
				}
			}
			if row.Source == "" || strings.EqualFold(row.Source, "spv") {
				row.Source = "memetracker"
				changed = true
			}
			continue
		}
		incoming = append(incoming, TxRecord{
			Txid:          txid,
			Direction:     "in",
			AmountDOGE:    amt,
			RawHex:        rawHex,
			Address:       strings.TrimSpace(jsonStringAny(m["tracked_address"])),
			Source:        "memetracker",
			Confirmations: 0,
			SeenAt:        time.Now().UTC(),
		})
	}
	if len(incoming) > 0 {
		st.Transactions = mergeTxRecords(st.Transactions, incoming)
		changed = true
	}
	return changed
}

func (s *Server) mergeTxListWithMemeTracker(wf *WalletFile, st *WalletState) []txListRow {
	mtrOverlay := map[string]bool{}
	eng, engErr := s.ensureMempoolEngine(wf)
	if eng != nil && engErr == nil {
		_, mtrLive, _, _ := eng.DashboardSnapshot()
		for _, m := range mtrLive {
			if !truthyAny(m["tracked_match"]) {
				continue
			}
			txid := normalizeTxid(jsonStringAny(m["txid"]))
			if txid == "" {
				continue
			}
			mtrOverlay[txid] = true
		}
	}
	out := make([]txListRow, 0, len(st.Transactions))
	for _, t := range st.Transactions {
		tr := txListRow{TxRecord: t, Pending: t.Confirmations == 0}
		if mtrOverlay[normalizeTxid(t.Txid)] && t.Confirmations == 0 {
			tr.Pending = true
			tr.Source = "memetracker"
		}
		out = append(out, tr)
	}
	return out
}


func (s *Server) applySPVConfirmations(st *WalletState, logTail string) bool {
	confirmed := parseSPVConfirmedTxids(logTail)
	if len(confirmed) == 0 {
		return false
	}
	changed := false
	for i := range st.Transactions {
		id := normalizeTxid(st.Transactions[i].Txid)
		if id == "" {
			continue
		}
		if _, ok := confirmed[id]; !ok {
			continue
		}
		st.Transactions[i].Txid = id
		if st.Transactions[i].Confirmations <= 0 {
			st.Transactions[i].Confirmations = 1
			if st.Transactions[i].Source == "memetracker" || st.Transactions[i].Source == "" {
				st.Transactions[i].Source = "spv"
			}
			changed = true
		}
	}
	return changed
}

func (s *Server) applySPVRawHex(st *WalletState, logTail string) bool {
	byTxid := parseSPVRawTxHexByTxid(logTail)
	if len(byTxid) == 0 {
		return false
	}
	changed := false
	existing := make(map[string]struct{}, len(st.Transactions))
	for i := range st.Transactions {
		id := normalizeTxid(st.Transactions[i].Txid)
		if id == "" {
			continue
		}
		existing[id] = struct{}{}
		raw := byTxid[id]
		if raw == "" {
			continue
		}
		if st.Transactions[i].RawHex == "" {
			st.Transactions[i].RawHex = raw
			changed = true
		}
	}
	now := time.Now().UTC()
	for id, raw := range byTxid {
		if id == "" || raw == "" {
			continue
		}
		if _, ok := existing[id]; ok {
			continue
		}
		st.Transactions = append(st.Transactions, TxRecord{
			Txid:          id,
			Direction:     "unknown",
			AmountDOGE:    0,
			RawHex:        raw,
			Confirmations: 0,
			Source:        "spv",
			SeenAt:        now,
		})
		existing[id] = struct{}{}
		changed = true
	}
	return changed
}

// applySPVSeenTxids ensures txids visible in SPV logs appear in state even when
// explorer/mempool sources are unavailable.
func (s *Server) applySPVSeenTxids(st *WalletState, logTail string) bool {
	seen := parseSPVSeenTxids(logTail)
	if len(seen) == 0 {
		return false
	}
	existing := make(map[string]struct{}, len(st.Transactions))
	for i := range st.Transactions {
		id := normalizeTxid(st.Transactions[i].Txid)
		if id == "" {
			continue
		}
		existing[id] = struct{}{}
	}
	now := time.Now().UTC()
	added := false
	for id := range seen {
		if _, ok := existing[id]; ok {
			continue
		}
		st.Transactions = append(st.Transactions, TxRecord{
			Txid:          id,
			Direction:     "unknown",
			AmountDOGE:    0,
			Confirmations: 0,
			Source:        "spv",
			SeenAt:        now,
		})
		existing[id] = struct{}{}
		added = true
	}
	return added
}

// Blockchair uses smallest units for Dogecoin (×1e8) when the number is large.
func normalizeDogecoinUnits(v float64) float64 {
	if v >= 1e6 {
		return v / 1e8
	}
	return v
}

func parseFloatAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}

// backgroundMetricsLoop samples SPV + header info and appends metric points.
func (s *Server) backgroundMetricsLoop() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for range tick.C {
		s.mu.Lock()
		wf, err := s.loadWallet()
		if errors.Is(err, ErrWalletLocked) {
			s.startSPVNodeFromWatchState()
			s.mu.Unlock()
			continue
		}
		if err != nil || wf == nil {
			s.mu.Unlock()
			continue
		}
		s.startSPVNode(wf)
		s.mu.Unlock()

		spv := s.readSPVStatus()
		logTail, _ := spv["log_tail"].(string)
		hdr := parseSPVLogHeaderInfo(logTail)
		applySPVStatusHeaderInfo(&hdr, spv)
		running, _ := spv["running"].(bool)
		tipHeight := int64(0)
		switch v := spv["header_height"].(type) {
		case int64:
			tipHeight = v
		case int:
			tipHeight = int64(v)
		case float64:
			tipHeight = int64(v)
		}
		tipUnix := int64(0)
		switch v := spv["header_unix_time"].(type) {
		case int64:
			tipUnix = v
		case int:
			tipUnix = int64(v)
		case float64:
			tipUnix = int64(v)
		}

		s.stateMergeMu.Lock()
		st, err := s.loadState()
		if err != nil {
			s.stateMergeMu.Unlock()
			continue
		}
		spvTxSeen := parseSPVTxSeenCount(logTail)
		_ = s.applySPVSeenTxids(st, logTail)
		_ = s.applySPVConfirmations(st, logTail)
		_ = s.applySPVRawHex(st, logTail)
		_ = s.enrichSPVTxFromRawHex(st, wf)
		_ = s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
		_ = s.mergeTransactionsFromSPVWalletDB(wf, st)
		if s.mergeTransactionsFromSuchListUnspent(wf, st) {
			_ = s.enrichSPVTxFromRawHex(st, wf)
		}
		mempoolRelay := 0
		if eng, eerr := s.ensureMempoolEngine(wf); eerr == nil && eng != nil {
			mempoolRelay, _, _, _ = eng.DashboardSnapshot()
		}
		s.appendMetricPoint(st, MetricPoint{
			T:                 time.Now().UTC(),
			HeaderHeight:      hdr.HeaderHeight,
			BestBlockHash:     hdr.BestBlockHash,
			SPVRunning:        running,
			PeerCount:         hdr.PeerCount,
			MempoolTxCount:    hdr.MempoolTxCount,
			SPVTxSeenCount:    spvTxSeen,
			MempoolRelayCount: mempoolRelay,
		})
		_ = s.saveState(st)
		s.stateMergeMu.Unlock()
	}
}
