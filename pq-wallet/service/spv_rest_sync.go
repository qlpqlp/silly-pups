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
	// RawHex is set when REST or logs supply full tx hex for enrichSPVTxFromRawHex.
	RawHex string
	// Source overrides TxRecord.Source when non-empty (e.g. "manual" vs default "spv").
	Source string
	// Optional lines from /getTransactions (spent UTXO blocks) — spending tx + Dogecoin Wallet-style payee.
	SpendTxid            string
	PayTo                string
	PayAmountDOGE        float64
	SpendBlockHeight     int64
	SpendConfirmations   int
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
		spendTx := normalizeTxid(cur["spend_txid"])
		payTo := strings.TrimSpace(cur["pay_to"])
		payAmtStr := strings.TrimSpace(cur["pay_amount"])
		var payAmt float64
		if payAmtStr != "" {
			if f, err := strconv.ParseFloat(payAmtStr, 64); err == nil {
				payAmt = f
			}
		}
		spH := int64(parseIntDefault(cur["spend_height"], 0))
		spConf := parseIntDefault(cur["spend_confirmations"], 0)
		// spendable:0 on /getTransactions means "this wallet UTXO was consumed later", NOT that this txid is an
		// outgoing payment — receives that were later spent still show spendable:0. Net direction comes from
		// enrichSPVTxFromRawHex + walletNetFromPrevoutIndex, not from this flag.
		rows = append(rows, spvRESTTxRow{
			Txid:               txid,
			Vout:               uint32(vout),
			Address:            addr,
			AmountDOGE:         absFloat(amt),
			Direction:          rowDir,
			Confirmations:      conf,
			BlockHeight:        blkH,
			SeenAt:             seen,
			SpendTxid:          spendTx,
			PayTo:              payTo,
			PayAmountDOGE:      absFloat(payAmt),
			SpendBlockHeight:   spH,
			SpendConfirmations: spConf,
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

// parseSPVRESTGetSpends parses GET /getSpends (libdogecoin outgoing-tx blocks with nested "output:" sections).
// rest.c defines total_in=wallet debit (koinu), sent=external vout sum, change=wallet (mine) vout sum, fee=debit-total_out.
// Wallet net effect for the tx is (mine outputs - debit) == (change - total_in) in header units — matches explorer net pills.
// When total_in is 0 (wallet debit not indexed), we emphasize wallet credits for mixed txs (SoChain-style small receives).
func parseSPVRESTGetSpends(raw string) []spvRESTTxRow {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n")
	if raw == "" {
		return nil
	}
	var rows []spvRESTTxRow
	for _, blk := range strings.Split(raw, "----------------------") {
		blk = strings.TrimSpace(blk)
		if blk == "" {
			continue
		}
		first := strings.ToLower(strings.TrimSpace(strings.SplitN(blk, "\n", 2)[0]))
		if strings.HasPrefix(first, "outgoing transactions") || strings.HasPrefix(first, "total sent") {
			continue
		}
		var txid string
		var height int64
		var debit, sentHdr, changeHdr, feeHdr float64
		type outRec struct {
			addr string
			mine bool
			amt  float64
		}
		var outs []outRec
		mode := "hdr"
		var cur outRec
		curOpen := false
		flushCur := func() {
			if mode == "out" && curOpen {
				outs = append(outs, cur)
			}
			cur = outRec{}
			curOpen = false
		}
		for _, ln := range strings.Split(blk, "\n") {
			ts := strings.TrimSpace(ln)
			if ts == "" {
				continue
			}
			if strings.EqualFold(ts, "output:") {
				flushCur()
				mode = "out"
				curOpen = true
				continue
			}
			i := strings.IndexAny(ln, ":=")
			if i <= 0 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(ln[:i]))
			v := strings.TrimSpace(ln[i+1:])
			switch mode {
			case "hdr":
				switch k {
				case "txid":
					txid = normalizeTxid(v)
				case "height":
					height, _ = strconv.ParseInt(v, 10, 64)
				case "total_in":
					debit, _ = strconv.ParseFloat(v, 64)
				case "sent":
					sentHdr, _ = strconv.ParseFloat(v, 64)
				case "change":
					changeHdr, _ = strconv.ParseFloat(v, 64)
				case "fee":
					feeHdr, _ = strconv.ParseFloat(v, 64)
				}
			case "out":
				if !curOpen {
					continue
				}
				switch k {
				case "address":
					cur.addr = v
				case "is_mine":
					cur.mine = (v == "1")
				case "amount":
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						cur.amt = math.Abs(f)
					}
				}
			}
		}
		flushCur()
		if txid == "" {
			continue
		}
		mineSum := changeHdr
		extSum := sentHdr
		var mineAddr, extAddr string
		var mineFromOuts, extFromOuts float64
		for _, o := range outs {
			if o.mine {
				mineFromOuts += o.amt
				if mineAddr == "" && o.addr != "" && o.addr != "(non-p2pkh)" {
					mineAddr = o.addr
				}
			} else if o.addr != "" && o.addr != "(non-p2pkh)" {
				extFromOuts += o.amt
				if extAddr == "" {
					extAddr = o.addr
				}
			}
		}
		if mineFromOuts > 0 {
			mineSum = mineFromOuts
		}
		if extFromOuts > 0 {
			extSum = extFromOuts
		}
		const eps = 1e-9
		var dir string
		var amt float64
		addr := ""
		if debit > eps {
			net := mineSum - debit
			if math.Abs(net) < eps {
				continue
			}
			if net < 0 {
				dir = "out"
				amt = round2(math.Abs(net))
				addr = extAddr
			} else {
				dir = "in"
				amt = round2(net)
				addr = mineAddr
			}
		} else {
			if mineSum > eps && extSum > eps {
				dir = "in"
				amt = round2(mineSum)
				addr = mineAddr
			} else if extSum > eps && mineSum <= eps {
				dir = "out"
				amt = round2(extSum + feeHdr)
				addr = extAddr
			} else if mineSum > eps {
				dir = "in"
				amt = round2(mineSum)
				addr = mineAddr
			} else if extSum > eps {
				dir = "out"
				amt = round2(extSum + feeHdr)
				addr = extAddr
			} else {
				continue
			}
		}
		if amt <= 0 {
			continue
		}
		rows = append(rows, spvRESTTxRow{
			Txid:        txid,
			Direction:   dir,
			AmountDOGE:  amt,
			Address:     addr,
			BlockHeight: height,
		})
	}
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

// parseSPVRESTWalletBalance parses GET /getBalance (libdogecoin SPV REST):
// https://lib.dogecoin.org/docs/rest — body line: Wallet balance: <balance>
func parseSPVRESTWalletBalance(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	for _, ln := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if !strings.HasPrefix(low, "wallet balance") {
			continue
		}
		i := strings.IndexAny(ln, ":=")
		if i <= 0 {
			continue
		}
		v := strings.TrimSpace(ln[i+1:])
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f, true
		}
	}
	return 0, false
}

