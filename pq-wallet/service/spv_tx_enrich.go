package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

func walletP2PKHHash160Map(wf *WalletFile) map[string]string {
	out := make(map[string]string)
	if wf == nil {
		return out
	}
	for _, a := range wf.AllDistinctP2PKHAddresses() {
		addr := strings.TrimSpace(a)
		if addr == "" {
			continue
		}
		payload, _, err := base58CheckDecode(addr)
		if err != nil || len(payload) != 20 {
			continue
		}
		out[hex.EncodeToString(payload)] = addr
	}
	return out
}

func dogeP2PKHAddrFromH160(h160 []byte, testnet bool) string {
	if len(h160) != 20 {
		return ""
	}
	ver := byte(0x1e)
	if testnet {
		ver = 0x71
	}
	return base58CheckEncode(ver, h160)
}

// rawTxFlow classifies legacy P2PKH outputs between this wallet and external counterparties.
type rawTxFlow struct {
	WalletSats   int64
	ExternalSats int64
	WalletAddr   string
	ExternalAddr string
}

func isOpReturnScript(script []byte) bool {
	return len(script) >= 1 && script[0] == 0x6a
}

func decodeSPVRawTxFlow(rawHex string, walletByHash160 map[string]string, testnet bool) (rawTxFlow, error) {
	var out rawTxFlow
	rawHex = strings.TrimSpace(strings.ToLower(rawHex))
	if rawHex == "" {
		return out, errors.New("empty raw hex")
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return out, err
	}
	if len(raw) < 10 {
		return out, errors.New("raw tx too short")
	}
	off := 0
	if off+4 > len(raw) {
		return out, errors.New("truncated version")
	}
	off += 4
	if off+2 <= len(raw) && raw[off] == 0x00 && raw[off+1] == 0x01 {
		off += 2
	}
	nin, err := readCompactSize(raw, &off)
	if err != nil {
		return out, err
	}
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return out, errors.New("truncated txin")
		}
		off += 36
		slen, err := readCompactSize(raw, &off)
		if err != nil {
			return out, err
		}
		if !consume(&off, slen, len(raw)) {
			return out, errors.New("truncated scriptSig")
		}
		if off+4 > len(raw) {
			return out, errors.New("truncated sequence")
		}
		off += 4
	}
	nout, err := readCompactSize(raw, &off)
	if err != nil {
		return out, err
	}
	var bestWalletVal, bestExtVal int64
	var extNonP2PKH int64
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return out, errors.New("truncated output value")
		}
		valueSats := int64(binary.LittleEndian.Uint64(raw[off : off+8]))
		off += 8
		slen, err := readCompactSize(raw, &off)
		if err != nil {
			return out, err
		}
		if !consume(&off, slen, len(raw)) {
			return out, errors.New("truncated scriptPubKey")
		}
		script := raw[off-int(slen) : off]
		if isOpReturnScript(script) {
			continue
		}
		h160, ok := p2pkhHash160FromScript(script)
		if !ok {
			// Non-standard scripts (P2SH, witness-ish payloads, PQ carrier outputs, etc.) still represent
			// value leaving the wallet when this tx spends wallet inputs. Treat them as external for
			// direction classification even if we cannot derive a human-readable address string.
			if valueSats > 0 {
				extNonP2PKH += valueSats
				if valueSats >= bestExtVal {
					bestExtVal = valueSats
				}
			}
			continue
		}
		hh := hex.EncodeToString(h160)
		if addr, ok := walletByHash160[hh]; ok {
			out.WalletSats += valueSats
			if valueSats >= bestWalletVal {
				bestWalletVal = valueSats
				out.WalletAddr = addr
			}
			continue
		}
		extAddr := dogeP2PKHAddrFromH160(h160, testnet)
		if extAddr == "" {
			continue
		}
		out.ExternalSats += valueSats
		if valueSats >= bestExtVal {
			bestExtVal = valueSats
			out.ExternalAddr = extAddr
		}
	}
	// If we saw external-looking value in non-P2PKH outputs, fold it into ExternalSats for spend detection.
	// Keep ExternalAddr if we already found a readable P2PKH counterparty; otherwise leave blank.
	if extNonP2PKH > 0 {
		out.ExternalSats += extNonP2PKH
	}
	return out, nil
}

func p2pkhHash160FromScript(script []byte) ([]byte, bool) {
	if len(script) == 25 &&
		script[0] == 0x76 &&
		script[1] == 0xa9 &&
		script[2] == 0x14 &&
		script[23] == 0x88 &&
		script[24] == 0xac {
		out := make([]byte, 20)
		copy(out, script[3:23])
		return out, true
	}
	return nil, false
}

