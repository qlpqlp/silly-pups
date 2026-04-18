package main

import (
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	dogeMainnetP2PKHVersion byte = 0x1e // 30
	dogeTestnetP2PKHVersion byte = 0x71 // 113
)

// dogeP2PKHScriptFromAddress decodes a Dogecoin P2PKH address to scriptPubKey bytes (legacy P2PKH).
func dogeP2PKHScriptFromAddress(addr string, testnet bool) ([]byte, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}
	payload, version, err := base58CheckDecode(addr)
	if err != nil {
		return nil, fmt.Errorf("decode address: %w", err)
	}
	if len(payload) != 20 {
		return nil, fmt.Errorf("expected 20-byte pubkey hash")
	}
	wantVer := dogeMainnetP2PKHVersion
	if testnet {
		wantVer = dogeTestnetP2PKHVersion
	}
	if version != wantVer {
		return nil, fmt.Errorf("unexpected address version %d (want %d)", version, wantVer)
	}
	out := make([]byte, 0, 25)
	out = append(out, 0x76, 0xa0, 0x14)
	out = append(out, payload...)
	out = append(out, 0x88, 0xac)
	return out, nil
}

func scriptBytesToHex(b []byte) string {
	return hex.EncodeToString(b)
}
