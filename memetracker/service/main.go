package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MAGIC              = 0xC0C0C0C0
	COMMAND_LEN        = 12
	MSG_WITNESS_FLAG   = 1 << 30
	MSG_TX             = 1
	NODE_NETWORK       = 1 << 0
	NODE_WITNESS       = 1 << 3
	GETDATA_BATCH      = 48
	MAX_TX_FETCH_INV   = 200
	MEMPOOL_RESYNC_SEC = 90
	SESSION_SEC        = 300
)

var mainnetP2PKHVersion = byte(0x1E)
var testnetP2PKHVersion = byte(0x71)

// Same seed hostnames as memetracker/mainnet Dogecoin DNS.
var mainnetDNSSeeds = []string{
	"seed.dogecoin.org",
	"seed.dogecoin.net",
	"seed.multidoge.org",
	"seed2.multidoge.org",
	"seed.dogecoin.com",
}

// ------- Utilities -------

func sha256d(data []byte) [32]byte {
	h1 := sha256.Sum256(data)
	h2 := sha256.Sum256(h1[:])
	return h2
}

func mustEnvDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envString(key, def string) string {
	return mustEnvDefault(key, def)
}

// ------- Base58Check (P2PKH -> hash160) -------

var b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func b58Index(b byte) (int64, error) {
	idx := strings.IndexByte(b58Alphabet, b)
	if idx < 0 {
		return 0, fmt.Errorf("invalid base58 char: %q", b)
	}
	return int64(idx), nil
}

func b58Decode(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty base58")
	}
	n := new(big.Int)
	for i := 0; i < len(s); i++ {
		c := s[i]
		v, err := b58Index(c)
		if err != nil {
			return nil, err
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(v))
	}

	// Leading '1's are leading 0x00 bytes.
	pad := 0
	for pad < len(s) && s[pad] == '1' {
		pad++
	}

	raw := n.Bytes() // big-endian, no leading zeros
	out := make([]byte, 0, pad+len(raw))
	out = append(out, make([]byte, pad)...)
	out = append(out, raw...)
	return out, nil
}

func b58checkDecode(addr string) ([]byte, error) {
	raw, err := b58Decode(addr)
	if err != nil {
		return nil, err
	}
	if len(raw) < 5 {
		return nil, errors.New("invalid address length")
	}
	payload, chk := raw[:len(raw)-4], raw[len(raw)-4:]
	sum := sha256d(payload)
	if !strings.EqualFold(hex.EncodeToString(sum[:4]), hex.EncodeToString(chk)) {
		return nil, errors.New("bad checksum")
	}
	return payload, nil
}

func decodePayoutToHash160(address string, network string) ([]byte, error) {
	wantVer := mainnetP2PKHVersion
	if strings.ToLower(network) != "mainnet" {
		wantVer = testnetP2PKHVersion
	}
	p, err := b58checkDecode(address)
	if err != nil {
		return nil, err
	}
	if len(p) != 21 || p[0] != wantVer {
		return nil, fmt.Errorf("need P2PKH base58 address for %s", network)
	}
	return p[1:21], nil
}

// ------- Tx parsing -------

func readVarInt(data []byte, off *int) (uint64, error) {
	if *off >= len(data) {
		return 0, errors.New("eof")
	}
	b0 := data[*off]
	*off++
	if b0 < 0xFD {
		return uint64(b0), nil
	}
	if b0 == 0xFD {
		if *off+2 > len(data) {
			return 0, errors.New("eof")
		}
		v := binary.LittleEndian.Uint16(data[*off:])
		*off += 2
		return uint64(v), nil
	}
	if b0 == 0xFE {
		if *off+4 > len(data) {
			return 0, errors.New("eof")
		}
		v := binary.LittleEndian.Uint32(data[*off:])
		*off += 4
		return uint64(v), nil
	}
	// 0xFF
	if *off+8 > len(data) {
		return 0, errors.New("eof")
	}
	v := binary.LittleEndian.Uint64(data[*off:])
	*off += 8
	return v, nil
}

