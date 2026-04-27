package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
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

// fetchUTXOsFromExplorer now resolves spendable outputs from libdogecoin's local spv_wallet.db
// (kept function name for compatibility with existing send flow).
func (s *Server) fetchUTXOsFromExplorer(ctx context.Context, address string) ([]ExplorerUTXO, error) {
	dbPath := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("spv wallet db not available yet: %s", dbPath)
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("missing wallet address for local UTXO lookup")
	}
	// Preferred path: native libdogecoin/such command, when available in the current build.
	testnet := false
	if wf, err := s.loadWallet(); err == nil && wf != nil {
		testnet = strings.EqualFold(wf.Network, "testnet")
	}
	if utxos, err := s.runSuchListUnspent(address, testnet); err == nil && len(utxos) > 0 {
		return utxos, nil
	}
	// Fallback for binary libdogecoin wallets: use spvnode REST getUTXOs and convert rows.
	if raw, err := s.fetchSPVREST("/getUTXOs"); err == nil {
		rows := parseSPVRESTRows(raw, "in")
		restUtxos := make([]ExplorerUTXO, 0, len(rows))
		for _, r := range rows {
			id := normalizeTxid(r.Txid)
			if id == "" {
				continue
			}
			if r.Address != "" && !strings.EqualFold(strings.TrimSpace(r.Address), address) {
				continue
			}
			val := int64(math.Round(r.AmountDOGE * 1e8))
			if val <= 0 {
				continue
			}
			restUtxos = append(restUtxos, ExplorerUTXO{
				TxID:  id,
				Vout:  r.Vout,
				Value: val,
			})
		}
		if len(restUtxos) > 0 {
			return restUtxos, nil
		}
	}
	if !isSQLiteDatabaseFile(dbPath) {
		return nil, fmt.Errorf("spv wallet file is not SQLite and such list_unspent returned no UTXOs for this address")
	}

	tablesRaw, err := s.sqliteRows(ctx, dbPath, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, fmt.Errorf("spv wallet db table scan failed: %w", err)
	}
	var tables []string
	for _, r := range tablesRaw {
		t := strings.TrimSpace(r)
		if t == "" || strings.HasPrefix(strings.ToLower(t), "sqlite_") {
			continue
		}
		tables = append(tables, t)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("spv wallet db has no visible tables")
	}

	var out []ExplorerUTXO
	seen := map[string]struct{}{}
	for _, t := range tables {
		rows, qerr := s.queryUTXOTable(ctx, dbPath, t, address)
		if qerr != nil || len(rows) == 0 {
			continue
		}
		for _, u := range rows {
			k := strings.ToLower(u.TxID) + ":" + strconv.FormatUint(uint64(u.Vout), 10)
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no spendable UTXOs found in local spv wallet db yet")
	}
	return out, nil
}

// isSQLiteDatabaseFile returns true when path looks like a SQLite3 database (libdogecoin's
// default spv_wallet.db is often a binary logdb file with the same .db extension — not SQLite).
func isSQLiteDatabaseFile(path string) bool {
	b := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Read(b); err != nil || len(b) < 15 {
		return false
	}
	return string(b[:15]) == "SQLite format 3"
}

func (s *Server) sqliteRows(ctx context.Context, dbPath, query string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "sqlite3", "-noheader", "-batch", "-separator", "\t", dbPath, query)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %w (%s)", err, strings.TrimSpace(string(b)))
	}
	txt := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(txt, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		out = append(out, ln)
	}
	return out, nil
}

