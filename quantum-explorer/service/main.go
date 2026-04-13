package main

import (
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
	addressIndex    map[string][]string
	lastSPVLogErr   string

	// SPV-derived chain index: JSON file (default) or PostgreSQL when QE_POSTGRES_URL is set.
	chain chainBackend

	watchdogMu       sync.Mutex
	lastSPVRetry     time.Time
	lastMempoolRetry time.Time
}

type SPVHeader struct {
	Height    int    `json:"height"`
	Hash      string `json:"hash"`
	Raw       string `json:"raw"`
	Timestamp string `json:"timestamp,omitempty"` // RFC3339 or raw from log (e.g. ctime)
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
		// Matches tallest checkpoint baked into libdogecoin (rev a120e03 mainnet); spvnode cannot start above this until the library ships newer checkpoints.
		Checkpoint: Checkpoint{
			Height:    6093890,
			Hash:      "7ecb28519e0c144261e511fd8706f8b54a93620cac31c41b5bcb0135f0d86a2b",
			Timestamp: "2026-02-20T21:59:00Z",
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

// buildPQVerificationDetail parses raw tx hex for UI: full decode + PQ strict verifier (see decode map).
func buildPQVerificationDetail(rawHex string, network string) map[string]any {
	h := strings.TrimSpace(rawHex)
	out := map[string]any{
		"raw_tx_hex_available": false,
		"decode":               nil,
		"strict":               nil,
	}
	if h == "" {
		out["note"] = "No raw transaction hex (need P2P full tx in mempool cache or QE_EXPLORER_TX_API)."
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
	// Flag order: -b scan last. -p enables use_checkpoints in libdogecoin; -q + -l picks the latest *embedded*
	// checkpoint on a fresh headers DB (avoids syncing headers from genesis). Custom admin JSON checkpoint is
	// not passed to spvnode — only this binary's dogecoin_mainnet_checkpoint_array / testnet array exists upstream.
	args := []string{"-f", "0", "-c", "-l"}
	if strings.TrimSpace(env("QE_SPV_USE_CHECKPOINT", "1")) != "0" {
		args = append(args, "-p", "-q")
	}
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

const watchdogMinInterval = 25 * time.Second

// subsystemWatchdog periodically restarts mempool and SPV if autostart is enabled and they are not running
// (e.g. first launch failed, binary path appeared later, or the child process exited).
func (a *app) subsystemWatchdog() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for range tick.C {
		a.maybeRestartMempool()
		a.maybeRestartSPV()
	}
}

func (a *app) maybeRestartMempool() {
	a.mu.RLock()
	ok := a.engRunning
	a.mu.RUnlock()
	if ok {
		return
	}
	a.watchdogMu.Lock()
	if time.Since(a.lastMempoolRetry) < watchdogMinInterval {
		a.watchdogMu.Unlock()
		return
	}
	a.lastMempoolRetry = time.Now()
	a.watchdogMu.Unlock()
	if err := a.startMempool(); err != nil {
		log.Printf("[quantum-explorer] mempool watchdog: start failed: %v", err)
	} else {
		log.Printf("[quantum-explorer] mempool watchdog: tracker started")
	}
}

func (a *app) maybeRestartSPV() {
	if strings.TrimSpace(os.Getenv("QE_SPV_AUTO_START")) == "0" {
		return
	}
	a.mu.RLock()
	ok := a.spvRunning
	a.mu.RUnlock()
	if ok {
		return
	}
	a.watchdogMu.Lock()
	if time.Since(a.lastSPVRetry) < watchdogMinInterval {
		a.watchdogMu.Unlock()
		return
	}
	a.lastSPVRetry = time.Now()
	a.watchdogMu.Unlock()
	if err := a.startSPV(); err != nil {
		log.Printf("[quantum-explorer] SPV watchdog: start failed: %v", err)
	} else {
		log.Printf("[quantum-explorer] SPV watchdog: spvnode started")
	}
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

		// SPV log: ingest header chain index + mark confirmed txids.
		logPath := filepath.Join(a.storageDir, "spv.log")
		logBytes, err := os.ReadFile(logPath)
		logStr := string(logBytes)
		if len(logStr) > 1<<22 {
			logStr = logStr[len(logStr)-(1<<22):]
		}
		a.mu.Lock()
		if err != nil {
			a.lastSPVLogErr = err.Error()
			a.mu.Unlock()
			continue
		}
		a.lastSPVLogErr = ""
		a.mu.Unlock()

		chg, ingErr := a.ingestSPVLogChain(logStr)
		if ingErr != nil {
			log.Printf("[quantum-explorer] chain ingest: %v", ingErr)
		} else if chg {
			if err := a.chain.Persist(); err != nil {
				log.Printf("[quantum-explorer] chain persist: %v", err)
			}
		}

		a.mu.Lock()
		found := map[string]struct{}{}
		for _, m := range reTxid.FindAllString(logStr, -1) {
			found[strings.ToLower(m)] = struct{}{}
		}
		for txid, tx := range a.txs {
			if _, ok := found[strings.ToLower(txid)]; ok {
				tx.Confirmed = true
			}
		}
		a.reindexUnsafe()
		a.mu.Unlock()
		a.persistTxs()
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

func (a *app) publicStatus(w http.ResponseWriter, _ *http.Request) {
	rows := a.latest(100)
	m := map[string]int{"mempool_live": 0, "pq_seen": len(rows), "confirmed": 0, "invalid": 0, "pq_valid": 0}
	for _, t := range rows {
		if t.Confirmed {
			m["confirmed"]++
		}
		if !t.PQValid {
			m["invalid"]++
		} else {
			m["pq_valid"]++
		}
	}
	m["mempool_live"] = len(rows) - m["confirmed"]
	logPath := filepath.Join(a.storageDir, "spv.log")
	logBytes, _ := os.ReadFile(logPath)
	logStr := string(logBytes)
	snap := parseSPVLogSnapshot(logStr)
	headerRows := make([]map[string]any, 0, 16)
	for _, h := range a.recentChainHeaders(15) {
		headerRows = append(headerRows, map[string]any{
			"height": h.Height, "hash": h.Hash, "timestamp": h.Timestamp,
			"raw": h.RawSource, "source": "chain_index",
		})
	}
	if len(headerRows) == 0 {
		for _, row := range snap.HeaderRows {
			if len(headerRows) >= 15 {
				break
			}
			headerRows = append(headerRows, map[string]any{
				"height": row.Height, "hash": row.Hash, "timestamp": row.Timestamp,
				"raw": row.Raw, "source": "spv_log_fallback",
			})
		}
	}
	if len(headerRows) > 15 {
		headerRows = headerRows[:15]
	}
	headers := headerRows
	pqFoundOnSPV := false
	pqConfirmedValid := 0
	pqConfirmedInvalid := 0
	for _, t := range rows {
		if t.Confirmed {
			pqFoundOnSPV = true
			if t.PQValid {
				pqConfirmedValid++
			} else {
				pqConfirmedInvalid++
			}
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

	writeJSON(w, 200, map[string]any{
		"metrics":                m,
		"latest":                 rows,
		"latest_pq_transactions": latestPQ,
		"mempool": map[string]any{
			"running":     a.engRunning,
			"start_error": a.mempoolStartErr,
		},
		"spv": map[string]any{
			"running":              a.spvRunning,
			"start_error":          a.spvStartErr,
			"latest_headers":       headers,
			"pq_found_on_spv":      pqFoundOnSPV,
			"headers_available":    len(headers),
			"tip_height":           snap.TipHeight,
			"tip_hash":             snap.TipHash,
			"tip_time_from_log":    snap.LastTipTime,
			"peer_count_hint":      snap.PeerCount,
			"mempool_tx_hint":      snap.MempoolTxHint,
			"indexed_tx_count":     len(rows),
			"pq_confirmed_valid":   pqConfirmedValid,
			"pq_confirmed_invalid": pqConfirmedInvalid,
		},
		"chain_index": map[string]any{
			"summary":         chainSum,
			"recent_headers":  chainHdrs,
			"block_detail_qs": "GET /api/public/block?height=<n> or &hash=<64hex>",
		},
	})
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
		writeJSON(w, 404, map[string]string{"error": "block header not in SPV chain index yet"})
		return
	}
	txids, err := a.chain.ListTxidsForHeight(b.Height)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	a.mu.RLock()
	eng := a.eng
	tpl := strings.TrimSpace(a.cfg.ExplorerTxAPI)
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	a.mu.RUnlock()
	txSummaries := make([]map[string]any, 0, len(txids))
	for _, id := range txids {
		id = strings.ToLower(id)
		rawHex := a.fetchRawTxHex(id, eng, tpl)
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
			row["note"] = "Heuristic link from SPV log; not in PQ index. Decode works if raw tx was relayed."
		}
		if rawHex == "" {
			row["decode_note"] = "No raw tx bytes yet — keep mempool running or set QE_EXPLORER_TX_API."
		}
		txSummaries = append(txSummaries, row)
	}

	writeJSON(w, 200, map[string]any{
		"block":                   b,
		"associated_transactions": txSummaries,
		"notes": []string{
			"Block headers are indexed in the chain database (from SPV ingestion), not read from the full spv.log for UI.",
			"Transactions listed here are heuristic 64-hex links from SPV log lines; full decoding requires raw tx (P2P or API).",
			"A future full indexer can store every relayed tx and UTXO set for exact fees.",
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
	eng := a.eng
	tpl := strings.TrimSpace(a.cfg.ExplorerTxAPI)
	net := strings.ToLower(strings.TrimSpace(a.cfg.Network))
	a.mu.RUnlock()
	rawHex := a.fetchRawTxHex(q, eng, tpl)
	cp := *tx
	writeJSON(w, 200, map[string]any{
		"tx":              &cp,
		"pq_verification": buildPQVerificationDetail(rawHex, net),
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
		a.mu.RLock()
		for _, id := range txidsByAddr {
			id = strings.ToLower(id)
			if t := a.txs[id]; t != nil {
				cp := *t
				out = append(out, &cp)
			}
		}
		a.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"query": q, "kind": "address", "results": out})
		return
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, q); matched {
		blk, err := a.chain.GetByHash(q)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if blk != nil {
			writeJSON(w, 200, map[string]any{
				"query":       q,
				"kind":        "block_hash",
				"block":       blk,
				"block_query": "/api/public/block?hash=" + q,
			})
			return
		}
		for _, t := range rows {
			if strings.EqualFold(t.Txid, q) {
				writeJSON(w, 200, map[string]any{
					"query":     q,
					"kind":      "txid",
					"results":   []*PQTx{t},
					"tx_detail": "/api/public/tx?txid=" + q,
				})
				return
			}
		}
		writeJSON(w, 200, map[string]any{
			"query": q,
			"kind":  "unknown_hex64",
			"note":  "Not a known block hash in the SPV chain index or a PQ-indexed txid",
		})
		return
	}
	if h, err := strconv.Atoi(q); err == nil && q == strconv.Itoa(h) && h > 0 {
		blk, err := a.chain.GetByHeight(h)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if blk != nil {
			writeJSON(w, 200, map[string]any{
				"query":       q,
				"kind":        "block_height",
				"block":       blk,
				"block_query": "/api/public/block?height=" + q,
			})
			return
		}
	}
	for _, t := range rows {
		if containsIgnoreCase(t.Txid, q) {
			out = append(out, t)
		}
	}
	kind := "text"
	if _, err := strconv.Atoi(q); err == nil {
		kind = "block-height-no-index"
	}
	writeJSON(w, 200, map[string]any{"query": q, "kind": kind, "results": out})
}

func (a *app) adminStatus(w http.ResponseWriter, _ *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	note := "spvnode ignores admin JSON; it uses libdogecoin's embedded checkpoints (-p/-q). This pup's default reference height (~6093890) matches an older libdogecoin rev — your spvnode binary may embed a higher checkpoint (e.g. ~6.16M). If the header tip seems stuck: peers may be slow or absent; the library may need a newer release for fresher checkpoints; or headers.db may need reset (backup, delete headers.db, restart SPV) so -q picks the latest embedded anchor. This is an upstream libdogecoin/network limit, not Quantum Explorer."
	writeJSON(w, 200, map[string]any{
		"mempool_running":     a.engRunning,
		"mempool_start_error": a.mempoolStartErr,
		"spv_running":         a.spvRunning,
		"spv_start_error":     a.spvStartErr,
		"checkpoint":          a.cfg.Checkpoint,
		"network":             a.cfg.Network,
		"last_spv_log_err":    a.lastSPVLogErr,
		"spv_checkpoint_help": note,
		"spv_use_checkpoint":  strings.TrimSpace(env("QE_SPV_USE_CHECKPOINT", "1")) != "0",
		"chain_index_backend": a.chain.Kind(),
		"storage_dir":         a.storageDir,
		"spv_log_path":        filepath.Join(a.storageDir, "spv.log"),
		"libdogecoin_spvnode": strings.TrimSpace(env("LIBDOGECOIN_SPVNODE", "spvnode")),
		"diagnostics_api":     "GET /api/admin/<token>/diagnostics",
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
	}
	a.cfg = loadConfig(a.cfgPath)
	a.txs = loadTxs(a.storePath)
	log.Printf("[quantum-explorer] chain index backend=%s", cb.Kind())
	a.reindexUnsafe()
	_ = saveJSON(a.cfgPath, a.cfg)
	if err := a.startMempool(); err != nil {
		log.Printf("[quantum-explorer] mempool autostart failed: %v", err)
	}
	if strings.TrimSpace(os.Getenv("QE_SPV_AUTO_START")) == "0" {
		log.Printf("[quantum-explorer] SPV autostart skipped (QE_SPV_AUTO_START=0)")
	} else if err := a.startSPV(); err != nil {
		log.Printf("[quantum-explorer] SPV autostart failed: %v (set LIBDOGECOIN_SPVNODE to your spvnode binary path)", err)
	} else {
		log.Printf("[quantum-explorer] SPV autostart invoked (QE_SPV_USE_CHECKPOINT=%q)", env("QE_SPV_USE_CHECKPOINT", "1"))
	}
	go a.subsystemWatchdog()
	go a.refreshLoop()

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/api/public/status", a.publicStatus)
	publicMux.HandleFunc("/api/public/block", a.publicBlock)
	publicMux.HandleFunc("/api/public/tx", a.publicTxDetail)
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
		case "/diagnostics":
			a.adminDiagnostics(w, r)
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
		case "/mempool/restart":
			a.adminRestartMempool(w)
		case "/spv/start":
			if err := a.startSPV(); err != nil && !errors.Is(err, os.ErrNotExist) {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true, "note": "SPV started; headers use libdogecoin embedded checkpoints when QE_SPV_USE_CHECKPOINT=1. Delete headers.db if a prior run synced from genesis."})
		case "/spv/stop":
			a.stopSPV()
			writeJSON(w, 200, map[string]any{"ok": true})
		case "/spv/restart":
			a.adminRestartSPV(w)
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