func scriptPubKeyHash160(script []byte) ([]byte, bool) {
	// P2PKH: OP_DUP OP_HASH160 0x14 <20> OP_EQUALVERIFY OP_CHECKSIG
	if len(script) == 25 &&
		script[0] == 0x76 &&
		script[1] == 0xA9 &&
		script[2] == 0x14 &&
		script[23] == 0x88 &&
		script[24] == 0xAC {
		out := make([]byte, 20)
		copy(out, script[3:23])
		return out, true
	}

	// P2SH: OP_HASH160 0x14 <20> OP_EQUAL
	if len(script) == 23 &&
		script[0] == 0xA9 &&
		script[1] == 0x14 &&
		script[22] == 0x87 {
		out := make([]byte, 20)
		copy(out, script[2:22])
		return out, true
	}

	// v0 P2WPKH (OP_0 0x14 <20>)
	if len(script) == 22 && script[0] == 0x00 && script[1] == 0x14 {
		out := make([]byte, 20)
		copy(out, script[2:22])
		return out, true
	}

	return nil, false
}

type txOutput struct {
	valueSats int64
	script    []byte
}

func parseTxOutputs(raw []byte) ([]txOutput, bool, error) {
	if len(raw) < 8 {
		return nil, false, nil
	}

	off := 0

	// version (4 bytes)
	if off+4 > len(raw) {
		return nil, false, errors.New("truncated_version")
	}
	off += 4

	isSegwit := false
	if off+2 <= len(raw) && raw[off] == 0 && raw[off+1] == 1 {
		isSegwit = true
		off += 2
	}

	// vin count
	nin, err := readVarInt(raw, &off)
	if err != nil {
		return nil, isSegwit, err
	}

	// skip vin scripts and sequences
	for i := 0; i < int(nin); i++ {
		// outpoint: 32 hash + 4 vout
		if off+36 > len(raw) {
			return nil, isSegwit, errors.New("truncated_txin")
		}
		off += 32 + 4
		// scriptSig
		slen, err := readVarInt(raw, &off)
		if err != nil {
			return nil, isSegwit, err
		}
		if off+int(slen) > len(raw) {
			return nil, isSegwit, errors.New("truncated_scriptSig")
		}
		off += int(slen)
		// sequence (4 bytes)
		if off+4 > len(raw) {
			return nil, isSegwit, errors.New("truncated_sequence")
		}
		off += 4
	}

	// vin done, now vout count
	nout, err := readVarInt(raw, &off)
	if err != nil {
		return nil, isSegwit, err
	}

	outs := make([]txOutput, 0, int(nout))
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return nil, isSegwit, errors.New("truncated_value")
		}
		value := int64(binary.LittleEndian.Uint64(raw[off:]))
		off += 8

		slen, err := readVarInt(raw, &off)
		if err != nil {
			return nil, isSegwit, err
		}
		if off+int(slen) > len(raw) {
			return nil, isSegwit, errors.New("truncated_pk_script")
		}
		script := raw[off : off+int(slen)]
		off += int(slen)

		cp := make([]byte, len(script))
		copy(cp, script)
		outs = append(outs, txOutput{valueSats: value, script: cp})
	}

	// Skip witness data after outputs.
	if isSegwit {
		for i := 0; i < int(nin); i++ {
			nstk, err := readVarInt(raw, &off)
			if err != nil {
				return nil, isSegwit, err
			}
			for j := 0; j < int(nstk); j++ {
				elen, err := readVarInt(raw, &off)
				if err != nil {
					return nil, isSegwit, err
				}
				if off+int(elen) > len(raw) {
					return nil, isSegwit, errors.New("truncated_witness")
				}
				off += int(elen)
			}
		}
	}

	return outs, isSegwit, nil
}

