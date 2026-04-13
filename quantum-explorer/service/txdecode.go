package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// DecodeTxJSON returns a JSON-serializable map for APIs and UI (educational fields included).
func DecodeTxJSON(rawHex string, network string) map[string]any {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		network = "mainnet"
	}
	h := strings.TrimSpace(rawHex)
	if h == "" {
		return map[string]any{"error": "empty hex", "learn": txLearnGeneral()}
	}
	raw, err := hex.DecodeString(h)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	p, err := parseBitcoinTx(raw)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	txid := p.txIDHex()
	wtxid := ""
	if p.segwit {
		wtxid = wtxIDHexRaw(raw)
	}
	var totalOut int64
	outs := make([]map[string]any, 0, len(p.outputs))
	for i, o := range p.outputs {
		kind, addr := dogeAddressFromScript(o.scriptPubKey, network)
		row := map[string]any{
			"n":            i,
			"value_sats":   o.value,
			"value_doge":   satsToDoge(o.value),
			"script_kind":  kind,
			"script_hex":   hex.EncodeToString(o.scriptPubKey),
			"script_len":   len(o.scriptPubKey),
			"address":      addr,
			"address_note": addressNote(kind, addr),
		}
		if tag := scriptAddressTag(o.scriptPubKey); tag != "" {
			row["pq_hint_tag"] = tag
		}
		if d, ok := extractOpReturnData(o.scriptPubKey); ok {
			row["kind_detail"] = "op_return"
			row["op_return_hex"] = hex.EncodeToString(d)
			row["op_return_preview"] = opReturnPreview(d)
		}
		totalOut += o.value
		outs = append(outs, row)
	}
	ins := make([]map[string]any, 0, len(p.inputs))
	for i, in := range p.inputs {
		prev := reverseCopy32(in.prevTxID)
		row := map[string]any{
			"n":              i,
			"prev_txid":      hex.EncodeToString(prev),
			"prev_vout":      in.prevVout,
			"sequence":       fmt.Sprintf("0x%08x", in.sequence),
			"script_sig_hex": hex.EncodeToString(in.scriptSig),
			"script_sig_len": len(in.scriptSig),
			"script_sig_asm": scriptSigAsm(in.scriptSig),
		}
		if isCoinbase(prev, in.prevVout) {
			row["coinbase"] = true
			row["learn"] = "Coinbase inputs create new coins; fee is implicit (block reward)."
		}
		ins = append(ins, row)
	}
	out := map[string]any{
		"txid":              txid,
		"segwit":            p.segwit,
		"version":           p.version,
		"locktime":          p.locktime,
		"locktime_note":     locktimeNote(p.locktime),
		"size_bytes":        len(raw),
		"base_size_bytes":   len(p.legacyBytesForTxID()),
		"weight":            p.calcWeight(len(raw)),
		"vsize":             p.calcVsize(len(raw)),
		"inputs":            ins,
		"outputs":           outs,
		"output_total_sats": totalOut,
		"output_total_doge": satsToDoge(totalOut),
		"fee_sats":          nil,
		"fee_note":          "Fee = sum(input values) − sum(outputs). Input values need the spent UTXOs (full node / indexer). Here you still see accurate output amounts and scripts.",
		"network":           network,
		"learn":             txLearnGeneral(),
		"decode_engine":     "native_go_bitcoin_style",
	}
	if p.segwit && len(p.witness) > 0 {
		wsum := make([]map[string]any, 0, len(p.witness))
		for i, st := range p.witness {
			items := make([]map[string]any, 0, len(st))
			for j, it := range st {
				hx := hex.EncodeToString(it.data)
				if len(hx) > 200 {
					hx = hx[:200] + "…"
				}
				items = append(items, map[string]any{"n": j, "hex": hx, "bytes": len(it.data)})
			}
			wsum = append(wsum, map[string]any{"input_index": i, "stack": items})
		}
		out["witness"] = wsum
	}
	if p.segwit && wtxid != "" {
		out["learn_segwit"] = "SegWit: txid hashes the legacy serialization (no witness). wtxid hashes the full transaction including witness."
		out["wtxid"] = wtxid
	}
	valid, reason, ev, tags := verifyPQStrict(h)
	out["pq_strict"] = map[string]any{"valid": valid, "reason": reason, "evidence": ev, "output_tags": tags}
	return out
}

