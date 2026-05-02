package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

func hash256dLEHex(header80 []byte) string {
	if len(header80) < 80 {
		return ""
	}
	first := sha256.Sum256(header80[:80])
	second := sha256.Sum256(first[:])
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		out[i] = second[31-i]
	}
	return hex.EncodeToString(out)
}

// augmentSPVLogWithHeaderHashes appends block hash from libdogecoin file-based headers.db to lines that mention a height but have no 64-hex hash yet.
func (s *Server) augmentSPVLogWithHeaderHashes(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	hdb := filepath.Join(s.storageDir, "headers.db")
	for i, ln := range lines {
		if reBlockHash.MatchString(ln) {
			continue
		}
		var h int64
		if m := reLooseHeight.FindStringSubmatch(ln); len(m) > 1 {
			h, _ = strconv.ParseInt(m[1], 10, 64)
		} else if m := reBlockAt.FindStringSubmatch(ln); len(m) > 1 {
			h, _ = strconv.ParseInt(m[1], 10, 64)
		} else if m := reHeaderHeight.FindStringSubmatch(ln); len(m) > 1 {
			h, _ = strconv.ParseInt(m[1], 10, 64)
		}
		if h <= 0 {
			continue
		}
		if !isLibdogecoinHeadersFileFormat(hdb) {
			continue
		}
		hash := libdogecoinHeaderHashHexAtHeight(hdb, h)
		if hash == "" {
			continue
		}
		lines[i] = strings.TrimRight(ln, " \t") + "  hash=" + hash
	}
	return strings.Join(lines, "\n")
}

func (s *Server) handleLogsSPV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	n := 260
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 && x <= 4000 {
			n = x
		}
	}
	text, err := readLastNLinesFromFile(s.spvLogPath(), 2<<20, n)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err != nil || strings.TrimSpace(text) == "" {
		_, _ = w.Write([]byte("(spv log empty or unavailable)\n"))
		return
	}
	text = s.augmentSPVLogWithHeaderHashes(text)
	_, _ = w.Write([]byte(text))
}

