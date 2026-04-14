package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ipRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string][]int64
}

func newIPRateLimiterFromEnv() *ipRateLimiter {
	limit := 120
	if n, err := strconv.Atoi(strings.TrimSpace(env("QE_PUBLIC_RATE_LIMIT", "120"))); err == nil && n > 0 {
		limit = n
	}
	secs := 60
	if n, err := strconv.Atoi(strings.TrimSpace(env("QE_PUBLIC_RATE_WINDOW_SEC", "60"))); err == nil && n > 0 {
		secs = n
	}
	return &ipRateLimiter{
		limit:   limit,
		window:  time.Duration(secs) * time.Second,
		clients: map[string][]int64{},
	}
}

func (rl *ipRateLimiter) allow(ip string, now time.Time) bool {
	if rl == nil || rl.limit <= 0 {
		return true
	}
	if strings.TrimSpace(ip) == "" {
		ip = "unknown"
	}
	cutoff := now.Add(-rl.window).Unix()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	prev := rl.clients[ip]
	keep := prev[:0]
	for _, ts := range prev {
		if ts >= cutoff {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= rl.limit {
		rl.clients[ip] = keep
		return false
	}
	keep = append(keep, now.Unix())
	rl.clients[ip] = keep
	return true
}

func (a *app) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.rl == nil {
			next(w, r)
			return
		}
		ip := requestClientIP(r)
		if !a.rl.allow(ip, time.Now().UTC()) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":       "rate limit exceeded",
				"retry_after": int(a.rl.window.Seconds()),
			})
			return
		}
		next(w, r)
	}
}
