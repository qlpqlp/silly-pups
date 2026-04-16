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

func (a *app) publicMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	hoursRaw := strings.TrimSpace(r.URL.Query().Get("hours"))
	hours := 24
	if hoursRaw == "0" {
		hours = 0 // all-time
	} else {
		hours = parsePositiveInt(hoursRaw, 24, 1, 24*365*25)
	}
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if buckets, err := a.cidx.metricBuckets(ctx, hours); err == nil {
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
		"source":  "pq_json_store",
		"buckets": buckets,
	})
}

func (a *app) publicActivityBuckets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 200, map[string]any{"hours": 24, "source": "unavailable", "buckets": []map[string]any{}})
		return
	}
	hoursRaw := strings.TrimSpace(r.URL.Query().Get("hours"))
	hours := 24
	if hoursRaw == "0" {
		hours = 0 // all-time
	} else {
		hours = parsePositiveInt(hoursRaw, 24, 1, 24*365*25)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	buckets, err := a.cidx.activityBuckets(ctx, hours)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"hours":   hours,
		"source":  "core_activity_buckets",
		"buckets": buckets,
	})
}
