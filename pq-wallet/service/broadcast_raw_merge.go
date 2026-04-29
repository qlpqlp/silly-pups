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
	reBroadcastTxLine      = regexp.MustCompile(`(?i)^(tx_broadcast|broadcast)\s+txid=([0-9a-f]{64})\s+signed_hex_len=(\d+)`)
	reBroadcastHexChunk    = regexp.MustCompile(`(?i)^(tx_broadcast|broadcast)\s+SIGNED_RAW_HEX\s+off=(\d+)\s+len=(\d+)\s+([0-9a-f]+)\s*$`)
)

func stripBroadcastLogTimePrefix(line string) string {
	line = strings.TrimSpace(line)
	return strings.TrimSpace(reBroadcastLogTimePrefix.ReplaceAllString(line, ""))
}

type broadcastHexChunk struct {
	off int
	hex string
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

func assembleBroadcastHexChunks(chunks []broadcastHexChunk, wantLen int) (string, bool) {
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].off < chunks[j].off })
	var b strings.Builder
	expect := 0
	for _, c := range chunks {
		if c.off != expect {
			return "", false
		}
		raw, err := hex.DecodeString(c.hex)
		if err != nil || len(raw) == 0 {
			return "", false
		}
		b.WriteString(strings.ToLower(c.hex))
		expect += len(raw)
		if expect > wantLen {
			return "", false
		}
	}
	if expect != wantLen {
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
