package main

import (
	"net/http"
	"strconv"
	"strings"
)

func readLastNLinesFromFile(path string, maxBytes, n int) (string, error) {
	if n <= 0 {
		n = 420
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
	n := 420
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 5000 {
			n = x
		}
	}
	text, err := readLastNLinesFromFile(s.spvLogPath(), 8<<20, n)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("(log not available yet)\n"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

// handleLogsSMPV returns the tail of spv.log filtered to SMPV / mempool-related lines (same underlying file as SPV).
func (s *Server) handleLogsSMPV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	n := 420
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 5000 {
			n = x
		}
	}
	text, err := readLastNLinesFromFile(s.spvLogPath(), 8<<20, maxInt(n*20, 2000))
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("(log not available yet)\n"))
		return
	}
	filtered := filterSMPVMempoolLines(text, n)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(filtered))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func filterSMPVMempoolLines(text string, maxLines int) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []string
	for _, line := range lines {
		low := strings.ToLower(line)
		if strings.Contains(low, "smpv") || strings.Contains(low, "mempool") {
			out = append(out, line)
		}
	}
	if len(out) <= maxLines {
		return strings.Join(out, "\n")
	}
	return strings.Join(out[len(out)-maxLines:], "\n")
}

func (s *Server) handleLogsBroadcast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	n := 420
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 5000 {
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
