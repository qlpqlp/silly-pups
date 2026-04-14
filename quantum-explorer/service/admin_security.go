package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func parseIPAllowlistList(list []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range list {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		out[p] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func requestClientIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		items := strings.Split(xff, ",")
		if len(items) > 0 {
			return strings.TrimSpace(items[0])
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (a *app) adminIPAllowed(r *http.Request) bool {
	if len(a.adminAllowlist) == 0 {
		return true
	}
	ip := requestClientIP(r)
	_, ok := a.adminAllowlist[ip]
	return ok
}

func parseProtectedPaths(rawList []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range rawList {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		out[p] = struct{}{}
	}
	return out
}

func parseAPIClients(clients []APIClient) []APIClient {
	out := make([]APIClient, 0, len(clients))
	for _, c := range clients {
		c.Name = strings.TrimSpace(c.Name)
		c.Token = strings.TrimSpace(c.Token)
		if c.Token == "" {
			continue
		}
		c.Allowlist = compactStrings(c.Allowlist)
		out = append(out, c)
	}
	return out
}

func compactStrings(list []string) []string {
	out := make([]string, 0, len(list))
	seen := map[string]struct{}{}
	for _, v := range list {
		s := strings.TrimSpace(v)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (a *app) applyAccessConfigLocked() {
	a.adminAllowlist = parseIPAllowlistList(a.cfg.AdminAllowlist)
	a.publicAllowlist = parseIPAllowlistList(a.cfg.PublicAPIAllowlist)
	a.publicToken = strings.TrimSpace(a.cfg.PublicAPIToken)
	a.publicProtectedPaths = parseProtectedPaths(a.cfg.PublicProtectedPaths)
	a.cfg.PublicAPIClients = parseAPIClients(a.cfg.PublicAPIClients)
	if a.cfg.PublicRateLimit <= 0 {
		a.cfg.PublicRateLimit = 120
	}
	if a.cfg.PublicRateWindowSec <= 0 {
		a.cfg.PublicRateWindowSec = 60
	}
	a.rl = &ipRateLimiter{
		limit:   a.cfg.PublicRateLimit,
		window:  time.Duration(a.cfg.PublicRateWindowSec) * time.Second,
		clients: map[string][]int64{},
	}
}

func (a *app) publicEndpointAllowed(r *http.Request) bool {
	if a.publicToken == "" && len(a.publicAllowlist) == 0 && len(a.cfg.PublicAPIClients) == 0 {
		return true
	}
	path := strings.TrimSpace(r.URL.Path)
	if _, needsProtection := a.publicProtectedPaths[path]; !needsProtection {
		return true
	}
	if len(a.publicAllowlist) > 0 {
		ip := requestClientIP(r)
		if _, ok := a.publicAllowlist[ip]; ok {
			return true
		}
	}
	if a.publicToken != "" {
		got := strings.TrimSpace(r.Header.Get("X-API-Token"))
		if got == "" {
			got = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		if got != "" && got == a.publicToken {
			return true
		}
	}
	if len(a.cfg.PublicAPIClients) > 0 {
		got := strings.TrimSpace(r.Header.Get("X-API-Token"))
		if got == "" {
			got = strings.TrimSpace(r.URL.Query().Get("token"))
		}
		if got != "" {
			for _, c := range a.cfg.PublicAPIClients {
				if got != c.Token {
					continue
				}
				if len(c.Allowlist) == 0 {
					return true
				}
				ip := requestClientIP(r)
				for _, allowedIP := range c.Allowlist {
					if ip == allowedIP {
						return true
					}
				}
			}
		}
	}
	return false
}

func (a *app) adminAccessGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, 200, map[string]any{
		"admin_allowlist":        a.cfg.AdminAllowlist,
		"public_api_token_set":   strings.TrimSpace(a.cfg.PublicAPIToken) != "",
		"public_api_allowlist":   a.cfg.PublicAPIAllowlist,
		"public_protected_paths": a.cfg.PublicProtectedPaths,
		"public_rate_limit":      a.cfg.PublicRateLimit,
		"public_rate_window_sec": a.cfg.PublicRateWindowSec,
		"public_api_clients":     a.cfg.PublicAPIClients,
	})
}

func (a *app) adminAccessSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var in struct {
		AdminAllowlist      []string    `json:"admin_allowlist"`
		PublicAPIToken      string      `json:"public_api_token"`
		PublicAPIAllowlist  []string    `json:"public_api_allowlist"`
		PublicProtectedPath []string    `json:"public_protected_paths"`
		PublicRateLimit     int         `json:"public_rate_limit"`
		PublicRateWindowSec int         `json:"public_rate_window_sec"`
		PublicAPIClients    []APIClient `json:"public_api_clients"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	a.mu.Lock()
	a.cfg.AdminAllowlist = compactStrings(in.AdminAllowlist)
	a.cfg.PublicAPIToken = strings.TrimSpace(in.PublicAPIToken)
	a.cfg.PublicAPIAllowlist = compactStrings(in.PublicAPIAllowlist)
	a.cfg.PublicProtectedPaths = compactStrings(in.PublicProtectedPath)
	if len(a.cfg.PublicProtectedPaths) == 0 {
		a.cfg.PublicProtectedPaths = []string{"/api/public/core/search", "/api/public/core/summary"}
	}
	if in.PublicRateLimit <= 0 {
		a.cfg.PublicRateLimit = 120
	} else {
		a.cfg.PublicRateLimit = in.PublicRateLimit
	}
	if in.PublicRateWindowSec <= 0 {
		a.cfg.PublicRateWindowSec = 60
	} else {
		a.cfg.PublicRateWindowSec = in.PublicRateWindowSec
	}
	a.cfg.PublicAPIClients = parseAPIClients(in.PublicAPIClients)
	cfg := a.cfg
	a.applyAccessConfigLocked()
	a.mu.Unlock()
	if err := saveJSON(a.cfgPath, cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "note": "access policy updated"})
}
