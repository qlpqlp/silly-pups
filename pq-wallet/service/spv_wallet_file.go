package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// libdogecoin spv_wallet.db binary layout (see vendors/libdogecoin/src/wallet.c):
// file header: magic 4 + uint32 LE version + 32-byte genesis hash (uint256)
// records: magic 4 + compact-size reclen + 1-byte rectype + reclen payload bytes
// WALLET_DB_REC_TYPE_TX (2) payload = dogecoin_wallet_wtx_serialize: height u32 LE + tx_hash_cache 32 + raw tx

var (
	walletFileHdrMagic = []byte{0xa8, 0xf0, 0x11, 0xc5}
	walletFileRecMagic = []byte{0xc8, 0xf2, 0x69, 0x1e}
	// dogecoin_chainparams_main.genesisblockhash / dogecoin_chainparams_test — must match chainparams.c
	dogeWalletGenesisMainnet = []byte{
		0x91, 0x56, 0x35, 0x2c, 0x18, 0x18, 0xb3, 0x2e, 0x90, 0xc9, 0xe7, 0x92, 0xef, 0xd6, 0xa1, 0x1a,
		0x82, 0xfe, 0x79, 0x56, 0xa6, 0x30, 0xf0, 0x3b, 0xbe, 0xe2, 0x36, 0xce, 0xda, 0xe3, 0x91, 0x1a,
	}
	dogeWalletGenesisTestnet = []byte{
		0x9e, 0x55, 0x50, 0x73, 0xd0, 0xc4, 0xf3, 0x64, 0x56, 0xdb, 0x89, 0x51, 0xf4, 0x49, 0x70, 0x4d,
		0x54, 0x4d, 0x28, 0x26, 0xd9, 0xaa, 0x60, 0x63, 0x6b, 0x40, 0x37, 0x46, 0x26, 0x78, 0x0a, 0xbb,
	}
)

const (
	walletDBRecTypeTx       = 2
	walletFileVersionMax   = 1
	walletFileHeaderSize    = 4 + 4 + 32
	walletWtxPrefixSize     = 4 + 32 // height + tx_hash_cache
)

func readWalletVarLen32(r io.Reader) (uint32, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	switch b[0] {
	case 0xfd:
		var u2 [2]byte
		if _, err := io.ReadFull(r, u2[:]); err != nil {
			return 0, err
		}
		return uint32(binary.LittleEndian.Uint16(u2[:])), nil
	case 0xfe:
		var u4 [4]byte
		if _, err := io.ReadFull(r, u4[:]); err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint32(u4[:]), nil
	case 0xff:
		var u8 [8]byte
		if _, err := io.ReadFull(r, u8[:]); err != nil {
			return 0, err
		}
		v := binary.LittleEndian.Uint64(u8[:])
		if v > 0xffffffff {
			return 0, fmt.Errorf("record length overflow")
		}
		return uint32(v), nil
	default:
		return uint32(b[0]), nil
	}
}

func genesisBytesForWalletNetwork(testnet bool) []byte {
	if testnet {
		return dogeWalletGenesisTestnet
	}
	return dogeWalletGenesisMainnet
}

// parseSPVWalletFileTxRows scans a libdogecoin binary wallet (spv_wallet.db) and returns one REST-shaped
// row per distinct txid with raw hex and block height from on-disk wtx records.
func parseSPVWalletFileTxRows(path string, testnet bool) ([]spvRESTTxRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	hdr := make([]byte, walletFileHeaderSize)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, err
	}
	if len(hdr) < walletFileHeaderSize {
		return nil, fmt.Errorf("wallet header short")
	}
	if !bytes.Equal(hdr[0:4], walletFileHdrMagic) {
		return nil, fmt.Errorf("wallet header magic mismatch")
	}
	ver := binary.LittleEndian.Uint32(hdr[4:8])
	if ver > walletFileVersionMax {
		return nil, fmt.Errorf("unsupported wallet file version %d", ver)
	}
	wantGen := genesisBytesForWalletNetwork(testnet)
	if len(wantGen) != 32 {
		return nil, fmt.Errorf("internal genesis length")
	}
	if !bytes.Equal(hdr[8:40], wantGen) {
		net := "mainnet"
		if testnet {
			net = "testnet"
		}
		return nil, fmt.Errorf("wallet genesis does not match %s chain", net)
	}

	var rows []spvRESTTxRow
	txSeen := make(map[string]struct{})
	recMagic := make([]byte, 4)
	for {
		if _, err := io.ReadFull(f, recMagic); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, err
		}
		if !bytes.Equal(recMagic, walletFileRecMagic) {
			return nil, fmt.Errorf("invalid record magic")
		}
		reclen, err := readWalletVarLen32(f)
		if err != nil {
			return nil, err
		}
		var rectype [1]byte
		if _, err := io.ReadFull(f, rectype[:]); err != nil {
			return nil, err
		}
		if reclen > 32*1024*1024 {
			return nil, fmt.Errorf("oversized wallet record")
		}
		payload := make([]byte, reclen)
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil, err
		}
		if rectype[0] != walletDBRecTypeTx {
			continue
		}
		if len(payload) < walletWtxPrefixSize+10 {
			continue
		}
		height := binary.LittleEndian.Uint32(payload[0:4])
		txBody := payload[walletWtxPrefixSize:]
		wireLen, err := legacyTxWireLen(txBody)
		if err != nil || wireLen <= 0 {
			continue
		}
		if wireLen > len(txBody) {
			continue
		}
		txSlice := txBody[:wireLen]
		txid := dogeLegacyTxidHex(txSlice)
		if txid == "" {
			continue
		}
		id := normalizeTxid(txid)
		if _, dup := txSeen[id]; dup {
			continue
		}
		txSeen[id] = struct{}{}
		rows = append(rows, spvRESTTxRow{
			Txid:        id,
			Direction:   "unknown",
			BlockHeight: int64(height),
			RawHex:      hex.EncodeToString(txSlice),
			Source:      "spv_wallet",
		})
	}
	return rows, nil
}
