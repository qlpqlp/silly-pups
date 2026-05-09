package main

import (
	"context"
	"net/http"
	"time"
)

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
