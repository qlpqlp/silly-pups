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
