package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reHeaderHeight = regexp.MustCompile(`(?i)(?:best\s+height|chain\s+height|new\s+best|synced.*?height|headers?\s*:\s*|tip[^\d]{0,12})(?:[^\d]{0,32})(\d{4,9})`)
	reLooseHeight  = regexp.MustCompile(`(?i)\bheight[:\s=#]+(\d{4,9})\b`)
	reBlockAt      = regexp.MustCompile(`(?i)\bblock\s*#?\s*(\d{4,9})\b`)
	reBlockHash    = regexp.MustCompile(`\b([a-fA-F0-9]{64})\b`)
	// Lines like " ... 5000123 ..." near height/tip keywords
	reContextHeight = regexp.MustCompile(`(?i)(?:height|headers?|tip|sync|chain|block)[^\n]{0,120}?(\d{5,9})`)
)

// SPVHeaderInfo is parsed from spvnode log tail (best-effort).
type SPVHeaderInfo struct {
	HeaderHeight  int64
	BestBlockHash string
}

func parseSPVLogHeaderInfo(log string) SPVHeaderInfo {
	var out SPVHeaderInfo
	if log == "" {
		return out
	}
	tail := log
	if len(tail) > 96*1024 {
		tail = tail[len(tail)-96*1024:]
	}
	if m := reHeaderHeight.FindStringSubmatch(tail); len(m) > 1 {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			out.HeaderHeight = n
		}
	}
	if out.HeaderHeight == 0 {
		if m := reLooseHeight.FindStringSubmatch(tail); len(m) > 1 {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				out.HeaderHeight = n
			}
		}
	}
	if out.HeaderHeight == 0 {
		if m := reBlockAt.FindStringSubmatch(tail); len(m) > 1 {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				out.HeaderHeight = n
			}
		}
	}
	if out.HeaderHeight == 0 {
		out.HeaderHeight = bestHeightScan(tail)
	}
	// Prefer last long hex that looks like a block hash (appears after height line often)
	if m := reBlockHash.FindAllString(tail, -1); len(m) > 0 {
		out.BestBlockHash = strings.ToLower(m[len(m)-1])
	}
	return out
}

func bestHeightScan(s string) int64 {
	var best int64
	for _, m := range reContextHeight.FindAllStringSubmatch(s, -1) {
		if len(m) < 2 {
			continue
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n < 10_000 || n > 100_000_000 {
			continue
		}
		if n > best {
			best = n
		}
	}
	return best
}
