package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// mergeTransactionsFromSPVWalletDB would merge tx rows from a local wallet DB; libdogecoin ships a binary spv_wallet.db,
// so transaction rows come from SPV REST and log enrichment instead.
func (s *Server) mergeTransactionsFromSPVWalletDB(wf *WalletFile, st *WalletState) bool {
	if wf == nil || st == nil {
		return false
	}
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	return false
}

func parseIntDefault(s string, d int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return d
	}
	return n
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// mergeTransactionsFromSuchListUnspent refreshes the spendable-DOGE cache from `such list_unspent`
// when the CLI works; otherwise tries SPV REST /getBalance per libdogecoin docs. It does not merge
// tx rows (those come from SPV REST /getTransactions, logs, and DB pipelines).
func (s *Server) mergeTransactionsFromSuchListUnspent(wf *WalletFile, st *WalletState) bool {
	if wf == nil || st == nil {
		return false
	}
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	spendableDOGE := 0.0
	successfulSuchReads := 0
	for _, addr := range wf.AllDistinctP2PKHAddresses() {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		utxos, err := s.runSuchListUnspent(addr, testnet)
		if err != nil {
			continue
		}
		successfulSuchReads++
		if len(utxos) == 0 {
			continue
		}
		for _, u := range utxos {
			doge := float64(u.Value) / 1e8
			if doge > 0 {
				spendableDOGE += doge
			}
		}
	}
	// Only update cached spendable if at least one such call succeeded.
	// This avoids replacing a good cached balance with zero on transient CLI/SPV failures.
	if successfulSuchReads > 0 {
		s.suchMergeMu.Lock()
		s.lastSuchSpendableDOGE = round2(spendableDOGE)
		s.lastSuchSpendableAt = time.Now()
		s.suchMergeMu.Unlock()
	}
	// Keep this function spendable-only. UTXO snapshots do not encode reliable
	// IN/OUT transaction direction during sync (change outputs can look like IN).
	// Authoritative tx rows must come from SPV REST and log enrichment.
	return false
}