func addressNote(kind, addr string) string {
	if addr == "" {
		switch kind {
		case "p2wpkh":
			return "Native SegWit v0 output; bech32 address not generated in this build (script shown)."
		default:
			return "Non-standard or complex script; inspect script hex."
		}
	}
	return "Base58Check Dogecoin address for this script type (mainnet/testnet from NETWORK)."
}

func opReturnPreview(d []byte) string {
	const max = 200
	if len(d) <= max {
		return printableASCII(d)
	}
	return printableASCII(d[:max]) + "…"
}

func printableASCII(d []byte) string {
	var sb strings.Builder
	for _, b := range d {
		if b >= 32 && b < 127 {
			sb.WriteByte(b)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

func scriptSigAsm(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) <= 128 {
		return fmt.Sprintf("%d byte(s) scriptsig", len(b))
	}
	return fmt.Sprintf("%d byte(s) scriptsig (large)", len(b))
}

func isCoinbase(prevTxID []byte, vout uint32) bool {
	if vout != 0xffffffff {
		return false
	}
	for _, x := range prevTxID {
		if x != 0 {
			return false
		}
	}
	return true
}

func locktimeNote(lt uint32) string {
	if lt == 0 {
		return "No locktime constraint."
	}
	if lt < 500000000 {
		return fmt.Sprintf("Block height lock: not valid before height %d (if used as height lock).", lt)
	}
	return fmt.Sprintf("Time lock UNIX %d (if used as time lock).", lt)
}

func txLearnGeneral() []string {
	return []string{
		"Dogecoin transactions move value via inputs (spend prior outputs) and outputs (create new UTXOs).",
		"Addresses encode scriptPubKey templates (P2PKH / P2SH). Raw scripts are authoritative on-chain.",
		"Mempool P2P gives full tx bytes when peers relay them; otherwise set QE_EXPLORER_TX_API for a fallback.",
	}
}

func reverseCopy32(b []byte) []byte {
	if len(b) != 32 {
		return b
	}
	o := make([]byte, 32)
	for i := 0; i < 32; i++ {
		o[i] = b[31-i]
	}
	return o
}

type txParsed struct {
	version  uint32
	locktime uint32
	segwit   bool
	inputs   []txInParsed
	outputs  []txOutParsed
	witness  [][]witnessItem
}

type txInParsed struct {
	prevTxID  []byte
	prevVout  uint32
	scriptSig []byte
	sequence  uint32
}

type txOutParsed struct {
	value        int64
	scriptPubKey []byte
}

type witnessItem struct {
	data []byte
}

func parseBitcoinTx(raw []byte) (*txParsed, error) {
	if len(raw) < 10 {
		return nil, errShort
	}
	off := 0
	version := binary.LittleEndian.Uint32(raw[off:])
	off += 4
	segwit := false
	if off+2 <= len(raw) && raw[off] == 0x00 && raw[off+1] == 0x01 {
		segwit = true
		off += 2
	}
	nin, err := readVarInt(raw, &off)
	if err != nil {
		return nil, err
	}
	inputs := make([]txInParsed, 0, nin)
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return nil, errShort
		}
		var in txInParsed
		in.prevTxID = append([]byte(nil), raw[off:off+32]...)
		off += 32
		in.prevVout = binary.LittleEndian.Uint32(raw[off:])
		off += 4
		sl, err := readVarInt(raw, &off)
		if err != nil || off+int(sl) > len(raw) {
			return nil, errShort
		}
		in.scriptSig = append([]byte(nil), raw[off:off+int(sl)]...)
		off += int(sl)
		if off+4 > len(raw) {
			return nil, errShort
		}
		in.sequence = binary.LittleEndian.Uint32(raw[off:])
		off += 4
		inputs = append(inputs, in)
	}
	nout, err := readVarInt(raw, &off)
	if err != nil {
		return nil, err
	}
	outputs := make([]txOutParsed, 0, nout)
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return nil, errShort
		}
		var o txOutParsed
		o.value = int64(binary.LittleEndian.Uint64(raw[off:]))
		off += 8
		sl, err := readVarInt(raw, &off)
		if err != nil || off+int(sl) > len(raw) {
			return nil, errShort
		}
		o.scriptPubKey = append([]byte(nil), raw[off:off+int(sl)]...)
		off += int(sl)
		outputs = append(outputs, o)
	}
	var witness [][]witnessItem
	if segwit {
		for i := 0; i < int(nin); i++ {
			nstack, err := readVarInt(raw, &off)
			if err != nil {
				return nil, err
			}
			st := make([]witnessItem, 0, nstack)
			for j := 0; j < int(nstack); j++ {
				ln, err := readVarInt(raw, &off)
				if err != nil || off+int(ln) > len(raw) {
					return nil, errShort
				}
				st = append(st, witnessItem{data: append([]byte(nil), raw[off:off+int(ln)]...)})
				off += int(ln)
			}
			witness = append(witness, st)
		}
	}
	if off+4 > len(raw) {
		return nil, errors.New("missing locktime")
	}
	locktime := binary.LittleEndian.Uint32(raw[off:])
	off += 4
	if off != len(raw) {
		return nil, fmt.Errorf("trailing bytes after locktime: got %d extra", len(raw)-off)
	}
	return &txParsed{
		version:  version,
		locktime: locktime,
		segwit:   segwit,
		inputs:   inputs,
		outputs:  outputs,
		witness:  witness,
	}, nil
}

