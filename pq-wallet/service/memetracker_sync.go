package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/inevitable360/silly-pups/pq-wallet/service/mempooltracker"
)

// ensureMempoolEngine starts the embedded MemeTracker P2P watcher once per storage dir / network.
func (s *Server) ensureMempoolEngine(wf *WalletFile) (*mempooltracker.Engine, error) {
	if wf == nil {
		return nil, nil
	}
	want := strings.ToLower(strings.TrimSpace(wf.Network))
	if want == "" {
		want = "mainnet"
	}

	s.mempoolMu.Lock()
	defer s.mempoolMu.Unlock()

	if !s.readServicePrefs().MemetrackerEnabled {
		if s.mempoolEngine != nil {
			s.mempoolEngine.Stop()
			s.mempoolEngine = nil
		}
		return nil, nil
	}

	if s.mempoolEngine != nil {
		if s.mempoolEngine.Network() == want {
			return s.mempoolEngine, nil
		}
		s.mempoolEngine.Stop()
		s.mempoolEngine = nil
	}

	dir := filepath.Join(s.storageDir, "mempooltracker")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	eng, err := mempooltracker.Start(mempooltracker.Options{
		StorageDir: dir,
		Network:    want,
	})
	if err != nil {
		return nil, err
	}
	s.mempoolEngine = eng
	return eng, nil
}

// syncMemeTracker uses the embedded mempool tracker to estimate unconfirmed DOGE not yet in wallet state.
func (s *Server) syncMemeTracker(ctx context.Context, wf *WalletFile, st *WalletState) (pending float64, err error) {
	_ = ctx
	if wf == nil {
		return 0, nil
	}
	eng, err := s.ensureMempoolEngine(wf)
	if err != nil {
		return 0, err
	}
	if eng == nil {
		return 0, nil
	}
	addrs := wf.AllDistinctP2PKHAddresses()
	if len(addrs) == 0 {
		return 0, nil
	}

	confirmed := make(map[string]struct{})
	unconfirmedOut := make(map[string]struct{})
	manualPendingOut := 0.0
	for _, t := range st.Transactions {
		id := normalizeTxid(t.Txid)
		if id == "" {
			continue
		}
		if t.Confirmations > 0 {
			confirmed[id] = struct{}{}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(t.Direction), "out") {
			unconfirmedOut[id] = struct{}{}
			if strings.EqualFold(strings.TrimSpace(t.Source), "manual") && t.AmountDOGE > 0 {
				manualPendingOut += t.AmountDOGE
			}
		}
	}
	var sum float64
	var lastErr error
	for _, addr := range addrs {
		pend, e := eng.PendingMempoolDOGE(addr, confirmed)
		if e != nil {
			lastErr = e
			continue
		}
		sum += pend
	}
	// Mempool tracker sums credits to watched wallet addresses. For local sends this includes
	// our own change/recovery outputs (TX_C/TX_R), which should not inflate pending as inbound.
	// Remove credits for txids we already classify as unconfirmed outgoing, then apply local
	// manual-send pending debits so dashboard pending reflects wallet UX expectations.
	if len(unconfirmedOut) > 0 {
		_, live, _, _ := eng.DashboardSnapshot()
		for _, row := range live {
			if !truthyAny(row["tracked_match"]) {
				continue
			}
			txid := normalizeTxid(jsonStringAny(row["txid"]))
			if txid == "" {
				continue
			}
			if _, ok := unconfirmedOut[txid]; !ok {
				continue
			}
			amt := floatFromAny(row["amount_doge"])
			if amt > 0 {
				sum -= amt
			}
		}
	}
	sum -= manualPendingOut
	if lastErr != nil && sum == 0 {
		return 0, lastErr
	}
	return sum, nil
}