func readCompactSize(raw []byte, off *int) (uint64, error) {
	if *off >= len(raw) {
		return 0, errors.New("compactsize eof")
	}
	p := raw[*off]
	*off = *off + 1
	switch p {
	case 0xfd:
		if *off+2 > len(raw) {
			return 0, errors.New("compactsize short u16")
		}
		v := uint64(binary.LittleEndian.Uint16(raw[*off : *off+2]))
		*off += 2
		return v, nil
	case 0xfe:
		if *off+4 > len(raw) {
			return 0, errors.New("compactsize short u32")
		}
		v := uint64(binary.LittleEndian.Uint32(raw[*off : *off+4]))
		*off += 4
		return v, nil
	case 0xff:
		if *off+8 > len(raw) {
			return 0, errors.New("compactsize short u64")
		}
		v := binary.LittleEndian.Uint64(raw[*off : *off+8])
		*off += 8
		return v, nil
	default:
		return uint64(p), nil
	}
}

func consume(off *int, n uint64, limit int) bool {
	if n > uint64(limit) {
		return false
	}
	if *off > limit-int(n) {
		return false
	}
	*off += int(n)
	return true
}

func (s *Server) enrichSPVTxFromRawHex(st *WalletState, wf *WalletFile) bool {
	if st == nil || wf == nil || len(st.Transactions) == 0 {
		return false
	}
	walletByHash160 := walletP2PKHHash160Map(wf)
	if len(walletByHash160) == 0 {
		return false
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	// If older state entries were created while `RawHex` parsing was failing, they may still be missing
	// `RawHex` even though the txid is present in `spvnode` logs. Backfill those txs once per call
	// so direction/amount doesn't get stuck at "unknown".
	needRawBackfill := false
	needByTxid := make(map[string]struct{})
	for i := range st.Transactions {
		tx := &st.Transactions[i]
		if strings.TrimSpace(tx.RawHex) != "" {
			continue
		}
		if !(strings.EqualFold(tx.Direction, "unknown") || strings.EqualFold(tx.Direction, "") || tx.Direction == "") && tx.Address != "" {
			// Not strictly needed: if direction isn't unknown and we already have an address, we skip.
			continue
		}
		id := normalizeTxid(tx.Txid)
		if id == "" {
			continue
		}
		needRawBackfill = true
		needByTxid[id] = struct{}{}
	}
	changed := false
	if needRawBackfill && len(needByTxid) > 0 {
		// Larger tail than the dashboard scan. This is still bounded and only happens when needed.
		if lb, err := readFileTail(s.spvLogPath(), 16<<20); err == nil && strings.TrimSpace(lb) != "" {
			byTxid := parseSPVRawTxHexByTxid(lb)
			for i := range st.Transactions {
				tx := &st.Transactions[i]
				if strings.TrimSpace(tx.RawHex) != "" {
					continue
				}
				id := normalizeTxid(tx.Txid)
				if id == "" {
					continue
				}
				if raw := strings.TrimSpace(byTxid[id]); raw != "" {
					tx.RawHex = raw
					changed = true
				}
			}
		}
	}
	cache := make(map[string]rawTxFlow)
	for i := range st.Transactions {
		tx := &st.Transactions[i]
		raw := strings.TrimSpace(tx.RawHex)
		if raw == "" {
			continue
		}
		fl, ok := cache[raw]
		if !ok {
			v, err := decodeSPVRawTxFlow(raw, walletByHash160, testnet)
			if err != nil {
				continue
			}
			cache[raw] = v
			fl = v
		}
		if fl.WalletSats == 0 && fl.ExternalSats == 0 {
			continue
		}
		isRecv := fl.ExternalSats == 0 && fl.WalletSats > 0
		isSend := fl.ExternalSats > 0
		dirBad := (isRecv && strings.EqualFold(tx.Direction, "out")) || (isSend && !isRecv && strings.EqualFold(tx.Direction, "in"))
		needs := tx.AmountDOGE == 0 || tx.Address == "" || strings.EqualFold(tx.Direction, "unknown") || tx.Direction == "" || dirBad
		if !needs {
			if tx.Source == "" {
				tx.Source = "spv"
				changed = true
			}
			continue
		}
		if isRecv {
			tx.Direction = "in"
			amt := round2(float64(fl.WalletSats) / 1e8)
			if tx.AmountDOGE == 0 || dirBad {
				tx.AmountDOGE = amt
				changed = true
			} else if strings.EqualFold(tx.Direction, "unknown") || tx.Direction == "" {
				tx.AmountDOGE = amt
				changed = true
			}
			if tx.Address == "" && fl.WalletAddr != "" {
				tx.Address = fl.WalletAddr
				changed = true
			}
			tx.FeeDOGE = 0
			changed = true
		} else if isSend {
			tx.Direction = "out"
			amt := round2(float64(fl.ExternalSats) / 1e8)
			if tx.AmountDOGE == 0 || dirBad || strings.EqualFold(tx.Direction, "unknown") || tx.Direction == "" {
				tx.AmountDOGE = amt
				changed = true
			}
			if tx.Address == "" && fl.ExternalAddr != "" {
				tx.Address = fl.ExternalAddr
				changed = true
			}
			changed = true
		}
		if tx.Source == "" {
			tx.Source = "spv"
			changed = true
		}
	}
	return changed
}
