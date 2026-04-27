package main

import (
	"context"
	"crypto/sha256"
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

// qeAppVersion is shown in the public UI and /api/public/status (keep in sync with manifest.json).
const qeAppVersion = "0.1.42"

// qeAppBuildHash is a release fingerprint (SHA-256 hex of "quantum-explorer-<version>"); bump when cutting a release.
const qeAppBuildHash = "c0633ec363b0ecc686219a6e52973c7683de991363739acf488df89ad19960b0"

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

type txInput struct {
	prevTxidLE []byte
	prevVout   uint32
	scriptSig  []byte
	sequence   uint32
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

func parseTxInputs(raw []byte) ([]txInput, error) {
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
	ins := make([]txInput, 0, nin)
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return nil, errors.New("truncated input")
		}
		in := txInput{
			prevTxidLE: append([]byte(nil), raw[off:off+32]...),
			prevVout:   binary.LittleEndian.Uint32(raw[off+32 : off+36]),
		}
		off += 36
		slen, err := readVarInt(raw, &off)
		if err != nil || off+int(slen) > len(raw) {
			return nil, errors.New("truncated scriptsig")
		}
		in.scriptSig = append([]byte(nil), raw[off:off+int(slen)]...)
		off += int(slen)
		if off+4 > len(raw) {
			return nil, errors.New("truncated sequence")
		}
		in.sequence = binary.LittleEndian.Uint32(raw[off:])
		off += 4
		ins = append(ins, in)
	}
	return ins, nil
}

type carrierPart struct {
	algo      string
	tag       string
	partIndex int
	partTotal int
	pkLen     int
	fullLen   int
	payload   []byte
	vin       int // input index carrying this carrier chunk
}

func parsePushOnlyScript(script []byte) ([][]byte, bool) {
	parts := make([][]byte, 0, 8)
	for i := 0; i < len(script); {
		op := script[i]
		i++
		switch {
		case op == 0x00:
			parts = append(parts, []byte{})
		case op >= 0x01 && op <= 0x4b:
			n := int(op)
			if i+n > len(script) {
				return nil, false
			}
			parts = append(parts, append([]byte(nil), script[i:i+n]...))
			i += n
		case op == 0x4c:
			if i+1 > len(script) {
				return nil, false
			}
			n := int(script[i])
			i++
			if i+n > len(script) {
				return nil, false
			}
			parts = append(parts, append([]byte(nil), script[i:i+n]...))
			i += n
		case op == 0x4d:
			if i+2 > len(script) {
				return nil, false
			}
			n := int(binary.LittleEndian.Uint16(script[i:]))
			i += 2
			if i+n > len(script) {
				return nil, false
			}
			parts = append(parts, append([]byte(nil), script[i:i+n]...))
			i += n
		default:
			return nil, false
		}
	}
	return parts, true
}

func parseCarrierPartFromScriptSig(script []byte) (carrierPart, bool) {
	pushes, ok := parsePushOnlyScript(script)
	if !ok || len(pushes) < 6 {
		return carrierPart{}, false
	}
	tag8 := pushes[0]
	hdr8 := pushes[1]
	if len(tag8) != 8 || len(hdr8) != 8 {
		return carrierPart{}, false
	}
	tag := string(tag8)
	algo := ""
	switch tag {
	case "FLC1FULL":
		algo = "FLC1"
	case "DIL2FULL", "DL21FULL":
		algo = "DIL2"
	case "RCG4FULL":
		algo = "RCG4"
	default:
		return carrierPart{}, false
	}
	if hdr8[0] != 0x01 {
		return carrierPart{}, false
	}
	partIdx := int(hdr8[1])
	partTot := int(hdr8[2])
	pkLen := int(binary.BigEndian.Uint16(hdr8[4:6]))
	fullLen := int(binary.BigEndian.Uint16(hdr8[6:8]))
	if partTot < 1 || partIdx < 0 || partIdx >= partTot || pkLen < 1 || fullLen < pkLen {
		return carrierPart{}, false
	}
	payload := make([]byte, 0, len(pushes[2])+len(pushes[3])+len(pushes[4]))
	payload = append(payload, pushes[2]...)
	payload = append(payload, pushes[3]...)
	payload = append(payload, pushes[4]...)
	return carrierPart{
		algo:      algo,
		tag:       tag,
		partIndex: partIdx,
		partTotal: partTot,
		pkLen:     pkLen,
		fullLen:   fullLen,
		payload:   payload,
	}, true
}

