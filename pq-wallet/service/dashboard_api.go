package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	s.startSPVNode(wf)
	spv := s.readSPVStatus()
	logTail, _ := spv["log_tail"].(string)
	hdr := parseSPVLogHeaderInfo(logTail)
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

	inSum := 0.0
	outSum := 0.0
	for _, t := range st.Transactions {
		if strings.EqualFold(t.Direction, "in") {
			inSum += t.AmountDOGE
		}
		if strings.EqualFold(t.Direction, "out") {
			outSum += t.AmountDOGE
		}
	}
	spendable := st.ExplorerBalanceDOGE
	if spendable <= 0 {
		spendable = math.Max(0, inSum-outSum)
	}

	var currentPeer any
	if hdr.CurrentPeer != nil {
		currentPeer = hdr.CurrentPeer
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
	if currentPeer == nil && eng != nil && engErr == nil {
		for _, w := range mtrWorkers {
			conn, _ := w["connected"].(bool)
			addr, _ := w["address"].(string)
			if !conn || strings.TrimSpace(addr) == "" {
				continue
			}
			wid, _ := w["worker_id"].(int)
			currentPeer = &PeerConnectionInfo{
				NodeID:            wid,
				Address:           addr,
				SubVersion:        "Embedded mempool watcher (P2P)",
				RemoteStartHeight: 0,
			}
			break
		}
	}

	mtrEngErr := ""
	if engErr != nil {
		mtrEngErr = engErr.Error()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"wallet": wf,
		"dashboard": map[string]any{
			"spv": map[string]any{
				"running":               running,
				"header_height":         hdr.HeaderHeight,
				"best_block_hash":       hdr.BestBlockHash,
				"peer_count":            hdr.PeerCount,
				"header_count_hint":     hdr.HeaderCountHint,
				"spv_peer_hosts":        hdr.SPVPeerHosts,
				"mempool_tx_count":      mempoolDisplay,
				"mempool_tx_count_spv_log": hdr.MempoolTxCount,
				"current_peer":          currentPeer,
				"peers_recent":          hdr.PeersRecent,
				"log_tail":              logTail,
			},
			"memetracker": map[string]any{
				"mempool_tx_count":     mtrCount,
				"mempool_transactions": mtrLive,
				"worker_peers":         mtrWorkers,
				"workers_connected":  mtrConn,
				"engine_ok":            eng != nil && engErr == nil,
				"engine_error":         mtrEngErr,
			},
			"totals": map[string]any{
				"received_doge":         round4(inSum),
				"sent_doge":             round4(outSum),
				"spendable_hint_doge":   round4(spendable),
				"pending_mempool_doge":  round4(pendingMeme),
				"memetracker_error":     memeErrStr,
				"tx_count":              len(st.Transactions),
				"last_explorer_sync":    st.LastExplorerSync,
			},
			"metrics_sample": tailMetrics(st.Metrics, 120),
		},
	})
}

func tailMetrics(m []MetricPoint, n int) []MetricPoint {
	if n <= 0 || len(m) <= n {
		return m
	}
	return m[len(m)-n:]
}

func round4(f float64) float64 {
	return math.Round(f*1e4) / 1e4
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
	writeJSON(w, http.StatusOK, map[string]any{"transactions": st.Transactions})
}

func (s *Server) syncTransactionsFromNetwork(ctx context.Context, wf *WalletFile) ([]TxRecord, float64, error) {
	var out []TxRecord
	var balance float64
	p := wf.PrimaryAddress()
	if p == nil {
		return out, 0, nil
	}
	addr := strings.TrimSpace(p.P2PKH)
	if addr == "" {
		return out, 0, nil
	}

	base := strings.TrimSpace(s.explorerAddr)
	if base != "" {
		txs, bal, err := s.fetchBlockchairAddress(ctx, base, addr)
		if err == nil {
			out = append(out, txs...)
			balance = bal
		}
	}

	seen := make(map[string]struct{})
	for _, t := range out {
		if t.Txid == "" || strings.HasPrefix(t.Txid, "_") {
			continue
		}
		if len(seen) >= 12 {
			break
		}
		if _, ok := seen[t.Txid]; ok {
			continue
		}
		seen[t.Txid] = struct{}{}
		if s.txPQHintFromExplorer(ctx, t.Txid) {
			for i := range out {
				if out[i].Txid == t.Txid {
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
		s.appendMetricPoint(st, MetricPoint{
			T:             time.Now().UTC(),
			HeaderHeight:  hdr.HeaderHeight,
			BestBlockHash: hdr.BestBlockHash,
			SPVRunning:    running,
			PeerCount:     hdr.PeerCount,
			MempoolTxCount: hdr.MempoolTxCount,
		})
		_ = s.saveState(st)
		s.mu.Unlock()
	}
}
