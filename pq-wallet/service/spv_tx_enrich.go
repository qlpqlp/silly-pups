package main

import (
	"encoding/hex"
	"encoding/binary"
	"errors"
	"strings"
)

type rawTxWalletView struct {
	IncomingDOGE float64
	Address      string
	Matched      bool
}

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

func decodeSPVRawTxWalletView(rawHex string, walletByHash160 map[string]string) (rawTxWalletView, error) {
	var out rawTxWalletView
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
	// version
	if off+4 > len(raw) {
		return out, errors.New("truncated version")
	}
	off += 4
	// segwit marker+flag (defensive; doge uses legacy, but parser stays tolerant)
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
		off += 36 // prevout hash + index
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
	var incomingSats int64
	firstAddr := ""
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
		if h160, ok := p2pkhHash160FromScript(script); ok {
			if addr, ok := walletByHash160[hex.EncodeToString(h160)]; ok {
				out.Matched = true
				incomingSats += valueSats
				if firstAddr == "" {
					firstAddr = addr
				}
			}
		}
	}
	out.IncomingDOGE = float64(incomingSats) / 1e8
	out.Address = firstAddr
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
	changed := false
	cache := make(map[string]rawTxWalletView)
	for i := range st.Transactions {
		tx := &st.Transactions[i]
		raw := strings.TrimSpace(tx.RawHex)
		if raw == "" {
			continue
		}
		needs := tx.AmountDOGE == 0 || tx.Address == "" || strings.EqualFold(tx.Direction, "unknown") || tx.Direction == ""
		if !needs {
			continue
		}
		view, ok := cache[raw]
		if !ok {
			v, err := decodeSPVRawTxWalletView(raw, walletByHash160)
			if err != nil {
				continue
			}
			cache[raw] = v
			view = v
		}
		if !view.Matched {
			continue
		}
		if tx.AmountDOGE == 0 && view.IncomingDOGE > 0 {
			tx.AmountDOGE = view.IncomingDOGE
			changed = true
		}
		if tx.Address == "" && view.Address != "" {
			tx.Address = view.Address
			changed = true
		}
		if tx.Direction == "" || strings.EqualFold(tx.Direction, "unknown") {
			tx.Direction = "in"
			changed = true
		}
		if tx.Source == "" {
			tx.Source = "spv"
			changed = true
		}
	}
	return changed
}
