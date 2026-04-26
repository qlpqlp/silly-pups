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

// handleDashboard returns balances, SPV header parse, metrics tail, and merged tx summary.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			s.startSPVNodeFromWatchState()
			writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil, "locked": true, "sealed": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusOK, map[string]any{"wallet": nil, "dashboard": nil})
		return
	}
	st, _ := s.loadState()
	svcPrefs := s.readServicePrefs()
	s.startSPVNode(wf)
	spv := s.readSPVStatus()
	logTail, _ := spv["log_tail"].(string)
	hdr := parseSPVLogHeaderInfo(logTail)
	seenChanged := s.applySPVSeenTxids(st, logTail)
	rawChanged := s.applySPVRawHex(st, logTail)
	enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
	dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
	suchChanged := s.mergeTransactionsFromSuchListUnspent(wf, st)
	if suchChanged && s.enrichSPVTxFromRawHex(st, wf) {
		enrichedChanged = true
	}
	if s.applySPVConfirmations(st, logTail) {
		_ = s.saveState(st)
	} else if seenChanged || rawChanged || enrichedChanged || dbChanged || suchChanged {
		_ = s.saveState(st)
	}
	if changed, err := s.maybeRotateHDReceiveAddress(wf, st); err == nil && changed {
		_ = s.saveWallet(wf)
		s.startSPVNode(wf)
	}
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

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pendingMeme, memeErr := s.syncMemeTracker(ctx, wf, st)
	memeErrStr := ""
	if memeErr != nil {
		memeErrStr = memeErr.Error()
	}

	eng, engErr := s.ensureMempoolEngine(wf)
	mtrCount, mtrConn := 0, 0
	var mtrLive, mtrWorkers []map[string]any
	if eng != nil && engErr == nil {
		mtrCount, mtrLive, mtrWorkers, mtrConn = eng.DashboardSnapshot()
	}
	mempoolDisplay := hdr.MempoolTxCount
	if eng != nil && engErr == nil {
		mempoolDisplay = mtrCount
	}
	// Peer panel: SPV handshake lines from spv.log only (not MemeTracker workers).

	mtrEngErr := ""
	if engErr != nil {
		mtrEngErr = engErr.Error()
	}
	if eng != nil && engErr == nil {
		if s.persistMemeTrackerTxs(st, mtrLive) || s.enrichSPVTxFromRawHex(st, wf) {
			_ = s.saveState(st)
		}
	}
	if changed, err := s.maybeRotateHDReceiveAddress(wf, st); err == nil && changed {
		_ = s.saveWallet(wf)
		s.startSPVNode(wf)
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
	defer s.mu.Unlock()
	wf, err := s.loadWallet()
	if err != nil {
		if errors.Is(err, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "locked", "need_unlock": true, "transactions": []TxRecord{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"transactions": []TxRecord{}})
		return
	}
	if wf == nil {
		writeJSON(w, http.StatusOK, map[string]any{"transactions": []TxRecord{}})
		return
	}
	st, err := s.loadState()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	engTx, engTxErr := s.ensureMempoolEngine(wf)
	if engTx != nil && engTxErr == nil {
		_, mtrLive, _, _ := engTx.DashboardSnapshot()
		if s.persistMemeTrackerTxs(st, mtrLive) {
			_ = s.saveState(st)
		}
	}
	spv := s.readSPVStatus()
	if logTail, _ := spv["log_tail"].(string); logTail != "" {
		changed := s.applySPVSeenTxids(st, logTail)
		rawChanged := s.applySPVRawHex(st, logTail)
		enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
		dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
		suchChanged := s.mergeTransactionsFromSuchListUnspent(wf, st)
		if suchChanged {
			if s.enrichSPVTxFromRawHex(st, wf) {
				enrichedChanged = true
			}
		}
		if s.applySPVConfirmations(st, logTail) || changed || rawChanged || enrichedChanged || dbChanged || suchChanged {
			_ = s.saveState(st)
		}
	}
	if changed, err := s.maybeRotateHDReceiveAddress(wf, st); err == nil && changed {
		_ = s.saveWallet(wf)
		s.startSPVNode(wf)
	}
	out := s.mergeTxListWithMemeTracker(wf, st)
	writeJSON(w, http.StatusOK, map[string]any{"transactions": out})
}

func syncLagSeconds(headerUnix int64) int64 {
	if headerUnix <= 0 {
		return 0
	}
	lag := time.Now().UTC().Unix() - headerUnix
	if lag < 0 {
		return 0
	}
	return lag
}

func syncLagLabel(headerUnix int64) string {
	lag := syncLagSeconds(headerUnix)
	if lag <= 0 {
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
		spv := s.readSPVStatus()
		logTail, _ := spv["log_tail"].(string)
		hdr := parseSPVLogHeaderInfo(logTail)
		running, _ := spv["running"].(bool)
		st, err := s.loadState()
		if err != nil {
			s.mu.Unlock()
			continue
		}
		spvTxSeen := parseSPVTxSeenCount(logTail)
		_ = s.applySPVSeenTxids(st, logTail)
		_ = s.applySPVConfirmations(st, logTail)
		_ = s.applySPVRawHex(st, logTail)
		_ = s.enrichSPVTxFromRawHex(st, wf)
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
		s.mu.Unlock()
	}
}