func (s *Server) queryUTXOTable(ctx context.Context, dbPath, table, address string) ([]ExplorerUTXO, error) {
	colsRaw, err := s.sqliteRows(ctx, dbPath, fmt.Sprintf("PRAGMA table_info(%s)", sqliteIdent(table)))
	if err != nil {
		return nil, err
	}
	var cols []string
	for _, r := range colsRaw {
		parts := strings.Split(r, "\t")
		if len(parts) < 2 {
			continue
		}
		c := strings.TrimSpace(parts[1])
		if c != "" {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s has no columns", table)
	}

	txidCol := pickCol(cols, "txid", "tx_hash", "transaction_hash", "hash", "outpoint_txid")
	voutCol := pickCol(cols, "vout", "n", "out_n", "output_index", "tx_pos", "idx")
	valCol := pickCol(cols, "value", "amount", "koinu", "satoshis", "coin", "output_value")
	if txidCol == "" || voutCol == "" || valCol == "" {
		return nil, fmt.Errorf("table %s missing txid/vout/value shape", table)
	}
	scriptCol := pickCol(cols, "scriptpubkey", "script_pubkey", "script_hex", "pk_script", "script")
	addrCol := pickCol(cols, "address", "addr", "p2pkh", "pubkey_address")
	spentBool := pickCol(cols, "spent", "is_spent", "spendable")
	spentTx := pickCol(cols, "spent_txid", "spent_by_txid", "spend_txid", "spending_txid")

	var where []string
	if addrCol != "" {
		where = append(where, fmt.Sprintf("LOWER(%s)=LOWER('%s')", sqliteIdent(addrCol), sqlQuote(address)))
	}
	if spentBool != "" {
		// Treat 0/NULL as unspent; spendable=1 also accepted.
		w := fmt.Sprintf("(%s IS NULL OR %s=0 OR %s='0' OR %s=1 OR %s='1')",
			sqliteIdent(spentBool), sqliteIdent(spentBool), sqliteIdent(spentBool), sqliteIdent(spentBool), sqliteIdent(spentBool))
		where = append(where, w)
	}
	if spentTx != "" {
		where = append(where, fmt.Sprintf("(%s IS NULL OR %s='')", sqliteIdent(spentTx), sqliteIdent(spentTx)))
	}
	// Prefer rows that actually look like tx outpoints.
	where = append(where, fmt.Sprintf("LENGTH(%s)>=64", sqliteIdent(txidCol)))

	sel := []string{
		fmt.Sprintf("%s AS txid", sqliteIdent(txidCol)),
		fmt.Sprintf("%s AS vout", sqliteIdent(voutCol)),
		fmt.Sprintf("%s AS val", sqliteIdent(valCol)),
	}
	if scriptCol != "" {
		sel = append(sel, fmt.Sprintf("%s AS script", sqliteIdent(scriptCol)))
	} else {
		sel = append(sel, "'' AS script")
	}
	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(sel, ","), sqliteIdent(table))
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " LIMIT 500"

	rows, err := s.sqliteRows(ctx, dbPath, q)
	if err != nil {
		return nil, err
	}
	out := make([]ExplorerUTXO, 0, len(rows))
	for _, r := range rows {
		parts := strings.Split(r, "\t")
		if len(parts) < 4 {
			continue
		}
		txid := normalizeTxid(parts[0])
		if txid == "" {
			continue
		}
		voutU64, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			continue
		}
		value, err := parseValueKoinu(parts[2])
		if err != nil || value <= 0 {
			continue
		}
		scr := strings.TrimSpace(parts[3])
		out = append(out, ExplorerUTXO{
			TxID:         txid,
			Vout:         uint32(voutU64),
			Value:        value,
			ScriptPubHex: strings.ToLower(scr),
		})
	}
	return out, nil
}

func parseValueKoinu(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}
	// Prefer integer interpretation (koinu/sats in wallet db).
	if !strings.Contains(s, ".") {
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n, nil
		}
	}
	// Fallback: decimal DOGE value.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(f * 1e8), nil
}

func pickCol(cols []string, names ...string) string {
	lowerToOrig := make(map[string]string, len(cols))
	for _, c := range cols {
		lowerToOrig[strings.ToLower(strings.TrimSpace(c))] = c
	}
	for _, n := range names {
		if got, ok := lowerToOrig[strings.ToLower(n)]; ok {
			return got
		}
	}
	return ""
}

func sqliteIdent(name string) string {
	name = strings.ReplaceAll(name, `"`, `""`)
	return `"` + name + `"`
}

func sqlQuote(s string) string {
	return strings.ReplaceAll(s, `'`, `''`)
}