func queryBool(q url.Values, key string) bool {
	v := strings.TrimSpace(q.Get(key))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func clampInt(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// handleLogsSPVDeep returns a structured plain-text digest of spv.log plus optional headers.db raw header bytes.
// Query flags (all optional):
//   lines=N tail window (default 900, max 4000)
//   augment=1|0  (default 1) append hash= from headers.db for height-only lines
//   tail=1 include full augmented tail after digest sections
//   merkle=1 filter merkle/proof/inclusion-ish lines
//   hex=1 filter very long hex lines (likely raw tx / wire dumps)
//   addr=1 scan for wallet P2PKH address substrings (requires unlocked/plaintext wallet)
//   raw_header=1 include libdogecoin headers.db 80-byte header hex + hash256d check for height=… or best known height
//   height=N explicit height for raw_header probe
func (s *Server) handleLogsSPVDeep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	q := r.URL.Query()
	n := clampInt(func() int {
		x, _ := strconv.Atoi(strings.TrimSpace(q.Get("lines")))
		return x
	}(), 50, 4000, 900)

	wantAugment := !queryBool(q, "no_augment") && (q.Get("augment") == "" || queryBool(q, "augment"))
	wantTail := queryBool(q, "tail")
	wantMerkle := queryBool(q, "merkle")
	wantHex := queryBool(q, "hex")
	wantAddr := queryBool(q, "addr")
	wantRaw := queryBool(q, "raw_header")

	raw, err := readLastNLinesFromFile(s.spvLogPath(), 3<<20, n)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	var b strings.Builder
	fmt.Fprintf(&b, "pq-wallet SPV deep digest (last %d lines of spv.log)\n", n)
	fmt.Fprintf(&b, "log_path=%s\n", s.spvLogPath())
	if err != nil {
		fmt.Fprintf(&b, "\n")
		if os.IsNotExist(err) {
			fmt.Fprintf(&b, "(spv.log not found — the file is created when SPV runs and spvnode writes to it. Start SPV from Settings → Background services, or restart the pup; then refresh.)\n")
		} else {
			fmt.Fprintf(&b, "(read error: %v)\n", err)
		}
		_, _ = w.Write([]byte(b.String()))
		return
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	aug := raw
	if wantAugment {
		aug = s.augmentSPVLogWithHeaderHashes(raw)
	}
	lines := strings.Split(aug, "\n")

	// Wallet addresses (optional)
	var addrs []string
	if wantAddr {
		s.mu.Lock()
		wf, werr := s.loadWallet()
		s.mu.Unlock()
		if werr != nil {
			if errors.Is(werr, ErrWalletLocked) {
				fmt.Fprintf(&b, "\n[addr scan skipped: wallet locked / sealed]\n")
			} else {
				fmt.Fprintf(&b, "\n[addr scan skipped: %v]\n", werr)
			}
		} else if wf != nil {
			for _, a := range wf.AllDistinctP2PKHAddresses() {
				a = strings.TrimSpace(a)
				if a != "" {
					addrs = append(addrs, a)
				}
			}
		}
	}

	st, _ := s.loadState()
	stateH := int64(0)
	stateHash := ""
	if st != nil && len(st.Metrics) > 0 {
		for i := len(st.Metrics) - 1; i >= 0; i-- {
			if st.Metrics[i].HeaderHeight > 0 {
				stateH = st.Metrics[i].HeaderHeight
				stateHash = strings.TrimSpace(st.Metrics[i].BestBlockHash)
				break
			}
		}
	}
	hdbPath := filepath.Join(s.storageDir, "headers.db")
	var dbMax int64
	if isLibdogecoinHeadersFileFormat(hdbPath) {
		if tip, err := libdogecoinHeadersFileTipHeight(hdbPath); err == nil {
			dbMax = tip
		}
	}
	fmt.Fprintf(&b, "\n[state.json header_height=%d best_block_hash=%s] [headers.db max_height=%d]\n", stateH, stateHash, dbMax)

	// Count buckets
	var nMerkle, nHexish, nAddrHit int
	var merkleSamples, hexSamples, addrSamples []string
	push := func(dst *[]string, ln string, cap int) {
		if len(*dst) >= cap {
			return
		}
		*dst = append(*dst, trimLineForDebug(ln, 520))
	}
	lowLines := make([]string, len(lines))
	for i := range lines {
		lowLines[i] = strings.ToLower(lines[i])
	}
	for i, ln := range lines {
		ll := lowLines[i]
		if reSpvConfirmedLine.MatchString(ln) || strings.Contains(ll, "merkleblock") || strings.Contains(ll, "partial_merkle") || strings.Contains(ll, "filterload") {
			nMerkle++
			push(&merkleSamples, ln, 40)
		}
		t := strings.TrimSpace(strings.ReplaceAll(ln, " ", ""))
		if len(t) >= 160 && reHexOnly.MatchString(t) {
			nHexish++
			push(&hexSamples, ln, 24)
		}
		if len(addrs) > 0 {
			for _, a := range addrs {
				if strings.Contains(ll, strings.ToLower(a)) {
					nAddrHit++
					push(&addrSamples, ln, 40)
					break
				}
			}
		}
	}
	fmt.Fprintf(&b, "\n== counts ==\n")
	fmt.Fprintf(&b, "lines_total=%d\n", len(lines))
	fmt.Fprintf(&b, "merkle_or_filter_or_confirmish_lines=%d\n", nMerkle)
	fmt.Fprintf(&b, "long_hex_token_lines(>=160 hex chars)=%d\n", nHexish)
	if wantAddr {
		fmt.Fprintf(&b, "wallet_address_substring_hits=%d (addrs=%d)\n", nAddrHit, len(addrs))
	}

	if wantMerkle && nMerkle > 0 {
		fmt.Fprintf(&b, "\n== merkle / inclusion / filter (sample up to 40) ==\n")
		for _, s := range merkleSamples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	if wantHex && nHexish > 0 {
		fmt.Fprintf(&b, "\n== long hex lines (sample up to 24; truncated) ==\n")
		for _, s := range hexSamples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	if wantAddr && nAddrHit > 0 {
		fmt.Fprintf(&b, "\n== address hits (sample up to 40; truncated) ==\n")
		for _, s := range addrSamples {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}

	if wantRaw {
		hProbe := int64(0)
		if v := strings.TrimSpace(q.Get("height")); v != "" {
			if x, err := strconv.ParseInt(v, 10, 64); err == nil && x > 0 {
				hProbe = x
			}
		}
		if hProbe <= 0 {
			if stateH > 0 {
				hProbe = stateH
			} else if dbMax > 0 {
				hProbe = dbMax
			}
		}
		fmt.Fprintf(&b, "\n== headers.db raw header probe ==\n")
		fmt.Fprintf(&b, "height_used=%d\n", hProbe)
		if hProbe <= 0 {
			b.WriteString("(no height available — set ?height=N)\n")
		} else if !isLibdogecoinHeadersFileFormat(hdbPath) {
			fmt.Fprintf(&b, "format=unknown (not libdogecoin file layout); path=%s\n", hdbPath)
		} else {
			hx := libdogecoinHeader80HexAtHeight(hdbPath, hProbe)
			hashStored := strings.ToLower(strings.TrimSpace(libdogecoinHeaderHashHexAtHeight(hdbPath, hProbe)))
			fmt.Fprintf(&b, "meta format=libdogecoin_file path=%s\n", hdbPath)
			if hx == "" {
				b.WriteString("(no record at height in headers file)\n")
			} else {
				fmt.Fprintf(&b, "header80_hex_len_chars=%d\n", len(hx))
				show := hx
				if len(show) > 400 {
					show = hx[:400] + "…"
				}
				fmt.Fprintf(&b, "header80_hex_prefix=%s\n", show)
				rawBytes, err := hex.DecodeString(hx)
				if err != nil || len(rawBytes) < 80 {
					fmt.Fprintf(&b, "decode_to_80b: err=%v len=%d\n", err, len(rawBytes))
				} else {
					calc := strings.ToLower(hash256dLEHex(rawBytes[:80]))
					fmt.Fprintf(&b, "hash256d_first80_le_hex=%s\n", calc)
					fmt.Fprintf(&b, "hash_from_headers_file_record=%s\n", hashStored)
					if hashStored != "" && calc != "" && hashStored == calc {
						b.WriteString("hash_match=YES (first 80 bytes hash to stored header hash field)\n")
					} else if hashStored != "" && calc != "" {
						b.WriteString("hash_match=NO (layout may differ, or stored hash is not block hash)\n")
					}
				}
			}
		}
	}

	if wantTail {
		fmt.Fprintf(&b, "\n== full augmented tail (%d lines) ==\n", len(lines))
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteByte('\n')
	}

	_, _ = w.Write([]byte(b.String()))
}

func trimLineForDebug(s string, max int) string {
	s = strings.TrimRight(s, "\r\n")
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
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
