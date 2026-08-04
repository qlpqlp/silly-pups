package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRPC       = "https://rpc.testnet.dogeos.com/"
	defaultExplorer  = "https://blockscout.testnet.dogeos.com"
	defaultBridge    = "https://portal.testnet.dogeos.com/bridge"
	defaultFaucet    = "https://faucet.testnet.dogeos.com"
	defaultPortal    = "https://portal.testnet.dogeos.com"
	defaultDocs      = "https://docs.dogeos.com/en/developers"
	defaultFeesDocs  = "https://docs.dogeos.com/en/developers/transaction-fees-on-dogeos"
	l1GasPriceOracle = "0x5300000000000000000000000000000000000002"
	expectedChainID  = int64(6281971)
)

type statusResponse struct {
	Ok        bool      `json:"ok"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
	Network   networkInfo `json:"network"`
	Chain     chainInfo   `json:"chain"`
	Fees      feeInfo     `json:"fees"`
	L1Oracle  l1OracleInfo `json:"l1_oracle"`
	Stats     explorerStats `json:"stats"`
	Recent    recentInfo  `json:"recent"`
	Links     linksInfo   `json:"links"`
	Bridge    bridgeInfo  `json:"bridge"`
}

type networkInfo struct {
	Name             string `json:"name"`
	ChainID          int64  `json:"chain_id"`
	ChainIDHex       string `json:"chain_id_hex"`
	Currency         string `json:"currency"`
	RPCURL           string `json:"rpc_url"`
	RPCReachable     bool   `json:"rpc_reachable"`
	ExplorerReachable bool  `json:"explorer_reachable"`
	ClientVersion    string `json:"client_version"`
	PeerCount        int64  `json:"peer_count"`
	Syncing          bool   `json:"syncing"`
}

type chainInfo struct {
	BlockNumber       int64   `json:"block_number"`
	AvgBlockTimeMs    float64 `json:"avg_block_time_ms"`
	AvgBlockTimeSec   float64 `json:"avg_block_time_sec"`
	BlocksPerMinute   float64 `json:"blocks_per_minute"`
}

type feeInfo struct {
	GasPriceWei           string  `json:"gas_price_wei"`
	GasPriceGwei          float64 `json:"gas_price_gwei"`
	BaseFeeWei            string  `json:"base_fee_wei,omitempty"`
	BaseFeeGwei           float64 `json:"base_fee_gwei,omitempty"`
	ExplorerSlowGwei      float64 `json:"explorer_slow_gwei"`
	ExplorerAverageGwei   float64 `json:"explorer_average_gwei"`
	ExplorerFastGwei      float64 `json:"explorer_fast_gwei"`
	SimpleTransferDOGE    string  `json:"simple_transfer_doge"`
	SimpleTransferWei     string  `json:"simple_transfer_wei"`
	TokenApproveDOGE      string  `json:"token_approve_doge"`
	NetworkUtilizationPct float64 `json:"network_utilization_pct"`
	GasUsedToday          string  `json:"gas_used_today"`
	Note                  string  `json:"note"`
}

type l1OracleInfo struct {
	Address      string `json:"address"`
	Reachable    bool   `json:"reachable"`
	Overhead     string `json:"overhead"`
	Scalar       string `json:"scalar"`
	L1BaseFeeWei string `json:"l1_base_fee_wei"`
	L1BaseFeeGwei float64 `json:"l1_base_fee_gwei"`
	SampleL1FeeWei string `json:"sample_l1_fee_wei_100_zero_bytes"`
	SampleL1FeeDOGE string `json:"sample_l1_fee_doge_100_zero_bytes"`
	Note         string `json:"note"`
}

type explorerStats struct {
	TotalBlocks       string  `json:"total_blocks"`
	TotalTransactions string  `json:"total_transactions"`
	TotalAddresses    string  `json:"total_addresses"`
	TransactionsToday string  `json:"transactions_today"`
	TVL               *string `json:"tvl"`
	MarketCap         string  `json:"market_cap"`
}

type recentInfo struct {
	LatestBlocks []blockBrief `json:"latest_blocks"`
	LatestTxs    []txBrief    `json:"latest_txs"`
}

type blockBrief struct {
	Number    int64  `json:"number"`
	Hash      string `json:"hash"`
	TxCount   int    `json:"tx_count"`
	Timestamp string `json:"timestamp"`
}

type txBrief struct {
	Hash      string `json:"hash"`
	Method    string `json:"method"`
	Status    string `json:"status"`
	FeeWei    string `json:"fee_wei"`
	Timestamp string `json:"timestamp"`
	Block     int64  `json:"block_number"`
}

type linksInfo struct {
	RPC      string `json:"rpc"`
	Explorer string `json:"explorer"`
	Bridge   string `json:"bridge"`
	Faucet   string `json:"faucet"`
	Portal   string `json:"portal"`
	Docs     string `json:"docs"`
	FeesDocs string `json:"fees_docs"`
}

type bridgeInfo struct {
	PortalURL           string `json:"portal_url"`
	SupportsDeposit     bool   `json:"supports_deposit"`
	SupportsWithdraw    bool   `json:"supports_withdraw"`
	RelayHint           string `json:"relay_hint"`
	DepositSummary      string `json:"deposit_summary"`
	WithdrawSummary     string `json:"withdraw_summary"`
	LiveMetricsAvailable bool  `json:"live_metrics_available"`
	Note                string `json:"note"`
}

type cache struct {
	mu   sync.RWMutex
	at   time.Time
	data statusResponse
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	bind := env("DOGEOS_BIND", "")
	if bind == "" {
		bind = env("DBX_PUP_IP", "0.0.0.0")
	}
	port := env("DOGEOS_PORT", "8091")
	addr := bind + ":" + port

	cfg := collectorConfig{
		RPC:      env("DOGEOS_RPC_URL", defaultRPC),
		Explorer: strings.TrimRight(env("DOGEOS_EXPLORER_URL", defaultExplorer), "/"),
		Bridge:   env("DOGEOS_BRIDGE_URL", defaultBridge),
		Faucet:   env("DOGEOS_FAUCET_URL", defaultFaucet),
		Portal:   env("DOGEOS_PORTAL_URL", defaultPortal),
		Docs:     env("DOGEOS_DOCS_URL", defaultDocs),
		FeesDocs: env("DOGEOS_FEES_DOCS_URL", defaultFeesDocs),
	}

	var c cache
	go func() {
		for {
			s := collect(cfg)
			c.mu.Lock()
			c.data = s
			c.at = time.Now().UTC()
			c.mu.Unlock()
			time.Sleep(3 * time.Second)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		out := c.data
		at := c.at
		c.mu.RUnlock()
		if at.IsZero() {
			out = collect(cfg)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "dogeos-portal"})
	})
	mux.Handle("/", staticHandler())

	log.Printf("DogeOS portal listening on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

type collectorConfig struct {
	RPC, Explorer, Bridge, Faucet, Portal, Docs, FeesDocs string
}

func collect(cfg collectorConfig) statusResponse {
	now := time.Now().UTC()
	out := statusResponse{
		FetchedAt: now,
		Network: networkInfo{
			Name:     "DogeOS Chikyu Testnet",
			Currency: "DOGE",
			RPCURL:   cfg.RPC,
			ChainID:  expectedChainID,
		},
		Links: linksInfo{
			RPC: cfg.RPC, Explorer: cfg.Explorer, Bridge: cfg.Bridge,
			Faucet: cfg.Faucet, Portal: cfg.Portal, Docs: cfg.Docs, FeesDocs: cfg.FeesDocs,
		},
		Bridge: bridgeInfo{
			PortalURL:            cfg.Bridge,
			SupportsDeposit:      true,
			SupportsWithdraw:     true,
			RelayHint:            "Bridge relay can take up to 4 hours on testnet.",
			DepositSummary:       "Deposit: send Dogecoin testnet DOGE with OP_RETURN data from the portal bridge UI.",
			WithdrawSummary:      "Withdraw: submit on DogeOS via the portal; DOGE is relayed to your Dogecoin testnet address.",
			LiveMetricsAvailable: false,
			Note:                 "No public bridge metrics API is published yet. Use the portal for live deposit and withdraw status.",
		},
		Fees: feeInfo{
			Note: "Total fee = execution fee (gas_price * gas_used) + data/finality fee from L1GasPriceOracle. Wallet UIs may only show execution gas.",
		},
		L1Oracle: l1OracleInfo{
			Address: l1GasPriceOracle,
			Note:    "Predeploy L1GasPriceOracle used for data and finality fee estimates (docs.dogeos.com).",
		},
	}

	client := &http.Client{Timeout: 12 * time.Second}
	var errs []string

	chainHex, err := rpcCall(client, cfg.RPC, "eth_chainId", nil)
	if err != nil {
		errs = append(errs, "rpc: "+err.Error())
	} else {
		out.Network.RPCReachable = true
		out.Network.ChainIDHex = chainHex
		if id, ok := parseHexUint64(chainHex); ok {
			out.Network.ChainID = int64(id)
		}
	}

	if bn, err := rpcCall(client, cfg.RPC, "eth_blockNumber", nil); err == nil {
		if n, ok := parseHexUint64(bn); ok {
			out.Chain.BlockNumber = int64(n)
		}
	} else {
		errs = append(errs, "blockNumber: "+err.Error())
	}

	if gp, err := rpcCall(client, cfg.RPC, "eth_gasPrice", nil); err == nil {
		out.Fees.GasPriceWei = gp
		if wei, ok := parseHexBig(gp); ok {
			out.Fees.GasPriceGwei = weiToGwei(wei)
			transfer := new(big.Int).Mul(wei, big.NewInt(21000))
			approve := new(big.Int).Mul(wei, big.NewInt(50000))
			out.Fees.SimpleTransferWei = transfer.String()
			out.Fees.SimpleTransferDOGE = formatDOGE(transfer)
			out.Fees.TokenApproveDOGE = formatDOGE(approve)
		}
	}

	if ver, err := rpcCall(client, cfg.RPC, "web3_clientVersion", nil); err == nil {
		out.Network.ClientVersion = ver
	}
	if peers, err := rpcCall(client, cfg.RPC, "net_peerCount", nil); err == nil {
		if n, ok := parseHexUint64(peers); ok {
			out.Network.PeerCount = int64(n)
		}
	}
	if syncRaw, err := rpcRaw(client, cfg.RPC, "eth_syncing", nil); err == nil {
		out.Network.Syncing = syncRaw != "false" && syncRaw != `false`
	}

	if feeHist, err := rpcRawMap(client, cfg.RPC, "eth_feeHistory", []any{"0x5", "latest", []int{50}}); err == nil {
		if arr, ok := feeHist["baseFeePerGas"].([]any); ok && len(arr) > 0 {
			if s, ok := arr[len(arr)-1].(string); ok {
				out.Fees.BaseFeeWei = s
				if wei, ok := parseHexBig(s); ok {
					out.Fees.BaseFeeGwei = weiToGwei(wei)
				}
			}
		}
	}

	collectOracle(client, cfg.RPC, &out)
	collectExplorer(client, cfg.Explorer, &out, &errs)

	out.Ok = out.Network.RPCReachable || out.Network.ExplorerReachable
	if len(errs) > 0 && !out.Ok {
		out.Error = strings.Join(errs, "; ")
	} else if len(errs) > 0 {
		out.Error = strings.Join(errs, "; ")
	}
	return out
}

func collectOracle(client *http.Client, rpc string, out *statusResponse) {
	overhead, err1 := ethCall(client, rpc, l1GasPriceOracle, "0x0c18c162")
	scalar, err2 := ethCall(client, rpc, l1GasPriceOracle, "0xf45e65d8")
	l1base, err3 := ethCall(client, rpc, l1GasPriceOracle, "0x519b4bd3")
	if err1 != nil && err2 != nil && err3 != nil {
		return
	}
	out.L1Oracle.Reachable = true
	out.L1Oracle.Overhead = hexIntString(overhead)
	out.L1Oracle.Scalar = hexIntString(scalar)
	out.L1Oracle.L1BaseFeeWei = hexIntString(l1base)
	if wei, ok := parseHexBig(l1base); ok {
		out.L1Oracle.L1BaseFeeGwei = weiToGwei(wei)
	}
	// getL1Fee(bytes) for 100 zero bytes
	payload := "0x49948e0e" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000064" +
		strings.Repeat("00", 100)
	if fee, err := ethCall(client, rpc, l1GasPriceOracle, payload); err == nil {
		out.L1Oracle.SampleL1FeeWei = hexIntString(fee)
		if wei, ok := parseHexBig(fee); ok {
			out.L1Oracle.SampleL1FeeDOGE = formatDOGE(wei)
		}
	}
}

func collectExplorer(client *http.Client, base string, out *statusResponse, errs *[]string) {
	var stats map[string]any
	if err := getJSON(client, base+"/api/v2/stats", &stats); err != nil {
		*errs = append(*errs, "explorer stats: "+err.Error())
		return
	}
	out.Network.ExplorerReachable = true
	out.Stats.TotalBlocks = asString(stats["total_blocks"])
	out.Stats.TotalTransactions = asString(stats["total_transactions"])
	out.Stats.TotalAddresses = asString(stats["total_addresses"])
	out.Stats.TransactionsToday = asString(stats["transactions_today"])
	out.Stats.MarketCap = asString(stats["market_cap"])
	if v, ok := stats["tvl"].(string); ok {
		out.Stats.TVL = &v
	}
	if avg, ok := asFloat(stats["average_block_time"]); ok {
		out.Chain.AvgBlockTimeMs = avg
		out.Chain.AvgBlockTimeSec = avg / 1000.0
		if avg > 0 {
			out.Chain.BlocksPerMinute = 60000.0 / avg
		}
	}
	if util, ok := asFloat(stats["network_utilization_percentage"]); ok {
		out.Fees.NetworkUtilizationPct = util
	}
	out.Fees.GasUsedToday = asString(stats["gas_used_today"])
	if gp, ok := stats["gas_prices"].(map[string]any); ok {
		if v, ok := asFloat(gp["slow"]); ok {
			out.Fees.ExplorerSlowGwei = v
		}
		if v, ok := asFloat(gp["average"]); ok {
			out.Fees.ExplorerAverageGwei = v
		}
		if v, ok := asFloat(gp["fast"]); ok {
			out.Fees.ExplorerFastGwei = v
		}
	}

	var blocks []map[string]any
	if err := getJSON(client, base+"/api/v2/main-page/blocks", &blocks); err == nil {
		for i, b := range blocks {
			if i >= 5 {
				break
			}
			brief := blockBrief{
				Hash:      asString(b["hash"]),
				Timestamp: asString(b["timestamp"]),
			}
			if n, ok := asInt64(b["height"]); ok {
				brief.Number = n
			} else if n, ok := asInt64(b["number"]); ok {
				brief.Number = n
			}
			if txc, ok := b["transactions_count"].(float64); ok {
				brief.TxCount = int(txc)
			} else if txc, ok := b["transaction_count"].(float64); ok {
				brief.TxCount = int(txc)
			} else if txc, ok := b["tx_count"].(float64); ok {
				brief.TxCount = int(txc)
			}
			out.Recent.LatestBlocks = append(out.Recent.LatestBlocks, brief)
		}
	}

	var txs []map[string]any
	if err := getJSON(client, base+"/api/v2/main-page/transactions", &txs); err == nil {
		for i, t := range txs {
			if i >= 6 {
				break
			}
			brief := txBrief{
				Hash:      asString(t["hash"]),
				Method:    asString(t["method"]),
				Status:    asString(t["status"]),
				Timestamp: asString(t["timestamp"]),
			}
			if brief.Method == "" {
				brief.Method = "transfer"
			}
			if fee, ok := t["fee"].(map[string]any); ok {
				brief.FeeWei = asString(fee["value"])
			}
			if n, ok := asInt64(t["block_number"]); ok {
				brief.Block = n
			}
			out.Recent.LatestTxs = append(out.Recent.LatestTxs, brief)
		}
	}
}

func rpcCall(client *http.Client, url, method string, params []any) (string, error) {
	raw, err := rpcRaw(client, url, method, params)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return strings.Trim(raw, `"`), nil
	}
	return s, nil
}

