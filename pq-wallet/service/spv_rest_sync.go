package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type spvRESTTxRow struct {
	Txid          string
	Address       string
	AmountDOGE    float64
	Direction     string
	Confirmations int
}

func (s *Server) fetchSPVREST(path string) (string, error) {
	base := s.spvHTTPBaseURL()
	u := strings.TrimRight(base, "/") + path
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("spv http %s: %s", path, resp.Status)
	}
	return string(body), nil
}

func parseSPVRESTRows(raw, direction string) []spvRESTTxRow {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	var rows []spvRESTTxRow
	cur := map[string]string{}
	flush := func() {
		txid := normalizeTxid(cur["txid"])
		if txid == "" {
			cur = map[string]string{}
			return
		}
		amt, _ := strconv.ParseFloat(strings.TrimSpace(cur["amount"]), 64)
		conf := parseIntDefault(cur["confirmations"], 0)
		addr := strings.TrimSpace(cur["address"])
		rows = append(rows, spvRESTTxRow{
			Txid:          txid,
			Address:       addr,
			AmountDOGE:    absFloat(amt),
			Direction:     direction,
			Confirmations: conf,
		})
		cur = map[string]string{}
	}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "-----") || strings.Contains(strings.ToLower(ln), "total unspent") || strings.Contains(strings.ToLower(ln), "spent balance") {
			flush()
			continue
		}
		i := strings.Index(ln, ":")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(ln[:i]))
		v := strings.TrimSpace(ln[i+1:])
		cur[k] = v
	}
	flush()
	return rows
}

func parseSPVRESTChaintip(raw string) (height int64, bestHash string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for _, ln := range strings.Split(raw, "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if strings.Contains(low, "height") {
			i := strings.Index(ln, ":")
			if i > 0 {
				height = int64(parseIntDefault(strings.TrimSpace(ln[i+1:]), 0))
			}
		}
		if strings.Contains(low, "hash") {
			i := strings.Index(ln, ":")
			if i > 0 {
				h := strings.ToLower(strings.TrimSpace(ln[i+1:]))
				if len(h) == 64 && normalizeTxid(h) != "" {
					bestHash = h
				}
			}
		}
	}
	return
}

func parseSPVRESTTimestamp(raw string) int64 {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for _, ln := range strings.Split(raw, "\n") {
		i := strings.Index(ln, ":")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(ln[:i]))
		if strings.Contains(k, "time") || strings.Contains(k, "timestamp") {
			return int64(parseIntDefault(strings.TrimSpace(ln[i+1:]), 0))
		}
	}
	return 0
}

// mergeTransactionsFromSPVREST uses the spvnode REST API when available.
// It gives richer details for binary libdogecoin wallet files (non-SQLite).
func (s *Server) mergeTransactionsFromSPVREST(st *WalletState) bool {
	if st == nil {
		return false
	}
	utxoRaw, errU := s.fetchSPVREST("/getUTXOs")
	txRaw, errT := s.fetchSPVREST("/getTransactions")
	if errU != nil && errT != nil {
		return false
	}
	rows := parseSPVRESTRows(utxoRaw, "in")
	rows = append(rows, parseSPVRESTRows(txRaw, "out")...)
	if len(rows) == 0 {
		return false
	}
	byTxid := map[string]TxRecord{}
	for _, r := range rows {
		id := normalizeTxid(r.Txid)
		if id == "" {
			continue
		}
		prev, ok := byTxid[id]
		if !ok {
			byTxid[id] = TxRecord{
				Txid:          id,
				Direction:     r.Direction,
				AmountDOGE:    r.AmountDOGE,
				Address:       r.Address,
				Confirmations: r.Confirmations,
				Source:        "spv",
			}
			continue
		}
		prev.AmountDOGE += r.AmountDOGE
		if prev.Address == "" && r.Address != "" {
			prev.Address = r.Address
		}
		if r.Confirmations > prev.Confirmations {
			prev.Confirmations = r.Confirmations
		}
		byTxid[id] = prev
	}
	if len(byTxid) == 0 {
		return false
	}
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
			if !ok || t.AmountDOGE != o.AmountDOGE || t.Direction != o.Direction || t.Address != o.Address || t.Confirmations != o.Confirmations {
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
