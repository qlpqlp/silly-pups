package main

import (
	"regexp"
	"sort"
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
	reNetAddr      = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}:\d{2,5}\b`)
	reConnectedPeer = regexp.MustCompile(`(?i)Successfully connected to peer\s+(\d+)\s+\(([^)]+)\)`)
	rePQPeer = regexp.MustCompile(`(?i)PQ_PEER\s+node=(\d+)\s+ip=(\S+)\s+height=(\d+)`)
	reMempoolExplicit1 = regexp.MustCompile(`(?i)mempool[^\n]{0,64}(?:size|count|transactions?|txs?)\s*[:=]\s*(\d{1,9})`)
	reMempoolExplicit2 = regexp.MustCompile(`(?i)(?:^|\s)(\d{1,9})\s+transactions?\s+in\s+mempool`)
	reMempoolExplicit3 = regexp.MustCompile(`(?i)\[(?:smpv|mempool)\][^\n]{0,120}(\d{1,9})\s*(?:tx|txn|transaction)`)
	reSpvConfirmedLine = regexp.MustCompile(`(?i)\b(confirm|confirmed|merkle|inclusion|matched|proof)\b`)
)

// PeerConnectionInfo is parsed from libdogecoin net.c log lines (current / last handshake in the tail).
type PeerConnectionInfo struct {
	NodeID            int    `json:"node_id,omitempty"`
	Address           string `json:"address,omitempty"` // ip:port
	SubVersion        string `json:"sub_version,omitempty"`
	RemoteStartHeight int64  `json:"remote_start_height,omitempty"`
}

// SPVHeaderInfo is parsed from spvnode log tail (best-effort).
// Real spvnode output often uses lines: 64hex|height|timestamp|work…
type SPVHeaderInfo struct {
	HeaderHeight    int64
	BestBlockHash   string
	PeerCount       int
	HeaderCountHint int64 // e.g. lone first line "25139" (header index count)
	SPVPeerHosts    []string
	// MempoolTxCount is a heuristic from spv.log (not the embedded mempool tracker).
	MempoolTxCount int
	CurrentPeer    *PeerConnectionInfo
	// PeersRecent is a short history of handshakes (newest last), for dashboard detail.
	PeersRecent []PeerConnectionInfo
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
	out.SPVPeerHosts = parsePeerHostsFromLog(tail)
	out.MempoolTxCount = parseLibdogecoinMempoolTxCount(tail)
	out.CurrentPeer = parseCurrentPeerInfo(tail)
	out.PeersRecent = parsePeersRecent(tail)

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

func mergePeerIPMaps(log string) map[int]string {
	addrs := make(map[int]string)
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := reConnectedPeer.FindStringSubmatch(line); len(m) == 3 {
			id, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			addrs[id] = strings.TrimSpace(m[2])
		}
		if m := rePQPeer.FindStringSubmatch(line); len(m) == 4 {
			id, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			addrs[id] = strings.TrimSpace(m[2])
		}
	}
	return addrs
}

// parseConnectedNodeLine parses libdogecoin net.c: "Connected to node %d: %s (%d)".
// User agent may contain parentheses; we take the last " (height)" suffix on the line.
func parseConnectedNodeLine(line string) (id int, userAgent string, height int64, ok bool) {
	line = strings.TrimSpace(line)
	const pfx = "Connected to node "
	if len(line) < len(pfx) || !strings.EqualFold(line[:len(pfx)], pfx) {
		return 0, "", 0, false
	}
	rest := line[len(pfx):]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, "", 0, false
	}
	id64, err := strconv.ParseInt(rest[:i], 10, 64)
	if err != nil || id64 < 0 || id64 > 1<<20 {
		return 0, "", 0, false
	}
	id = int(id64)
	if i >= len(rest) || rest[i] != ':' {
		return 0, "", 0, false
	}
	rest = strings.TrimSpace(rest[i+1:])
	lastOpen := strings.LastIndex(rest, " (")
	if lastOpen < 0 {
		return 0, "", 0, false
	}
	hPart := strings.TrimSpace(rest[lastOpen+2:])
	if !strings.HasSuffix(hPart, ")") {
		return 0, "", 0, false
	}
	hStr := strings.TrimSpace(hPart[:len(hPart)-1])
	height, err = strconv.ParseInt(hStr, 10, 64)
	if err != nil {
		height = 0
	}
	userAgent = strings.TrimSpace(rest[:lastOpen])
	return id, userAgent, height, true
}

func parsePQPeerInfoLine(line string) (PeerConnectionInfo, bool) {
	m := rePQPeer.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) != 4 {
		return PeerConnectionInfo{}, false
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return PeerConnectionInfo{}, false
	}
	h, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		h = 0
	}
	return PeerConnectionInfo{
		NodeID:            id,
		Address:           strings.TrimSpace(m[2]),
		SubVersion:        "PQ_PEER",
		RemoteStartHeight: h,
	}, true
}

// parseCurrentPeerInfo returns the latest "Connected to node …" handshake in the log, merged with
// "Successfully connected to peer … (addr)" and PQ_PEER lines (libdogecoin net.c + pq patch).
func parseCurrentPeerInfo(log string) *PeerConnectionInfo {
	if strings.TrimSpace(log) == "" {
		return nil
	}
	addrs := mergePeerIPMaps(log)
	var nodes []PeerConnectionInfo
	var pqRows []PeerConnectionInfo
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if id, ua, h, ok := parseConnectedNodeLine(line); ok {
			nodes = append(nodes, PeerConnectionInfo{
				NodeID:            id,
				SubVersion:        ua,
				RemoteStartHeight: h,
			})
			continue
		}
		if p, ok := parsePQPeerInfoLine(line); ok {
			pqRows = append(pqRows, p)
		}
	}
	if len(nodes) > 0 {
		last := nodes[len(nodes)-1]
		if last.Address == "" {
			last.Address = addrs[last.NodeID]
		}
		return &last
	}
	if len(pqRows) > 0 {
		last := pqRows[len(pqRows)-1]
		if last.Address == "" {
			last.Address = addrs[last.NodeID]
		}
		return &last
	}
	return nil
}

// parsePeersRecent returns up to maxPeerLogEntries handshake rows with merged IPs.
func parsePeersRecent(log string) []PeerConnectionInfo {
	const maxPeerLogEntries = 12
	if strings.TrimSpace(log) == "" {
		return nil
	}
	addrs := mergePeerIPMaps(log)
	var out []PeerConnectionInfo
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if id, ua, h, ok := parseConnectedNodeLine(line); ok {
			out = append(out, PeerConnectionInfo{
				NodeID:            id,
				SubVersion:        ua,
				RemoteStartHeight: h,
				Address:           addrs[id],
			})
			continue
		}
		if p, ok := parsePQPeerInfoLine(line); ok {
			if p.Address == "" {
				p.Address = addrs[p.NodeID]
			}
			out = append(out, p)
		}
	}
	if len(out) <= maxPeerLogEntries {
		return out
	}
	return out[len(out)-maxPeerLogEntries:]
}

func uniqueSorted(ss []string) []string {
	seen := make(map[string]struct{})
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func parsePeerHostsFromLog(log string) []string {
	return uniqueSorted(reNetAddr.FindAllString(log, -1))
}

// parseLibdogecoinMempoolTxCount estimates mempool-related transaction activity from spv.log only.
// It first looks for explicit mempool size/count lines; otherwise it counts distinct 64-hex txids on
// mempool / inv lines (what your P2P peer has relayed into the log).
func parseLibdogecoinMempoolTxCount(log string) int {
	if strings.TrimSpace(log) == "" {
		return 0
	}
	best := 0
	for _, line := range strings.Split(log, "\n") {
		for _, re := range []*regexp.Regexp{reMempoolExplicit1, reMempoolExplicit2, reMempoolExplicit3} {
			if m := re.FindStringSubmatch(line); len(m) > 1 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > best && n < 100_000_000 {
					best = n
				}
			}
		}
	}
	if best > 0 {
		return best
	}
	return countDistinctMempoolTxHints(log)
}

// parseSPVTxSeenCount counts distinct 64-char txids on spv.log lines that look like wallet/P2P tx
// activity (not chain tip header rows). Used for the “SPV transaction count” chart.
func parseSPVTxSeenCount(log string) int {
	if strings.TrimSpace(log) == "" {
		return 0
	}
	seen := make(map[string]struct{})
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Skip header tip lines: hash|height|timestamp|...
		if len(line) > 64 && line[64] == '|' && strings.Count(line, "|") >= 2 {
			continue
		}
		low := strings.ToLower(line)
		// Avoid matching unrelated words like "context" (contains "tx").
		if !strings.Contains(low, "inv") && !strings.Contains(low, "merkle") && !strings.Contains(low, "watch") &&
			!strings.Contains(low, "bloom") && !strings.Contains(low, "wallet") && !strings.Contains(low, "transaction") &&
			!strings.Contains(low, "txid") && !strings.Contains(low, " tx") && !strings.Contains(low, "tx ") {
			continue
		}
		for _, hx := range reBlockHash.FindAllString(line, -1) {
			if len(hx) == 64 && isHex64(hx) {
				seen[strings.ToLower(hx)] = struct{}{}
			}
		}
	}
	return len(seen)
}

// parseSPVConfirmedTxids finds txids on lines that look like SPV confirmation/proof events.
func parseSPVConfirmedTxids(log string) map[string]struct{} {
	out := make(map[string]struct{})
	if strings.TrimSpace(log) == "" {
		return out
	}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		// Skip pipe tip rows like: hash|height|...
		if len(line) > 64 && line[64] == '|' && strings.Count(line, "|") >= 2 {
			continue
		}
		if !reSpvConfirmedLine.MatchString(low) {
			continue
		}
		for _, hx := range reBlockHash.FindAllString(line, -1) {
			id := normalizeTxid(hx)
			if id == "" {
				continue
			}
			out[id] = struct{}{}
		}
	}
	return out
}

func countDistinctMempoolTxHints(log string) int {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "|") {
			continue
		}
		low := strings.ToLower(line)
		if !strings.Contains(low, "mempool") && !strings.Contains(low, "inv") {
			continue
		}
		for _, hx := range reBlockHash.FindAllString(line, -1) {
			if len(hx) == 64 && isHex64(hx) {
				seen[strings.ToLower(hx)] = struct{}{}
			}
		}
	}
	return len(seen)
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
