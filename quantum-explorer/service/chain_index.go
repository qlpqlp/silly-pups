package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Max SPV log size to scan for chain ingestion per refresh cycle.
const chainLogIngestMax = 8 << 20

var (
	reLogHeightThenTx = regexp.MustCompile(`(?i)(?:height|block\s*#?)\s*[:\s=#]+(\d{4,9}).{0,700}?\b([0-9a-f]{64})\b`)
	reLogTxThenHeight = regexp.MustCompile(`(?i)\b([0-9a-f]{64})\b.{0,700}?(?:height|block\s*#?)\s*[:\s=#]+(\d{4,9})`)
)

// ingestSPVLogChain updates header and heuristic tx→height links via chainBackend (Postgres-safe without app.mu).
func (a *app) ingestSPVLogChain(fullLog string) (bool, error) {
	if a.chain == nil {
		return false, nil
	}
	if len(fullLog) > chainLogIngestMax {
		fullLog = fullLog[len(fullLog)-chainLogIngestMax:]
	}
	changed := false
	snap := parseSPVLogSnapshot(fullLog)

	for _, row := range snap.HeaderRows {
		c, err := a.chain.UpsertHeader(row.Height, row.Hash, row.Timestamp, row.Raw)
		if err != nil {
			return changed, err
		}
		if c {
			changed = true
		}
	}
	if snap.TipHeight > 0 {
		c, err := a.chain.UpsertHeader(int(snap.TipHeight), snap.TipHash, snap.LastTipTime, "new_headers_tip")
		if err != nil {
			return changed, err
		}
		if c {
			changed = true
		}
	}

	for _, line := range strings.Split(fullLog, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Count(line, "|") >= 2 {
			parts := strings.SplitN(line, "|", 3)
			if len(parts) >= 2 {
				hx := strings.TrimSpace(parts[0])
				if len(hx) == 64 && isHex64String(hx) {
					continue
				}
			}
		}
		for _, m := range reLogHeightThenTx.FindAllStringSubmatch(line, -1) {
			h, _ := strconv.Atoi(m[1])
			tx := strings.ToLower(m[2])
			ins, err := a.chain.AppendTxLinkIfNew(h, tx)
			if err != nil {
				return changed, err
			}
			if ins {
				changed = true
			}
		}
		for _, m := range reLogTxThenHeight.FindAllStringSubmatch(line, -1) {
			tx := strings.ToLower(m[1])
			h, _ := strconv.Atoi(m[2])
			ins, err := a.chain.AppendTxLinkIfNew(h, tx)
			if err != nil {
				return changed, err
			}
			if ins {
				changed = true
			}
		}
	}
	return changed, nil
}