func txidHex(raw []byte) string {
	if len(raw) < 8 {
		sum := sha256d(raw)
		rev := make([]byte, 32)
		for i := 0; i < 32; i++ {
			rev[i] = sum[31-i]
		}
		return hex.EncodeToString(rev)
	}

	off := 4
	isSegwit := len(raw) >= off+2 && raw[off] == 0 && raw[off+1] == 1
	if isSegwit {
		off += 2
	}
	bodyStart := off

	// Skip inputs
	nin, err := readVarInt(raw, &off)
	if err != nil {
		sum := sha256d(raw)
		rev := make([]byte, 32)
		for i := 0; i < 32; i++ {
			rev[i] = sum[31-i]
		}
		return hex.EncodeToString(rev)
	}
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += 32 + 4 // outpoint + sequence
		slen, err := readVarInt(raw, &off)
		if err != nil {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += int(slen)
		if off+4 > len(raw) {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += 4 // locktime? (actually sequence already skipped; but this port is close enough for our usage)
	}

	// NOTE: To keep this implementation reliable, we do NOT use the simplistic off handling above.
	// Instead, use a safer approach: derive txid from the non-witness preimage by re-parsing structure.
	// We re-implement the python logic closely below.

	// Re-parse correctly:
	off = 4
	isSegwit = len(raw) >= off+2 && raw[off] == 0 && raw[off+1] == 1
	if isSegwit {
		off += 2
	}
	bodyStart = off
	nin64, err := readVarInt(raw, &off)
	if err != nil {
		sum := sha256d(raw)
		rev := make([]byte, 32)
		for i := 0; i < 32; i++ {
			rev[i] = sum[31-i]
		}
		return hex.EncodeToString(rev)
	}
	nin = nin64

	// inputs: [outpoint(32)+scriptLen+script+sequence(4)] repeated
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		// skip outpoint
		off += 32
		// script length + script
		slen64, err := readVarInt(raw, &off)
		if err != nil {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += int(slen64)
		// sequence
		if off+4 > len(raw) {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += 4
	}

	nout64, err := readVarInt(raw, &off)
	if err != nil {
		sum := sha256d(raw)
		rev := make([]byte, 32)
		for i := 0; i < 32; i++ {
			rev[i] = sum[31-i]
		}
		return hex.EncodeToString(rev)
	}
	for i := 0; i < int(nout64); i++ {
		if off+8 > len(raw) {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += 8
		slen64, err := readVarInt(raw, &off)
		if err != nil {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		off += int(slen64)
	}
	endOutputs := off

	rest := endOutputs
	if isSegwit {
		nwi64, err := readVarInt(raw, &rest)
		if err != nil {
			sum := sha256d(raw)
			rev := make([]byte, 32)
			for i := 0; i < 32; i++ {
				rev[i] = sum[31-i]
			}
			return hex.EncodeToString(rev)
		}
		for i := 0; i < int(nwi64); i++ {
			ns64, err := readVarInt(raw, &rest)
			if err != nil {
				sum := sha256d(raw)
				rev := make([]byte, 32)
				for i := 0; i < 32; i++ {
					rev[i] = sum[31-i]
				}
				return hex.EncodeToString(rev)
			}
			for j := 0; j < int(ns64); j++ {
				el64, err := readVarInt(raw, &rest)
				if err != nil {
					sum := sha256d(raw)
					rev := make([]byte, 32)
					for i := 0; i < 32; i++ {
						rev[i] = sum[31-i]
					}
					return hex.EncodeToString(rev)
				}
				rest += int(el64)
			}
		}
	}
	var locktime []byte
	if rest+4 <= len(raw) {
		locktime = raw[rest : rest+4]
	} else {
		locktime = []byte{0, 0, 0, 0}
	}

	var preimage []byte
	if isSegwit {
		// preimage = version(4) + body(non-witness, from body_start to end_outputs) + locktime
		preimage = append(append([]byte{}, raw[0:4]...), raw[bodyStart:endOutputs]...)
		preimage = append(preimage, locktime...)
	} else {
		preimage = append(append([]byte{}, raw[0:endOutputs]...), locktime...)
	}

	sum := sha256d(preimage)
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = sum[31-i]
	}
	return hex.EncodeToString(rev)
}

func wtxidHex(raw []byte) string {
	sum := sha256d(raw)
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = sum[31-i]
	}
	return hex.EncodeToString(rev)
}

// ------- P2P protocol -------

func writeVarInt(n int) []byte {
	if n < 0xFD {
		return []byte{byte(n)}
	}
	if n <= 0xFFFF {
		out := make([]byte, 3)
		out[0] = 0xFD
		binary.LittleEndian.PutUint16(out[1:], uint16(n))
		return out
	}
	if n <= 0xFFFFFFFF {
		out := make([]byte, 5)
		out[0] = 0xFE
		binary.LittleEndian.PutUint32(out[1:], uint32(n))
		return out
	}
	out := make([]byte, 9)
	out[0] = 0xFF
	binary.LittleEndian.PutUint64(out[1:], uint64(n))
	return out
}

func buildMessage(command string, payload []byte) []byte {
	cmd := []byte(command)
	if len(cmd) > COMMAND_LEN {
		cmd = cmd[:COMMAND_LEN]
	}
	padded := make([]byte, COMMAND_LEN)
	copy(padded, cmd)
	checksum := sha256d(payload)

	out := make([]byte, 0, 24+len(payload))
	hdr := make([]byte, 0, 24)

	tmp := make([]byte, 4)
	binary.LittleEndian.PutUint32(tmp, uint32(MAGIC))
	hdr = append(hdr, tmp...)

	hdr = append(hdr, padded...)
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(len(payload)))
	hdr = append(hdr, size...)
	hdr = append(hdr, checksum[:4]...)

	out = append(out, hdr...)
	out = append(out, payload...)
	return out
}

func readExact(conn net.Conn, size int) ([]byte, error) {
	out := make([]byte, 0, size)
	for len(out) < size {
		part := make([]byte, size-len(out))
		n, err := conn.Read(part)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, io.EOF
		}
		out = append(out, part[:n]...)
	}
	return out, nil
}

