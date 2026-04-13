package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const spvLogTailMax = 512 * 1024

var (
	reSPVNewTip    = regexp.MustCompile(`(?i)New\s+headers\s+tip\s+height\s+(\d+)\s+from\s+(.+)`)
	reSPVPeerEq    = regexp.MustCompile(`(?i)peers?\s*[:=]\s*(\d+)`)
	reSPVConnEq    = regexp.MustCompile(`(?i)(?:connections?|connected)\s*[:=]\s*(\d+)`)
	reSPVMempoolTx = regexp.MustCompile(`(?i)mempool[^\n]{0,80}(?:size|count|transactions?|txs?)\s*[:=]\s*(\d{1,9})`)
)

type spvLogSnapshot struct {
	HeaderRows    []SPVHeader
	TipHeight     int64
	TipHash       string
	LastTipTime   string // from "New headers tip … from …" (often ctime)
	PeerCount     int
	MempoolTxHint int
}

func parseSPVLogSnapshot(log string) spvLogSnapshot {
	var out spvLogSnapshot
	if strings.TrimSpace(log) == "" {
		return out
	}
	if len(log) > spvLogTailMax {
		log = log[len(log)-spvLogTailMax:]
	}
	lines := strings.Split(log, "\n")

	var newTipH int64
	var newTipFrom string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if m := reSPVNewTip.FindStringSubmatch(line); len(m) == 3 {
			h, err := strconv.ParseInt(m[1], 10, 64)
			if err == nil && h > 0 {
				newTipH = h
				newTipFrom = strings.TrimSpace(strings.TrimSuffix(m[2], "\n"))
				break
			}
		}
	}

	type pipeRow struct {
		h    int64
		hash string
		raw  string
		ts   string
	}
	var pipes []pipeRow
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		hx := strings.TrimSpace(parts[0])
		if len(hx) != 64 || !isHex64String(hx) {
			continue
		}
		h, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || h < 0 {
			continue
		}
		ts := ""
		if len(parts) >= 3 {
			ts = strings.TrimSpace(parts[2])
		}
		pipes = append(pipes, pipeRow{h: h, hash: strings.ToLower(hx), raw: line, ts: ts})
	}
	sort.Slice(pipes, func(i, j int) bool { return pipes[i].h > pipes[j].h })

	var pipeMax int64
	var pipeMaxHash string
	if len(pipes) > 0 {
		pipeMax = pipes[0].h
		pipeMaxHash = pipes[0].hash
	}

	out.TipHeight = newTipH
	if pipeMax > out.TipHeight {
		out.TipHeight = pipeMax
	}
	out.TipHash = pipeMaxHash
	for _, p := range pipes {
		if p.h == out.TipHeight {
			out.TipHash = p.hash
			break
		}
	}
	out.LastTipTime = newTipFrom

	seen := make(map[int64]struct{})
	for _, p := range pipes {
		if _, ok := seen[p.h]; ok {
			continue
		}
		seen[p.h] = struct{}{}
		tsDisp := formatMaybeUnixTime(p.ts)
		out.HeaderRows = append(out.HeaderRows, SPVHeader{
			Height:    int(p.h),
			Hash:      p.hash,
			Raw:       p.raw,
			Timestamp: tsDisp,
		})
	}

	if len(out.HeaderRows) == 0 && out.TipHeight > 0 {
		out.HeaderRows = append(out.HeaderRows, SPVHeader{
			Height:    int(out.TipHeight),
			Hash:      out.TipHash,
			Raw:       "spvnode: New headers tip (no hash|height pipe row yet)",
			Timestamp: newTipFrom,
		})
	}

	bestPeer := 0
	for _, re := range []*regexp.Regexp{reSPVPeerEq, reSPVConnEq} {
		for _, m := range re.FindAllStringSubmatch(log, -1) {
			if len(m) < 2 {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 0 || n > 500000 {
				continue
			}
			if n > bestPeer {
				bestPeer = n
			}
		}
	}
	out.PeerCount = bestPeer

	for _, m := range reSPVMempoolTx.FindAllStringSubmatch(log, -1) {
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > out.MempoolTxHint {
			out.MempoolTxHint = n
		}
	}

	return out
}

func formatMaybeUnixTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 1_000_000_000 {
		return time.Unix(unix, 0).UTC().Format(time.RFC3339)
	}
	return s
}

func isHex64String(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}
