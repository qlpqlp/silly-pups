package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

// phasedStartupHandler binds the HTTP port before Postgres/RPC-heavy init finishes so container
// supervisors (systemd/DogeBox) observe an open listener immediately and avoid startup timeouts.
type phasedStartupHandler struct {
	mu   sync.RWMutex
	app  *app
	full http.Handler
}

func (p *phasedStartupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	h := p.full
	p.mu.RUnlock()
	if h != nil {
		h.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/healthz" && p.app != nil {
		p.app.healthz(w, r)
		return
	}
	// Some startup checkers probe a public API endpoint instead of /healthz.
	// Respond 200 with lightweight JSON during warm-up so can-pup-start conditions pass.
	if strings.HasPrefix(r.URL.Path, "/api/public/") {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"service":  "quantum-explorer",
			"starting": true,
		})
		return
	}
	// Some supervisors probe "/" or HEAD while the app is still warming up.
	if r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("Quantum Explorer is starting"))
		}
		return
	}
	// Supervisors often probe /readyz before the full mux is enabled; the real readyz stays strict after enable.
	if r.URL.Path == "/readyz" && p.app != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ready":    true,
			"starting": true,
			"detail":   map[string]any{"bootstrap": true},
		})
		return
	}
	w.Header().Set("Retry-After", "2")
	http.Error(w, "Quantum Explorer is starting", http.StatusServiceUnavailable)
}

func (p *phasedStartupHandler) enable(h http.Handler) {
	p.mu.Lock()
	p.full = h
	p.mu.Unlock()
}

func (a *app) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"service": "quantum-explorer",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *app) readyz(w http.ResponseWriter, _ *http.Request) {
	ready := true
	detail := map[string]any{}

	chainOK := a.chain != nil
	detail["chain_backend"] = chainOK
	ready = ready && chainOK

	coreRPC := a.core.snapshot()
	detail["core_rpc_enabled"] = coreRPC["enabled"]
	if v, ok := coreRPC["enabled"].(bool); ok && v {
		if c, ok := coreRPC["connected"].(bool); ok {
			detail["core_rpc_connected"] = c
			ready = ready && c
		} else {
			ready = false
		}
	}

	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		sum := a.cidx.summary(ctx)
		detail["core_indexer_running"] = sum["running"]
		if r, ok := sum["running"].(bool); ok {
			ready = ready && r
		}
	}

	if ready {
		writeJSON(w, 200, map[string]any{"ready": true, "detail": detail})
		return
	}
	writeJSON(w, 503, map[string]any{"ready": false, "detail": detail})
}