func (p *txParsed) legacyBytesForTxID() []byte {
	var buf []byte
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, p.version)
	buf = append(buf, b...)
	buf = appendVarIntTx(buf, uint64(len(p.inputs)))
	for _, in := range p.inputs {
		buf = append(buf, in.prevTxID...)
		vo := make([]byte, 4)
		binary.LittleEndian.PutUint32(vo, in.prevVout)
		buf = append(buf, vo...)
		buf = appendVarIntTx(buf, uint64(len(in.scriptSig)))
		buf = append(buf, in.scriptSig...)
		seq := make([]byte, 4)
		binary.LittleEndian.PutUint32(seq, in.sequence)
		buf = append(buf, seq...)
	}
	buf = appendVarIntTx(buf, uint64(len(p.outputs)))
	for _, o := range p.outputs {
		v := make([]byte, 8)
		binary.LittleEndian.PutUint64(v, uint64(o.value))
		buf = append(buf, v...)
		buf = appendVarIntTx(buf, uint64(len(o.scriptPubKey)))
		buf = append(buf, o.scriptPubKey...)
	}
	lt := make([]byte, 4)
	binary.LittleEndian.PutUint32(lt, p.locktime)
	buf = append(buf, lt...)
	return buf
}

func (p *txParsed) txIDHex() string {
	b := p.legacyBytesForTxID()
	h := sha256.Sum256(b)
	h2 := sha256.Sum256(h[:])
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = h2[31-i]
	}
	return hex.EncodeToString(rev)
}

func wtxIDHexRaw(raw []byte) string {
	h := sha256.Sum256(raw)
	h2 := sha256.Sum256(h[:])
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = h2[31-i]
	}
	return hex.EncodeToString(rev)
}

func (p *txParsed) calcWeight(totalLen int) int {
	stripped := len(p.legacyBytesForTxID())
	return stripped*3 + totalLen
}

func (p *txParsed) calcVsize(totalLen int) int {
	return (p.calcWeight(totalLen) + 3) / 4
}

func appendVarIntTx(buf []byte, v uint64) []byte {
	switch {
	case v < 0xfd:
		return append(buf, byte(v))
	case v <= 0xffff:
		buf = append(buf, 0xfd)
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(v))
		return append(buf, b...)
	case v <= 0xffffffff:
		buf = append(buf, 0xfe)
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		return append(buf, b...)
	default:
		buf = append(buf, 0xff)
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, v)
		return append(buf, b...)
	}
}