func parseInvPayload(payload []byte) ([]invItem, error) {
	off := 0
	n64, err := readVarInt(payload, &off)
	if err != nil {
		return nil, err
	}
	n := int(n64)
	if n > 100000 {
		n = 100000
	}
	out := make([]invItem, 0, n)
	for i := 0; i < n; i++ {
		if off+36 > len(payload) {
			break
		}
		invType := binary.LittleEndian.Uint32(payload[off:])
		h := payload[off+4 : off+36]
		cp := make([]byte, 32)
		copy(cp, h)
		off += 36
		out = append(out, invItem{invType: int(invType), hash: cp})
	}
	return out, nil
}

type invItem struct {
	invType int
	hash    []byte // 32 bytes in wire order
}

func invTypeIsTx(t int) bool {
	return (t & ^MSG_WITNESS_FLAG) == MSG_TX
}

func buildGetdataPayload(items []invItem) []byte {
	parts := make([]byte, 0, 1+len(items)*36)
	parts = append(parts, writeVarInt(len(items))...)
	for _, it := range items {
		tmp := make([]byte, 4)
		binary.LittleEndian.PutUint32(tmp, uint32(it.invType))
		parts = append(parts, tmp...)
		parts = append(parts, it.hash...)
	}
	return parts
}

func buildVersionPayload(p2pPort int) []byte {
	version := int32(70015)
	services := uint64(NODE_NETWORK | NODE_WITNESS)
	timestamp := uint64(time.Now().Unix())

	// addr_recv: (services=0, addr=16 zero, port big-end)
	addrRecv := make([]byte, 8+16+2)
	binary.LittleEndian.PutUint64(addrRecv[:8], 0)
	// 16 zero already
	binary.BigEndian.PutUint16(addrRecv[8+16:], uint16(p2pPort))

	addrFrom := make([]byte, 8+16+2)
	binary.LittleEndian.PutUint64(addrFrom[:8], 0)
	binary.BigEndian.PutUint16(addrFrom[8+16:], uint16(p2pPort))

	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)
	nonce := binary.LittleEndian.Uint64(nonceBytes)

	userAgent := "/memetracker-pup:0.0.1/"
	uaLen := len(userAgent)
	ua := make([]byte, 1+uaLen)
	ua[0] = byte(uaLen)
	copy(ua[1:], []byte(userAgent))

	startHeight := int32(0)
	relay := byte(1)

	out := make([]byte, 0, 110)
	tmp1 := make([]byte, 4)
	binary.LittleEndian.PutUint32(tmp1, uint32(version))
	out = append(out, tmp1...)
	tmp2 := make([]byte, 8)
	binary.LittleEndian.PutUint64(tmp2, services)
	out = append(out, tmp2...)

	tmp3 := make([]byte, 8)
	binary.LittleEndian.PutUint64(tmp3, timestamp)
	out = append(out, tmp3...)
	out = append(out, addrRecv...)
	out = append(out, addrFrom...)

	tmp4 := make([]byte, 8)
	binary.LittleEndian.PutUint64(tmp4, nonce)
	out = append(out, tmp4...)

	out = append(out, ua...)

	tmp5 := make([]byte, 4)
	binary.LittleEndian.PutUint32(tmp5, uint32(startHeight))
	out = append(out, tmp5...)
	out = append(out, relay)

	return out
}

// ------- Store (file-backed DB) -------

type TxRecord struct {
	Txid       string  `json:"txid"`
	Datetime   string  `json:"datetime"`
	AmountDoge float64 `json:"amount_doge"`
}

type AddressData struct {
	Address       string     `json:"address"`
	Hash160Hex    string     `json:"hash160_hex"`
	CallbackURL   string     `json:"callback_url,omitempty"`
	LastRequested time.Time  `json:"last_requested"`
	Txs           []TxRecord `json:"txs"`
}

