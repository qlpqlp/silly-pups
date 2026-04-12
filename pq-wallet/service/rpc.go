package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

func (s *Server) rpcCall(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	url := strings.TrimSpace(env("DOGE_RPC_URL", ""))
	if url == "" {
		return nil, fmt.Errorf("DOGE_RPC_URL not set")
	}
	user := env("DOGE_RPC_USER", "")
	pass := env("DOGE_RPC_PASS", "")
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		req.Header.Set("Authorization", "Basic "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("rpc json: %w body=%s", err, truncateStr(string(raw), 400))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc error: %v", out.Error)
	}
	return out.Result, nil
}

func (s *Server) rpcSendToAddress(ctx context.Context, address string, amount float64) (string, error) {
	res, err := s.rpcCall(ctx, "sendtoaddress", []any{address, amount})
	if err != nil {
		return "", err
	}
	var txid string
	if err := json.Unmarshal(res, &txid); err != nil {
		return "", err
	}
	return txid, nil
}

func (s *Server) rpcGetReceivedByAddress(ctx context.Context, address string) (float64, error) {
	res, err := s.rpcCall(ctx, "getreceivedbyaddress", []any{strings.TrimSpace(address)})
	if err != nil {
		return 0, err
	}
	var f float64
	if err := json.Unmarshal(res, &f); err != nil {
		return 0, err
	}
	return f, nil
}

func (s *Server) rpcGetTransaction(ctx context.Context, txid string) (map[string]any, error) {
	res, err := s.rpcCall(ctx, "gettransaction", []any{txid})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(res, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Server) handleRPCSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}
	var body struct {
		ToAddress string  `json:"to_address"`
		Amount    float64 `json:"amount_doge"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.ToAddress) == "" || body.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to_address and positive amount_doge required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	txid, err := s.rpcSendToAddress(ctx, strings.TrimSpace(body.ToAddress), body.Amount)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "txid": txid, "via": "dogecoin_core_rpc"})
}

func (s *Server) handleRPCGetTx(w http.ResponseWriter, r *http.Request) {
	txid := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/rpc/tx/"))
	if txid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing txid"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	m, err := s.rpcGetTransaction(ctx, txid)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tx": m})
}
