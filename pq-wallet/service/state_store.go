package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxMetricPoints = 400

// TxRecord is a cached transaction row for the UI (merged from SPV hints and P2P mempool visibility).
type TxRecord struct {
	Txid          string    `json:"txid"`
	Direction     string    `json:"direction"` // in | out | unknown
	AmountDOGE    float64   `json:"amount_doge"`
	RawHex        string    `json:"raw_hex,omitempty"`
	SPVHeaderRaw  string    `json:"spv_header_raw,omitempty"`   // optional block header raw hex (when available from SPV evidence)
	SPVMerkleRaw  string    `json:"spv_merkle_raw,omitempty"`   // optional merkle/proof raw hex (when available from SPV evidence)
	SPVProofNote  string    `json:"spv_proof_note,omitempty"`   // concise SPV evidence line for tx detail/debug
	SPVBlockHash  string    `json:"spv_block_hash,omitempty"`   // block hash associated with confirmation/proof
	SPVBlockHeight int64    `json:"spv_block_height,omitempty"` // block height associated with confirmation/proof
	Address       string    `json:"address,omitempty"`
	FeeDOGE       float64   `json:"fee_doge,omitempty"`
	Confirmations int       `json:"confirmations"`
	BlockHeight   int64     `json:"block_height,omitempty"`
	PQHint        bool      `json:"pq_hint"`
	PQVerified    bool      `json:"pq_verified"`
	Source        string    `json:"source"` // spv | memetracker | manual
	SeenAt        time.Time `json:"seen_at"`
}

// MetricPoint is one sample for charts (SPV tx visibility, mempool relay, etc.).
type MetricPoint struct {
	T             time.Time `json:"t"`
	HeaderHeight  int64     `json:"header_height"`
	BestBlockHash string    `json:"best_block_hash,omitempty"`
	SPVRunning    bool      `json:"spv_running"`
	PeerCount     int       `json:"peer_count,omitempty"`
	// MempoolTxCount is a heuristic from spv.log (legacy; charts prefer MempoolRelayCount).
	MempoolTxCount    int `json:"mempool_tx_count,omitempty"`
	SPVTxSeenCount    int `json:"spv_tx_seen_count,omitempty"`   // distinct txids from spv.log (wallet/relay activity)
	MempoolRelayCount int `json:"mempool_relay_count,omitempty"` // embedded MemeTracker relay visibility count
}

func directionRank(dir string) int {
	switch strings.ToLower(strings.TrimSpace(dir)) {
	case "out":
		return 3
	case "in":
		return 2
	default:
		return 1
	}
}

