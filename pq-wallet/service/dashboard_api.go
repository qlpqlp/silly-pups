package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// txListRow is one row for /api/transactions (explorer cache + MemeTracker mempool matches).
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
	if s.applySPVConfirmations(st, logTail) {
		_ = s.saveState(st)
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

	spendable := st.ExplorerBalanceDOGE
	if spendable <= 0 {
		var inSum, outSum float64
		for _, t := range st.Transactions {
			if strings.EqualFold(t.Direction, "in") {
				inSum += t.AmountDOGE
			}
			if strings.EqualFold(t.Direction, "out") {
				outSum += t.AmountDOGE
			}
		}
		spendable = math.Max(0, inSum-outSum)
	}

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
		if s.persistMemeTrackerTxs(st, mtrLive) {
			_ = s.saveState(st)
		}
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
				"last_explorer_sync":   st.LastExplorerSync,
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

// handleTransactions lists cached transactions; ?refresh=1 triggers explorer/RPC sync.
func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	refresh := strings.TrimSpace(r.URL.Query().Get("refresh")) == "1"
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
	if refresh {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		incoming, bal, err := s.syncTransactionsFromNetwork(ctx, wf)
		if err == nil {
			st.Transactions = mergeTxRecords(st.Transactions, incoming)
			if bal > 0 {
				st.ExplorerBalanceDOGE = bal
			}
			st.LastExplorerSync = time.Now().UTC()
			_ = s.saveState(st)
		}
	}
	spv := s.readSPVStatus()
	if logTail, _ := spv["log_tail"].(string); s.applySPVConfirmations(st, logTail) {
		_ = s.saveState(st)
	}
	out := s.mergeTxListWithMemeTracker(wf, st)
	writeJSON(w, http.StatusOK, map[string]any{"transactions": out})
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
	existing := make(map[string]struct{}, len(st.Transactions))
	for _, t := range st.Transactions {
		id := normalizeTxid(t.Txid)
		if id != "" {
			existing[id] = struct{}{}
		}
	}
	var incoming []TxRecord
	for _, m := range mtrLive {
		if !truthyAny(m["tracked_match"]) {
			continue
		}
		txid := normalizeTxid(jsonStringAny(m["txid"]))
		if txid == "" {
			continue
		}
		if _, ok := existing[txid]; ok {
			continue
		}
		existing[txid] = struct{}{}
		incoming = append(incoming, TxRecord{
			Txid:          txid,
			Direction:     "in",
			AmountDOGE:    floatFromAny(m["amount_doge"]),
			Source:        "memetracker",
			Confirmations: 0,
			SeenAt:        time.Now().UTC(),
		})
	}
	if len(incoming) == 0 {
		return false
	}
	st.Transactions = mergeTxRecords(st.Transactions, incoming)
	return true
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

func (s *Server) syncTransactionsFromNetwork(ctx context.Context, wf *WalletFile) ([]TxRecord, float64, error) {
	var out []TxRecord
	var balance float64
	addrs := wf.AllDistinctP2PKHAddresses()
	if len(addrs) == 0 {
		return out, 0, nil
	}

	base := strings.TrimSpace(s.explorerAddr)
	if base != "" {
		for _, addr := range addrs {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			txs, bal, err := s.fetchBlockchairAddress(ctx, base, addr)
			if err != nil {
				continue
			}
			out = append(out, txs...)
			balance += bal
		}
	}

	seen := make(map[string]struct{})
	for _, t := range out {
		id := normalizeTxid(t.Txid)
		if id == "" || strings.HasPrefix(id, "_") {
			continue
		}
		if len(seen) >= 12 {
			break
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if s.txPQHintFromExplorer(ctx, id) {
			for i := range out {
				if normalizeTxid(out[i].Txid) == id {
					out[i].PQHint = true
					out[i].PQVerified = true
				}
			}
		}
	}

	return out, balance, nil
}

func (s *Server) fetchBlockchairAddress(ctx context.Context, baseURL, address string) ([]TxRecord, float64, error) {
	url := strings.ReplaceAll(strings.TrimRight(baseURL, "/"), "{address}", address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0, err
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, 0, err
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, 0, nil
	}
	addrObj, _ := data[address].(map[string]any)
	if addrObj == nil {
		for _, v := range data {
			if m, ok := v.(map[string]any); ok {
				addrObj = m
				break
			}
		}
	}
	var rawBal float64
	if addrObj != nil {
		if a, ok := addrObj["address"].(map[string]any); ok {
			rawBal = parseFloatAny(a["balance"])
			if rawBal == 0 {
				rawBal = parseFloatAny(a["received"])
			}
		}
	}
	balance := normalizeDogecoinUnits(rawBal)
	var txs []TxRecord
	if addrObj != nil {
		rawList, _ := addrObj["transactions"].([]any)
		for _, item := range rawList {
			h := ""
			switch t := item.(type) {
			case string:
				h = t
			case map[string]any:
				if x, ok := t["hash"].(string); ok {
					h = x
				} else if x, ok := t["transaction_hash"].(string); ok {
					h = x
				}
			}
			if h == "" {
				continue
			}
			h = normalizeTxid(h)
			if h == "" {
				continue
			}
			txs = append(txs, TxRecord{
				Txid:          h,
				Direction:     "in",
				AmountDOGE:    0,
				Address:       address,
				Source:        "explorer",
				SeenAt:        time.Now().UTC(),
				Confirmations: 0,
			})
		}
	}
	return txs, balance, nil
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

func (s *Server) txPQHintFromExplorer(ctx context.Context, txid string) bool {
	base := strings.TrimSpace(s.explorer)
	if base == "" {
		return false
	}
	url := strings.ReplaceAll(base, "{txid}", txid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(b))
	return strings.Contains(lower, `"script_hex":"6a`) || strings.Contains(lower, `"script_hex": "6a`)
}

// backgroundMetricsLoop samples SPV + header info and appends metric points.
func (s *Server) backgroundMetricsLoop() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for range tick.C {
		s.mu.Lock()
		wf, err := s.loadWallet()
		if errors.Is(err, ErrWalletLocked) {
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
