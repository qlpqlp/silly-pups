package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type coreRPCConfig struct {
	URL      string
	User     string
	Password string
	Timeout  time.Duration
}

type coreRPCClient struct {
	cfg coreRPCConfig
	hc  *http.Client
}

func newCoreRPCClientFromEnv() *coreRPCClient {
	rawURL := strings.TrimSpace(os.Getenv("QE_CORE_RPC_URL"))
	if rawURL == "" {
		host := strings.TrimSpace(env("QE_CORE_RPC_HOST", "127.0.0.1"))
		port := strings.TrimSpace(env("QE_CORE_RPC_PORT", "22555"))
		rawURL = "http://" + host + ":" + port
	}
	timeoutMS := 8000
	if n, err := strconv.Atoi(strings.TrimSpace(env("QE_CORE_RPC_TIMEOUT_MS", "8000"))); err == nil && n >= 1000 {
		timeoutMS = n
	}
	return &coreRPCClient{
		cfg: coreRPCConfig{
			URL:      rawURL,
			User:     strings.TrimSpace(os.Getenv("QE_CORE_RPC_USER")),
			Password: strings.TrimSpace(os.Getenv("QE_CORE_RPC_PASSWORD")),
			Timeout:  time.Duration(timeoutMS) * time.Millisecond,
		},
		hc: &http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond},
	}
}

func (c *coreRPCClient) enabled() bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.cfg.URL) != ""
}

func (c *coreRPCClient) maskedURL() string {
	if c == nil {
		return ""
	}
	return redactDatabaseURL(c.cfg.URL)
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  any             `json:"error"`
}

