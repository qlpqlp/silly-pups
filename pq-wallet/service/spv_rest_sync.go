package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type spvRESTTxRow struct {
	Txid          string
	Vout          uint32
	Address       string
	AmountDOGE    float64
	Direction     string
	Confirmations int
	BlockHeight   int64
	SeenAt        time.Time
}

var (
	reSPVHex64 = regexp.MustCompile(`(?i)\b([0-9a-f]{64})\b`)
	reSPVInt   = regexp.MustCompile(`\b(\d{1,16})\b`)
	reSPVDate  = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\s*UTC|Z)?)\b`)
)

// normalizeRESTDirectionHint maps libdogecoin / SPV REST text hints to in|out.
func normalizeRESTDirectionHint(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if s == "in" || s == "recv" || s == "receive" || s == "incoming" || s == "credit" ||
		strings.Contains(s, "receive") || strings.Contains(s, "recv") || strings.Contains(s, "credit") {
		return "in"
	}
	if s == "out" || s == "send" || s == "sent" || s == "outgoing" || s == "spent" || s == "debit" ||
		s == "spend" || s == "withdraw" || s == "payment" || s == "paid" ||
		strings.Contains(s, "sent") || strings.Contains(s, "spent") || strings.Contains(s, "debit") ||
		strings.Contains(s, "spend") || strings.Contains(s, "withdraw") {
		return "out"
	}
	return ""
}

func directionFromRESTCur(cur map[string]string, fallback string) string {
	for _, k := range []string{"direction", "type", "io", "kind", "in_out", "flow", "side"} {
		if d := normalizeRESTDirectionHint(cur[k]); d != "" {
			return d
		}
	}
	return fallback
}

func parseSPVDateTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, ly := range layouts {
		if t, err := time.Parse(ly, s); err == nil {
			return t.UTC().Unix()
		}
	}
	if m := reSPVDate.FindStringSubmatch(s); len(m) >= 2 {
		raw := strings.TrimSpace(strings.ReplaceAll(m[1], "T", " "))
		raw = strings.TrimSuffix(raw, "Z")
		raw = strings.TrimSpace(raw)
		if strings.HasSuffix(strings.ToUpper(raw), "UTC") {
			if t, err := time.Parse("2006-01-02 15:04:05 MST", raw); err == nil {
				return t.UTC().Unix()
			}
		}
		if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
			return t.UTC().Unix()
		}
	}
	return 0
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
	parseUnixToTime := func(s string) time.Time {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}
		}
		// Whole token must be digits (optional leading '-') so we never treat "2026-04-28 …" as Unix.
		for i := 0; i < len(s); i++ {
			c := s[i]
			if i == 0 && c == '-' {
				continue
			}
			if c < '0' || c > '9' {
				return time.Time{}
			}
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}
		}
		if n > 1_000_000_000_000 {
			n = n / 1000
		}
		if n < 1231006505 {
			return time.Time{}
		}
		return time.Unix(n, 0).UTC()
	}
	parseAnyToTime := func(s string) time.Time {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}
		}
		if t := parseUnixToTime(s); !t.IsZero() {
			return t
		}
		if unix := parseSPVDateTime(s); unix >= 1231006505 {
			return time.Unix(unix, 0).UTC()
		}
		return time.Time{}
	}
	parsePipeFallback := func(ln string) (spvRESTTxRow, bool) {
		var row spvRESTTxRow
		row.Direction = direction
		lowLn := strings.ToLower(ln)
		if strings.Contains(lowLn, "recv") || strings.Contains(lowLn, "receive") || strings.Contains(lowLn, "incoming") || strings.Contains(lowLn, "credit") {
			row.Direction = "in"
		} else if strings.Contains(lowLn, "sent") || strings.Contains(lowLn, "spent") || strings.Contains(lowLn, "debit") || strings.Contains(lowLn, "outgoing") ||
			strings.Contains(lowLn, "spend") || strings.Contains(lowLn, "withdraw") {
			row.Direction = "out"
		}
		if m := reSPVHex64.FindStringSubmatch(ln); len(m) >= 2 {
			row.Txid = normalizeTxid(m[1])
		}
		if row.Txid == "" {
			return spvRESTTxRow{}, false
		}
		parts := strings.Split(ln, "|")
		for _, p := range parts {
			tok := strings.TrimSpace(p)
			if tok == "" {
				continue
			}
			low := strings.ToLower(tok)
			if row.Address == "" && (strings.HasPrefix(tok, "D") || strings.HasPrefix(tok, "A") || strings.HasPrefix(tok, "n")) && len(tok) >= 26 && len(tok) <= 64 {
				row.Address = tok
				continue
			}
			if row.SeenAt.IsZero() {
				if ts := parseAnyToTime(tok); !ts.IsZero() && ts.Unix() >= 1231006505 {
					row.SeenAt = ts
					continue
				}
			}
			if strings.Contains(tok, ".") {
				if f, err := strconv.ParseFloat(tok, 64); err == nil && f > 0 {
					row.AmountDOGE = math.Abs(f)
					continue
				}
			}
			if row.Vout == 0 && reSPVInt.MatchString(tok) && !strings.ContainsAny(tok, ".-") {
				n := parseIntDefault(tok, -1)
				if n >= 0 && n < 100000 {
					row.Vout = uint32(n)
					continue
				}
			}
			if row.Confirmations == 0 && strings.Contains(low, "conf") {
				if n := parseIntDefault(tok, 0); n > 0 {
					row.Confirmations = n
					continue
				}
			}
		}
		return row, true
	}
	flush := func() {
		txid := normalizeTxid(cur["txid"])
		if txid == "" {
			cur = map[string]string{}
			return
		}
		amt, _ := strconv.ParseFloat(strings.TrimSpace(cur["amount"]), 64)
		conf := parseIntDefault(cur["confirmations"], 0)
		addr := strings.TrimSpace(cur["address"])
		vout := parseIntDefault(cur["vout"], 0)
		if vout < 0 {
			vout = 0
		}
		seen := parseAnyToTime(cur["timestamp"])
		if seen.IsZero() {
			seen = parseAnyToTime(cur["time"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["block_time"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["seen_at"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["date"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["time_received"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["header_time"])
		}
		if seen.IsZero() {
			seen = parseAnyToTime(cur["block_timestamp"])
		}
		rowDir := directionFromRESTCur(cur, direction)
		blkH := int64(parseIntDefault(cur["height"], 0))
		// spendable:0 on /getTransactions means "this wallet UTXO was consumed later", NOT that this txid is an
		// outgoing payment — receives that were later spent still show spendable:0. Net direction comes from
		// enrichSPVTxFromRawHex + walletNetFromPrevoutIndex, not from this flag.
		rows = append(rows, spvRESTTxRow{
			Txid:          txid,
			Vout:          uint32(vout),
			Address:       addr,
			AmountDOGE:    absFloat(amt),
			Direction:     rowDir,
			Confirmations: conf,
			BlockHeight:   blkH,
			SeenAt:        seen,
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
		i := strings.IndexAny(ln, ":=")
		if i <= 0 {
			if strings.Contains(ln, "|") {
				if row, ok := parsePipeFallback(ln); ok {
					rows = append(rows, row)
				}
			}
			continue
		}
		k := strings.ToLower(strings.TrimSpace(ln[:i]))
		v := strings.TrimSpace(ln[i+1:])
		cur[k] = v
	}
	flush()
	return rows
}

func (s *Server) fetchTxTimestampFromSoChain(txid string, testnet bool) time.Time {
	txid = normalizeTxid(txid)
	if txid == "" {
		return time.Time{}
	}
	coin := "DOGE"
	if testnet {
		coin = "DOGETEST"
	}
	u := fmt.Sprintf("https://sochain.com/api/v2/tx/%s/%s", coin, txid)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return time.Time{}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Time{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Time{}
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return time.Time{}
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		return time.Time{}
	}
	n := int64(parseIntDefault(fmt.Sprint(data["time"]), 0))
	if n >= 1231006505 {
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func parseSPVRESTChaintip(raw string) (height int64, bestHash string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ""
	}
	if h, bh, ts := parsePipeHeaderTip(raw); h > 0 {
		height = h
		if bh != "" {
			bestHash = bh
		}
		_ = ts
	}
	if strings.HasPrefix(raw, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			for _, k := range []string{"height", "chain_height", "tip_height", "best_height"} {
				if n := int64(parseIntDefault(fmt.Sprint(obj[k]), 0)); n > 0 {
					height = n
					break
				}
			}
			for _, k := range []string{"hash", "best_block_hash", "block_hash", "tip_hash"} {
				h := normalizeTxid(fmt.Sprint(obj[k]))
				if h != "" {
					bestHash = h
					break
				}
			}
		}
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for _, ln := range strings.Split(raw, "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if strings.Contains(low, "height") {
			i := strings.IndexAny(ln, ":=")
			if i > 0 && height <= 0 {
				if n := int64(parseIntDefault(strings.TrimSpace(ln[i+1:]), 0)); n > 0 {
					height = n
				}
			}
		}
		if strings.Contains(low, "hash") {
			i := strings.IndexAny(ln, ":=")
			if i > 0 && bestHash == "" {
				h := strings.ToLower(strings.TrimSpace(ln[i+1:]))
				if len(h) == 64 && normalizeTxid(h) != "" {
					bestHash = h
				}
			}
		}
	}
	if bestHash == "" {
		if m := reSPVHex64.FindStringSubmatch(raw); len(m) >= 2 {
			bestHash = normalizeTxid(m[1])
		}
	}
	if height <= 0 {
		matches := reSPVInt.FindAllStringSubmatch(raw, -1)
		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			n := int64(parseIntDefault(m[1], 0))
			if n > height {
				height = n
			}
		}
	}
	return
}

func parseSPVRESTTimestamp(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if _, _, ts := parsePipeHeaderTip(raw); ts > 0 {
		return ts
	}
	if ts := parseSPVDateTime(raw); ts > 0 {
		return ts
	}
	if reSPVInt.MatchString(raw) && !strings.ContainsAny(raw, " \n\t:={}") {
		n := int64(parseIntDefault(raw, 0))
		if n > 1_000_000_000_000 {
			n = n / 1000
		}
		if n >= 1231006505 {
			return n
		}
		return 0
	}
	if strings.HasPrefix(raw, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			for _, k := range []string{"timestamp", "time", "header_unix_time", "header_time"} {
				v := strings.TrimSpace(fmt.Sprint(obj[k]))
				if ts := parseSPVDateTime(v); ts > 0 {
					return ts
				}
				n := int64(parseIntDefault(v, 0))
				if n > 1_000_000_000_000 {
					n = n / 1000
				}
				if n >= 1231006505 {
					return n
				}
			}
		}
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for _, ln := range strings.Split(raw, "\n") {
		i := strings.IndexAny(ln, ":=")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(ln[:i]))
		if strings.Contains(k, "time") || strings.Contains(k, "timestamp") {
			v := strings.TrimSpace(ln[i+1:])
			if ts := parseSPVDateTime(v); ts > 0 {
				return ts
			}
			n := int64(parseIntDefault(v, 0))
			if n > 1_000_000_000_000 {
				n = n / 1000
			}
			if n >= 1231006505 {
				return n
			}
		}
	}
	if m := reSPVInt.FindStringSubmatch(raw); len(m) >= 2 {
		n := int64(parseIntDefault(m[1], 0))
		if n > 1_000_000_000_000 {
			n = n / 1000
		}
		if n >= 1231006505 {
			return n
		}
	}
	return 0
}

// seenAtApproxFromBlockHeight maps a confirmed block height to an approximate UTC time using
// tip height/time and a fixed mean block interval (Dogecoin ~1 min). Used when REST omits timestamps
// but includes height: lines.
func seenAtApproxFromBlockHeight(txHeight, tipHeight, tipUnix int64) time.Time {
	if txHeight <= 0 || tipHeight <= 0 || tipUnix <= 0 || tipUnix < 1_000_000_000 {
		return time.Time{}
	}
	depth := tipHeight - txHeight
	if depth < 0 {
		return time.Time{}
	}
	const avgBlockSec int64 = 60
	sec := depth * avgBlockSec
	approx := tipUnix - sec
	if approx < 1231006505 {
		return time.Time{}
	}
	return time.Unix(approx, 0).UTC()
}

// backfillSeenAtFromTip sets SeenAt from chain tip time + confirmations when SPV rows omit timestamps
// (Dogecoin Wallet-style: show approximate block time from header tip and depth).
func backfillSeenAtFromTip(txs []TxRecord, tipHeight, tipUnix int64) ([]TxRecord, bool) {
	if tipUnix <= 0 || tipHeight <= 0 || len(txs) == 0 {
		return txs, false
	}
	const avgBlockSec int64 = 60
	changed := false
	out := make([]TxRecord, len(txs))
	copy(out, txs)
	for i := range out {
		t := &out[i]
		if !t.SeenAt.IsZero() || t.Confirmations <= 0 {
			continue
		}
		conf := int64(t.Confirmations)
		if conf > tipHeight+1 {
			continue
		}
		sec := (conf - 1) * avgBlockSec
		if sec < 0 {
			sec = 0
		}
		approx := tipUnix - sec
		if approx >= 1231006505 {
			t.SeenAt = time.Unix(approx, 0).UTC()
			changed = true
		}
	}
	return out, changed
}

// mergeTransactionsFromSPVREST uses the spvnode REST API when available.
// It gives richer details for binary libdogecoin wallet files (non-SQLite).
// tipHeight/tipUnix from readSPVStatus improve SeenAt when REST rows lack times.
func (s *Server) mergeTransactionsFromSPVREST(st *WalletState, tipHeight, tipUnix int64) bool {
	if st == nil {
		return false
	}
	// Never use mis-parsed "unix" values (e.g. ISO year prefix) for SeenAt backfill.
	if tipUnix > 0 && tipUnix < 1_000_000_000 {
		tipUnix = 0
	}
	utxoRaw, errU := s.fetchSPVREST("/getUTXOs")
	txRaw, errT := s.fetchSPVREST("/getTransactions")
	if errU != nil && errT != nil {
		return false
	}
	// Use both sources for coverage:
	// - /getTransactions is the primary transaction history source
	// - /getUTXOs fills gaps for receive-side rows some SPV builds omit from tx history
	//
	// Guardrail: if a txid exists in /getTransactions, do not merge /getUTXOs rows for
	// that same txid (prevents UTXO/change rows from mutating OUT tx history).
	txRows := parseSPVRESTRows(txRaw, "unknown")
	utxoRows := parseSPVRESTRows(utxoRaw, "in")
	rows := make([]spvRESTTxRow, 0, len(txRows)+len(utxoRows))
	rows = append(rows, txRows...)
	txSeen := make(map[string]struct{}, len(txRows))
	for _, r := range txRows {
		if id := normalizeTxid(r.Txid); id != "" {
			txSeen[id] = struct{}{}
		}
	}
	for _, r := range utxoRows {
		id := normalizeTxid(r.Txid)
		if id == "" {
			continue
		}
		if _, ok := txSeen[id]; ok {
			continue
		}
		rows = append(rows, r)
	}
	for i := range rows {
		if rows[i].SeenAt.IsZero() && rows[i].BlockHeight > 0 && tipHeight > 0 && tipUnix > 0 {
			if ts := seenAtApproxFromBlockHeight(rows[i].BlockHeight, tipHeight, tipUnix); !ts.IsZero() {
				rows[i].SeenAt = ts
			}
		}
	}
	if len(rows) == 0 {
		return false
	}
	byTxid := map[string]TxRecord{}
	var txOrder []string
	testnet := false
	if wf, err := s.loadWallet(); err == nil && wf != nil {
		testnet = strings.EqualFold(wf.Network, "testnet")
	}
	tsLookups := 0
	maxChainTimestampLookups := parseIntDefault(strings.TrimSpace(os.Getenv("PUP_SPV_CHAIN_TS_LOOKUPS")), 0)
	if maxChainTimestampLookups < 0 {
		maxChainTimestampLookups = 0
	}
	if maxChainTimestampLookups > 8 {
		maxChainTimestampLookups = 8
	}
	for i := range rows {
		r := &rows[i]
		id := normalizeTxid(r.Txid)
		if id == "" {
			continue
		}
		if r.SeenAt.IsZero() && r.Confirmations > 0 && tsLookups < maxChainTimestampLookups {
			if ts := s.fetchTxTimestampFromSoChain(id, testnet); !ts.IsZero() {
				r.SeenAt = ts
			}
			tsLookups++
		}
		prev, ok := byTxid[id]
		if !ok {
			byTxid[id] = TxRecord{
				Txid:          id,
				Direction:     r.Direction,
				AmountDOGE:    r.AmountDOGE,
				Address:       r.Address,
				Confirmations: r.Confirmations,
				BlockHeight:   r.BlockHeight,
				Source:        "spv",
				SeenAt:        r.SeenAt,
			}
			txOrder = append(txOrder, id)
			continue
		}
		// bitcoinj lists one Transaction per txid (Dogecoin Wallet transaction screen). libdogecoin REST may
		// emit multiple lines per txid (one per spent output / vout). Do not sum those amounts.
		if prev.AmountDOGE == 0 && r.AmountDOGE != 0 {
			prev.AmountDOGE = r.AmountDOGE
		}
		if prev.Address == "" && r.Address != "" {
			prev.Address = r.Address
		}
		// Duplicate txids: do not force OUT over IN (mislabels receives listed under spent-output history).
		if prev.Direction == "" || strings.EqualFold(prev.Direction, "unknown") {
			if r.Direction != "" && !strings.EqualFold(r.Direction, "unknown") {
				prev.Direction = r.Direction
			}
		}
		if r.Confirmations > prev.Confirmations {
			prev.Confirmations = r.Confirmations
		}
		if r.BlockHeight > prev.BlockHeight {
			prev.BlockHeight = r.BlockHeight
		}
		if prev.SeenAt.IsZero() && !r.SeenAt.IsZero() {
			prev.SeenAt = r.SeenAt
		}
		byTxid[id] = prev
	}
	if len(byTxid) == 0 {
		return false
	}
	incoming := make([]TxRecord, 0, len(txOrder))
	for _, id := range txOrder {
		incoming = append(incoming, byTxid[id])
	}
	before := len(st.Transactions)
	merged := mergeTxRecords(st.Transactions, incoming)
	if tipHeight > 0 && tipUnix > 0 {
		if patched, ok := backfillSeenAtFromTip(merged, tipHeight, tipUnix); ok {
			merged = patched
		}
	}
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
			if !ok || t.AmountDOGE != o.AmountDOGE || t.Direction != o.Direction || t.Address != o.Address || t.Confirmations != o.Confirmations || t.BlockHeight != o.BlockHeight || !t.SeenAt.Equal(o.SeenAt) {
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
