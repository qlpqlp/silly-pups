package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// spvRESTProbeMaxBytes caps how much we read from spvnode for the debug probe (wallet/headers can be large).
const spvRESTProbeMaxBytes = 512 << 10

// spvRESTProbeTextMax is the max runes we embed as JSON text for plain responses (UI still usable).
const spvRESTProbeTextMax = 400_000

// spvRESTDocumentedPaths are GET paths from https://lib.dogecoin.org/docs/rest (whitelist only).
var spvRESTDocumentedPaths = []string{
	"/getBalance",
	"/getAddresses",
	"/getTransactions",
	"/getSpends",
	"/getUTXOs",
	"/getWallet",
	"/getHeaders",
	"/getChaintip",
	"/getTimestamp",
	"/getLastBlockInfo",
}

func isAllowedSPVRESTProbePath(p string) bool {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for _, doc := range spvRESTDocumentedPaths {
		if p == doc {
			return true
		}
	}
	return false
}

func normalizeSPVRESTProbeQueryPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") && isAllowedSPVRESTProbePath(raw) {
		return raw
	}
	s := strings.TrimPrefix(raw, "/")
	low := strings.ToLower(s)
	switch low {
	case "getbalance":
		return "/getBalance"
	case "getaddresses":
		return "/getAddresses"
	case "gettransactions":
		return "/getTransactions"
	case "getspends":
		return "/getSpends"
	case "getutxos":
		return "/getUTXOs"
	case "getwallet":
		return "/getWallet"
	case "getheaders":
		return "/getHeaders"
	case "getchaintip":
		return "/getChaintip"
	case "gettimestamp":
		return "/getTimestamp"
	case "getlastblockinfo":
		return "/getLastBlockInfo"
	}
	return ""
}

// spvHTTPGetProbe performs a GET to the local SPV REST base and returns status, headers snippet, and body (capped).
func (s *Server) spvHTTPGetProbe(path string) (finalURL string, status int, contentType string, body []byte, err error) {
	base := strings.TrimRight(s.spvHTTPBaseURL(), "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	finalURL = base + path
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, finalURL, nil)
	if err != nil {
		return finalURL, 0, "", nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return finalURL, 0, "", nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, spvRESTProbeMaxBytes))
	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	return finalURL, resp.StatusCode, ct, b, nil
}

func spvRESTResponseLooksBinary(path, contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "octet-stream") {
		return true
	}
	if path == "/getWallet" || path == "/getHeaders" {
		return true
	}
	if len(body) >= 3 && body[0] == 'S' && body[1] == 'Q' && body[2] == 'L' {
		return false
	}
	// Heuristic: high NUL ratio
	if len(body) > 200 {
		nul := 0
		for i := 0; i < len(body) && i < 8000; i++ {
			if body[i] == 0 {
				nul++
			}
		}
		if nul > 8 {
			return true
		}
	}
	return false
}

// handleDebugSPVREST proxies a whitelisted GET to the libdogecoin SPV REST server (SPV_HTTP_ADDR).
// Query: path=/getBalance  (or path=getBalance). See https://lib.dogecoin.org/docs/rest
func (s *Server) handleDebugSPVREST(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	if s.strictSettingsPeekBlocked() {
		writeStrictSettingsAuthRequired(w)
		return
	}
	s.mu.Lock()
	_, werr := s.loadWallet()
	s.mu.Unlock()
	if werr != nil {
		if errors.Is(werr, ErrWalletLocked) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wallet locked"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no wallet"})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("endpoint"))
	}
	path := normalizeSPVRESTProbeQueryPath(raw)
	if path == "" || !isAllowedSPVRESTProbePath(path) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "unknown or missing path; allowed documented GET paths only",
			"allowed_paths": spvRESTDocumentedPaths,
			"docs":          "https://lib.dogecoin.org/docs/rest",
		})
		return
	}
	finalURL, status, ct, body, err := s.spvHTTPGetProbe(path)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": fmt.Sprintf("request failed: %v", err),
			"url":   finalURL,
			"path":  path,
		})
		return
	}
	out := map[string]any{
		"ok":            status >= 200 && status < 300,
		"url":           finalURL,
		"path":          path,
		"status":        status,
		"content_type":  ct,
		"bytes_read":    len(body),
		"docs":          "https://lib.dogecoin.org/docs/rest",
		"spv_http_base": s.spvHTTPBaseURL(),
	}
	bin := spvRESTResponseLooksBinary(path, ct, body)
	out["binary"] = bin
	if bin {
		prefixLen := 48 << 10
		if len(body) < prefixLen {
			prefixLen = len(body)
		}
		out["body_base64_prefix"] = base64.StdEncoding.EncodeToString(body[:prefixLen])
		if len(body) > prefixLen {
			out["body_base64_prefix_note"] = fmt.Sprintf("first %d bytes only (response truncated at %d bytes read cap)", prefixLen, spvRESTProbeMaxBytes)
		}
	} else {
		txt := string(body)
		truncated := false
		if len(txt) > spvRESTProbeTextMax {
			txt = txt[:spvRESTProbeTextMax]
			truncated = true
		}
		out["body_text"] = txt
		out["body_text_truncated"] = truncated
	}
	writeJSON(w, http.StatusOK, out)
}