type Store struct {
	mu            sync.RWMutex
	storageDir    string
	addressesDir  string
	listLimit     int
	retentionDays int

	watchByHash map[string]*AddressData // hash160hex -> data
}

func NewStore(storageDir string, listLimit int, retentionDays int) (*Store, error) {
	addressesDir := filepath.Join(storageDir, "addresses")
	if err := os.MkdirAll(addressesDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		storageDir:    storageDir,
		addressesDir:  addressesDir,
		listLimit:     listLimit,
		retentionDays: retentionDays,
		watchByHash:   make(map[string]*AddressData),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	s.purgeExpiredLocked(time.Now())
	return s, nil
}

func (s *Store) fileForHash(hashHex string) string {
	return filepath.Join(s.addressesDir, hashHex+".json")
}

func (s *Store) load() error {
	entries, err := os.ReadDir(s.addressesDir)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.addressesDir, name))
		if err != nil {
			continue
		}
		var ad AddressData
		if err := json.Unmarshal(b, &ad); err != nil {
			continue
		}
		if ad.Hash160Hex == "" {
			// best-effort: infer from filename
			ad.Hash160Hex = strings.TrimSuffix(name, ".json")
		}
		if ad.Address == "" {
			continue
		}
		// Ensure newest-first and truncated.
		if len(ad.Txs) > s.listLimit {
			ad.Txs = ad.Txs[:s.listLimit]
		}
		// Purge old
		if now.Sub(ad.LastRequested) > time.Duration(s.retentionDays)*24*time.Hour {
			continue
		}
		s.watchByHash[ad.Hash160Hex] = &ad
	}
	return nil
}

func (s *Store) purgeExpiredLocked(now time.Time) {
	for hashHex, ad := range s.watchByHash {
		if now.Sub(ad.LastRequested) > time.Duration(s.retentionDays)*24*time.Hour {
			delete(s.watchByHash, hashHex)
			_ = os.Remove(s.fileForHash(hashHex))
		}
	}
}

func (s *Store) persistAddressLocked(ad *AddressData) error {
	tmp := struct {
		Address       string     `json:"address"`
		Hash160Hex    string     `json:"hash160_hex"`
		CallbackURL   string     `json:"callback_url,omitempty"`
		LastRequested time.Time  `json:"last_requested"`
		Txs           []TxRecord `json:"txs"`
	}{
		Address:       ad.Address,
		Hash160Hex:    ad.Hash160Hex,
		CallbackURL:   ad.CallbackURL,
		LastRequested: ad.LastRequested,
		Txs:           ad.Txs,
	}
	b, err := json.Marshal(tmp)
	if err != nil {
		return err
	}
	fn := s.fileForHash(ad.Hash160Hex)
	tmpfn := fn + ".tmp"
	if err := os.WriteFile(tmpfn, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpfn, fn)
}

func (s *Store) UpsertTracking(address string, hash160 []byte, callbackURL string) (alreadyMonitoring bool, recent []TxRecord, appliedCallback string, err error) {
	hashHex := hex.EncodeToString(hash160)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if ad, ok := s.watchByHash[hashHex]; ok {
		alreadyMonitoring = true
		ad.LastRequested = now
		if strings.TrimSpace(callbackURL) != "" {
			ad.CallbackURL = strings.TrimSpace(callbackURL)
		}
		if err := s.persistAddressLocked(ad); err != nil {
			return alreadyMonitoring, nil, "", err
		}
		return alreadyMonitoring, ad.Txs, ad.CallbackURL, nil
	}

	ad := &AddressData{
		Address:       address,
		Hash160Hex:    hashHex,
		CallbackURL:   strings.TrimSpace(callbackURL),
		LastRequested: now,
		Txs:           []TxRecord{},
	}
	s.watchByHash[hashHex] = ad
	if err := s.persistAddressLocked(ad); err != nil {
		return false, nil, "", err
	}
	return false, ad.Txs, ad.CallbackURL, nil
}

