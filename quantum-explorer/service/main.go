package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inevitable360/silly-pups/quantum-explorer/service/mempooltracker"
)

//go:embed static/*
var staticFS embed.FS

type Checkpoint struct {
	Height    int    `json:"height"`
	Hash      string `json:"hash"`
	Timestamp string `json:"timestamp"`
}

type Config struct {
	HTTPPort      int        `json:"http_port"`
	Network       string     `json:"network"`
	AdminToken    string     `json:"admin_token"`
	ExplorerTxAPI string     `json:"explorer_tx_api"`
	Checkpoint    Checkpoint `json:"checkpoint"`
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

	eng             *mempooltracker.Engine
	engRunning      bool
	mempoolStartErr string
	spvCmd          *exec.Cmd
	spvRunning      bool
	spvStartErr     string
	txs             map[string]*PQTx
	blockIndex      map[string][]string
	addressIndex    map[string][]string
	lastSPVLogErr   string
}

type SPVHeader struct {
	Height int    `json:"height"`
	Hash   string `json:"hash"`
	Raw    string `json:"raw"`
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
			Height:    6150000,
			Hash:      "9e8f55907f17dc4870cdc9e6ea75236782190c13c10a8126baea3b1da94593c5",
			Timestamp: "2026-04-03T03:16:25Z",
		},
	}
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

// fetchRawTxHex must not be called while holding a.mu (write lock); it only reads eng and uses HTTP.
func (a *app) fetchRawTxHex(txid string, eng *mempooltracker.Engine, explorerAPI string) string {
	tpl := strings.TrimSpace(explorerAPI)
	id := strings.ToLower(strings.TrimSpace(txid))
	if id == "" {
		return ""
	}
	if eng != nil {
		if h := eng.RawTxHex(id); h != "" {
			return h
		}
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

func (a *app) reindexUnsafe() {
	a.addressIndex = map[string][]string{}
	a.blockIndex = map[string][]string{}
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

func (a *app) startMempool() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.engRunning {
		return nil
	}
	eng, err := mempooltracker.Start(mempooltracker.Options{
		StorageDir: filepath.Join(a.storageDir, "mempool"),
		Network:    strings.ToLower(a.cfg.Network),
	})
	if err != nil {
		a.mempoolStartErr = err.Error()
		return err
	}
	a.eng = eng
	a.engRunning = true
	a.mempoolStartErr = ""
	log.Printf("[quantum-explorer] mempool tracker started (network=%s)", strings.ToLower(a.cfg.Network))
	return nil
}

func (a *app) stopMempool() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.eng != nil {
		a.eng.Stop()
	}
	a.eng = nil
	a.engRunning = false
}

func (a *app) startSPV() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spvRunning {
		return nil
	}
	spv := strings.TrimSpace(env("LIBDOGECOIN_SPVNODE", "spvnode"))
	logPath := filepath.Join(a.storageDir, "spv.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// Flag order must match libdogecoin spvnode expectations: subcommand "scan" via -b is last
	// (same as pq-wallet: -f 0 -c -l [-a addr] -w -h -b scan). Putting -b scan before -w/-h breaks parsing → exit 1.
	args := []string{"-f", "0", "-c", "-l"}
	if w := strings.TrimSpace(env("QE_SPV_WATCH_ADDRESS", "")); w != "" {
		args = append(args, "-a", w)
	}
	args = append(args,
		"-w", filepath.Join(a.storageDir, "spv_wallet.db"),
		"-h", filepath.Join(a.storageDir, "headers.db"),
		"-b", "scan",
	)
	if strings.EqualFold(a.cfg.Network, "testnet") {
		args = append([]string{"-t"}, args...)
	}
	cmd := exec.Command(spv, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		a.spvStartErr = err.Error()
		return err
	}
	a.spvCmd = cmd
	a.spvRunning = true
	a.spvStartErr = ""
	log.Printf("[quantum-explorer] spvnode started pid=%d", cmd.Process.Pid)
	go func(c *exec.Cmd, lf *os.File) {
		err := c.Wait()
		_ = lf.Close()
		a.mu.Lock()
		if err != nil && a.spvStartErr == "" {
			a.spvStartErr = "spv process exited: " + err.Error()
		}
		a.spvRunning = false
		a.spvCmd = nil
		a.mu.Unlock()
	}(cmd, f)
	return nil
}

func (a *app) stopSPV() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spvCmd != nil && a.spvCmd.Process != nil {
		_ = a.spvCmd.Process.Kill()
	}
	a.spvRunning = false
	a.spvCmd = nil
}

