package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *app) withPublicAccess(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.publicEndpointAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next(w, r)
	}
}

func (a *app) coreIndexerStatus() map[string]any {
	if a.cidx == nil {
		return map[string]any{
			"enabled": false,
			"note":    "Core indexer requires PostgreSQL chain backend (QE_POSTGRES_URL) and Core RPC env vars.",
		}
	}
	return a.cidx.status()
}

func (a *app) publicCoreSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 25
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	out, err := a.cidx.search(ctx, q, limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, out)
}

func (a *app) publicCoreSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	if a.cidx == nil {
		writeJSON(w, 200, map[string]any{"indexer": a.coreIndexerStatus(), "core_rpc": a.core.snapshot()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"indexer":  a.cidx.summary(ctx),
		"core_rpc": a.core.snapshot(),
	})
}

func (a *app) publicCoreRecentTxs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	limit := 100
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := a.cidx.recentTransactions(ctx, limit, mode)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"rows":  rows,
		"limit": limit,
		"mode":  mode,
	})
}

func (a *app) publicCoreRecentBlocks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable (requires postgres backend + rpc)"})
		return
	}
	limit := 30
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	rows, err := a.cidx.recentBlocks(ctx, limit)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{
		"rows":  rows,
		"limit": limit,
	})
}

func (a *app) adminStartCoreIndexer(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	if err := a.cidx.start(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "status": a.cidx.status()})
}

func (a *app) adminStopCoreIndexer(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	a.cidx.stop()
	writeJSON(w, 200, map[string]any{"ok": true, "status": a.cidx.status()})
}

func (a *app) adminCoreIndexerStatus(w http.ResponseWriter) {
	if a.cidx == nil {
		writeJSON(w, 200, map[string]any{"enabled": false})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	writeJSON(w, 200, a.cidx.summary(ctx))
}
