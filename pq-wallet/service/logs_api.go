package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func readLastNLinesFromFile(path string, maxBytes, n int) (string, error) {
	if n <= 0 {
		n = 200
	}
	raw, err := readFileTail(path, maxBytes)
	if err != nil {
		return "", err
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n"), nil
	}
	return strings.Join(lines[len(lines)-n:], "\n"), nil
}

func (s *Server) handleLogsSPV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("SPV log is disabled in this build. Use /api/spv/status and transaction APIs instead.\n"))
}

// handleLogsMempoolTracker returns a text snapshot of the embedded MemeTracker P2P session (stderr is not captured here).
func (s *Server) handleLogsMempoolTracker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	s.mu.Lock()
	wf, err := s.loadWallet()
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err != nil || wf == nil {
		_, _ = w.Write([]byte("(wallet not loaded)\n"))
		return
	}
	eng, err := s.ensureMempoolEngine(wf)
	if err != nil {
		_, _ = fmt.Fprintf(w, "(mempool engine error: %v)\n", err)
		return
	}
	if eng == nil {
		_, _ = w.Write([]byte("(no mempool engine)\n"))
		return
	}
	n, live, workers, nConn := eng.DashboardSnapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "Embedded MemeTracker — unique tx ids seen on relay (visibility): %d\n", n)
	fmt.Fprintf(&b, "P2P worker sessions with active connection: %d\n", nConn)
	for _, row := range workers {
		fmt.Fprintf(&b, "  worker %v  connected=%v  %v  updated=%v\n", row["worker_id"], row["connected"], row["address"], row["updated"])
	}
	fmt.Fprintf(&b, "Live mempool rows (recent, up to 80):\n")
	for _, row := range live {
		fmt.Fprintf(&b, "  %v  tracked=%v  %v DOGE\n", row["txid"], row["tracked_match"], row["amount_doge"])
	}
	if len(live) == 0 {
		b.WriteString("  (none in freshness window — waiting for inv/tx traffic)\n")
	}
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) handleLogsBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 2000 {
			n = x
		}
	}
	text, err := readLastNLinesFromFile(s.broadcastLogPath(), 2<<20, n)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("(no broadcast attempts logged yet)\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}
