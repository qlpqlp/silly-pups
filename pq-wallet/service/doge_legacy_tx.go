package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
)

// Legacy Bitcoin/Dogecoin transaction serialization (BIP144 witness not used here).
// Unsigned txs use empty scriptSig; signing is done by libdogecoin `such` on the hex.

const (
	legacyTxVersion      int32  = 1
	legacyTxInSequence   uint32 = 0xffffffff
	legacyHashHexMaxLen         = 64
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
