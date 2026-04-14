package main

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func parsePositiveInt(raw string, def, min, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func txMatchesMode(t *PQTx, mode string) bool {
	switch mode {
	case "quantum":
		return t != nil && t.PQValid
	case "non-quantum":
		return t != nil && !t.PQValid
	default:
		return t != nil
	}
}

func (a *app) publicMempool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "all"
	}
	if mode != "all" && mode != "quantum" && mode != "non-quantum" {
		writeJSON(w, 400, map[string]string{"error": "mode must be one of: all, quantum, non-quantum"})
		return
	}
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 10, 1, 200)
	offset := parsePositiveInt(r.URL.Query().Get("offset"), 0, 0, 50000)

	rows := a.latest(10000)
	filtered := make([]*PQTx, 0, len(rows))
	for _, t := range rows {
		if txMatchesMode(t, mode) {
			filtered = append(filtered, t)
		}
	}

	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	writeJSON(w, 200, map[string]any{
		"mode":   mode,
		"limit":  limit,
		"offset": offset,
		"total":  total,
		"rows":   page,
	})
}

func (a *app) publicMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	hours := parsePositiveInt(r.URL.Query().Get("hours"), 24, 1, 168)
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if buckets, err := a.cidx.metricBuckets(ctx, hours); err == nil && len(buckets) > 0 {
			writeJSON(w, 200, map[string]any{
				"hours":   hours,
				"source":  "core_hourly_metrics",
				"buckets": buckets,
			})
			return
		}
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	byHour := map[string]map[string]int{}
	rows := a.latest(10000)
	for _, t := range rows {
		ts := t.LastSeen
		if strings.TrimSpace(ts) == "" {
			ts = t.FirstSeen
		}
		if strings.TrimSpace(ts) == "" {
			continue
		}
		tt, err := time.Parse(time.RFC3339, ts)
		if err != nil || tt.Before(cutoff) {
			continue
		}
		bucket := tt.UTC().Format("2006-01-02T15:00:00Z")
		b := byHour[bucket]
		if b == nil {
			b = map[string]int{"all": 0, "quantum": 0, "non_quantum": 0, "confirmed": 0, "pending": 0}
			byHour[bucket] = b
		}
		b["all"]++
		if t.PQValid {
			b["quantum"]++
		} else {
			b["non_quantum"]++
		}
		if t.Confirmed {
			b["confirmed"]++
		} else {
			b["pending"]++
		}
	}

	keys := make([]string, 0, len(byHour))
	for k := range byHour {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buckets := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		b := byHour[k]
		buckets = append(buckets, map[string]any{
			"hour":        k,
			"all":         b["all"],
			"quantum":     b["quantum"],
			"non_quantum": b["non_quantum"],
			"confirmed":   b["confirmed"],
			"pending":     b["pending"],
		})
	}

	writeJSON(w, 200, map[string]any{
		"hours":   hours,
		"source":  "in_memory_tracker",
		"buckets": buckets,
	})
}