func (c *coreRPCClient) call(ctx context.Context, method string, params any, out any) error {
	if !c.enabled() {
		return errors.New("core rpc is not configured")
	}
	reqBody, err := json.Marshal(rpcReq{
		JSONRPC: "1.0",
		ID:      "quantum-explorer",
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if c.cfg.User != "" {
		r.SetBasicAuth(c.cfg.User, c.cfg.Password)
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("rpc http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var rr rpcResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rr); err != nil {
		return err
	}
	if rr.Error != nil {
		return fmt.Errorf("rpc %s error: %v", method, rr.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rr.Result, out)
}

// getRawTransactionHex returns raw tx hex via getrawtransaction (verbosity false).
// For confirmed txs, pass blockHash when the node has no -txindex so Core can locate the tx.
// getRawTransactionVerboseWithHeight calls getrawtransaction with verbosity true.
// When the transaction is confirmed, it loads the enclosing block to return height.
// Unconfirmed or non-indexed transactions return height -1 (caller may still use hex from getRawTransactionHex).
func (c *coreRPCClient) getRawTransactionVerboseWithHeight(ctx context.Context, txid string) (rawHex string, blockHash string, height int64, err error) {
	height = -1
	txid = strings.ToLower(strings.TrimSpace(txid))
	if len(txid) != 64 || !isHex64String(txid) {
		return "", "", -1, fmt.Errorf("invalid txid")
	}
	var obj map[string]any
	if err := c.call(ctx, "getrawtransaction", []any{txid, true}, &obj); err != nil {
		return "", "", -1, err
	}
	if h, ok := obj["hex"].(string); ok {
		rawHex = strings.TrimSpace(h)
	}
	if bh, ok := obj["blockhash"].(string); ok {
		bh = strings.ToLower(strings.TrimSpace(bh))
		if len(bh) == 64 && isHex64String(bh) {
			blockHash = bh
			var blk map[string]any
			if err := c.call(ctx, "getblock", []any{blockHash, 1}, &blk); err == nil && blk != nil {
				height = anyInt64(blk["height"])
			}
		}
	}
	if rawHex == "" {
		return "", blockHash, height, fmt.Errorf("getrawtransaction: missing hex field")
	}
	return rawHex, blockHash, height, nil
}

func (c *coreRPCClient) getRawTransactionHex(ctx context.Context, txid, blockHash string) (string, error) {
	if c == nil || !c.enabled() {
		return "", errors.New("core rpc is not configured")
	}
	txid = strings.ToLower(strings.TrimSpace(txid))
	blockHash = strings.ToLower(strings.TrimSpace(blockHash))
	var hexStr string
	if len(blockHash) == 64 && isHex64String(blockHash) {
		if err := c.call(ctx, "getrawtransaction", []any{txid, false, blockHash}, &hexStr); err == nil {
			if s := strings.TrimSpace(hexStr); s != "" {
				return s, nil
			}
		}
	}
	if err := c.call(ctx, "getrawtransaction", []any{txid, false}, &hexStr); err != nil {
		return "", err
	}
	return strings.TrimSpace(hexStr), nil
}

// getVoutScriptPubKeyHex returns the prevout's scriptPubKey hex from a verbose getrawtransaction.
func (c *coreRPCClient) getVoutScriptPubKeyHex(ctx context.Context, txid string, vout int64) (string, error) {
	if c == nil || !c.enabled() {
		return "", errors.New("core rpc is not configured")
	}
	txid = strings.ToLower(strings.TrimSpace(txid))
	if len(txid) != 64 || !isHex64String(txid) || vout < 0 {
		return "", fmt.Errorf("invalid txid or vout")
	}
	var obj map[string]any
	if err := c.call(ctx, "getrawtransaction", []any{txid, true}, &obj); err != nil {
		return "", err
	}
	vouts, ok := obj["vout"].([]any)
	if !ok {
		return "", fmt.Errorf("getrawtransaction: missing vout")
	}
	for _, vv := range vouts {
		vm, ok := vv.(map[string]any)
		if !ok {
			continue
		}
		var n int64 = -1
		switch v := vm["n"].(type) {
		case float64:
			n = int64(v)
		case int:
			n = int64(v)
		case int64:
			n = v
		case json.Number:
			if x, err := v.Int64(); err == nil {
				n = x
			}
		}
		if n != vout {
			continue
		}
		spk, ok := vm["scriptPubKey"].(map[string]any)
		if !ok {
			continue
		}
		if hexStr, ok := spk["hex"].(string); ok && strings.TrimSpace(hexStr) != "" {
			return strings.ToLower(strings.TrimSpace(hexStr)), nil
		}
	}
	return "", fmt.Errorf("vout %d scriptPubKey.hex not found", vout)
}

func (c *coreRPCClient) snapshot() map[string]any {
	out := map[string]any{
		"enabled":    c.enabled(),
		"rpc_url":    c.maskedURL(),
		"user_set":   c.cfg.User != "",
		"timeout_ms": c.cfg.Timeout.Milliseconds(),
		"zmq": map[string]string{
			"rawtx":     strings.TrimSpace(os.Getenv("QE_CORE_ZMQ_RAWTX")),
			"hashblock": strings.TrimSpace(os.Getenv("QE_CORE_ZMQ_HASHBLOCK")),
		},
	}
	if !c.enabled() {
		out["note"] = "Set QE_CORE_RPC_URL (or host/port/user/password env) to enable Dogecoin Core RPC integration."
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	var bc map[string]any
	var mem map[string]any
	var count int64
	if err := c.call(ctx, "getblockchaininfo", []any{}, &bc); err != nil {
		out["connected"] = false
		out["error"] = err.Error()
		return out
	}
	_ = c.call(ctx, "getmempoolinfo", []any{}, &mem)
	_ = c.call(ctx, "getblockcount", []any{}, &count)
	out["connected"] = true
	out["blockchaininfo"] = bc
	out["mempoolinfo"] = mem
	out["blockcount"] = count
	return out
}

func coreRPCConfigURLFromConfig() string {
	rawURL := strings.TrimSpace(os.Getenv("QE_CORE_RPC_URL"))
	if rawURL != "" {
		return rawURL
	}
	host := strings.TrimSpace(env("QE_CORE_RPC_HOST", ""))
	port := strings.TrimSpace(env("QE_CORE_RPC_PORT", ""))
	if host == "" || port == "" {
		return ""
	}
	u := url.URL{
		Scheme: "http",
		Host:   host + ":" + port,
	}
	return u.String()
}
