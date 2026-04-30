package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
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

func (s *Server) computeSuchSpendableDOGE(wf *WalletFile) (float64, bool) {
	if wf == nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sumKoinu := int64(0)
	seen := map[string]struct{}{}
	successfulReads := 0
	for _, addr := range wf.AllDistinctP2PKHAddresses() {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		utxos, err := s.fetchUTXOsFromExplorer(ctx, addr)
		if err != nil {
			continue
		}
		successfulReads++
		for _, u := range utxos {
			txid := normalizeTxid(u.TxID)
			if txid == "" || u.Value <= 0 {
				continue
			}
			k := txid + ":" + strconv.FormatUint(uint64(u.Vout), 10)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			sumKoinu += u.Value
		}
	}
	if successfulReads == 0 {
		return 0, false
	}
	sum := round2(float64(sumKoinu) / 1e8)
	s.suchMergeMu.Lock()
	s.lastSuchSpendableDOGE = sum
	s.lastSuchSpendableAt = time.Now()
	s.suchMergeMu.Unlock()
	return sum, true
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
	tipHeight := spvHeaderHeightFromMap(spv)
	tipUnix := spvHeaderUnixFromMap(spv)

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
	seenChanged := false
	rawChanged := false
	confirmChanged := false
	if logTail != "" {
		seenChanged = s.applySPVSeenTxids(st, logTail)
		rawChanged = s.applySPVRawHex(st, logTail)
		confirmChanged = s.applySPVConfirmations(st, logTail)
	}
	restChanged := s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
	bcChanged := s.mergeTransactionsFromBroadcastLog(st)
	bcRawChanged := s.mergeRawHexFromBroadcastLog(st)
	dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
	suchChanged := false
	if s.shouldRunSuchMerge(20 * time.Second) {
		suchChanged = s.mergeTransactionsFromSuchListUnspent(wf, st)
	}
	enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
	if seenChanged || rawChanged || confirmChanged || enrichedChanged || restChanged || bcChanged || bcRawChanged || dbChanged || suchChanged {
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
	// Prefer direct UTXO-derived spendable from such list_unspent when available.
	// This is the authoritative spendable source for "Available balance".
	if utxoSpendable, ok := s.computeSuchSpendableDOGE(wf); ok {
		spendable = math.Max(0, utxoSpendable)
	} else if utxoSpendable, ok := s.latestSuchSpendable(10 * time.Minute); ok {
		// If such is transiently unavailable, keep last known spendable.
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
				"such_pqc":                      s.cachedSuchPQCProbe(r.Context()),
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

func spvHeaderHeightFromMap(spv map[string]any) int64 {
	if spv == nil {
		return 0
	}
	switch v := spv["header_height"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func spvHeaderUnixFromMap(spv map[string]any) int64 {
	if spv == nil {
		return 0
	}
	switch v := spv["header_unix_time"].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

// applySPVStatusHeaderInfo overlays REST-derived chain tip onto log-parsed header info.
// Log pipe lines can mis-parse timestamps (e.g. year prefix as "unix"); REST values win when plausible.
func applySPVStatusHeaderInfo(hdr *SPVHeaderInfo, spv map[string]any) {
	if hdr == nil || spv == nil {
		return
	}
	if sh := spvHeaderHeightFromMap(spv); sh > 0 {
		hdr.HeaderHeight = sh
	}
	if v, ok := spv["best_block_hash"].(string); ok {
		v = strings.TrimSpace(v)
		if id := normalizeTxid(v); len(id) == 64 {
			hdr.BestBlockHash = id
		}
	}
	if su := spvHeaderUnixFromMap(spv); su > 1231006505 {
		hdr.HeaderUnixTime = su
	}
}

// mergeTransactionsFromBroadcastLog tags txids we attempted to broadcast as outgoing spends.
func (s *Server) mergeTransactionsFromBroadcastLog(st *WalletState) bool {
	if st == nil {
		return false
	}
	tail, err := readFileTail(s.broadcastLogPath(), 2<<20)
	if err != nil || strings.TrimSpace(tail) == "" {
		return false
	}
	ids := extractBroadcastOutTxidsFromTail(tail)
	if len(ids) == 0 {
		return false
	}
	incoming := make([]TxRecord, 0, len(ids))
	for _, id := range ids {
		incoming = append(incoming, TxRecord{Txid: id, Direction: "out", Source: "spv"})
	}
	merged := mergeTxRecords(st.Transactions, incoming)
	before := len(st.Transactions)
	changed := len(merged) != before
	if !changed {
		oldByID := map[string]TxRecord{}
		for _, t := range st.Transactions {
			id := normalizeTxid(t.Txid)
			if id != "" {
				oldByID[id] = t
			}
		}
		for _, t := range merged {
			id := normalizeTxid(t.Txid)
			o, ok := oldByID[id]
			if !ok || t.Direction != o.Direction || t.AmountDOGE != o.AmountDOGE || t.Address != o.Address ||
				t.Confirmations != o.Confirmations || !t.SeenAt.Equal(o.SeenAt) {
				changed = true
				break
			}
		}
	}
	if changed {
		st.Transactions = merged
	}
	return changed
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
	tipHeight := spvHeaderHeightFromMap(spv)
	tipUnix := spvHeaderUnixFromMap(spv)

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
	logTail, _ := spv["log_tail"].(string)
	changed := false
	rawChanged := false
	confirmChanged := false
	if logTail != "" {
		changed = s.applySPVSeenTxids(st, logTail)
		rawChanged = s.applySPVRawHex(st, logTail)
		confirmChanged = s.applySPVConfirmations(st, logTail)
	}
	restChanged := s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
	bcChanged := s.mergeTransactionsFromBroadcastLog(st)
	bcRawChanged := s.mergeRawHexFromBroadcastLog(st)
	dbChanged := s.mergeTransactionsFromSPVWalletDB(wf, st)
	suchChanged := false
	if s.shouldRunSuchMerge(20 * time.Second) {
		suchChanged = s.mergeTransactionsFromSuchListUnspent(wf, st)
	}
	enrichedChanged := s.enrichSPVTxFromRawHex(st, wf)
	if changed || rawChanged || confirmChanged || enrichedChanged || restChanged || bcChanged || bcRawChanged || dbChanged || suchChanged {
		_ = s.saveState(st)
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
	// Dogecoin chain tips are 2013+ wall times. Tiny values are almost always mis-parsed log tokens
	// (same numeric floor as spv_* “plausible on-chain unix” checks elsewhere in this service).
	if headerUnix <= 1231006505 {
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

// txIsLikelyWalletChangeEcho suppresses list rows that look like pure wallet-side credits whose
// first input spends an output from a transaction we already treat as OUT (typical change-back
// after a send). This mirrors Dogecoin Wallet / bitcoinj hiding internal change from RECEIVED.
func txIsLikelyWalletChangeEcho(t TxRecord, wf *WalletFile, testnet bool, walletH160 map[string]string, outTxids map[string]struct{}) bool {
	if wf == nil || len(walletH160) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(t.Source), "memetracker") {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(t.Direction), "in") {
		return false
	}
	raw := strings.TrimSpace(t.RawHex)
	if raw == "" {
		return false
	}
	fl, err := decodeSPVRawTxFlow(raw, walletH160, testnet)
	if err != nil || fl.ExternalSats != 0 || fl.WalletSats <= 0 {
		return false
	}
	prev := firstInputPrevTxidFromRawHex(raw)
	if prev == "" {
		return false
	}
	_, ok := outTxids[prev]
	return ok
}

func (s *Server) mergeTxListWithMemeTracker(wf *WalletFile, st *WalletState) []txListRow {
	mtrOverlay := map[string]bool{}
	walletAddrSet := map[string]struct{}{}
	if wf != nil {
		for _, a := range wf.AllDistinctP2PKHAddresses() {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" {
				continue
			}
			walletAddrSet[a] = struct{}{}
		}
	}
	outTxids := map[string]struct{}{}
	for _, t := range st.Transactions {
		if strings.EqualFold(strings.TrimSpace(t.Direction), "out") {
			if id := normalizeTxid(t.Txid); id != "" {
				outTxids[id] = struct{}{}
			}
		}
	}
	testnet := wf != nil && strings.EqualFold(wf.Network, "testnet")
	walletH160 := walletP2PKHHash160Map(wf)
	var prevIdx map[string]prevoutWalletMeta
	if len(walletH160) > 0 {
		logBlob := ""
		if lb, err := readFileTail(s.spvLogPath(), spvLogTailForEnrich); err == nil {
			logBlob = lb
		}
		prevIdx = buildPrevoutWalletIndex(collectUniqueRawHexes(st, logBlob), walletH160)
	}
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
		if txIsLikelyWalletChangeEcho(t, wf, testnet, walletH160, outTxids) {
			continue
		}
		tr := txListRow{TxRecord: t, Pending: t.Confirmations == 0}
		if len(walletH160) > 0 && strings.TrimSpace(tr.RawHex) != "" && prevIdx != nil {
			fl, err := decodeSPVRawTxFlow(tr.RawHex, walletH160, testnet)
			if err == nil {
				net, _, _, netOk := walletNetFromPrevoutIndex(tr.RawHex, walletH160, testnet, prevIdx)
				if netOk && net != 0 {
					var netAbs int64 = net
					if netAbs < 0 {
						netAbs = -netAbs
					}
					amt := round2(float64(netAbs) / 1e8)
					if net < 0 {
						tr.Direction = "out"
						if amt > 0 {
							tr.AmountDOGE = amt
						}
						if fl.ExternalAddr != "" {
							tr.Address = fl.ExternalAddr
						}
					} else {
						tr.Direction = "in"
						if amt > 0 {
							tr.AmountDOGE = amt
						}
						if strings.TrimSpace(tr.Address) == "" && fl.WalletAddr != "" {
							tr.Address = fl.WalletAddr
						}
					}
				} else if fl.ExternalSats > 0 {
					tr.Direction = "out"
					paySats := fl.CounterpartySats
					if paySats <= 0 {
						paySats = fl.ExternalSats
					}
					amt := round2(float64(paySats) / 1e8)
					if amt > 0 {
						tr.AmountDOGE = amt
					}
					if fl.ExternalAddr != "" {
						tr.Address = fl.ExternalAddr
					}
				}
			}
		}
		// Last-mile guardrail for API output:
		// only force OUT when address is explicitly non-wallet.
		// Do not force IN for wallet-address rows (can be spend tx change rows).
		if strings.EqualFold(strings.TrimSpace(tr.Direction), "unknown") || strings.TrimSpace(tr.Direction) == "" {
			addr := strings.ToLower(strings.TrimSpace(tr.Address))
			if addr != "" {
				if _, ok := walletAddrSet[addr]; !ok {
					tr.Direction = "out"
				}
			} else if tr.AmountDOGE > 0 && strings.EqualFold(strings.TrimSpace(tr.Source), "memetracker") {
				tr.Direction = "in"
			}
		}
		if mtrOverlay[normalizeTxid(t.Txid)] && t.Confirmations == 0 {
			tr.Pending = true
			tr.Source = "memetracker"
		}
		out = append(out, tr)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i]
		tj := out[j]
		ui := ti.SeenAt.Unix()
		uj := tj.SeenAt.Unix()
		if ui != uj {
			return ui > uj
		}
		if ti.BlockHeight != tj.BlockHeight {
			return ti.BlockHeight > tj.BlockHeight
		}
		if ti.Confirmations != tj.Confirmations {
			return ti.Confirmations > tj.Confirmations
		}
		return strings.ToLower(strings.TrimSpace(ti.Txid)) > strings.ToLower(strings.TrimSpace(tj.Txid))
	})
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
			SeenAt:        time.Time{},
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
			SeenAt:        time.Time{},
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
		tipHeight := spvHeaderHeightFromMap(spv)
		tipUnix := spvHeaderUnixFromMap(spv)

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
		_ = s.mergeTransactionsFromSPVREST(st, tipHeight, tipUnix)
		_ = s.mergeTransactionsFromBroadcastLog(st)
		_ = s.mergeRawHexFromBroadcastLog(st)
		_ = s.mergeTransactionsFromSPVWalletDB(wf, st)
		_ = s.mergeTransactionsFromSuchListUnspent(wf, st)
		_ = s.enrichSPVTxFromRawHex(st, wf)
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
