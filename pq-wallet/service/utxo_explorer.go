package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExplorerUTXO is one spendable output for the wallet address (from explorer API).
type ExplorerUTXO struct {
	TxID         string
	Vout         uint32
	Value        int64  // koinu (smallest units)
	ScriptPubHex string // hex, optional; if empty caller derives from wallet pubkey
}

// utxosFromSPVRESTForAddress converts GET /getUTXOs (libdogecoin SPV REST) into ExplorerUTXO rows
// for one wallet address. See https://lib.dogecoin.org/docs/rest
func (s *Server) utxosFromSPVRESTForAddress(address string) []ExplorerUTXO {
	raw, err := s.fetchSPVREST("/getUTXOs")
	if err != nil {
		return nil
	}
	rows := parseSPVRESTRows(raw, "in")
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	// If REST omits address on rows, attributing every row to each wallet address would multiply balances.
	allowEmptyRESTAddr := false
	if wf, err := s.loadWallet(); err == nil && wf != nil {
		addrs := wf.AllDistinctP2PKHAddresses()
		if len(addrs) == 1 && strings.EqualFold(strings.TrimSpace(addrs[0]), address) {
			allowEmptyRESTAddr = true
		}
	}
	out := make([]ExplorerUTXO, 0, len(rows))
	for _, r := range rows {
		id := normalizeTxid(r.Txid)
		if id == "" {
			continue
		}
		if r.Address != "" && !strings.EqualFold(strings.TrimSpace(r.Address), address) {
			continue
		}
		if strings.TrimSpace(r.Address) == "" && !allowEmptyRESTAddr {
			continue
		}
		val := int64(math.Round(r.AmountDOGE * 1e8))
		if val <= 0 {
			continue
		}
		out = append(out, ExplorerUTXO{
			TxID:  id,
			Vout:  r.Vout,
			Value: val,
		})
	}
	return out
}

// fetchUTXOsFromExplorer resolves spendable outputs: such list_unspent, then SPV REST /getUTXOs.
func (s *Server) fetchUTXOsFromExplorer(ctx context.Context, address string) ([]ExplorerUTXO, error) {
	_ = ctx
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("spv wallet db not available yet: %s", dbPath)
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("missing wallet address for local UTXO lookup")
	}
	testnet := false
	if wf, err := s.loadWallet(); err == nil && wf != nil {
		testnet = strings.EqualFold(wf.Network, "testnet")
	}
	if utxos, err := s.runSuchListUnspent(address, testnet); err == nil && len(utxos) > 0 {
		return utxos, nil
	}
	if utxos := s.utxosFromSPVRESTForAddress(address); len(utxos) > 0 {
		return utxos, nil
	}
	return nil, fmt.Errorf("SPV REST /getUTXOs and such list_unspent returned no UTXOs for this address (libdogecoin spv_wallet.db is not queried directly)")
}

func parseValueKoinu(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	if !strings.Contains(s, ".") {
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n, nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 1e8), nil
}
