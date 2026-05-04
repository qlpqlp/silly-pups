package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
)

// Legacy Bitcoin/Dogecoin transaction serialization (BIP144 witness not used here).
// Unsigned txs use empty scriptSig; signing is done by libdogecoin `such` on the hex.

const (
	legacyTxVersion     int32  = 1
	legacyTxInSequence  uint32 = 0xffffffff
	legacyHashHexMaxLen        = 64
)

// txidWireBytes parses a display-order txid hex into the 32-byte order used in serialized outpoints.
func txidWireBytes(displayHex string) ([32]byte, error) {
	var dst [32]byte
	src := displayHex
	if len(src) > legacyHashHexMaxLen {
		return dst, fmt.Errorf("txid string too long")
	}
	var srcBytes []byte
	if len(src)%2 == 0 {
		srcBytes = []byte(src)
	} else {
		srcBytes = make([]byte, 1+len(src))
		srcBytes[0] = '0'
		copy(srcBytes[1:], src)
	}
	var reversedHash [32]byte
	decLen := hex.DecodedLen(len(srcBytes))
	if decLen > 32 {
		return dst, fmt.Errorf("invalid txid hex length")
	}
	_, err := hex.Decode(reversedHash[32-decLen:], srcBytes)
	if err != nil {
		return dst, err
	}
	const hashSize = 32
	for i, b := range reversedHash[:hashSize/2] {
		dst[i], dst[hashSize-1-i] = reversedHash[hashSize-1-i], b
	}
	return dst, nil
}

func writeVarInt(w io.Writer, val uint64) error {
	var buf [9]byte
	switch {
	case val < 0xfd:
		buf[0] = uint8(val)
		_, err := w.Write(buf[:1])
		return err
	case val <= math.MaxUint16:
		buf[0] = 0xfd
		binary.LittleEndian.PutUint16(buf[1:3], uint16(val))
		_, err := w.Write(buf[:3])
		return err
	case val <= math.MaxUint32:
		buf[0] = 0xfe
		binary.LittleEndian.PutUint32(buf[1:5], uint32(val))
		_, err := w.Write(buf[:5])
		return err
	default:
		buf[0] = 0xff
		if _, err := w.Write(buf[:1]); err != nil {
			return err
		}
		binary.LittleEndian.PutUint64(buf[:8], val)
		_, err := w.Write(buf[:8])
		return err
	}
}

type txOutWire struct {
	Value    int64
	PkScript []byte
}

// serializeUnsignedLegacyP2PKHTx builds unsigned raw tx bytes (empty scriptSig per input).
func serializeUnsignedLegacyP2PKHTx(
	selected []ExplorerUTXO,
	outputs []txOutWire,
	lockTime uint32,
) ([]byte, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, legacyTxVersion); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, uint64(len(selected))); err != nil {
		return nil, err
	}
	for _, u := range selected {
		h, err := txidWireBytes(u.TxID)
		if err != nil {
			return nil, fmt.Errorf("txid %s: %w", u.TxID, err)
		}
		if _, err := buf.Write(h[:]); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.LittleEndian, u.Vout); err != nil {
			return nil, err
		}
		if err := writeVarInt(&buf, 0); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.LittleEndian, legacyTxInSequence); err != nil {
			return nil, err
		}
	}
	if err := writeVarInt(&buf, uint64(len(outputs))); err != nil {
		return nil, err
	}
	for _, out := range outputs {
		if err := binary.Write(&buf, binary.LittleEndian, uint64(out.Value)); err != nil {
			return nil, err
		}
		if err := writeVarInt(&buf, uint64(len(out.PkScript))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(out.PkScript); err != nil {
			return nil, err
		}
	}
	if err := binary.Write(&buf, binary.LittleEndian, lockTime); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dogeLegacyTxidHex returns display-order txid (double-SHA256, byte-reversed) for a serialized transaction.
func dogeLegacyTxidHex(tx []byte) string {
	if len(tx) == 0 {
		return ""
	}
	h1 := sha256.Sum256(tx)
	h2 := sha256.Sum256(h1[:])
	out := make([]byte, 32)
	for i := range h2 {
		out[i] = h2[31-i]
	}
	return hex.EncodeToString(out)
}

// parseLegacyTxOutputs walks legacy tx inputs then returns output vector (value + pkScript).
func parseLegacyTxOutputs(raw []byte) ([]txOutWire, error) {
	off := 0
	if len(raw) < 6 {
		return nil, fmt.Errorf("tx too short")
	}
	off += 4 // version
	nin, err := readCompactSize(raw, &off)
	if err != nil {
		return nil, err
	}
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return nil, fmt.Errorf("truncated txin %d", i)
		}
		off += 36
		slen, err := readCompactSize(raw, &off)
		if err != nil {
			return nil, err
		}
		if int64(slen) > int64(len(raw)-off) {
			return nil, fmt.Errorf("truncated scriptsig %d", i)
		}
		off += int(slen)
		if off+4 > len(raw) {
			return nil, fmt.Errorf("truncated sequence %d", i)
		}
		off += 4
	}
	nout, err := readCompactSize(raw, &off)
	if err != nil {
		return nil, err
	}
	var outs []txOutWire
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return nil, fmt.Errorf("truncated output value %d", i)
		}
		val := int64(binary.LittleEndian.Uint64(raw[off : off+8]))
		off += 8
		pklen, err := readCompactSize(raw, &off)
		if err != nil {
			return nil, err
		}
		if int64(pklen) > int64(len(raw)-off) {
			return nil, fmt.Errorf("truncated pkscript %d", i)
		}
		pk := make([]byte, pklen)
		copy(pk, raw[off:off+int(pklen)])
		off += int(pklen)
		outs = append(outs, txOutWire{Value: val, PkScript: pk})
	}
	return outs, nil
}

