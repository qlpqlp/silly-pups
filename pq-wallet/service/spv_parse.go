package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reHeaderHeight = regexp.MustCompile(`(?i)(?:best\s+height|chain\s+height|new\s+best|synced.*?height|headers?\s*:\s*)(?:[^\d]{0,24})(\d{5,})`)
	reLooseHeight  = regexp.MustCompile(`(?i)\bheight[:\s=]+(\d{5,})`)
	reBlockHash    = regexp.MustCompile(`\b([a-fA-F0-9]{64})\b`)
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
	// Prefer last long hex that looks like a block hash (appears after height line often)
	if m := reBlockHash.FindAllString(tail, -1); len(m) > 0 {
		out.BestBlockHash = strings.ToLower(m[len(m)-1])
	}
	return out
}