// WalletState is persisted as state.json (separate from keys).
type WalletState struct {
	Version      int           `json:"version"`
	Transactions []TxRecord    `json:"transactions"`
	Metrics      []MetricPoint `json:"metrics"`
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
		id := normalizeTxid(t.Txid)
		if id == "" {
			continue
		}
		t.Txid = id
		if _, ok := byID[id]; !ok {
			order = append(order, id)
		}
		byID[id] = t
	}
	for _, t := range incoming {
		id := normalizeTxid(t.Txid)
		if id == "" {
			continue
		}
		t.Txid = id
		if prev, ok := byID[id]; ok {
			manualOutHint := strings.EqualFold(strings.TrimSpace(t.Source), "manual") &&
				strings.EqualFold(strings.TrimSpace(t.Direction), "out")
			// Rows recorded at broadcast (send_pq_safe / send) carry exact pay-to + amount; SPV REST may later
			// surface the same txid as a "receive" (change UTXO) or mis-sized decode — never clobber those fields.
			manualSendPersisted := strings.EqualFold(strings.TrimSpace(prev.Source), "manual") &&
				strings.EqualFold(strings.TrimSpace(prev.Direction), "out")
			if manualOutHint {
				// Wallet-originated send metadata is authoritative for list display:
				// it carries the exact pay-to destination and amount selected at send time.
				prev.Direction = "out"
				if t.AmountDOGE > 0 {
					prev.AmountDOGE = t.AmountDOGE
				}
				if strings.TrimSpace(t.Address) != "" {
					prev.Address = t.Address
				}
			}
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
			if !manualSendPersisted {
				if t.AmountDOGE != 0 && prev.AmountDOGE == 0 {
					prev.AmountDOGE = t.AmountDOGE
				}
				if prev.Address == "" && t.Address != "" {
					prev.Address = t.Address
				}
			}
			if prev.RawHex == "" && t.RawHex != "" {
				prev.RawHex = t.RawHex
			}
			if prev.SPVHeaderRaw == "" && t.SPVHeaderRaw != "" {
				prev.SPVHeaderRaw = t.SPVHeaderRaw
			}
			if prev.SPVMerkleRaw == "" && t.SPVMerkleRaw != "" {
				prev.SPVMerkleRaw = t.SPVMerkleRaw
			}
			if prev.SPVProofNote == "" && t.SPVProofNote != "" {
				prev.SPVProofNote = t.SPVProofNote
			}
			if prev.SPVBlockHash == "" && t.SPVBlockHash != "" {
				prev.SPVBlockHash = t.SPVBlockHash
			}
			if t.SPVBlockHeight > prev.SPVBlockHeight {
				prev.SPVBlockHeight = t.SPVBlockHeight
			}
			if t.Direction != "" && t.Direction != "unknown" {
				restHint := strings.TrimSpace(t.RawHex) == ""
				enrichedFromRaw := strings.TrimSpace(prev.RawHex) != ""
				// SPV REST rows are UTXO/spend-centric; enrichSPVTxFromRawHex applies bitcoinj-style net from raw hex.
				// Never let a REST hint overwrite direction already reconciled from decoded raw tx.
				if enrichedFromRaw && restHint {
					// keep prev.Direction
				} else if manualSendPersisted && !strings.EqualFold(strings.TrimSpace(t.Source), "manual") {
					// Local send row wins over SPV/REST (e.g. /getUTXOs lists change back to self as "in" same txid).
				} else if directionRank(t.Direction) >= directionRank(prev.Direction) {
					prev.Direction = t.Direction
				}
			}
			if t.Source != "" && !manualSendPersisted {
				prev.Source = t.Source
			}
			// Prefer confirmed-chain timestamp once available, instead of keeping a mempool seen time forever.
			// Also replace placeholder wall times (e.g. log-seeded rows with no block height) when SPV supplies
			// height-derived SeenAt.
			// Manual broadcast rows record SeenAt at send time; SPV parsers sometimes attach an older mistaken
			// wall time when confirmations increase — never regress SeenAt backward for those rows.
			if manualSendPersisted {
				if !t.SeenAt.IsZero() {
					if prev.SeenAt.IsZero() {
						prev.SeenAt = t.SeenAt
					} else if !t.SeenAt.Before(prev.SeenAt) &&
						(t.Confirmations > prev.Confirmations ||
							t.BlockHeight > prev.BlockHeight ||
							(t.BlockHeight > 0 && prev.BlockHeight == 0)) {
						prev.SeenAt = t.SeenAt
					}
				}
			} else if (prev.SeenAt.IsZero() && !t.SeenAt.IsZero()) ||
				(t.Confirmations > prev.Confirmations && !t.SeenAt.IsZero()) ||
				(t.BlockHeight > 0 && !t.SeenAt.IsZero() && prev.BlockHeight == 0) {
				prev.SeenAt = t.SeenAt
			}
			byID[id] = prev
			continue
		}
		byID[id] = t
		order = append(order, id)
	}
	out := make([]TxRecord, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func normalizeTxid(txid string) string {
	id := strings.ToLower(strings.TrimSpace(txid))
	if len(id) != 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return ""
	}
	return id
}
