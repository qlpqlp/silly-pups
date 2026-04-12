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
	p := wf.PrimaryAddress()
	if p == nil {
		return 0, nil
	}
	addr := strings.TrimSpace(p.P2PKH)
	if addr == "" {
		return 0, nil
	}

	confirmed := make(map[string]struct{})
	for _, t := range st.Transactions {
		if t.Txid == "" {
			continue
		}
		if t.Confirmations > 0 {
			confirmed[t.Txid] = struct{}{}
		}
	}
	return eng.PendingMempoolDOGE(addr, confirmed)
}
