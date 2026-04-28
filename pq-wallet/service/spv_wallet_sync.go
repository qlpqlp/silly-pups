package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type spvDBTxRow struct {
	Txid          string
	Address       string
	AmountDOGE    float64
	Direction     string
	Confirmations int
	BlockHeight   int64
	SeenAt        time.Time
}

// mergeTransactionsFromSPVWalletDB loads tx rows from libdogecoin's local spv_wallet.db and merges them into state.
// This moves PQ Wallet closer to dogecoin-wallet behavior: local persisted wallet history drives tx UI, not explorer APIs.
func (s *Server) mergeTransactionsFromSPVWalletDB(wf *WalletFile, st *WalletState) bool {
	if wf == nil || st == nil {
		return false
	}
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	if !isSQLiteDatabaseFile(dbPath) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	rows, err := s.readSPVWalletDBTxRows(ctx, dbPath, wf.AllDistinctP2PKHAddresses())
	if err != nil || len(rows) == 0 {
		return false
	}
	incoming := make([]TxRecord, 0, len(rows))
	for _, r := range rows {
		tr := TxRecord{
			Txid:          normalizeTxid(r.Txid),
			Direction:     r.Direction,
			AmountDOGE:    r.AmountDOGE,
			Address:       r.Address,
			Confirmations: r.Confirmations,
			BlockHeight:   r.BlockHeight,
			Source:        "spv",
			SeenAt:        r.SeenAt,
		}
		if tr.Txid == "" {
			continue
		}
		incoming = append(incoming, tr)
	}
	if len(incoming) == 0 {
		return false
	}
	before := len(st.Transactions)
	merged := mergeTxRecords(st.Transactions, incoming)
	changed := len(merged) != before
	if !changed {
		// detect field upgrades even when row count is unchanged
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
			if !ok {
				changed = true
				break
			}
			if t.Confirmations != o.Confirmations || t.AmountDOGE != o.AmountDOGE || t.Direction != o.Direction || t.Address != o.Address || t.BlockHeight != o.BlockHeight {
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

func (s *Server) readSPVWalletDBTxRows(ctx context.Context, dbPath string, walletAddrs []string) ([]spvDBTxRow, error) {
	tablesRaw, err := s.sqliteRows(ctx, dbPath, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	addrSet := map[string]struct{}{}
	for _, a := range walletAddrs {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			addrSet[a] = struct{}{}
		}
	}

	byTxid := map[string]spvDBTxRow{}
	for _, t := range tablesRaw {
		table := strings.TrimSpace(t)
		if table == "" || strings.HasPrefix(strings.ToLower(table), "sqlite_") {
			continue
		}
		rows, err := s.readSPVWalletDBTable(ctx, dbPath, table, addrSet)
		if err != nil {
			continue
		}
		for _, r := range rows {
			id := normalizeTxid(r.Txid)
			if id == "" {
				continue
			}
			prev, ok := byTxid[id]
			if !ok || r.Confirmations > prev.Confirmations {
				byTxid[id] = r
				continue
			}
			// enrich previous row with any missing fields.
			if prev.Address == "" && r.Address != "" {
				prev.Address = r.Address
			}
			if prev.AmountDOGE == 0 && r.AmountDOGE != 0 {
				prev.AmountDOGE = r.AmountDOGE
			}
			if (prev.Direction == "" || prev.Direction == "unknown") && r.Direction != "" {
				prev.Direction = r.Direction
			}
			if prev.BlockHeight == 0 && r.BlockHeight > 0 {
				prev.BlockHeight = r.BlockHeight
			}
			if prev.SeenAt.IsZero() && !r.SeenAt.IsZero() {
				prev.SeenAt = r.SeenAt
			}
			byTxid[id] = prev
		}
	}
	out := make([]spvDBTxRow, 0, len(byTxid))
	for _, r := range byTxid {
		out = append(out, r)
	}
	return out, nil
}

func (s *Server) readSPVWalletDBTable(ctx context.Context, dbPath, table string, addrSet map[string]struct{}) ([]spvDBTxRow, error) {
	colsRaw, err := s.sqliteRows(ctx, dbPath, "PRAGMA table_info("+sqliteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	var cols []string
	for _, r := range colsRaw {
		parts := strings.Split(r, "\t")
		if len(parts) >= 2 {
			cols = append(cols, strings.TrimSpace(parts[1]))
		}
	}
	txidCol := pickCol(cols, "txid", "tx_hash", "transaction_hash", "outpoint_txid", "hash")
	if txidCol == "" {
		return nil, nil
	}
	addrCol := pickCol(cols, "address", "addr", "p2pkh", "pubkey_address")
	amountCol := pickCol(cols, "amount", "value", "delta", "credit", "debit", "koinu", "satoshis")
	dirCol := pickCol(cols, "direction", "dir", "type", "inout")
	confCol := pickCol(cols, "confirmations", "confirmation", "depth", "conf")
	heightCol := pickCol(cols, "block_height", "height")
	timeCol := pickCol(cols, "seen_at", "timestamp", "time", "block_time", "created_at")

	sel := []string{sqliteIdent(txidCol) + " AS txid"}
	if addrCol != "" {
		sel = append(sel, sqliteIdent(addrCol)+" AS addr")
	} else {
		sel = append(sel, "'' AS addr")
	}
	if amountCol != "" {
		sel = append(sel, sqliteIdent(amountCol)+" AS amount")
	} else {
		sel = append(sel, "'' AS amount")
	}
	if dirCol != "" {
		sel = append(sel, sqliteIdent(dirCol)+" AS dir")
	} else {
		sel = append(sel, "'' AS dir")
	}
	if confCol != "" {
		sel = append(sel, sqliteIdent(confCol)+" AS conf")
	} else {
		sel = append(sel, "'0' AS conf")
	}
	if heightCol != "" {
		sel = append(sel, sqliteIdent(heightCol)+" AS height")
	} else {
		sel = append(sel, "'0' AS height")
	}
	if timeCol != "" {
		sel = append(sel, sqliteIdent(timeCol)+" AS ts")
	} else {
		sel = append(sel, "'0' AS ts")
	}
	q := "SELECT " + strings.Join(sel, ",") + " FROM " + sqliteIdent(table)
	if timeCol != "" {
		q += " ORDER BY " + sqliteIdent(timeCol) + " DESC"
	} else if heightCol != "" {
		q += " ORDER BY " + sqliteIdent(heightCol) + " DESC"
	} else {
		q += " ORDER BY rowid DESC"
	}
	q += " LIMIT 5000"
	rows, err := s.sqliteRows(ctx, dbPath, q)
	if err != nil {
		return nil, err
	}
	out := make([]spvDBTxRow, 0, len(rows))
	for _, r := range rows {
		parts := strings.Split(r, "\t")
		if len(parts) < 7 {
			continue
		}
		txid := normalizeTxid(parts[0])
		if txid == "" {
			continue
		}
		addr := strings.TrimSpace(parts[1])
		// Keep non-wallet counterparties too (closer to dogecoin-wallet transaction rows).
		// If the DB row has one of our receive addresses, keep that as-is.
		if len(addrSet) > 0 && addr != "" {
			if _, ok := addrSet[strings.ToLower(addr)]; !ok {
				// leave address untouched; direction/amount still provide useful detail.
			}
		}
		amountDOGE := parseDBMoneyToDOGE(parts[2])
		dir := normalizeDirection(parts[3], amountDOGE)
		conf := parseIntDefault(parts[4], 0)
		height := int64(parseIntDefault(parts[5], 0))
		seen := parseDBTime(parts[6])
		out = append(out, spvDBTxRow{
			Txid:          txid,
			Address:       addr,
			AmountDOGE:    absFloat(amountDOGE),
			Direction:     dir,
			Confirmations: conf,
			BlockHeight:   height,
			SeenAt:        seen,
		})
	}
	return out, nil
}

func parseIntDefault(s string, d int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return d
	}
	return n
}

func parseDBMoneyToDOGE(raw string) float64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if strings.Contains(s, ".") {
		f, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return f
		}
		return 0
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	// Heuristic: big integers are likely koinu/sats.
	if i > 5_000_000 || i < -5_000_000 {
		return float64(i) / 1e8
	}
	return float64(i)
}

func normalizeDirection(raw string, amountDOGE float64) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "in" || strings.Contains(s, "recv") || strings.Contains(s, "credit") {
		return "in"
	}
	if s == "out" || strings.Contains(s, "sent") || strings.Contains(s, "debit") {
		return "out"
	}
	if amountDOGE < 0 {
		return "out"
	}
	if amountDOGE > 0 {
		return "in"
	}
	return "unknown"
}

func parseDBTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		if n > 1_000_000_000_000 {
			return time.UnixMilli(n).UTC()
		}
		if n > 1_000_000_000 {
			return time.Unix(n, 0).UTC()
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// mergeTransactionsFromSuchListUnspent builds receive-side transaction rows from libdogecoin's
// wallet file via `such list_unspent` (works for the default binary spv_wallet.db). SQLite-only
// parsers miss this entirely; SPV log raw lines are optional.
func (s *Server) mergeTransactionsFromSuchListUnspent(wf *WalletFile, st *WalletState) bool {
	if wf == nil || st == nil {
		return false
	}
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	byTxid := make(map[string]TxRecord)
	spendableDOGE := 0.0
	for _, addr := range wf.AllDistinctP2PKHAddresses() {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		utxos, err := s.runSuchListUnspent(addr, testnet)
		if err != nil || len(utxos) == 0 {
			continue
		}
		for _, u := range utxos {
			id := normalizeTxid(u.TxID)
			if id == "" {
				continue
			}
			doge := float64(u.Value) / 1e8
			if doge > 0 {
				spendableDOGE += doge
			}
			prev, ok := byTxid[id]
			if !ok {
				byTxid[id] = TxRecord{
					Txid:          id,
					// UTXO-only rows can be receive OR self-change from an outgoing tx.
					// Keep unknown until REST/DB enrichment confirms direction.
					Direction:     "unknown",
					AmountDOGE:    doge,
					Address:       addr,
					Source:        "spv",
					Confirmations: 0,
				}
				continue
			}
			prev.AmountDOGE += doge
			if prev.Address == "" {
				prev.Address = addr
			}
			byTxid[id] = prev
		}
	}
	if len(byTxid) == 0 {
		s.suchMergeMu.Lock()
		s.lastSuchSpendableDOGE = 0
		s.lastSuchSpendableAt = time.Now()
		s.suchMergeMu.Unlock()
		return false
	}
	s.suchMergeMu.Lock()
	s.lastSuchSpendableDOGE = round2(spendableDOGE)
	s.lastSuchSpendableAt = time.Now()
	s.suchMergeMu.Unlock()
	incoming := make([]TxRecord, 0, len(byTxid))
	for _, tr := range byTxid {
		incoming = append(incoming, tr)
	}
	before := len(st.Transactions)
	merged := mergeTxRecords(st.Transactions, incoming)
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
			if !ok {
				changed = true
				break
			}
			if t.AmountDOGE != o.AmountDOGE || t.Direction != o.Direction || t.Address != o.Address {
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
