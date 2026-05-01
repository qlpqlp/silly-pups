package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// SPV log tail for raw hex backfill + prevout index: older parent txs must be present here for bitcoinj-style net.
const spvLogTailForEnrich = 64 << 20

// firstInputPrevTxidFromRawHex returns the canonical prevout txid for the first non-coinbase input
// (wire hash byte-reversed to match explorer / normalizeTxid form). Empty if not parseable.
func firstInputPrevTxidFromRawHex(rawHex string) string {
	rawHex = strings.TrimSpace(strings.ToLower(rawHex))
	if rawHex == "" {
		return ""
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil || len(raw) < 10 {
		return ""
	}
	off := 0
	if off+4 > len(raw) {
		return ""
	}
	off += 4
	if off+2 <= len(raw) && raw[off] == 0x00 && raw[off+1] == 0x01 {
		off += 2
	}
	nin, err := readCompactSize(raw, &off)
	if err != nil || nin == 0 {
		return ""
	}
	if off+32 > len(raw) {
		return ""
	}
	prev := raw[off : off+32]
	rev := make([]byte, 32)
	for i := 0; i < 32; i++ {
		rev[i] = prev[31-i]
	}
	return normalizeTxid(hex.EncodeToString(rev))
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
// Counterparty and "your" address choices follow Dogecoin Wallet (bitcoinj) list semantics:
//   - sent row address: first output that is not to the wallet (WalletUtils.getToAddressOfSent)
//   - received row address: first output to the wallet (getWalletAddressOfReceived)
// CounterpartySats is the value of that first non-wallet P2PKH output (the payment line), not fee/change.
// ExternalSats sums all external-facing value (multiple recipients + non-P2PKH) for spend detection.
type rawTxFlow struct {
	WalletSats        int64
	ExternalSats      int64
	CounterpartySats  int64 // first external P2PKH output value only; 0 if none
	WalletAddr        string
	ExternalAddr      string
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
	var extNonP2PKH int64
	walletAddrDone := false
	externalAddrDone := false
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
			}
			continue
		}
		hh := hex.EncodeToString(h160)
		if addr, ok := walletByHash160[hh]; ok {
			out.WalletSats += valueSats
			if !walletAddrDone && addr != "" {
				out.WalletAddr = addr
				walletAddrDone = true
			}
			continue
		}
		extAddr := dogeP2PKHAddrFromH160(h160, testnet)
		if extAddr == "" {
			continue
		}
		out.ExternalSats += valueSats
		if !externalAddrDone {
			out.ExternalAddr = extAddr
			out.CounterpartySats = valueSats
			externalAddrDone = true
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
	logBlob := ""
	if lb, err := readFileTail(s.spvLogPath(), spvLogTailForEnrich); err == nil {
		logBlob = lb
	}
	// Backfill RawHex from spv.log for any row that is still missing it. SPV REST often labels spends as
	// "in" (UTXO/credit view); skipping backfill when direction+address were already filled prevented
	// decodeSPVRawTxFlow from ever correcting those rows.
	needRawBackfill := false
	needByTxid := make(map[string]struct{})
	for i := range st.Transactions {
		tx := &st.Transactions[i]
		if strings.TrimSpace(tx.RawHex) != "" {
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
	if needRawBackfill && len(needByTxid) > 0 && strings.TrimSpace(logBlob) != "" {
		byTxid := parseSPVRawTxHexByTxid(logBlob)
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
	// Some restores have tx raws outside the current tail window. If still missing,
	// scan the whole spv.log when reasonably sized so spend rows can resolve recipient/out amount.
	if needRawBackfill && len(needByTxid) > 0 {
		stillMissing := make(map[string]struct{})
		for i := range st.Transactions {
			tx := &st.Transactions[i]
			if strings.TrimSpace(tx.RawHex) != "" {
				continue
			}
			id := normalizeTxid(tx.Txid)
			if id == "" {
				continue
			}
			if _, wanted := needByTxid[id]; wanted {
				stillMissing[id] = struct{}{}
			}
		}
		if len(stillMissing) > 0 {
			logPath := s.spvLogPath()
			const maxFullSPVLogScan = 256 << 20
			if fi, err := os.Stat(logPath); err == nil && !fi.IsDir() && fi.Size() > 0 && fi.Size() <= maxFullSPVLogScan {
				if full, err := os.ReadFile(logPath); err == nil && len(full) > 0 {
					byTxid := parseSPVRawTxHexByTxid(string(full))
					for i := range st.Transactions {
						tx := &st.Transactions[i]
						if strings.TrimSpace(tx.RawHex) != "" {
							continue
						}
						id := normalizeTxid(tx.Txid)
						if id == "" {
							continue
						}
						if _, wanted := stillMissing[id]; !wanted {
							continue
						}
						if raw := strings.TrimSpace(byTxid[id]); raw != "" {
							tx.RawHex = raw
							changed = true
						}
					}
					// Reuse the larger blob for prevout indexing below.
					logBlob = string(full)
				}
			}
		}
	}
	// Prevouts indexed only from raw txs already stored (state + spv.log). Net matches bitcoinj-style getValue
	// when inputs spending our UTXOs are fully resolved; sends with unknown funding txs fall back to output-side totals.
	prevIdx := buildPrevoutWalletIndex(collectUniqueRawHexes(st, logBlob), walletByHash160)
	cache := make(map[string]rawTxFlow)
	for i := range st.Transactions {
		tx := &st.Transactions[i]
		raw := strings.TrimSpace(tx.RawHex)
		if raw == "" {
			continue
		}
		// Broadcast-time OUT rows already store the user-chosen amount + destination; raw decode can disagree
		// on PQ / carrier / multi-output layouts and would corrupt the activity list and balance views.
		if strings.EqualFold(strings.TrimSpace(tx.Source), "manual") && strings.EqualFold(strings.TrimSpace(tx.Direction), "out") {
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
		net, _, _, netOk := walletNetFromPrevoutIndex(raw, walletByHash160, testnet, prevIdx)
		if netOk && net != 0 {
			amtSats := net
			if amtSats < 0 {
				amtSats = -amtSats
			}
			if net < 0 {
				// Dogecoin Wallet style: OUT row amount is what was paid to counterparty (first external P2PKH output),
				// not our change output and not net+fee when we can identify the payment line.
				if fl.CounterpartySats > 0 {
					amtSats = fl.CounterpartySats
				}
				amt := round2(float64(amtSats) / 1e8)
				if !strings.EqualFold(strings.TrimSpace(tx.Direction), "out") {
					tx.Direction = "out"
					changed = true
				}
				if tx.AmountDOGE != amt {
					tx.AmountDOGE = amt
					changed = true
				}
				// Dogecoin Wallet: OUT row shows who was paid, not our change address (REST often attaches wallet addr).
				if fl.ExternalAddr != "" && !strings.EqualFold(strings.TrimSpace(tx.Address), strings.TrimSpace(fl.ExternalAddr)) {
					tx.Address = fl.ExternalAddr
					changed = true
				}
				if tx.FeeDOGE != 0 {
					tx.FeeDOGE = 0
					changed = true
				}
			} else {
				amt := round2(float64(amtSats) / 1e8)
				if !strings.EqualFold(strings.TrimSpace(tx.Direction), "in") {
					tx.Direction = "in"
					changed = true
				}
				if tx.AmountDOGE != amt {
					tx.AmountDOGE = amt
					changed = true
				}
				if tx.Address == "" && fl.WalletAddr != "" {
					tx.Address = fl.WalletAddr
					changed = true
				}
				if tx.FeeDOGE != 0 {
					tx.FeeDOGE = 0
					changed = true
				}
			}
			if tx.Source == "" {
				tx.Source = "spv"
				changed = true
			}
			continue
		}
		if fl.WalletSats == 0 && fl.ExternalSats == 0 {
			continue
		}
		isRecv := fl.ExternalSats == 0 && fl.WalletSats > 0
		isSend := fl.ExternalSats > 0
		if !isRecv && !isSend {
			continue
		}
		if isRecv {
			amt := round2(float64(fl.WalletSats) / 1e8)
			if !strings.EqualFold(strings.TrimSpace(tx.Direction), "in") {
				tx.Direction = "in"
				changed = true
			}
			if tx.AmountDOGE != amt {
				tx.AmountDOGE = amt
				changed = true
			}
			if tx.Address == "" && fl.WalletAddr != "" {
				tx.Address = fl.WalletAddr
				changed = true
			}
			if tx.FeeDOGE != 0 {
				tx.FeeDOGE = 0
				changed = true
			}
		} else if isSend {
			paySats := fl.CounterpartySats
			if paySats <= 0 {
				paySats = fl.ExternalSats
			}
			amt := round2(float64(paySats) / 1e8)
			if !strings.EqualFold(strings.TrimSpace(tx.Direction), "out") {
				tx.Direction = "out"
				changed = true
			}
			if tx.AmountDOGE != amt {
				tx.AmountDOGE = amt
				changed = true
			}
			if fl.ExternalAddr != "" && !strings.EqualFold(strings.TrimSpace(tx.Address), strings.TrimSpace(fl.ExternalAddr)) {
				tx.Address = fl.ExternalAddr
				changed = true
			}
		}
		if tx.Source == "" {
			tx.Source = "spv"
			changed = true
		}
	}
	return changed
}
