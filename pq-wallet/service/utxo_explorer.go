package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ExplorerUTXO is one spendable output for the wallet address (from explorer API).
type ExplorerUTXO struct {
	TxID         string
	Vout         uint32
	Value        int64 // koinu (smallest units)
	ScriptPubHex string // hex, optional; if empty caller derives from wallet pubkey
}

// fetchUTXOsFromExplorer loads unspent outputs for address using Blockchair-style dashboards URL.
func (s *Server) fetchUTXOsFromExplorer(ctx context.Context, address string) ([]ExplorerUTXO, error) {
	base := strings.TrimSpace(s.explorerAddr)
	if base == "" {
		return nil, fmt.Errorf("EXPLORER_ADDRESS_API is not set (needed for automatic send; use Blockchair dashboards address URL with {address})")
	}
	url := strings.ReplaceAll(strings.TrimRight(base, "/"), "{address}", address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("explorer JSON: %w", err)
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("explorer response missing data")
	}
	addrObj, _ := data[address].(map[string]any)
	if addrObj == nil {
		for _, v := range data {
			if m, ok := v.(map[string]any); ok {
				addrObj = m
				break
			}
		}
	}
	if addrObj == nil {
		return nil, fmt.Errorf("explorer response missing address entry")
	}
	rawList, _ := addrObj["utxo"].([]any)
	if rawList == nil {
		return nil, fmt.Errorf("no utxo[] in explorer response (Blockchair dashboards address includes utxo list)")
	}
	var out []ExplorerUTXO
	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		txid := strings.TrimSpace(getStr(m, "transaction_hash", "hash", "txid", "tx_id"))
		if txid == "" {
			continue
		}
		vout := getUint32(m, "index", "vout", "tx_index")
		val := int64(getFloat(m, "value"))
		if val <= 0 {
			continue
		}
		scr := strings.TrimSpace(getStr(m, "script_hex", "script"))
		out = append(out, ExplorerUTXO{TxID: txid, Vout: vout, Value: val, ScriptPubHex: scr})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no spendable UTXOs reported for this address")
	}
	return out, nil
}

func getStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatInt(int64(t), 10)
			case json.Number:
				return t.String()
			}
		}
	}
	return ""
}

func getUint32(m map[string]any, keys ...string) uint32 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return uint32(t)
			case string:
				n, _ := strconv.ParseUint(strings.TrimSpace(t), 10, 32)
				return uint32(n)
			case json.Number:
				n, _ := t.Int64()
				return uint32(n)
			}
		}
	}
	return 0
}

func getFloat(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return t
			case string:
				f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
				return f
			case json.Number:
				f, _ := t.Float64()
				return f
			}
		}
	}
	return 0
}
