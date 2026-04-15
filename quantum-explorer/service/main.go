package main

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed static/*
var staticFS embed.FS

type Checkpoint struct {
	Height    int    `json:"height"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

type APIClient struct {
	Name      string   `json:"name"`
	Token     string   `json:"token"`
	Allowlist []string `json:"allowlist"`
}

type Config struct {
	HTTPPort             int         `json:"http_port"`
	Network              string      `json:"network"`
	AdminToken           string      `json:"admin_token"`
	ExplorerTxAPI        string      `json:"explorer_tx_api"`
	Checkpoint           Checkpoint  `json:"checkpoint"`
	AdminAllowlist       []string    `json:"admin_allowlist,omitempty"`
	PublicAPIToken       string      `json:"public_api_token,omitempty"`
	PublicAPIAllowlist   []string    `json:"public_api_allowlist,omitempty"`
	PublicProtectedPaths []string    `json:"public_protected_paths,omitempty"`
	PublicRateLimit      int         `json:"public_rate_limit,omitempty"`
	PublicRateWindowSec  int         `json:"public_rate_window_sec,omitempty"`
	PublicAPIClients     []APIClient `json:"public_api_clients,omitempty"`
}

type PQTx struct {
	Txid       string   `json:"txid"`
	Addresses  []string `json:"addresses"`
	FirstSeen  string   `json:"first_seen"`
	LastSeen   string   `json:"last_seen"`
	Confirmed  bool     `json:"confirmed"`
	PQValid    bool     `json:"pq_valid"`
	PQScore    int      `json:"pq_score"`
	PQReason   string   `json:"pq_reason,omitempty"`
	PQEvidence []string `json:"pq_evidence,omitempty"`
	Verifier   string   `json:"verifier,omitempty"`
}

type app struct {
	mu         sync.RWMutex
	cfg        Config
	cfgPath    string
	storePath  string
	storageDir string

	txs          map[string]*PQTx
	addressIndex map[string][]string

	// Local chain index: JSON file (default) or PostgreSQL when QE_POSTGRES_URL is set.
	chain chainBackend
	core  *coreRPCClient
	cidx  *coreIndexer

	adminAllowlist       map[string]struct{}
	publicAllowlist      map[string]struct{}
	publicToken          string
	publicProtectedPaths map[string]struct{}
	rl                   *ipRateLimiter
}

func env(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func envInt(key string, def int) int {
	n, err := strconv.Atoi(env(key, strconv.Itoa(def)))
	if err != nil || n < 1 || n > 65535 {
		return def
	}
	return n
}

func defaultConfig() Config {
	return Config{
		HTTPPort:      envInt("PUBLIC_PORT", 33666),
		Network:       env("NETWORK", "mainnet"),
		AdminToken:    env("QE_ADMIN_TOKEN", "QUANTUM-TOKEN"),
		ExplorerTxAPI: env("QE_EXPLORER_TX_API", ""),
		Checkpoint: Checkpoint{
			Height:    6093890,
			Hash:      "7ecb28519e0c144261e511fd8706f8b54a93620cac31c41b5bcb0135f0d86a2b",
			Timestamp: "2026-02-20T21:59:00Z",
		},
		AdminAllowlist:       splitCSVEnv("QE_ADMIN_ALLOWLIST"),
		PublicAPIToken:       strings.TrimSpace(os.Getenv("QE_PUBLIC_API_TOKEN")),
		PublicAPIAllowlist:   splitCSVEnv("QE_PUBLIC_API_ALLOWLIST"),
		PublicProtectedPaths: splitCSVEnvWithDefault("QE_PUBLIC_PROTECTED_PATHS", ""),
		PublicRateLimit:      envIntBounded("QE_PUBLIC_RATE_LIMIT", 120, 1, 100000),
		PublicRateWindowSec:  envIntBounded("QE_PUBLIC_RATE_WINDOW_SEC", 60, 1, 86400),
	}
}

func splitCSVEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitCSVEnvWithDefault(key, def string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		raw = def
	}
	out := []string{}
	for _, p := range strings.Split(raw, ",") {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func envIntBounded(key string, def, min, max int) int {
	n, err := strconv.Atoi(env(key, strconv.Itoa(def)))
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

func loadConfig(path string) Config {
	cfg := defaultConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(b, &cfg)
	if strings.TrimSpace(cfg.AdminToken) == "" {
		cfg.AdminToken = "QUANTUM-TOKEN"
	}
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		cfg.HTTPPort = 33666
	}
	if cfg.Network == "" {
		cfg.Network = "mainnet"
	}
	if cfg.Checkpoint.Height <= 0 {
		cfg.Checkpoint = defaultConfig().Checkpoint
	}
	if cfg.PublicRateLimit <= 0 {
		cfg.PublicRateLimit = defaultConfig().PublicRateLimit
	}
	if cfg.PublicRateWindowSec <= 0 {
		cfg.PublicRateWindowSec = defaultConfig().PublicRateWindowSec
	}
	if len(cfg.PublicProtectedPaths) == 0 {
		cfg.PublicProtectedPaths = defaultConfig().PublicProtectedPaths
	}
	return cfg
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadTxs(path string) map[string]*PQTx {
	out := map[string]*PQTx{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var rows []*PQTx
	if json.Unmarshal(b, &rows) == nil {
		for _, r := range rows {
			if r != nil && r.Txid != "" {
				out[r.Txid] = r
			}
		}
	}
	return out
}

func (a *app) persistTxs() {
	a.mu.RLock()
	rows := make([]*PQTx, 0, len(a.txs))
	for _, t := range a.txs {
		cp := *t
		rows = append(rows, &cp)
	}
	a.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastSeen > rows[j].LastSeen })
	_ = saveJSON(a.storePath, rows)
}

func containsIgnoreCase(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func classifyPQ(txid string, tracked bool, addrCount int) (valid bool, score int) {
	if tracked && addrCount > 0 {
		return true, 60
	}
	return false, 0
}

type txOutput struct {
	valueSats int64
	script    []byte
}

func readVarInt(data []byte, off *int) (uint64, error) {
	if *off < 0 || *off >= len(data) {
		return 0, io.EOF
	}
	b0 := data[*off]
	*off++
	if b0 < 0xfd {
		return uint64(b0), nil
	}
	switch b0 {
	case 0xfd:
		if *off+2 > len(data) {
			return 0, io.EOF
		}
		v := binary.LittleEndian.Uint16(data[*off:])
		*off += 2
		return uint64(v), nil
	case 0xfe:
		if *off+4 > len(data) {
			return 0, io.EOF
		}
		v := binary.LittleEndian.Uint32(data[*off:])
		*off += 4
		return uint64(v), nil
	default:
		if *off+8 > len(data) {
			return 0, io.EOF
		}
		v := binary.LittleEndian.Uint64(data[*off:])
		*off += 8
		return v, nil
	}
}

func parseTxOutputs(raw []byte) ([]txOutput, error) {
	if len(raw) < 10 {
		return nil, errors.New("short tx")
	}
	off := 4 // version
	segwit := off+2 <= len(raw) && raw[off] == 0 && raw[off+1] == 1
	if segwit {
		off += 2
	}
	nin, err := readVarInt(raw, &off)
	if err != nil {
		return nil, err
	}
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return nil, errors.New("truncated input")
		}
		off += 36
		slen, err := readVarInt(raw, &off)
		if err != nil || off+int(slen) > len(raw) {
			return nil, errors.New("truncated scriptsig")
		}
		off += int(slen)
		if off+4 > len(raw) {
			return nil, errors.New("truncated sequence")
		}
		off += 4
	}
	nout, err := readVarInt(raw, &off)
	if err != nil {
		return nil, err
	}
	outs := make([]txOutput, 0, nout)
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return nil, errors.New("truncated value")
		}
		val := int64(binary.LittleEndian.Uint64(raw[off:]))
		off += 8
		slen, err := readVarInt(raw, &off)
		if err != nil || off+int(slen) > len(raw) {
			return nil, errors.New("truncated scriptpubkey")
		}
		scr := make([]byte, int(slen))
		copy(scr, raw[off:off+int(slen)])
		off += int(slen)
		outs = append(outs, txOutput{valueSats: val, script: scr})
	}
	return outs, nil
}

func extractOpReturnData(script []byte) ([]byte, bool) {
	if len(script) < 2 || script[0] != 0x6a {
		return nil, false
	}
	// OP_RETURN <PUSHDATA...> (simple single push)
	if len(script) >= 2 {
		n := int(script[1])
		if n > 0 && 2+n <= len(script) {
			return script[2 : 2+n], true
		}
	}
	return nil, false
}

func scriptAddressTag(script []byte) string {
	// P2PKH: 76 a9 14 <20> 88 ac
	if len(script) == 25 && script[0] == 0x76 && script[1] == 0xa9 && script[2] == 0x14 && script[23] == 0x88 && script[24] == 0xac {
		return "p2pkh:" + hex.EncodeToString(script[3:23])
	}
	// P2SH: a9 14 <20> 87
	if len(script) == 23 && script[0] == 0xa9 && script[1] == 0x14 && script[22] == 0x87 {
		return "p2sh:" + hex.EncodeToString(script[2:22])
	}
	return ""
}

func extractHexFromAny(v any, depth int) string {
	if depth > 12 || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if len(s) >= 64 && len(s)%2 == 0 {
			if _, err := hex.DecodeString(s); err == nil {
				return strings.ToLower(s)
			}
		}
	case map[string]any:
		keys := []string{"raw_hex", "hex", "tx_hex", "rawtx", "raw", "transaction_hex"}
		for _, k := range keys {
			if h := extractHexFromAny(x[k], depth+1); h != "" {
				return h
			}
		}
		for _, vv := range x {
			if h := extractHexFromAny(vv, depth+1); h != "" {
				return h
			}
		}
	case []any:
		for _, vv := range x {
			if h := extractHexFromAny(vv, depth+1); h != "" {
				return h
			}
		}
	}
	return ""
}

// fetchRawTxHex fetches raw tx hex via optional QE_EXPLORER_TX_API template ({txid}).
func (a *app) fetchRawTxHex(txid string, explorerAPI string) string {
	tpl := strings.TrimSpace(explorerAPI)
	id := strings.ToLower(strings.TrimSpace(txid))
	if id == "" {
		return ""
	}
	if tpl == "" {
		return ""
	}
	url := strings.ReplaceAll(tpl, "{txid}", id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ""
	}
	var body any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&body); err != nil {
		return ""
	}
	return extractHexFromAny(body, 0)
}

func verifyPQStrict(rawHex string) (valid bool, reason string, evidence []string, outputTags []string) {
	raw, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil {
		return false, "raw tx hex decode failed", []string{"decode_error"}, nil
	}
	outs, err := parseTxOutputs(raw)
	if err != nil {
		return false, "raw tx parse failed", []string{"parse_error"}, nil
	}
	markers := []string{"falcon", "dilithium", "raccoon", "raccoon-g", "tx_c", "tx_r", "pq", "commit"}
	var foundMarkers []string
	hasOpRet := false
	for _, o := range outs {
		if tag := scriptAddressTag(o.script); tag != "" {
			outputTags = append(outputTags, tag)
		}
		d, ok := extractOpReturnData(o.script)
		if !ok {
			continue
		}
		hasOpRet = true
		txt := strings.ToLower(string(d))
		hexTxt := strings.ToLower(hex.EncodeToString(d))
		for _, m := range markers {
			if strings.Contains(txt, m) || strings.Contains(hexTxt, hex.EncodeToString([]byte(m))) {
				foundMarkers = append(foundMarkers, m)
			}
		}
	}
	if !hasOpRet {
		return false, "no OP_RETURN commitment detected", []string{"missing_op_return"}, outputTags
	}
	if len(foundMarkers) == 0 {
		return false, "OP_RETURN present but no recognized PQ commitment marker", []string{"op_return_without_pq_marker"}, outputTags
	}
	uniq := map[string]struct{}{}
	ev := make([]string, 0, len(foundMarkers))
	for _, m := range foundMarkers {
		if _, ok := uniq[m]; ok {
			continue
		}
		uniq[m] = struct{}{}
		ev = append(ev, "marker:"+m)
	}
	return true, "recognized PQ commitment markers in OP_RETURN", ev, outputTags
}

// buildPQVerificationDetail parses raw tx hex for UI: full decode + PQ strict verifier (see decode map).
func buildPQVerificationDetail(rawHex string, network string) map[string]any {
	h := strings.TrimSpace(rawHex)
	out := map[string]any{
		"raw_tx_hex_available": false,
		"decode":               nil,
		"strict":               nil,
	}
	if h == "" {
		out["note"] = "No raw transaction hex (set QE_EXPLORER_TX_API or use Core-indexed tx detail)."
		out["decode"] = DecodeTxJSON("", network)
		return out
	}
	if _, err := hex.DecodeString(h); err != nil {
		out["decode_error"] = err.Error()
		return out
	}
	out["raw_tx_hex_available"] = true
	out["raw_tx_hex_length"] = len(h)
	if len(h) > 256 {
		out["raw_tx_hex_preview"] = h[:256] + "…"
	} else {
		out["raw_tx_hex_preview"] = h
	}
	dec := DecodeTxJSON(h, network)
	out["decode"] = dec
	if _, has := dec["error"]; has {
		return out
	}
	valid, reason, ev, tags := verifyPQStrict(h)
	out["strict"] = map[string]any{
		"valid":       valid,
		"reason":      reason,
		"evidence":    ev,
		"output_tags": tags,
	}
	return out
}

func (a *app) enrichDecodeWithPrevouts(ctx context.Context, pq map[string]any) map[string]any {
	if a == nil || a.cidx == nil || pq == nil {
		return pq
	}
	dec, ok := pq["decode"].(map[string]any)
	if !ok || dec == nil {
		return pq
	}
	inputs, ok := dec["inputs"].([]map[string]any)
	if ok {
		for _, in := range inputs {
			prevTx := strings.ToLower(strings.TrimSpace(fmt.Sprint(in["prev_txid"])))
			prevVout := int64(-1)
			switch v := in["prev_vout"].(type) {
			case int:
				prevVout = int64(v)
			case int64:
				prevVout = v
			case float64:
				prevVout = int64(v)
			case json.Number:
				if n, err := v.Int64(); err == nil {
					prevVout = n
				}
			}
			if len(prevTx) != 64 || prevVout < 0 {
				continue
			}
			prev, found, err := a.cidx.prevoutByTxVout(ctx, prevTx, prevVout)
			if err != nil || !found || prev == nil {
				continue
			}
			in["prev_output"] = prev
		}
		dec["inputs"] = inputs
		pq["decode"] = dec
		return pq
	}
	rawInputs, ok := dec["inputs"].([]any)
	if !ok {
		return pq
	}
	for _, row := range rawInputs {
		in, ok := row.(map[string]any)
		if !ok {
			continue
		}
		prevTx := strings.ToLower(strings.TrimSpace(fmt.Sprint(in["prev_txid"])))
		prevVout := int64(-1)
		switch v := in["prev_vout"].(type) {
		case int:
			prevVout = int64(v)
		case int64:
			prevVout = v
		case float64:
			prevVout = int64(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				prevVout = n
			}
		}
		if len(prevTx) != 64 || prevVout < 0 {
			continue
		}
		prev, found, err := a.cidx.prevoutByTxVout(ctx, prevTx, prevVout)
		if err != nil || !found || prev == nil {
			continue
		}
		in["prev_output"] = prev
	}
	dec["inputs"] = rawInputs
	pq["decode"] = dec
	return pq
}

func (a *app) reindexUnsafe() {
	a.addressIndex = map[string][]string{}
	for txid, t := range a.txs {
		for _, ad := range t.Addresses {
			ad = strings.TrimSpace(strings.ToLower(ad))
			if ad == "" {
				continue
			}
			a.addressIndex[ad] = append(a.addressIndex[ad], txid)
		}
	}
}

func (a *app) adminTokenValid(token string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return strings.TrimSpace(token) != "" && strings.TrimSpace(token) == strings.TrimSpace(a.cfg.AdminToken)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) latest(limit int) []*PQTx {
	a.mu.RLock()
	rows := make([]*PQTx, 0, len(a.txs))
	for _, t := range a.txs {
		cp := *t
		rows = append(rows, &cp)
	}
	a.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastSeen > rows[j].LastSeen })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func (a *app) publicStatus(w http.ResponseWriter, r *http.Request) {
	rows := a.latest(100)
	m := map[string]int{"pq_seen": len(rows), "confirmed": 0, "invalid": 0, "pq_valid": 0, "non_quantum": 0}
	for _, t := range rows {
		if t.Confirmed {
			m["confirmed"]++
		}
		if !t.PQValid {
			m["invalid"]++
			m["non_quantum"]++
		} else {
			m["pq_valid"]++
		}
	}
	latestPQ := rows
	if len(latestPQ) > 10 {
		cp := make([]*PQTx, 10)
		copy(cp, latestPQ[:10])
		latestPQ = cp
	}
	chainSum := a.chainSummaryMap()
	chainHdrs := a.recentChainHeaders(12)

	status := map[string]any{
		"metrics":                m,
		"latest":                 rows,
		"latest_pq_transactions": latestPQ,
		"chain_index": map[string]any{
			"summary":         chainSum,
			"recent_headers":  chainHdrs,
			"block_detail_qs": "GET /api/public/block?height=<n> or &hash=<64hex>",
		},
		"core":         a.core.snapshot(),
		"core_indexer": a.coreIndexerStatus(),
	}
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		ex := map[string]any{
			"enabled": true,
			"summary": a.cidx.summary(ctx),
			"pq":      a.cidx.pqAggregates(ctx),
		}
		if rb, err := a.cidx.recentBlocks(ctx, 15); err == nil {
			ex["recent_blocks"] = rb
		}
		status["explorer"] = ex
	} else {
		status["explorer"] = map[string]any{
			"enabled": false,
			"note":    "Set QE_POSTGRES_URL (embedded Postgres in this pup) and Dogecoin Core RPC for full chain indexing.",
		}
	}
	writeJSON(w, 200, status)
}

func (a *app) publicBlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	hash := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hash")))
	heightStr := strings.TrimSpace(r.URL.Query().Get("height"))
	if hash == "" && heightStr == "" {
		writeJSON(w, 400, map[string]string{"error": "provide hash= or height="})
		return
	}
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		decodeLimit := envIntBounded("QE_BLOCK_DECODE_LIMIT", 50, 1, 200)
		if s := strings.TrimSpace(r.URL.Query().Get("decode_limit")); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
				decodeLimit = n
			}
		}
		var hp *int64
		var hpStr *string
		if heightStr != "" {
			if hi, err := strconv.ParseInt(heightStr, 10, 64); err == nil && hi >= 0 {
				hp = &hi
			}
		}
		if hash != "" {
			if len(hash) != 64 || !isHex64String(hash) {
				writeJSON(w, 400, map[string]string{"error": "hash must be 64 hex chars"})
				return
			}
			hs := hash
			hpStr = &hs
		}
		if hp != nil || hpStr != nil {
			detail, err := a.cidx.blockDetail(ctx, hp, hpStr, decodeLimit)
			if err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			if found, _ := detail["found"].(bool); found {
				bm := detail["block"].(map[string]any)
				bh := int(anyInt64(bm["height"]))
				ts := time.Unix(anyInt64(bm["time_unix"]), 0).UTC().Format(time.RFC3339)
				b := &IndexedBlockHeader{Height: bh, Hash: fmt.Sprint(bm["hash"]), Timestamp: ts}
				writeJSON(w, 200, map[string]any{
					"source":                  "core_index",
					"block":                   b,
					"associated_transactions": detail["transactions"],
					"tx_count":                bm["tx_count"],
					"decode_limit":            decodeLimit,
					"notes": []string{
						"Dogecoin Core RPC indexer (local PostgreSQL).",
						fmt.Sprintf("Full transaction list below; optional decode for the first %d txs (decode_limit) when raw hex is stored.", decodeLimit),
					},
				})
				return
			}
		}
	}
	var b *IndexedBlockHeader
	var err error
	if hash != "" {
		if len(hash) != 64 || !isHex64String(hash) {
			writeJSON(w, 400, map[string]string{"error": "hash must be 64 hex chars"})
			return
		}
		b, err = a.chain.GetByHash(hash)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
	}
	if b == nil && heightStr != "" {
		h, err2 := strconv.Atoi(heightStr)
		if err2 != nil || h <= 0 {
			writeJSON(w, 400, map[string]string{"error": "invalid height"})
			return
		}
		b, err = a.chain.GetByHeight(h)
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if b == nil {
		writeJSON(w, 404, map[string]string{"error": "block header not in local chain index yet"})
		return
	}
	txids, err := a.chain.ListTxidsForHeight(b.Height)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	a.mu.RLock()
	tpl := strings.TrimSpace(a.cfg.ExplorerTxAPI)
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	a.mu.RUnlock()
	txSummaries := make([]map[string]any, 0, len(txids))
	for _, id := range txids {
		id = strings.ToLower(id)
		rawHex := a.fetchRawTxHex(id, tpl)
		var dec map[string]any
		if rawHex != "" {
			dec = DecodeTxJSON(rawHex, net)
		}
		a.mu.RLock()
		t := a.txs[id]
		a.mu.RUnlock()
		row := map[string]any{
			"txid":         id,
			"decode":       dec,
			"detail_query": "/api/public/tx?txid=" + id,
		}
		if t != nil {
			cp := *t
			row["pq_tx"] = &cp
		} else {
			row["pq_tx"] = nil
			row["note"] = "Heuristic link from chain index; not in PQ store. Decode works if raw tx is available."
		}
		if rawHex == "" {
			row["decode_note"] = "No raw tx bytes yet — set QE_EXPLORER_TX_API or open tx via Core indexer."
		}
		txSummaries = append(txSummaries, row)
	}

	writeJSON(w, 200, map[string]any{
		"block":                   b,
		"associated_transactions": txSummaries,
		"notes": []string{
			"Block headers come from the local chain index (legacy JSON/Postgres store).",
			"Transactions listed here are heuristic height↔txid links when present; full decoding needs raw tx (Core RPC or QE_EXPLORER_TX_API).",
			"Prefer Core-indexed block detail when PostgreSQL + indexer are enabled.",
		},
	})
}

func (a *app) publicTxDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("txid")))
	if len(q) != 64 || !isHex64String(q) {
		writeJSON(w, 400, map[string]string{"error": "txid must be 64 hex characters"})
		return
	}
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		rawHex, qState, pqReas, blkH, tm, vOut, blkHash, okRow, err := a.cidx.txRowByID(ctx, q)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if okRow {
			if strings.TrimSpace(rawHex) == "" && a.core != nil && a.core.enabled() {
				if h, err := a.core.getRawTransactionHex(ctx, q, blkHash); err == nil && strings.TrimSpace(h) != "" {
					rawHex = strings.TrimSpace(h)
					_ = a.cidx.backfillRawHex(ctx, q, rawHex)
					r2, qs2, pr2, bh2, tm2, vo2, bkh2, ok2, err2 := a.cidx.txRowByID(ctx, q)
					if err2 == nil && ok2 {
						rawHex, qState, pqReas, blkH, tm, vOut, blkHash = r2, qs2, pr2, bh2, tm2, vo2, bkh2
					}
				}
			}
			pqValid := false
			pqScore := 0
			if strings.TrimSpace(rawHex) != "" {
				if ok, _, _, _ := verifyPQStrict(rawHex); ok {
					pqValid = true
					pqScore = 100
				}
			} else {
				pqValid = qState == "quantum"
				if pqValid {
					pqScore = 100
				}
			}
			score := pqScore
			seen := time.Unix(tm, 0).UTC().Format(time.RFC3339)
			cp := PQTx{
				Txid:      q,
				Confirmed: true,
				PQValid:   pqValid,
				PQScore:   score,
				PQReason:  pqReas,
				Verifier:  "strict-v1",
				FirstSeen: seen,
				LastSeen:  seen,
			}
			pq := buildPQVerificationDetail(rawHex, net)
			pq = a.enrichDecodeWithPrevouts(ctx, pq)
			writeJSON(w, 200, map[string]any{
				"source":          "core_index",
				"tx":              &cp,
				"pq_verification": pq,
				"core": map[string]any{
					"block_height": blkH, "block_hash": blkHash, "timestamp": seen, "quantum_state": qState, "value_out_sats": vOut,
				},
			})
			return
		}
	}
	a.mu.RLock()
	tx, ok := a.txs[q]
	if !ok {
		for k, v := range a.txs {
			if strings.EqualFold(k, q) {
				tx, ok = v, true
				q = strings.ToLower(k)
				break
			}
		}
	}
	a.mu.RUnlock()
	if !ok || tx == nil {
		writeJSON(w, 404, map[string]string{"error": "transaction not in PQ index"})
		return
	}
	a.mu.RLock()
	tpl := strings.TrimSpace(a.cfg.ExplorerTxAPI)
	a.mu.RUnlock()
	rawHex := a.fetchRawTxHex(q, tpl)
	cp := *tx
	pq := buildPQVerificationDetail(rawHex, net)
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		pq = a.enrichDecodeWithPrevouts(ctx, pq)
		cancel()
	}
	writeJSON(w, 200, map[string]any{
		"tx":              &cp,
		"pq_verification": pq,
	})
}

func (a *app) publicSearch(w http.ResponseWriter, r *http.Request) {
	rawQ := strings.TrimSpace(r.URL.Query().Get("q"))
	if rawQ == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	qLower := strings.ToLower(rawQ)
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		out, err := a.cidx.search(ctx, rawQ, 50)
		if err == nil && out != nil {
			if e, ok := out["error"].(string); ok && strings.TrimSpace(e) != "" {
				// fall through to legacy
			} else {
				out["source"] = "core_index"
				writeJSON(w, 200, out)
				return
			}
		}
	}
	rows := a.latest(1000)
	out := make([]*PQTx, 0)
	a.mu.RLock()
	txidsByAddr := a.addressIndex[rawQ]
	a.mu.RUnlock()
	if len(txidsByAddr) > 0 {
		a.mu.RLock()
		for _, id := range txidsByAddr {
			id = strings.ToLower(id)
			if t := a.txs[id]; t != nil {
				cp := *t
				out = append(out, &cp)
			}
		}
		a.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"query": rawQ, "kind": "address", "results": out})
		return
	}
	if matched, _ := regexp.MatchString(`^[0-9a-fA-F]{64}$`, rawQ); matched {
		blk, err := a.chain.GetByHash(qLower)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if blk != nil {
			writeJSON(w, 200, map[string]any{
				"query":       rawQ,
				"kind":        "block_hash",
				"block":       blk,
				"block_query": "/api/public/block?hash=" + qLower,
			})
			return
		}
		for _, t := range rows {
			if strings.EqualFold(t.Txid, qLower) {
				writeJSON(w, 200, map[string]any{
					"query":     rawQ,
					"kind":      "txid",
					"results":   []*PQTx{t},
					"tx_detail": "/api/public/tx?txid=" + qLower,
				})
				return
			}
		}
		writeJSON(w, 200, map[string]any{
			"query": rawQ,
			"kind":  "unknown_hex64",
			"note":  "Not a known block hash in the local chain index or a PQ-indexed txid",
		})
		return
	}
	if h, err := strconv.Atoi(rawQ); err == nil && rawQ == strconv.Itoa(h) && h > 0 {
		blk, err := a.chain.GetByHeight(h)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if blk != nil {
			writeJSON(w, 200, map[string]any{
				"query":       rawQ,
				"kind":        "block_height",
				"block":       blk,
				"block_query": "/api/public/block?height=" + rawQ,
			})
			return
		}
	}
	for _, t := range rows {
		if containsIgnoreCase(t.Txid, rawQ) {
			out = append(out, t)
		}
	}
	kind := "text"
	if _, err := strconv.Atoi(rawQ); err == nil {
		kind = "block-height-no-index"
	}
	writeJSON(w, 200, map[string]any{"query": rawQ, "kind": kind, "results": out})
}

func (a *app) adminStatus(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, 200, map[string]any{
		"checkpoint":          a.cfg.Checkpoint,
		"network":             a.cfg.Network,
		"chain_index_backend": a.chain.Kind(),
		"storage_dir":         a.storageDir,
		"diagnostics_api":     "GET /api/admin/<token>/diagnostics",
		"core_rpc":            a.core.snapshot(),
		"core_indexer":        a.coreIndexerStatus(),
		"admin_allowlist_set": len(a.adminAllowlist) > 0,
		"public_protection": map[string]any{
			"token_set":       a.publicToken != "",
			"allowlist_set":   len(a.publicAllowlist) > 0,
			"protected_paths": len(a.publicProtectedPaths),
			"rate_limit":      a.rl.limit,
			"rate_window_sec": int(a.rl.window.Seconds()),
		},
	})
}

func (a *app) adminCheckpoint(w http.ResponseWriter, r *http.Request) {
	var cp Checkpoint
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cp); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if cp.Height <= 0 || strings.TrimSpace(cp.Hash) == "" {
		writeJSON(w, 400, map[string]string{"error": "height/hash required"})
		return
	}
	a.mu.Lock()
	a.cfg.Checkpoint = cp
	cfg := a.cfg
	a.mu.Unlock()
	if err := saveJSON(a.cfgPath, cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "checkpoint": cp})
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func (a *app) chainSummaryMap() map[string]any {
	if a.chain == nil {
		return map[string]any{"header_count": 0, "tip_height": 0, "tx_links": 0}
	}
	hc, tip, txh, err := a.chain.Summary()
	if err != nil {
		return map[string]any{"header_count": 0, "tip_height": 0, "tx_links": 0, "error": err.Error()}
	}
	return map[string]any{"header_count": hc, "tip_height": tip, "tx_links": txh}
}

func (a *app) recentChainHeaders(n int) []IndexedBlockHeader {
	if a.chain == nil {
		return nil
	}
	h, err := a.chain.RecentHeaders(n)
	if err != nil {
		return nil
	}
	return h
}

func main() {
	storage := env("QE_STORAGE_DIR", "/storage/quantum-explorer")
	must(os.MkdirAll(storage, 0o755))
	cb, err := openChainBackend(storage)
	if err != nil {
		log.Fatalf("[quantum-explorer] chain backend: %v", err)
	}
	a := &app{
		cfgPath:      filepath.Join(storage, "quantum-explorer-config.json"),
		storePath:    filepath.Join(storage, "quantum-explorer-pqtx.json"),
		storageDir:   storage,
		txs:          map[string]*PQTx{},
		addressIndex: map[string][]string{},
		chain:        cb,
		core:         newCoreRPCClientFromEnv(),
	}
	a.cfg = loadConfig(a.cfgPath)
	a.applyAccessConfigLocked()
	if pg, ok := cb.(*postgresChainBackend); ok {
		a.cidx = newCoreIndexer(pg.db, a.core, a.cfg.Network)
	}
	a.txs = loadTxs(a.storePath)
	log.Printf("[quantum-explorer] chain index backend=%s", cb.Kind())
	a.reindexUnsafe()
	_ = saveJSON(a.cfgPath, a.cfg)
	if a.cidx != nil {
		a.cidx.autoStartIfEnabled()
	}

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", a.healthz)
	publicMux.HandleFunc("/readyz", a.readyz)
	publicMux.HandleFunc("/api/public/status", a.withRateLimit(a.publicStatus))
	publicMux.HandleFunc("/api/public/block", a.withRateLimit(a.publicBlock))
	publicMux.HandleFunc("/api/public/tx", a.withRateLimit(a.publicTxDetail))
	publicMux.HandleFunc("/api/public/search", a.withRateLimit(a.publicSearch))
	publicMux.HandleFunc("/api/public/metrics", a.withRateLimit(a.publicMetrics))
	publicMux.HandleFunc("/api/public/core/search", a.withRateLimit(a.withPublicAccess(a.publicCoreSearch)))
	publicMux.HandleFunc("/api/public/core/summary", a.withRateLimit(a.withPublicAccess(a.publicCoreSummary)))
	publicMux.HandleFunc("/api/public/core/recent-txs", a.withRateLimit(a.withPublicAccess(a.publicCoreRecentTxs)))
	publicMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		if !a.adminIPAllowed(r) {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, "/admin/")
		token = strings.TrimSpace(strings.Trim(token, "/"))
		if !a.adminTokenValid(token) {
			http.NotFound(w, r)
			return
		}
		b, err := staticFS.ReadFile("static/admin.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	publicMux.HandleFunc("/api/admin/", func(w http.ResponseWriter, r *http.Request) {
		if !a.adminIPAllowed(r) {
			http.NotFound(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/admin/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(parts[0])
		action := "/" + strings.TrimSpace(parts[1])
		if !a.adminTokenValid(token) {
			http.NotFound(w, r)
			return
		}
		switch action {
		case "/status":
			a.adminStatus(w, r)
		case "/diagnostics":
			a.adminDiagnostics(w, r)
		case "/checkpoint":
			a.adminCheckpoint(w, r)
		case "/access":
			if r.Method == http.MethodGet {
				a.adminAccessGet(w, r)
				return
			}
			a.adminAccessSet(w, r)
		case "/core-indexer/start":
			a.adminStartCoreIndexer(w)
		case "/core-indexer/stop":
			a.adminStopCoreIndexer(w)
		case "/core-indexer/status":
			a.adminCoreIndexerStatus(w)
		default:
			http.NotFound(w, r)
		}
	})

	publicMux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		b, err := staticFS.ReadFile("static/logo.png")
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	})
	publicMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})

	publicAddr := ":" + strconv.Itoa(a.cfg.HTTPPort)
	log.Printf("[quantum-explorer] public listening on %s network=%s", publicAddr, a.cfg.Network)
	log.Printf("[quantum-explorer] admin UI path tokenized: /admin/<TOKEN>")
	handler := withRequestLogging(withSecurityHeaders(publicMux))
	log.Fatal(http.ListenAndServe(publicAddr, handler))
}