func (a *app) refreshLoop() {
	reTxid := regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
	for {
		time.Sleep(3 * time.Second)
		a.mu.RLock()
		eng := a.eng
		running := a.engRunning
		tpl := strings.TrimSpace(a.cfg.ExplorerTxAPI)
		a.mu.RUnlock()
		if running && eng != nil {
			_, live, _, _ := eng.DashboardSnapshot()
			a.mu.Lock()
			for _, row := range live {
				txid, _ := row["txid"].(string)
				if txid == "" {
					continue
				}
				seen := time.Now().UTC().Format(time.RFC3339)
				tx := a.txs[txid]
				if tx == nil {
					tx = &PQTx{Txid: txid, FirstSeen: seen}
					a.txs[txid] = tx
				}
				tx.LastSeen = seen
				tx.Confirmed = false
				tracked, _ := row["tracked_match"].(bool)
				addr, _ := row["address"].(string)
				if strings.TrimSpace(addr) != "" {
					found := false
					for _, cur := range tx.Addresses {
						if cur == addr {
							found = true
							break
						}
					}
					if !found {
						tx.Addresses = append(tx.Addresses, addr)
					}
				}
				tx.PQValid, tx.PQScore = classifyPQ(txid, tracked, len(tx.Addresses))
				rawHex := a.fetchRawTxHex(txid, eng, tpl)
				if rawHex == "" {
					tx.PQValid = false
					tx.PQScore = 0
					tx.PQReason = "strict verifier: no raw tx (waiting for P2P tx message or set QE_EXPLORER_TX_API as fallback)"
					tx.PQEvidence = []string{"no_raw_tx"}
					tx.Verifier = "strict-v1"
				} else {
					ok, reason, ev, tags := verifyPQStrict(rawHex)
					tx.PQValid = ok
					if ok {
						tx.PQScore = 100
					} else {
						tx.PQScore = 0
					}
					tx.PQReason = reason
					tx.PQEvidence = ev
					tx.Verifier = "strict-v1"
					if len(tx.Addresses) == 0 && len(tags) > 0 {
						tx.Addresses = append(tx.Addresses, tags...)
					}
				}
			}
			a.reindexUnsafe()
			a.mu.Unlock()
			a.persistTxs()
		}

		// Mark confirmed txids when they appear in spv.log.
		logPath := filepath.Join(a.storageDir, "spv.log")
		f, err := os.Open(logPath)
		if err != nil {
			a.mu.Lock()
			a.lastSPVLogErr = err.Error()
			a.mu.Unlock()
			continue
		}
		sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
		found := map[string]struct{}{}
		for sc.Scan() {
			line := sc.Text()
			for _, m := range reTxid.FindAllString(line, -1) {
				found[strings.ToLower(m)] = struct{}{}
			}
		}
		_ = f.Close()
		a.mu.Lock()
		for txid, tx := range a.txs {
			if _, ok := found[strings.ToLower(txid)]; ok {
				tx.Confirmed = true
			}
		}
		a.reindexUnsafe()
		a.mu.Unlock()
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

func parseSPVHeadersFromLog(logText string, limit int) []SPVHeader {
	if limit <= 0 {
		limit = 10
	}
	// Common spvnode header formats we may see in logs:
	// 1) "<64hex>|<height>|<timestamp>|..."
	// 2) lines containing "height <n>" and optionally a 64-hex hash
	rePipe := regexp.MustCompile(`(?i)\b([0-9a-f]{64})\|([0-9]{1,12})\b`)
	reHeight := regexp.MustCompile(`(?i)\bheight[:=\s]+([0-9]{1,12})\b`)
	reHash := regexp.MustCompile(`(?i)\b([0-9a-f]{64})\b`)

	lines := strings.Split(logText, "\n")
	out := make([]SPVHeader, 0, limit)
	seen := make(map[int]struct{})
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if m := rePipe.FindStringSubmatch(line); len(m) == 3 {
			h, _ := strconv.Atoi(m[2])
			if h <= 0 {
				continue
			}
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, SPVHeader{Height: h, Hash: strings.ToLower(m[1]), Raw: line})
			continue
		}
		hm := reHeight.FindStringSubmatch(line)
		if len(hm) != 2 {
			continue
		}
		h, _ := strconv.Atoi(hm[1])
		if h <= 0 {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		hash := ""
		if m := reHash.FindStringSubmatch(line); len(m) == 2 {
			hash = strings.ToLower(m[1])
		}
		seen[h] = struct{}{}
		out = append(out, SPVHeader{Height: h, Hash: hash, Raw: line})
	}
	return out
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

func (a *app) publicStatus(w http.ResponseWriter, _ *http.Request) {
	rows := a.latest(100)
	m := map[string]int{"mempool_live": 0, "pq_seen": len(rows), "confirmed": 0, "invalid": 0}
	for _, t := range rows {
		if t.Confirmed {
			m["confirmed"]++
		}
		if !t.PQValid {
			m["invalid"]++
		}
	}
	m["mempool_live"] = len(rows) - m["confirmed"]
	logPath := filepath.Join(a.storageDir, "spv.log")
	logBytes, _ := os.ReadFile(logPath)
	headers := parseSPVHeadersFromLog(string(logBytes), 10)
	pqFoundOnSPV := false
	for _, t := range rows {
		if t.Confirmed {
			pqFoundOnSPV = true
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"metrics": m,
		"latest":  rows,
		"mempool": map[string]any{
			"running":     a.engRunning,
			"start_error": a.mempoolStartErr,
		},
		"spv": map[string]any{
			"running":           a.spvRunning,
			"start_error":       a.spvStartErr,
			"latest_headers":    headers,
			"pq_found_on_spv":   pqFoundOnSPV,
			"headers_available": len(headers),
		},
	})
}

func (a *app) publicSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("q")))
	if q == "" {
		writeJSON(w, 400, map[string]string{"error": "missing q"})
		return
	}
	rows := a.latest(1000)
	out := make([]*PQTx, 0)
	a.mu.RLock()
	txidsByAddr := a.addressIndex[q]
	a.mu.RUnlock()
	if len(txidsByAddr) > 0 {
		set := map[string]struct{}{}
		for _, id := range txidsByAddr {
			set[id] = struct{}{}
		}
		for _, t := range rows {
			if _, ok := set[t.Txid]; ok {
				out = append(out, t)
			}
		}
		writeJSON(w, 200, map[string]any{"query": q, "kind": "address", "results": out})
		return
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, q); matched {
		for _, t := range rows {
			if strings.EqualFold(t.Txid, q) {
				writeJSON(w, 200, map[string]any{"query": q, "kind": "txid", "results": []*PQTx{t}})
				return
			}
		}
	}
	for _, t := range rows {
		if containsIgnoreCase(t.Txid, q) {
			out = append(out, t)
		}
	}
	kind := "text"
	if _, err := strconv.Atoi(q); err == nil {
		kind = "block-height"
	}
	writeJSON(w, 200, map[string]any{"query": q, "kind": kind, "results": out})
}

