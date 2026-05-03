package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// prevoutWalletMeta is one chain output learned from raw tx hex seen locally (SPV / log / broadcast — P2P path only).
type prevoutWalletMeta struct {
	Value      int64
	WalletRecv bool // pays to our P2PKH (UTXO we can spend)
}

func prevoutIndexKey(txid string, vout uint32) string {
	return normalizeTxid(txid) + ":" + strconv.FormatUint(uint64(vout), 10)
}

// txPrevOut is one legacy tx input's outpoint (display txid + vout).
type txPrevOut struct {
	Txid string
	Vout uint32
}

var errTruncLegacyTxIn = errors.New("truncated legacy tx input")

// parseLegacyTxPrevouts lists each input's previous txid (display byte order) and vout.
func parseLegacyTxPrevouts(raw []byte) ([]txPrevOut, error) {
	if len(raw) < 10 {
		return nil, nil
	}
	off := 0
	off += 4 // version
	if off+2 <= len(raw) && raw[off] == 0x00 && raw[off+1] == 0x01 {
		off += 2
	}
	nin, err := readCompactSize(raw, &off)
	if err != nil {
		return nil, err
	}
	out := make([]txPrevOut, 0, nin)
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return nil, errTruncLegacyTxIn
		}
		prev := raw[off : off+32]
		off += 32
		vout := binary.LittleEndian.Uint32(raw[off : off+4])
		off += 4
		slen, err := readCompactSize(raw, &off)
		if err != nil {
			return nil, err
		}
		if !consume(&off, slen, len(raw)) {
			return nil, errTruncLegacyTxIn
		}
		if off+4 > len(raw) {
			return nil, errTruncLegacyTxIn
		}
		off += 4 // sequence
		rev := make([]byte, 32)
		for j := 0; j < 32; j++ {
			rev[j] = prev[31-j]
		}
		txid := normalizeTxid(hex.EncodeToString(rev))
		out = append(out, txPrevOut{Txid: txid, Vout: vout})
	}
	return out, nil
}

// buildPrevoutWalletIndex indexes every output of each raw tx (txid from double-SHA256), so input prevouts can be
// valued like bitcoinj's connected graph — data comes only from txs already observed (same as full SPV wallet).
func buildPrevoutWalletIndex(rawHexes []string, walletByHash160 map[string]string) map[string]prevoutWalletMeta {
	idx := make(map[string]prevoutWalletMeta)
	for _, hx := range rawHexes {
		hx = strings.TrimSpace(strings.ToLower(hx))
		if hx == "" {
			continue
		}
		raw, err := hex.DecodeString(hx)
		if err != nil || len(raw) < 10 {
			continue
		}
		txid := normalizeTxid(dogeLegacyTxidHex(raw))
		if txid == "" {
			continue
		}
		outs, err := parseLegacyTxOutputs(raw)
		if err != nil || len(outs) == 0 {
			continue
		}
		for vout, o := range outs {
			key := prevoutIndexKey(txid, uint32(vout))
			meta := prevoutWalletMeta{Value: o.Value}
			if h160, ok := p2pkhHash160FromScript(o.PkScript); ok {
				if _, mine := walletByHash160[hex.EncodeToString(h160)]; mine {
					meta.WalletRecv = true
				}
			}
			idx[key] = meta
		}
	}
	return idx
}

// walletNetFromPrevoutIndex computes bitcoinj Transaction.getValue(Wallet)-style net:
// sum(wallet outputs this tx) − sum(prevout values for inputs that spent wallet-owned UTXOs we know about.
//
// When a prevout is not in the index, it is usually someone else's coin (typical incoming payment);
// those inputs do not debit our wallet. Missing prevouts on sends would mis-estimate — callers should
// treat complete=false when ExternalSats>0 and any input was unknown (handled below).
func walletNetFromPrevoutIndex(rawHex string, walletByHash160 map[string]string, testnet bool, idx map[string]prevoutWalletMeta) (net int64, walletIn int64, walletOut int64, complete bool) {
	rawHex = strings.TrimSpace(strings.ToLower(rawHex))
	if rawHex == "" {
		return 0, 0, 0, false
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return 0, 0, 0, false
	}
	ins, err := parseLegacyTxPrevouts(raw)
	if err != nil {
		return 0, 0, 0, false
	}
	fl, err := decodeSPVRawTxFlow(rawHex, walletByHash160, testnet)
	if err != nil {
		return 0, 0, 0, false
	}
	walletOut = fl.WalletSats
	missingPrevout := false
	for _, in := range ins {
		if in.Txid == "" {
			continue
		}
		key := prevoutIndexKey(in.Txid, in.Vout)
		meta, ok := idx[key]
		if !ok {
			missingPrevout = true
			continue
		}
		if meta.WalletRecv {
			walletIn += meta.Value
		}
	}
	net = walletOut - walletIn
	if missingPrevout {
		// Spend to external: need full prevout graph to match bitcoinj — fall back to output-side decode.
		if fl.ExternalSats > 0 {
			return 0, 0, 0, false
		}
		// Incoming (only wallet outputs); foreign inputs are not our debits — net ≈ credits to us.
		if walletOut > 0 {
			net = walletOut
			walletIn = 0
			return net, walletIn, walletOut, true
		}
		return 0, 0, 0, false
	}
	// All prevouts were indexed, but the implied debit can still be bogus: the tail-only prevout
	// graph sometimes links unrelated txs or mis-tags funding outputs, while output-side decode
	// shows no external payees (ExternalSats==0) and only modest wallet credits — the usual
	// explorer "+0.01" receive pattern. SoChain-style nets must not flip those into large OUT rows.
	if net < 0 && fl.ExternalSats == 0 && fl.WalletSats > 0 && walletIn > fl.WalletSats*5 {
		net = fl.WalletSats
		walletIn = 0
	}
	return net, walletIn, walletOut, true
}

func collectUniqueRawHexes(st *WalletState, logTail string) []string {
	seen := make(map[string]struct{})
	var list []string
	if st != nil {
		for i := range st.Transactions {
			h := strings.TrimSpace(st.Transactions[i].RawHex)
			if h == "" {
				continue
			}
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			list = append(list, h)
		}
	}
	if strings.TrimSpace(logTail) != "" {
		for txid, raw := range parseSPVRawTxHexByTxid(logTail) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if !signedRawHexMatchesTxid(raw, txid) {
				continue
			}
			if _, ok := seen[raw]; ok {
				continue
			}
			seen[raw] = struct{}{}
			list = append(list, raw)
		}
	}
	return list
}

// signedRawHexMatchesTxid reports whether raw hex decodes to a legacy tx whose txid (wire byte order) equals wantTxid.
func signedRawHexMatchesTxid(rawHex, wantTxid string) bool {
	wantTxid = normalizeTxid(wantTxid)
	rawHex = strings.TrimSpace(strings.ToLower(rawHex))
	if wantTxid == "" || rawHex == "" {
		return false
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil || len(raw) < 10 {
		return false
	}
	got := normalizeTxid(dogeLegacyTxidHex(raw))
	return got != "" && got == wantTxid
}