func verifyCarrierPhase1(rawHex string, commitments map[string]map[string]any) map[string]any {
	raw, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil {
		return map[string]any{"present": false, "verified": false}
	}
	ins, err := parseTxInputs(raw)
	if err != nil || len(ins) == 0 {
		return map[string]any{"present": false, "verified": false}
	}
	parts := make([]carrierPart, 0, len(ins))
	for i, in := range ins {
		if p, ok := parseCarrierPartFromScriptSig(in.scriptSig); ok {
			p.vin = i
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return map[string]any{"present": false, "verified": false}
	}
	byKey := map[string][]carrierPart{}
	for _, p := range parts {
		k := fmt.Sprintf("%s|%d|%d|%d", p.algo, p.partTotal, p.pkLen, p.fullLen)
		byKey[k] = append(byKey[k], p)
	}
	for _, group := range byKey {
		if len(group) == 0 {
			continue
		}
		partTotal := group[0].partTotal
		slots := make([][]byte, partTotal)
		for _, p := range group {
			if p.partIndex >= 0 && p.partIndex < partTotal {
				slots[p.partIndex] = p.payload
			}
		}
		okAll := true
		for i := 0; i < partTotal; i++ {
			if slots[i] == nil {
				okAll = false
				break
			}
		}
		if !okAll {
			continue
		}
		carrierVin := group[0].vin
		full := make([]byte, 0, group[0].fullLen+64)
		for _, chunk := range slots {
			full = append(full, chunk...)
		}
		if len(full) < group[0].fullLen {
			continue
		}
		full = full[:group[0].fullLen]
		if group[0].pkLen >= len(full) {
			continue
		}
		pk := full[:group[0].pkLen]
		sig := full[group[0].pkLen:]
		buf := make([]byte, 0, len(pk)+len(sig))
		buf = append(buf, pk...)
		buf = append(buf, sig...)
		sum := sha256.Sum256(buf)
		commitHex := strings.ToLower(hex.EncodeToString(sum[:]))
		if m, ok := commitments[commitHex]; ok {
			out := map[string]any{
				"present":             true,
				"verified":            true,
				"source":              "carrier_scriptsig",
				"algorithm":           group[0].algo,
				"carrier_tag":         group[0].tag,
				"part_total":          group[0].partTotal,
				"pk_len":              len(pk),
				"sig_len":             len(sig),
				"commitment32":        commitHex,
				"matched_txc_txid":    m["txid"],
				"matched_txc_tag":     m["tag"],
				"carrier_input_index": carrierVin,
				"pqc_pubkey_hex":      strings.ToLower(hex.EncodeToString(pk)),
				"pqc_signature_hex":   strings.ToLower(hex.EncodeToString(sig)),
			}
			if bh, ok := m["block_height"].(int64); ok {
				out["matched_txc_block_height"] = bh
			} else if bh, ok := m["block_height"].(float64); ok {
				out["matched_txc_block_height"] = int64(bh)
			} else if bh, ok := m["block_height"].(int); ok {
				out["matched_txc_block_height"] = int64(bh)
			}
			return out
		}
	}
	return map[string]any{
		"present":    true,
		"verified":   false,
		"source":     "carrier_scriptsig",
		"reason":     "carrier payload found but SHA256(pk‖sig) did not match any indexed Phase-1 OP_RETURN commitment (expand QE_PQ_CARRIER_COMMITMENT_LOOKBACK_BLOCKS if TX_C is older)",
		"part_count": len(parts),
	}
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

func parseCanonicalPQCommitment(script []byte) (algoTag string, commitHex string, ok bool) {
	// Canonical Phase-1: OP_RETURN (0x6a) + push-36 (0x24) + 4-byte tag + 32-byte commitment.
	if len(script) != 38 || script[0] != 0x6a || script[1] != 0x24 {
		return "", "", false
	}
	tag := string(script[2:6])
	switch tag {
	case "FLC1", "DIL2", "RCG4":
		return tag, strings.ToLower(hex.EncodeToString(script[6:38])), true
	default:
		return "", "", false
	}
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
	hasOpRet := false
	canonicalMatches := make([]string, 0, 2)
	for _, o := range outs {
		if tag := scriptAddressTag(o.script); tag != "" {
			outputTags = append(outputTags, tag)
		}
		if len(o.script) > 0 && o.script[0] == 0x6a {
			hasOpRet = true
		}
		if algoTag, commitHex, ok := parseCanonicalPQCommitment(o.script); ok {
			canonicalMatches = append(canonicalMatches, algoTag+":"+commitHex)
			continue
		}
		d, ok := extractOpReturnData(o.script)
		if !ok {
			continue
		}
		_ = d
	}
	if !hasOpRet {
		return false, "no OP_RETURN commitment detected", []string{"missing_op_return"}, outputTags
	}
	if len(canonicalMatches) == 0 {
		return false, "OP_RETURN present but not canonical Phase-1 PQ commitment (need 6a24 + FLC1/DIL2/RCG4 + 32-byte commitment)", []string{"op_return_non_canonical_pq"}, outputTags
	}
	ev := make([]string, 0, len(canonicalMatches))
	for _, m := range canonicalMatches {
		parts := strings.SplitN(m, ":", 2)
		if len(parts) != 2 {
			continue
		}
		ev = append(ev, "phase1_tag:"+parts[0], "commitment32:"+parts[1])
	}
	return true, "recognized canonical Phase-1 PQ commitment in OP_RETURN (tag + commitment32)", ev, outputTags
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

func (a *app) enrichCarrierVerification(ctx context.Context, pq map[string]any, txid string, blockHeight int64, rawHex string) map[string]any {
	if a == nil || a.cidx == nil || pq == nil || blockHeight < 0 {
		return pq
	}
	if _, ok := pq["decode"].(map[string]any); !ok {
		return pq
	}
	rawHex = strings.TrimSpace(rawHex)
	if rawHex == "" {
		return pq
	}
	commitments, err := a.cidx.commitmentMapForCarrierVerify(ctx, blockHeight)
	if err != nil || commitments == nil {
		commitments = make(map[string]map[string]any)
	}
	// Merge same-block txs (covers TX_C not yet marked quantum_state in DB).
	rows, err := a.cidx.blockTxRows(ctx, blockHeight)
	if err == nil {
		for _, row := range rows {
			rh := strings.TrimSpace(fmt.Sprint(row["raw_hex"]))
			rxid := strings.ToLower(strings.TrimSpace(fmt.Sprint(row["txid"])))
			if rh == "" || rxid == "" {
				continue
			}
			b, err := hex.DecodeString(rh)
			if err != nil {
				continue
			}
			outs, err := parseTxOutputs(b)
			if err != nil {
				continue
			}
			for _, o := range outs {
				if tag, commit, ok := parseCanonicalPQCommitment(o.script); ok {
					ch := strings.ToLower(strings.TrimSpace(commit))
					if ch == "" {
						continue
					}
					if _, exists := commitments[ch]; !exists {
						commitments[ch] = map[string]any{"txid": rxid, "tag": tag, "block_height": blockHeight}
					}
				}
			}
		}
	}
	if len(commitments) == 0 {
		return pq
	}
	carrier := verifyCarrierPhase1(rawHex, commitments)
	carrier["commitment_lookback_blocks"] = pqCarrierCommitmentLookbackBlocks()
	carrier["indexed_commitment_count"] = len(commitments)
	if cm, ok := carrier["commitment32"].(string); ok && cm != "" {
		if txid == strings.ToLower(strings.TrimSpace(fmt.Sprint(carrier["matched_txc_txid"]))) {
			carrier["self_commitment"] = true
		}
	}
	a.maybeEnrichFalconCryptoVerify(ctx, carrier, rawHex)
	pq["carrier_phase1"] = carrier
	return pq
}

func strictCommitmentFromPQ(pq map[string]any) (algoTag, commitment32 string, ok bool) {
	if pq == nil {
		return "", "", false
	}
	strict, _ := pq["strict"].(map[string]any)
	ev, _ := strict["evidence"].([]string)
	if len(ev) == 0 {
		rawEv, _ := strict["evidence"].([]any)
		for _, v := range rawEv {
			ev = append(ev, fmt.Sprint(v))
		}
	}
	for _, e := range ev {
		e = strings.TrimSpace(e)
		switch {
		case strings.HasPrefix(e, "phase1_tag:"):
			algoTag = strings.TrimSpace(strings.TrimPrefix(e, "phase1_tag:"))
		case strings.HasPrefix(e, "commitment32:"):
			commitment32 = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, "commitment32:")))
		}
	}
	if len(commitment32) != 64 || !isHex64String(commitment32) {
		return "", "", false
	}
	return strings.ToUpper(strings.TrimSpace(algoTag)), commitment32, true
}

