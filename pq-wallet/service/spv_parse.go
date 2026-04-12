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
	reContextHeight = regexp.MustCompile(`(?i)(?:height|headers?|tip|sync|chain|block)[^\n]{0,120}?(\d{5,9})`)
	rePeerEq       = regexp.MustCompile(`(?i)peers?\s*[:=]\s*(\d+)`)
	rePeerWord     = regexp.MustCompile(`(?i)(?:^|[^\w])(\d{1,6})\s+(?:peer|peers)\b`)
	reConnEq       = regexp.MustCompile(`(?i)(?:connections?|connected)\s*[:=]\s*(\d+)`)
)

// SPVHeaderInfo is parsed from spvnode log tail (best-effort).
// Real spvnode output often uses lines: 64hex|height|timestamp|work…
type SPVHeaderInfo struct {
	HeaderHeight    int64
	BestBlockHash   string
	PeerCount       int
	SMPVActive      bool
	HeaderCountHint int64 // e.g. lone first line "25139" (header index count)
}

func parseSPVLogHeaderInfo(log string) SPVHeaderInfo {
	var out SPVHeaderInfo
	if log == "" {
		return out
	}
	tail := log
	if len(tail) > 512*1024 {
		tail = tail[len(tail)-512*1024:]
	}

	out.HeaderCountHint = parseLeadingCountHint(tail)
	h, hash := parsePipeHeaderTip(tail)
	if h > 0 {
		out.HeaderHeight = h
		out.BestBlockHash = hash
	}
	out.PeerCount = parsePeerCountFromLog(tail)
	out.SMPVActive = parseSMPVActive(tail)

	if out.HeaderHeight == 0 {
		if m := reHeaderHeight.FindStringSubmatch(tail); len(m) > 1 {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				out.HeaderHeight = n
			}
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
	if out.BestBlockHash == "" {
		if m := reBlockHash.FindAllString(tail, -1); len(m) > 0 {
			out.BestBlockHash = strings.ToLower(m[len(m)-1])
		}
	}
	return out
}

// parsePipeHeaderTip scans hash|height|… rows and returns the row with maximum height.
func parsePipeHeaderTip(s string) (height int64, hash string) {
	var best int64
	var bestHash string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		hStr := strings.TrimSpace(parts[1])
		h, err := strconv.ParseInt(hStr, 10, 64)
		if err != nil || h < 0 {
			continue
		}
		hx := strings.TrimSpace(parts[0])
		if len(hx) != 64 || !isHex64(hx) {
			continue
		}
		if h >= best {
			best = h
			bestHash = strings.ToLower(hx)
		}
	}
	return best, bestHash
}

func isHex64(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func parseLeadingCountHint(log string) int64 {
	trim := strings.TrimSpace(log)
	if trim == "" {
		return 0
	}
	first := strings.TrimSpace(strings.SplitN(trim, "\n", 2)[0])
	if strings.Contains(first, "|") {
		return 0
	}
	n, err := strconv.ParseInt(first, 10, 64)
	if err != nil || n < 0 || n > 500_000_000 {
		return 0
	}
	return n
}

func parsePeerCountFromLog(log string) int {
	best := 0
	for _, re := range []*regexp.Regexp{rePeerEq, rePeerWord, reConnEq} {
		for _, m := range re.FindAllStringSubmatch(log, -1) {
			if len(m) < 2 {
				continue
			}
			n, err := strconv.Atoi(m[1])
			if err != nil || n < 0 || n > 500000 {
				continue
			}
			if n > best {
				best = n
			}
		}
	}
	return best
}

func parseSMPVActive(log string) bool {
	return strings.Contains(strings.ToLower(log), "smpv")
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
