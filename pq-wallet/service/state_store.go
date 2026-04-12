package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const maxMetricPoints = 400

// TxRecord is a cached transaction row for the UI (merged from SPV hints, explorer, RPC).
type TxRecord struct {
	Txid          string    `json:"txid"`
	Direction     string    `json:"direction"` // in | out | unknown
	AmountDOGE    float64   `json:"amount_doge"`
	Address       string    `json:"address,omitempty"`
	Confirmations int       `json:"confirmations"`
	BlockHeight   int64     `json:"block_height,omitempty"`
	PQHint        bool      `json:"pq_hint"`
	PQVerified    bool      `json:"pq_verified"`
	Source        string    `json:"source"` // explorer | rpc | spv | manual
	SeenAt        time.Time `json:"seen_at"`
}

// MetricPoint is one sample for charts (block height, SPV up, etc.).
type MetricPoint struct {
	T                  time.Time `json:"t"`
	HeaderHeight       int64     `json:"header_height"`
	BestBlockHash      string    `json:"best_block_hash,omitempty"`
	SPVRunning         bool      `json:"spv_running"`
	PeerCount          int       `json:"peer_count,omitempty"`
	SMPVActive     bool `json:"smpv_active"`
	MempoolTxCount int  `json:"mempool_tx_count,omitempty"` // from libdogecoin spv.log only
}

// WalletState is persisted as state.json (separate from keys).
type WalletState struct {
	Version             int       `json:"version"`
	Transactions        []TxRecord `json:"transactions"`
	Metrics             []MetricPoint `json:"metrics"`
	ExplorerBalanceDOGE float64   `json:"explorer_balance_doge,omitempty"`
	LastExplorerSync    time.Time `json:"last_explorer_sync,omitempty"`
}

func (s *Server) statePath() string {
	return filepath.Join(s.storageDir, "state.json")
}

func (s *Server) loadState() (*WalletState, error) {
	b, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &WalletState{Version: 1, Transactions: nil, Metrics: nil}, nil
		}
		return nil, err
	}
	var st WalletState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Version == 0 {
		st.Version = 1
	}
	return &st, nil
}

func (s *Server) saveState(st *WalletState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

func (s *Server) appendMetricPoint(st *WalletState, p MetricPoint) {
	st.Metrics = append(st.Metrics, p)
	if len(st.Metrics) > maxMetricPoints {
		st.Metrics = st.Metrics[len(st.Metrics)-maxMetricPoints:]
	}
}

func mergeTxRecords(existing []TxRecord, incoming []TxRecord) []TxRecord {
	byID := make(map[string]TxRecord)
	order := make([]string, 0)
	for _, t := range existing {
		if t.Txid == "" {
			continue
		}
		if _, ok := byID[t.Txid]; !ok {
			order = append(order, t.Txid)
		}
		byID[t.Txid] = t
	}
	for _, t := range incoming {
		if t.Txid == "" {
			continue
		}
		if prev, ok := byID[t.Txid]; ok {
			// merge: prefer higher confirmations, OR pq flags
			if t.Confirmations > prev.Confirmations {
				prev.Confirmations = t.Confirmations
			}
			if t.BlockHeight > prev.BlockHeight {
				prev.BlockHeight = t.BlockHeight
			}
			if t.PQHint {
				prev.PQHint = true
			}
			if t.PQVerified {
				prev.PQVerified = true
			}
			if t.AmountDOGE != 0 && prev.AmountDOGE == 0 {
				prev.AmountDOGE = t.AmountDOGE
			}
			if t.Direction != "" && t.Direction != "unknown" {
				prev.Direction = t.Direction
			}
			if t.Source != "" {
				prev.Source = t.Source
			}
			byID[t.Txid] = prev
			continue
		}
		byID[t.Txid] = t
		order = append(order, t.Txid)
	}
	out := make([]TxRecord, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}