func (a *app) enrichReverseCarrierVerification(ctx context.Context, pq map[string]any, txid string, blockHeight int64) map[string]any {
	if a == nil || a.cidx == nil || pq == nil || blockHeight < 0 {
		return pq
	}
	algo, commit, ok := strictCommitmentFromPQ(pq)
	if !ok {
		return pq
	}
	match, err := a.cidx.findCarrierRevealByCommitment(ctx, commit, txid, blockHeight)
	if err != nil || match == nil {
		return pq
	}
	match["commitment32"] = commit
	match["matched_txc_txid"] = strings.ToLower(strings.TrimSpace(txid))
	if algo != "" {
		match["algorithm"] = algo
	}
	pq["carrier_reverse_phase1"] = match
	return pq
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
	m := map[string]int{
		"pq_seen":      len(rows),
		"pq_confirmed": 0,
		"pq_invalid":   0,
		"confirmed":    0,
		"invalid":      0,
		"pq_valid":     0,
		"non_quantum":  0,
	}
	for _, t := range rows {
		if t.Confirmed {
			m["confirmed"]++
		}
		if !t.PQValid {
			m["invalid"]++
			m["pq_invalid"]++
			m["non_quantum"]++
		} else {
			m["pq_valid"]++
			m["pq_confirmed"]++
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
		"app_version":            qeAppVersion,
		"build_hash":             qeAppBuildHash,
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
		if pqAgg, ok := ex["pq"].(map[string]int64); ok {
			m["pq_seen"] = int(pqAgg["all"])
			m["pq_confirmed"] = int(pqAgg["quantum"])
			m["pq_invalid"] = int(pqAgg["invalid_quantum"])
		}
		if rb, err := a.cidx.recentBlocks(ctx, 15); err == nil {
			ex["recent_blocks"] = rb
		}
		// Keep dashboard status payload small: UI only needs tens of rows; full list loads via /api/public/core/recent-txs if needed.
		if rq, err := a.cidx.recentTransactions(ctx, 80, "quantum"); err == nil {
			net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
			enrichedRQ := make([]map[string]any, 0, len(rq))
			for _, row := range rq {
				txid := strings.ToLower(strings.TrimSpace(fmt.Sprint(row["txid"])))
				if len(txid) != 64 || !isHex64String(txid) {
					continue
				}
				rawHex, qState, pqReason, blkH, _, _, _, okRow, err := a.cidx.txRowByID(ctx, txid)
				if err != nil || !okRow {
					continue
				}
				row["quantum_state"] = qState
				row["pq_reason"] = pqReason
				pq := buildPQVerificationDetail(rawHex, net)
				pq = a.enrichDecodeWithPrevouts(ctx, pq)
				pq = a.enrichCarrierVerification(ctx, pq, txid, blkH, rawHex)
				pq = a.enrichReverseCarrierVerification(ctx, pq, txid, blkH)
				row["pq_verification"] = pq
				if car, ok := pq["carrier_phase1"].(map[string]any); ok {
					if fcv, ok := car["falcon_crypto_verify"].(map[string]any); ok {
						row["falcon_status"] = strings.ToLower(strings.TrimSpace(fmt.Sprint(fcv["status"])))
					}
					row["matched_txc_txid"] = strings.ToLower(strings.TrimSpace(fmt.Sprint(car["matched_txc_txid"])))
				}
				if rev, ok := pq["carrier_reverse_phase1"].(map[string]any); ok {
					row["matched_txr_txid"] = strings.ToLower(strings.TrimSpace(fmt.Sprint(rev["matched_txr_txid"])))
				}
				row["pq_carrier_role"] = pqCarrierTXRole(row)
				enrichedRQ = append(enrichedRQ, row)
			}
			ex["recent_quantum"] = enrichedRQ
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
			pq = a.enrichCarrierVerification(ctx, pq, q, blkH, rawHex)
			pq = a.enrichReverseCarrierVerification(ctx, pq, q, blkH)
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
		_, _, _, blkH, _, _, _, okRow, _ := a.cidx.txRowByID(ctx, q)
		if okRow {
			pq = a.enrichCarrierVerification(ctx, pq, q, blkH, rawHex)
			pq = a.enrichReverseCarrierVerification(ctx, pq, q, blkH)
		}
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
		limit := envIntBounded("QE_PUBLIC_SEARCH_LIMIT", 50, 1, 200)
		if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 && n <= 200 {
			limit = n
		}
		out, err := a.cidx.search(ctx, rawQ, limit)
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

// rowHasQuantumPQ is true when strict OP_RETURN Phase-1 markers match, or when
// carrier_phase1 verification matched a TX_C commitment (TX_R-style reveal).
func rowHasQuantumPQ(row map[string]any) bool {
	if v, ok := row["pq_valid"].(bool); ok && v {
		return true
	}
	pq, ok := row["pq_verification"].(map[string]any)
	if !ok {
		return false
	}
	car, ok := pq["carrier_phase1"].(map[string]any)
	if !ok {
		return false
	}
	if v, ok := car["verified"].(bool); ok && v {
		return true
	}
	return false
}

func (a *app) adminCoreInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	if a.core == nil || !a.core.enabled() {
		writeJSON(w, 503, map[string]string{"error": "core rpc is not configured"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))

	buildTxRow := func(txid, rawHex string) map[string]any {
		rawHex = strings.TrimSpace(rawHex)
		valid, reason, ev, _ := verifyPQStrict(rawHex)
		pq := buildPQVerificationDetail(rawHex, net)
		return map[string]any{
			"txid":            strings.ToLower(strings.TrimSpace(txid)),
			"raw_hex_len":     len(rawHex),
			"pq_valid":        valid,
			"pq_reason":       reason,
			"pq_evidence":     ev,
			"pq_verification": pq,
		}
	}

	inspectBlock := func(blockHash string, blk map[string]any, kind string) {
		txidsAny, _ := blk["tx"].([]any)
		rows := make([]map[string]any, 0, len(txidsAny))
		pqCount := 0
		blkHeight := anyInt64(blk["height"])
		for _, v := range txidsAny {
			txid := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
			if len(txid) != 64 || !isHex64String(txid) {
				continue
			}
			rawHex, err := a.core.getRawTransactionHex(ctx, txid, blockHash)
			if err != nil || strings.TrimSpace(rawHex) == "" {
				rows = append(rows, map[string]any{"txid": txid, "error": "raw tx unavailable from core rpc"})
				continue
			}
			row := buildTxRow(txid, rawHex)
			if a.cidx != nil && blkHeight >= 0 {
				if pq, ok := row["pq_verification"].(map[string]any); ok {
					pq = a.enrichDecodeWithPrevouts(ctx, pq)
					pq = a.enrichCarrierVerification(ctx, pq, txid, blkHeight, rawHex)
					pq = a.enrichReverseCarrierVerification(ctx, pq, txid, blkHeight)
					row["pq_verification"] = pq
				}
			}
			if rowHasQuantumPQ(row) {
				pqCount++
			}
			rows = append(rows, row)
		}
		writeJSON(w, 200, map[string]any{
			"kind":               kind,
			"query":              q,
			"block_hash":         strings.ToLower(strings.TrimSpace(blockHash)),
			"block":              blk,
			"tx_count":           len(rows),
			"quantum_tx_count":   pqCount,
			"has_quantum_tx":     pqCount > 0,
			"transactions_check": rows,
		})
	}

	if h, err := strconv.ParseInt(q, 10, 64); err == nil && h >= 0 {
		var blockHash string
		if err := a.core.call(ctx, "getblockhash", []any{h}, &blockHash); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		var blk map[string]any
		if err := a.core.call(ctx, "getblock", []any{blockHash, 1}, &blk); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		inspectBlock(strings.ToLower(strings.TrimSpace(blockHash)), blk, "block_height")
		return
	}

	qLower := strings.ToLower(q)
	if len(qLower) == 64 && isHex64String(qLower) {
		var blk map[string]any
		if err := a.core.call(ctx, "getblock", []any{qLower, 1}, &blk); err == nil && blk != nil {
			inspectBlock(qLower, blk, "block_hash")
			return
		}
		rawHex, err := a.core.getRawTransactionHex(ctx, qLower, "")
		if err != nil || strings.TrimSpace(rawHex) == "" {
			writeJSON(w, 404, map[string]string{"error": "not found as block hash or txid via core rpc"})
			return
		}
		blkHeight := int64(-1)
		if a.cidx != nil {
			if _, _, h, e := a.core.getRawTransactionVerboseWithHeight(ctx, qLower); e == nil {
				blkHeight = h
			}
		}
		row := buildTxRow(qLower, rawHex)
		if a.cidx != nil {
			if pq, ok := row["pq_verification"].(map[string]any); ok {
				pq = a.enrichDecodeWithPrevouts(ctx, pq)
				if blkHeight >= 0 {
					pq = a.enrichCarrierVerification(ctx, pq, qLower, blkHeight, rawHex)
				}
				row["pq_verification"] = pq
			}
		}
		isPQ := rowHasQuantumPQ(row)
		writeJSON(w, 200, map[string]any{
			"kind":             "txid",
			"query":            q,
			"has_quantum_tx":   isPQ,
			"quantum_tx_count": map[bool]int{true: 1, false: 0}[isPQ],
			"transaction":      row,
		})
		return
	}

	writeJSON(w, 400, map[string]string{"error": "q must be block height or 64-char txid/block hash"})
}

func (a *app) adminStatus(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	pg := map[string]any{"configured": false, "running": false}
	if a.cidx != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		pg = a.cidx.postgresStatus(ctx)
		cancel()
	}
	writeJSON(w, 200, map[string]any{
		"network":             a.cfg.Network,
		"chain_index_backend": a.chain.Kind(),
		"storage_dir":         a.storageDir,
		"diagnostics_api":     "GET /api/admin/<token>/diagnostics",
		"database_api":        "POST /db/optimize, GET /db/export, POST /db/import, POST /db/query",
		"core_rpc":            a.core.snapshot(),
		"core_indexer":        a.coreIndexerStatus(),
		"postgres":            pg,
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
	var body struct {
		Height int64 `json:"height"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if body.Height < 0 {
		writeJSON(w, 400, map[string]string{"error": "height must be >= 0"})
		return
	}
	if a.cidx == nil {
		writeJSON(w, 503, map[string]string{"error": "core indexer unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	res, err := a.cidx.setStartHeightIfNeeded(ctx, body.Height)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	a.mu.Lock()
	cfg := a.cfg
	cfg.Checkpoint.Height = int(body.Height)
	cfg.Checkpoint.Hash = ""
	cfg.Checkpoint.Timestamp = time.Now().UTC().Format(time.RFC3339)
	a.mu.Unlock()
	if err := saveJSON(a.cfgPath, cfg); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "core_start_height": body.Height, "result": res})
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
	publicMux.HandleFunc("/api/public/activity-buckets", a.withRateLimit(a.publicActivityBuckets))
	publicMux.HandleFunc("/api/public/core/search", a.withRateLimit(a.withPublicAccess(a.publicCoreSearch)))
	publicMux.HandleFunc("/api/public/core/summary", a.withRateLimit(a.withPublicAccess(a.publicCoreSummary)))
	publicMux.HandleFunc("/api/public/core/recent-txs", a.withRateLimit(a.withPublicAccess(a.publicCoreRecentTxs)))
	publicMux.HandleFunc("/api/public/core/recent-blocks", a.withRateLimit(a.withPublicAccess(a.publicCoreRecentBlocks)))
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
		case "/core-indexer/rewind":
			a.adminCoreIndexerRewind(w, r)
		case "/core/inspect":
			a.adminCoreInspect(w, r)
		case "/db/optimize":
			a.adminDBOptimize(w, r)
		case "/db/export":
			a.adminDBExport(w, r)
		case "/db/import":
			a.adminDBImport(w, r)
		case "/db/query":
			a.adminDBQuery(w, r)
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
	publicMux.HandleFunc("/chart.umd.min.js", func(w http.ResponseWriter, _ *http.Request) {
		b, err := staticFS.ReadFile("static/chart.umd.min.js")
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
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