func (a *app) adminStatus(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, 200, map[string]any{
		"mempool_running":     a.engRunning,
		"mempool_start_error": a.mempoolStartErr,
		"spv_running":         a.spvRunning,
		"spv_start_error":     a.spvStartErr,
		"checkpoint":          a.cfg.Checkpoint,
		"network":             a.cfg.Network,
		"last_spv_log_err":    a.lastSPVLogErr,
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

func main() {
	storage := env("QE_STORAGE_DIR", "/storage/quantum-explorer")
	must(os.MkdirAll(storage, 0o755))
	a := &app{
		cfgPath:      filepath.Join(storage, "quantum-explorer-config.json"),
		storePath:    filepath.Join(storage, "quantum-explorer-pqtx.json"),
		storageDir:   storage,
		txs:          map[string]*PQTx{},
		blockIndex:   map[string][]string{},
		addressIndex: map[string][]string{},
	}
	a.cfg = loadConfig(a.cfgPath)
	a.txs = loadTxs(a.storePath)
	a.reindexUnsafe()
	_ = saveJSON(a.cfgPath, a.cfg)
	if err := a.startMempool(); err != nil {
		log.Printf("[quantum-explorer] mempool autostart failed: %v", err)
	}
	if err := a.startSPV(); err != nil {
		log.Printf("[quantum-explorer] SPV autostart failed: %v (set LIBDOGECOIN_SPVNODE to your spvnode binary path)", err)
	}
	go a.refreshLoop()

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/api/public/status", a.publicStatus)
	publicMux.HandleFunc("/api/public/search", a.publicSearch)
	publicMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
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
		case "/checkpoint":
			a.adminCheckpoint(w, r)
		case "/mempool/start":
			if err := a.startMempool(); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true})
		case "/mempool/stop":
			a.stopMempool()
			writeJSON(w, 200, map[string]any{"ok": true})
		case "/spv/start":
			if err := a.startSPV(); err != nil && !errors.Is(err, os.ErrNotExist) {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "note": "SPV started from configured checkpoint metadata."})
		case "/spv/stop":
			a.stopSPV()
			writeJSON(w, 200, map[string]any{"ok": true})
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
	log.Fatal(http.ListenAndServe(publicAddr, publicMux))
}