func (s *Store) AddTx(hashHex string, txid string, dt time.Time, amountDoge float64) (inserted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ad, ok := s.watchByHash[hashHex]
	if !ok {
		return false
	}

	// Dedupe by txid within the stored limited window.
	for _, r := range ad.Txs {
		if r.Txid == txid {
			return false
		}
	}

	rec := TxRecord{
		Txid:       txid,
		Datetime:   dt.UTC().Format(time.RFC3339),
		AmountDoge: amountDoge,
	}

	// Store newest first.
	ad.Txs = append([]TxRecord{rec}, ad.Txs...)
	if len(ad.Txs) > s.listLimit {
		ad.Txs = ad.Txs[:s.listLimit]
	}
	_ = s.persistAddressLocked(ad)
	return true
}

func (s *Store) CallbackTarget(hashHex string) (address string, callbackURL string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ad, found := s.watchByHash[hashHex]
	if !found {
		return "", "", false
	}
	return ad.Address, strings.TrimSpace(ad.CallbackURL), true
}

func (s *Store) GetRecent(address string, hash160 []byte) ([]TxRecord, bool) {
	hashHex := hex.EncodeToString(hash160)
	s.mu.RLock()
	defer s.mu.RUnlock()
	ad, ok := s.watchByHash[hashHex]
	if !ok {
		return nil, false
	}
	return ad.Txs, true
}

// ------- P2P watcher -------

type ProcessedSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

type PaymentCallbackPayload struct {
	Address    string  `json:"address"`
	Txid       string  `json:"txid"`
	AmountDoge float64 `json:"payment_amount"`
	Datetime   string  `json:"datetime"`
}

func notifyCallback(callbackURL string, payload PaymentCallbackPayload) {
	u := strings.TrimSpace(callbackURL)
	if u == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		log.Printf("[MTR-CB] invalid callback url=%q err=%v", u, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "memetracker-pup/1")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[MTR-CB] notify failed url=%q txid=%s err=%v", u, payload.Txid, err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[MTR-CB] notify non-2xx url=%q txid=%s status=%d", u, payload.Txid, resp.StatusCode)
	}
}

func NewProcessedSet() *ProcessedSet {
	return &ProcessedSet{m: make(map[string]struct{})}
}

func (ps *ProcessedSet) Has(k string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	_, ok := ps.m[k]
	return ok
}

func (ps *ProcessedSet) Add(k string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.m[k] = struct{}{}
}

func chooseSeeds(network string) ([]string, int) {
	if strings.ToLower(network) == "mainnet" {
		return mainnetDNSSeeds, 22556
	}
	// Simplified testnet support.
	return []string{"seed.testnet.dogecoin.org"}, 44556
}