func rpcRaw(client *http.Client, url, method string, params []any) (string, error) {
	if params == nil {
		params = []any{}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	return string(out.Result), nil
}

func rpcRawMap(client *http.Client, url, method string, params []any) (map[string]any, error) {
	raw, err := rpcRaw(client, url, method, params)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func ethCall(client *http.Client, rpc, to, data string) (string, error) {
	return rpcCall(client, rpc, "eth_call", []any{
		map[string]string{"to": to, "data": data},
		"latest",
	})
}

func getJSON(client *http.Client, url string, dest any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(b, dest)
}

func parseHexUint64(s string) (uint64, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 64)
	return v, err == nil
}

func parseHexBig(s string) (*big.Int, bool) {
	s = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "0x")
	if s == "" {
		return nil, false
	}
	n := new(big.Int)
	if _, ok := n.SetString(s, 16); !ok {
		return nil, false
	}
	return n, true
}

func hexIntString(hex string) string {
	n, ok := parseHexBig(hex)
	if !ok {
		return "0"
	}
	return n.String()
}

func weiToGwei(wei *big.Int) float64 {
	if wei == nil {
		return 0
	}
	f := new(big.Float).SetInt(wei)
	gwei := new(big.Float).Quo(f, big.NewFloat(1e9))
	out, _ := gwei.Float64()
	return out
}

func formatDOGE(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	f := new(big.Float).SetInt(wei)
	doge := new(big.Float).Quo(f, big.NewFloat(1e18))
	return doge.Text('f', 12)
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}