func findOutputIndexByPkScript(outs []txOutWire, want []byte) int {
	for i := range outs {
		if len(outs[i].PkScript) == len(want) && bytes.Equal(outs[i].PkScript, want) {
			return i
		}
	}
	return -1
}

// findAllPkScriptMatches returns every vout index whose pkScript equals want (in ascending order).
func findAllPkScriptMatches(outs []txOutWire, want []byte) []int {
	var idx []int
	for i := range outs {
		if len(outs[i].PkScript) == len(want) && bytes.Equal(outs[i].PkScript, want) {
			idx = append(idx, i)
		}
	}
	return idx
}

// buildUnsignedCarrierRevealTx spends one prevout with empty scriptSig and pays outValue to outPkScript (single output).
func buildUnsignedCarrierRevealTx(prevTxID string, prevVout uint32, outValue int64, outPkScript []byte) ([]byte, error) {
	if outValue <= 0 {
		return nil, fmt.Errorf("reveal output value must be positive")
	}
	if len(outPkScript) == 0 {
		return nil, fmt.Errorf("empty output script")
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, legacyTxVersion); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, 1); err != nil {
		return nil, err
	}
	h, err := txidWireBytes(prevTxID)
	if err != nil {
		return nil, err
	}
	if _, err := buf.Write(h[:]); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, prevVout); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, 0); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, legacyTxInSequence); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, 1); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint64(outValue)); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, uint64(len(outPkScript))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(outPkScript); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(0)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildUnsignedCarrierRevealTxMulti spends prevTxID at each listed vout (same tx) with empty scriptSig
// and pays a single P2PKH output of outValue (fee is implicit: sum(prev values) − outValue).
func buildUnsignedCarrierRevealTxMulti(prevTxID string, vouts []uint32, outValue int64, outPkScript []byte) ([]byte, error) {
	if len(vouts) == 0 {
		return nil, fmt.Errorf("no carrier prevouts")
	}
	if outValue <= 0 {
		return nil, fmt.Errorf("reveal output value must be positive")
	}
	if len(outPkScript) == 0 {
		return nil, fmt.Errorf("empty output script")
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, legacyTxVersion); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, uint64(len(vouts))); err != nil {
		return nil, err
	}
	h, err := txidWireBytes(prevTxID)
	if err != nil {
		return nil, err
	}
	for _, vo := range vouts {
		if _, err := buf.Write(h[:]); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.LittleEndian, vo); err != nil {
			return nil, err
		}
		if err := writeVarInt(&buf, 0); err != nil {
			return nil, err
		}
		if err := binary.Write(&buf, binary.LittleEndian, legacyTxInSequence); err != nil {
			return nil, err
		}
	}
	if err := writeVarInt(&buf, 1); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint64(outValue)); err != nil {
		return nil, err
	}
	if err := writeVarInt(&buf, uint64(len(outPkScript))); err != nil {
		return nil, err
	}
	if _, err := buf.Write(outPkScript); err != nil {
		return nil, err
	}
	if err := binary.Write(&buf, binary.LittleEndian, uint32(0)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