func mempoolSniffer(store *Store, network string, p2pHost string, p2pPort int, p2pLog int) {
	seeds, defaultPort := chooseSeeds(network)
	if p2pPort == 0 {
		p2pPort = defaultPort
	}
	if p2pHost != "" {
		seeds = []string{p2pHost}
	}

	processed := NewProcessedSet()

	logf := func(format string, args ...any) {
		if p2pLog <= 0 {
			return
		}
		log.Printf("[MTR-P2P] "+format, args...)
	}

	// Single outer loop; each peer session is blocking in this goroutine.
	seedIdx := 0
	for {
		seedHost := seeds[seedIdx%len(seeds)]
		seedIdx++

		ips, err := net.LookupHost(seedHost)
		if err != nil || len(ips) == 0 {
			log.Printf("[MTR-P2P] DNS resolve failed seed=%s:%d err=%v", seedHost, p2pPort, err)
			time.Sleep(3 * time.Second)
			continue
		}
		if len(ips) > 12 {
			ips = ips[:12]
		}

		for _, peer := range ips {
			if peer == "" {
				continue
			}
			logf("connecting TCP %s:%d …", peer, p2pPort)

			conn, err := net.DialTimeout("tcp", net.JoinHostPort(peer, strconv.Itoa(p2pPort)), 8*time.Second)
			if err != nil {
				logf("connect failed: %v", err)
				continue
			}
			_ = conn.SetDeadline(time.Now().Add(25 * time.Second))

			stateLastPeer := net.JoinHostPort(peer, strconv.Itoa(p2pPort))
			gotVerack := false
			mempoolSent := false
			lastMempoolResync := time.Time{}
			start := time.Now()

			logf("connected peer=%s sending version (handshake)", stateLastPeer)

			_ = conn.SetDeadline(time.Now().Add(25 * time.Second))
			_, _ = conn.Write(buildMessage("version", buildVersionPayload(p2pPort)))

			for time.Since(start) < SESSION_SEC {
				header, err := readExact(conn, 24)
				if err != nil {
					break
				}
				// Validate magic (little-endian in header)
				magic := binary.LittleEndian.Uint32(header[0:4])
				if magic != uint32(MAGIC) {
					continue
				}
				cmdRaw := header[4:16]
				cmd := strings.TrimRight(string(cmdRaw), "\x00")
				size := binary.LittleEndian.Uint32(header[16:20])
				payload := []byte{}
				if size > 0 {
					payload, err = readExact(conn, int(size))
					if err != nil {
						break
					}
				}

				switch cmd {
				case "version":
					_, _ = conn.Write(buildMessage("verack", nil))
				case "verack":
					gotVerack = true
					if !mempoolSent {
						_, _ = conn.Write(buildMessage("mempool", nil))
						mempoolSent = true
						lastMempoolResync = time.Now()
						logf("sent mempool request to peer=%s", stateLastPeer)
					}
				case "ping":
					_, _ = conn.Write(buildMessage("pong", payload))
				case "tx":
					// Fast path: if no watchers, skip parsing.
					store.mu.RLock()
					hasWatchers := len(store.watchByHash) > 0
					store.mu.RUnlock()
					if !hasWatchers {
						continue
					}

					outs, _, err := parseTxOutputs(payload)
					if err != nil {
						continue
					}
					txid := txidHex(payload)
					wtxid := wtxidHex(payload)

					if processed.Has(txid) || processed.Has(wtxid) {
						continue
					}

					// Sum amounts per watched address-hash.
					amtByHash := make(map[string]int64)
					store.mu.RLock()
					for _, o := range outs {
						h160, ok := scriptPubKeyHash160(o.script)
						if !ok {
							continue
						}
						hashHex := hex.EncodeToString(h160)
						if _, watching := store.watchByHash[hashHex]; watching {
							amtByHash[hashHex] += o.valueSats
						}
					}
					store.mu.RUnlock()

					if len(amtByHash) == 0 {
						continue
					}

					// Record payments and mark tx as processed (only when matched).
					dt := time.Now().UTC()
					matchedAny := false
					for hashHex, sats := range amtByHash {
						if sats <= 0 {
							continue
						}
						amountDoge := float64(sats) / 1e8
						if store.AddTx(hashHex, txid, dt, amountDoge) {
							matchedAny = true
							addr, cbURL, ok := store.CallbackTarget(hashHex)
							if ok && cbURL != "" {
								go notifyCallback(cbURL, PaymentCallbackPayload{
									Address:    addr,
									Txid:       txid,
									AmountDoge: amountDoge,
									Datetime:   dt.Format(time.RFC3339),
								})
							}
						}
					}
					if matchedAny {
						processed.Add(txid)
						processed.Add(wtxid)
					}

				case "inv":
					if !gotVerack || !mempoolSent {
						continue
					}
					store.mu.RLock()
					hasWatchers := len(store.watchByHash) > 0
					store.mu.RUnlock()
					if !hasWatchers {
						// Avoid wasting bandwidth when no addresses are being tracked.
						continue
					}
					if len(payload) == 0 {
						continue
					}
					invs, err := parseInvPayload(payload)
					if err != nil {
						continue
					}

					fetch := make([]invItem, 0, MAX_TX_FETCH_INV)
					invSeen := make(map[string]struct{})
					// Per inv message, request only tx-like items we haven't already processed.
					for _, it := range invs {
						if !invTypeIsTx(it.invType) {
							continue
						}
						txHashHex := reverseBytesToHex(it.hash) // inv hash is already wire-endian; match arcade
						if _, ok := invSeen[txHashHex]; ok {
							continue
						}
						invSeen[txHashHex] = struct{}{}
						if processed.Has(txHashHex) {
							continue
						}
						fetch = append(fetch, it)
						if len(fetch) >= MAX_TX_FETCH_INV {
							break
						}
					}

					for i := 0; i < len(fetch); i += GETDATA_BATCH {
						j := i + GETDATA_BATCH
						if j > len(fetch) {
							j = len(fetch)
						}
						pl := buildGetdataPayload(fetch[i:j])
						_, _ = conn.Write(buildMessage("getdata", pl))
					}
				}

				// Periodic mempool refresh.
				if gotVerack && mempoolSent && time.Since(lastMempoolResync) >= time.Duration(MEMPOOL_RESYNC_SEC)*time.Second {
					_, _ = conn.Write(buildMessage("mempool", nil))
					lastMempoolResync = time.Now()
				}
			}

			_ = conn.Close()
		}

		time.Sleep(5 * time.Second)
	}
}