// parseSPVRESTTotalUnspent parses the trailer line from GET /getUTXOs:
// Total Unspent: <total_unspent_balance>
func parseSPVRESTTotalUnspent(raw string) (float64, bool) {
	for _, ln := range strings.Split(strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n"), "\n") {
		low := strings.ToLower(strings.TrimSpace(ln))
		if !strings.HasPrefix(low, "total unspent") {
			continue
		}
		i := strings.IndexAny(ln, ":=")
		if i <= 0 {
			continue
		}
		v := strings.TrimSpace(ln[i+1:])
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f, true
		}
	}
	return 0, false
}

// computeSPVRESTSpendableDOGE returns total spendable DOGE from SPV REST when the node responds.
func (s *Server) computeSPVRESTSpendableDOGE() (float64, bool) {
	if raw, err := s.fetchSPVREST("/getBalance"); err == nil {
		if f, ok := parseSPVRESTWalletBalance(raw); ok {
			return round2(f), true
		}
	}
	if raw, err := s.fetchSPVREST("/getUTXOs"); err == nil {
		if f, ok := parseSPVRESTTotalUnspent(raw); ok {
			return round2(f), true
		}
	}
	return 0, false
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

// mergeSPVHintRowsIntoState merges REST- or wallet-file-shaped hint rows into wallet state transactions.
func (s *Server) mergeSPVHintRowsIntoState(st *WalletState, rows []spvRESTTxRow, tipHeight, tipUnix int64) bool {
	if st == nil {
		return false
	}
	if len(rows) == 0 {
		return false
	}
	if tipUnix > 0 && tipUnix < 1_000_000_000 {
		tipUnix = 0
	}
	for i := range rows {
		if rows[i].Confirmations == 0 && rows[i].BlockHeight > 0 && tipHeight > 0 {
			d := tipHeight - rows[i].BlockHeight + 1
			if d > 0 && d <= tipHeight+1 {
				rows[i].Confirmations = int(d)
			}
		}
		if rows[i].SeenAt.IsZero() && rows[i].BlockHeight > 0 && tipHeight > 0 && tipUnix > 0 {
			if ts := seenAtApproxFromBlockHeight(rows[i].BlockHeight, tipHeight, tipUnix); !ts.IsZero() {
				rows[i].SeenAt = ts
			}
		}
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
		src := strings.TrimSpace(r.Source)
		if src == "" {
			src = "spv"
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
				Source:        src,
				SeenAt:        r.SeenAt,
				RawHex:        strings.TrimSpace(r.RawHex),
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
		if strings.TrimSpace(prev.RawHex) == "" && strings.TrimSpace(r.RawHex) != "" {
			prev.RawHex = strings.TrimSpace(r.RawHex)
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
	// Spent-UTXO REST blocks are keyed by the *funding* txid; optional spend_txid + pay_* describe the actual
	// OUT row (bitcoinj / Dogecoin Wallet semantics). Materialize one TxRecord per spending txid.
	orderSeen := make(map[string]struct{}, len(txOrder))
	for _, id := range txOrder {
		orderSeen[id] = struct{}{}
	}
	for i := range rows {
		r := &rows[i]
		sid := normalizeTxid(r.SpendTxid)
		if sid == "" {
			continue
		}
		payTo := strings.TrimSpace(r.PayTo)
		payAmt := r.PayAmountDOGE
		if payTo == "" && payAmt <= 0 {
			continue
		}
		spConf := r.SpendConfirmations
		spBh := r.SpendBlockHeight
		if spConf <= 0 {
			spConf = r.Confirmations
		}
		if spBh <= 0 {
			spBh = r.BlockHeight
		}
		prev, ok := byTxid[sid]
		if !ok {
			spSrc := strings.TrimSpace(r.Source)
			if spSrc == "" {
				spSrc = "spv"
			}
			byTxid[sid] = TxRecord{
				Txid:          sid,
				Direction:     "out",
				AmountDOGE:    round2(payAmt),
				Address:       payTo,
				Confirmations: spConf,
				BlockHeight:   spBh,
				Source:        spSrc,
				SeenAt:        r.SeenAt,
				RawHex:        strings.TrimSpace(r.RawHex),
			}
			if _, dup := orderSeen[sid]; !dup {
				txOrder = append(txOrder, sid)
				orderSeen[sid] = struct{}{}
			}
			continue
		}
		if payTo != "" && strings.TrimSpace(prev.Address) == "" {
			prev.Address = payTo
		}
		if payAmt > 0 && prev.AmountDOGE <= 0 {
			prev.AmountDOGE = round2(payAmt)
		}
		if dir := strings.ToLower(strings.TrimSpace(prev.Direction)); dir == "" || dir == "unknown" || dir == "in" {
			prev.Direction = "out"
		}
		if spConf > prev.Confirmations {
			prev.Confirmations = spConf
		}
		if spBh > prev.BlockHeight {
			prev.BlockHeight = spBh
		}
		if prev.SeenAt.IsZero() && !r.SeenAt.IsZero() {
			prev.SeenAt = r.SeenAt
		}
		if strings.TrimSpace(prev.RawHex) == "" && strings.TrimSpace(r.RawHex) != "" {
			prev.RawHex = strings.TrimSpace(r.RawHex)
		}
		byTxid[sid] = prev
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
			if !ok || t.AmountDOGE != o.AmountDOGE || t.Direction != o.Direction || t.Address != o.Address || t.Confirmations != o.Confirmations || t.BlockHeight != o.BlockHeight || !t.SeenAt.Equal(o.SeenAt) || t.PQHint != o.PQHint || strings.TrimSpace(t.RawHex) != strings.TrimSpace(o.RawHex) {
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

// mergeTransactionsFromSPVREST uses the spvnode REST API when available.
// Gated by shouldIngestSPVRESTTxHints (PUP_SPV_REST_TX=0 disables).
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
	spendRaw, errS := s.fetchSPVREST("/getSpends")
	if errU != nil && errT != nil && errS != nil {
		return false
	}
	// Use multiple sources for coverage:
	// - /getSpends (when present) lists wallet-affecting txs with debit/mine outputs — net row per txid (in or out).
	// - /getTransactions lists spent wallet UTXOs (historical receives); default direction "in".
	// - /getUTXOs fills gaps for receive-side rows some SPV builds omit from tx history
	//
	// Guardrail: libdogecoin /getTransactions only lists *spent* wallet UTXOs (see rest.c). Unspent outputs
	// for the same funding txid only appear under /getUTXOs. Do not mark funding txids from /getTransactions
	// here — that would drop still-unspent vouts when another vout from the same tx was already spent.
	//
	// Mark spending txids (spend_txid) so /getUTXOs does not duplicate the spending payment row.
	spendRows := parseSPVRESTGetSpends(spendRaw)
	txRows := parseSPVRESTRows(txRaw, "in")
	utxoRows := parseSPVRESTRows(utxoRaw, "in")
	rows := make([]spvRESTTxRow, 0, len(spendRows)+len(txRows)+len(utxoRows))
	rows = append(rows, spendRows...)
	rows = append(rows, txRows...)
	txSeen := make(map[string]struct{}, len(spendRows)+len(txRows)+8)
	for _, r := range spendRows {
		if id := normalizeTxid(r.Txid); id != "" {
			txSeen[id] = struct{}{}
		}
	}
	for _, r := range txRows {
		if sid := normalizeTxid(r.SpendTxid); sid != "" {
			txSeen[sid] = struct{}{}
		}
	}
	for _, t := range st.Transactions {
		id := normalizeTxid(t.Txid)
		if id == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(t.Source), "manual") && strings.EqualFold(strings.TrimSpace(t.Direction), "out") {
			// Treat broadcast spend txids like REST "known" ids so /getUTXOs does not add a change row as IN.
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
	if len(rows) == 0 {
		return false
	}
	return s.mergeSPVHintRowsIntoState(st, rows, tipHeight, tipUnix)
}
