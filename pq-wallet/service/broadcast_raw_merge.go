package main

import (
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// broadcast.log lines are prefixed with RFC3339 time + space (see appendBroadcastLogLine).
	reBroadcastLogTimePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\S+\s+`)
	// logBroadcastDetails uses arbitrary tags: tx_broadcast, send_pq_safe, send_pq_safe_txr, …
	reBroadcastTxLine   = regexp.MustCompile(`(?i)^\S+\s+txid=([0-9a-f]{64})\s+signed_hex_len=(\d+)`)
	reBroadcastHexChunk = regexp.MustCompile(`(?i)^\S+\s+SIGNED_RAW_HEX\s+off=(\d+)\s+len=(\d+)\s+([0-9a-f]+)\s*$`)
	reBroadcastLineTxid = regexp.MustCompile(`(?i)^\S+\s+txid=([0-9a-f]{64})\b`)
	reBroadcastPaymentHintLine = regexp.MustCompile(`(?i)^\S+\s+PAYMENT_HINT\s+txid=([0-9a-f]{64})\s+to=([A-Za-z0-9]{26,64})\s+amount_doge=([0-9]+(?:\.[0-9]+)?)\s*$`)
	reBroadcastSpentPrevoutHintLine = regexp.MustCompile(`(?i)^\S+\s+SPENT_PREVOUT_HINT\s+prev_txid=([0-9a-f]{64})\s+prev_vout=(\d+)\s+spend_txid=([0-9a-f]{64})\s+to=([A-Za-z0-9]{26,64})\s+amount_doge=([0-9]+(?:\.[0-9]+)?)\s*$`)
	// sendtx sometimes logs: "start broadcasting transaction: <64hex>"
	reSendtxBroadcastStartLine = regexp.MustCompile(`(?i)start\s+broadcasting\s+transaction:\s*([0-9a-f]{64})\b`)
)

func stripBroadcastLogTimePrefix(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(reBroadcastLogTimePrefix.ReplaceAllString(line, ""))
}

// extractBroadcastOutTxidsFromTail returns txids from broadcast.log (any source tag + sendtx hints), first-seen order.
func extractBroadcastOutTxidsFromTail(tail string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(id string) {
		id = normalizeTxid(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, line := range strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n") {
		pl := stripBroadcastLogTimePrefix(strings.TrimSpace(line))
		if pl == "" {
			continue
		}
		if strings.Contains(strings.ToUpper(pl), "SIGNED_RAW_HEX") {
			continue
		}
		if m := reSendtxBroadcastStartLine.FindStringSubmatch(pl); len(m) >= 2 {
			add(m[1])
		}
		if m := reBroadcastLineTxid.FindStringSubmatch(pl); len(m) >= 2 {
			add(m[1])
		}
	}
	return out
}

type broadcastHexChunk struct {
	off int
	hex string
}

type broadcastPaymentHint struct {
	ToAddress  string
	AmountDOGE float64
}

// broadcastSpentPrevoutHint links /getTransactions "spent" rows (keyed by funding txid) to the real spend txid.
type broadcastSpentPrevoutHint struct {
	PrevTxid   string
	SpendTxid  string
	ToAddress  string
	AmountDOGE float64
}

func extractBroadcastPaymentHintsFromTail(tail string) map[string]broadcastPaymentHint {
	out := map[string]broadcastPaymentHint{}
	for _, line := range strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n") {
		pl := stripBroadcastLogTimePrefix(strings.TrimSpace(line))
		if pl == "" {
			continue
		}
		m := reBroadcastPaymentHintLine.FindStringSubmatch(pl)
		if len(m) < 4 {
			continue
		}
		txid := normalizeTxid(m[1])
		to := strings.TrimSpace(m[2])
		amt, _ := strconv.ParseFloat(strings.TrimSpace(m[3]), 64)
		if txid == "" || to == "" || amt <= 0 {
			continue
		}
		out[txid] = broadcastPaymentHint{ToAddress: to, AmountDOGE: amt}
	}
	return out
}

func extractBroadcastSpentPrevoutHintsFromTail(tail string) []broadcastSpentPrevoutHint {
	var out []broadcastSpentPrevoutHint
	for _, line := range strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n") {
		pl := stripBroadcastLogTimePrefix(strings.TrimSpace(line))
		if pl == "" {
			continue
		}
		m := reBroadcastSpentPrevoutHintLine.FindStringSubmatch(pl)
		if len(m) < 6 {
			continue
		}
		prev := normalizeTxid(m[1])
		spend := normalizeTxid(m[3])
		to := strings.TrimSpace(m[4])
		amt, _ := strconv.ParseFloat(strings.TrimSpace(m[5]), 64)
		if prev == "" || spend == "" || to == "" || amt <= 0 {
			continue
		}
		out = append(out, broadcastSpentPrevoutHint{
			PrevTxid:   prev,
			SpendTxid:  spend,
			ToAddress:  to,
			AmountDOGE: amt,
		})
	}
	return out
}

// applyBroadcastSpentPrevoutRewrites rewrites SPV /getTransactions rows that use the funding txid as the row key
// into the actual broadcast spend txid with pay-to metadata (matches PAYMENT_HINT / local send row).
func applyBroadcastSpentPrevoutRewrites(st *WalletState, tail string) bool {
	if st == nil {
		return false
	}
	hints := extractBroadcastSpentPrevoutHintsFromTail(tail)
	if len(hints) == 0 {
		return false
	}
	prevMeta := make(map[string]broadcastSpentPrevoutHint, len(hints))
	for _, h := range hints {
		prevMeta[h.PrevTxid] = h
	}
	changed := false
	for i := range st.Transactions {
		id := normalizeTxid(st.Transactions[i].Txid)
		h, ok := prevMeta[id]
		if !ok {
			continue
		}
		st.Transactions[i].Txid = h.SpendTxid
		st.Transactions[i].Direction = "out"
		st.Transactions[i].Address = h.ToAddress
		st.Transactions[i].AmountDOGE = h.AmountDOGE
		st.Transactions[i].Source = "manual"
		changed = true
	}
	if !changed {
		return false
	}
	st.Transactions = mergeTxRecords(nil, st.Transactions)
	return true
}

// mergeRawHexFromBroadcastLog reassembles SIGNED_RAW_HEX chunks from broadcast.log into TxRecord.RawHex
// so enrichSPVTxFromRawHex can classify OUT spends and counterparty amounts.
func (s *Server) mergeRawHexFromBroadcastLog(st *WalletState) bool {
	if st == nil {
		return false
	}
	tail, err := readFileTail(s.broadcastLogPath(), 4<<20)
	if err != nil || strings.TrimSpace(tail) == "" {
		return false
	}
	type acc struct {
		wantLen int
		chunks  []broadcastHexChunk
	}
	curTx := ""
	blocks := map[string]*acc{}
	flushBlock := func() {
		curTx = ""
	}
	lines := strings.Split(strings.ReplaceAll(tail, "\r\n", "\n"), "\n")
	for _, line := range lines {
		pl := stripBroadcastLogTimePrefix(line)
		if pl == "" {
			continue
		}
		if m := reBroadcastTxLine.FindStringSubmatch(pl); len(m) >= 4 {
			want, _ := strconv.Atoi(strings.TrimSpace(m[3]))
			id := normalizeTxid(m[2])
			if id == "" || want <= 0 {
				flushBlock()
				continue
			}
			curTx = id
			blocks[id] = &acc{wantLen: want, chunks: nil}
			continue
		}
		if curTx == "" {
			continue
		}
		if m := reBroadcastHexChunk.FindStringSubmatch(pl); len(m) >= 5 {
			off, _ := strconv.Atoi(m[2])
			hexPart := strings.TrimSpace(strings.ToLower(m[4]))
			if off < 0 || hexPart == "" {
				continue
			}
			b := blocks[curTx]
			if b == nil {
				continue
			}
			b.chunks = append(b.chunks, broadcastHexChunk{off: off, hex: hexPart})
		}
	}
	changed := false
	for txid, b := range blocks {
		if b == nil || b.wantLen <= 0 || len(b.chunks) == 0 {
			continue
		}
		// wantLen is len(hex) ASCII chars (see logBroadcastDetails).
		full, ok := assembleBroadcastHexChunks(b.chunks, b.wantLen)
		if !ok || full == "" {
			continue
		}
		if !looksLikeDogecoinRawTxHex(full) {
			continue
		}
		for i := range st.Transactions {
			if normalizeTxid(st.Transactions[i].Txid) != txid {
				continue
			}
			if strings.TrimSpace(st.Transactions[i].RawHex) == "" {
				st.Transactions[i].RawHex = full
				changed = true
			}
			break
		}
	}
	return changed
}

// assembleBroadcastHexChunks joins SIGNED_RAW_HEX chunks. off/len are byte offsets into the ASCII hex string
// (same as logBroadcastDetails); wantHexChars is signed_hex_len (= len(hex) in the broadcaster).
func assembleBroadcastHexChunks(chunks []broadcastHexChunk, wantHexChars int) (string, bool) {
	if wantHexChars <= 0 || wantHexChars%2 != 0 {
		return "", false
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].off < chunks[j].off })
	var b strings.Builder
	nextOff := 0
	totalChars := 0
	for _, c := range chunks {
		if c.off != nextOff {
			return "", false
		}
		if len(c.hex)%2 != 0 {
			return "", false
		}
		if _, err := hex.DecodeString(c.hex); err != nil {
			return "", false
		}
		b.WriteString(strings.ToLower(c.hex))
		n := len(c.hex)
		nextOff += n
		totalChars += n
		if totalChars > wantHexChars {
			return "", false
		}
	}
	if totalChars != wantHexChars {
		return "", false
	}
	return b.String(), true
}

func looksLikeDogecoinRawTxHex(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) < 20 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}