func reverseBytesToHex(b []byte) string {
	// Used to convert inventory hashes to the same endianness as txidHex() returns.
	r := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		r[i] = b[len(b)-1-i]
	}
	return hex.EncodeToString(r)
}

// ------- HTTP API -------

func deriveCallbackURL(r *http.Request, address string) string {
	// Preferred explicit callback from caller.
	qCB := strings.TrimSpace(r.URL.Query().Get("callback"))
	if qCB != "" {
		if u, err := url.ParseRequestURI(qCB); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return qCB
		}
	}
	hCB := strings.TrimSpace(r.Header.Get("X-Callback-Url"))
	if hCB != "" {
		if u, err := url.ParseRequestURI(hCB); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return hCB
		}
	}

	// Fallback: infer from requester IP and configurable callback port.
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if host != "" {
		if idx := strings.Index(host, ","); idx >= 0 {
			host = strings.TrimSpace(host[:idx])
		}
	}
	if host == "" {
		h, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
		if err == nil {
			host = strings.TrimSpace(h)
		}
	}
	if host == "" {
		return ""
	}
	scheme := strings.TrimSpace(envString("MTR_CALLBACK_SCHEME", "http"))
	if scheme != "https" {
		scheme = "http"
	}
	port := envInt("MTR_CALLBACK_PORT", 10001)
	return fmt.Sprintf("%s://%s/%s/", scheme, net.JoinHostPort(host, strconv.Itoa(port)), url.PathEscape(address))
}

func main() {
	log.SetOutput(os.Stderr)

	publicPort := envInt("MTR_HTTP_PORT", envInt("PUBLIC_PORT", 8084))
	bindIP := envString("MTR_HTTP_BIND", envString("DBX_PUP_IP", "0.0.0.0"))
	network := strings.ToLower(envString("MTR_NETWORK", envString("NETWORK", "mainnet")))
	listLimit := envInt("MTR_LIST_LIMIT", envInt("LIST_LIMIT", 10))
	retentionDays := envInt("MTR_RETENTION_DAYS", envInt("RETENTION_DAYS", 7))
	storageDir := envString("MTR_STORAGE_DIR", "/storage/memetracker")
	p2pHost := strings.TrimSpace(envString("MTR_P2P_HOST", envString("P2P_HOST", "")))
	p2pPort := envInt("MTR_P2P_PORT", envInt("P2P_PORT", 22556))
	p2pLog := envInt("MTR_P2P_LOG", envInt("P2P_LOG", 1))

	store, err := NewStore(storageDir, listLimit, retentionDays)
	if err != nil {
		log.Fatalf("failed init store: %v", err)
	}

	go func() {
		t := time.NewTicker(1 * time.Minute)
		defer t.Stop()
		for now := range t.C {
			store.mu.Lock()
			store.purgeExpiredLocked(now.UTC())
			store.mu.Unlock()
		}
	}()

	go mempoolSniffer(store, network, p2pHost, p2pPort, p2pLog)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":              true,
			"server_time_utc": time.Now().UTC().Format(time.RFC3339),
			"retention_days":  retentionDays,
		})
	})

	mux.HandleFunc("/track/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		addr := strings.TrimPrefix(r.URL.Path, "/track/")
		addr = strings.TrimSpace(addr)
		addr = strings.Trim(addr, "/")
		if addr == "" {
			http.Error(w, "missing address", http.StatusBadRequest)
			return
		}

		hash160, err := decodePayoutToHash160(addr, network)
		if err != nil {
			http.Error(w, "invalid address: "+err.Error(), http.StatusBadRequest)
			return
		}

		callbackURL := deriveCallbackURL(r, addr)
		already, recents, appliedCallback, err := store.UpsertTracking(addr, hash160, callbackURL)
		if err != nil {
			http.Error(w, "storage error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"address":            addr,
			"monitored":          true,
			"already_monitoring": already,
			"callback_url":       appliedCallback,
			"retention_days":     retentionDays,
			"stored_tx_limit":    listLimit,
			"transactions":       recents,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{
		Addr:         net.JoinHostPort(bindIP, strconv.Itoa(publicPort)),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	log.Printf("[MTR] listening on %s (network=%s, listLimit=%d, retentionDays=%d)", srv.Addr, network, listLimit, retentionDays)
	log.Fatal(srv.ListenAndServe())
}
