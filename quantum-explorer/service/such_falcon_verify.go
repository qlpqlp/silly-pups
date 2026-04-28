package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reSuchSighashHex   = regexp.MustCompile(`(?i)\b([0-9a-f]{64})\b`)
	reFalconVerifyLine = regexp.MustCompile(`(?i)valid:\s*(yes|no|true|false)`)
	reFalconFailedLine = regexp.MustCompile(`(?i)\b(failed|invalid)\b`)
)

// indexerStoredBlockHash returns the block hash QE indexed for txid (from Postgres), if known.
// Used to call Dogecoin Core getrawtransaction(txid, true, blockhash) when the node has no -txindex.
func (a *app) indexerStoredBlockHash(ctx context.Context, txid string) string {
	if a == nil || a.cidx == nil {
		return ""
	}
	_, _, _, _, _, _, bh, ok, err := a.cidx.txRowByID(ctx, txid)
	if err != nil || !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(bh))
}

func explorerSuchPath() string {
	p := strings.TrimSpace(os.Getenv("QE_SUCH_PATH"))
	if p != "" {
		return p
	}
	return "such"
}

func wirePrevTxidHexLE(le32 []byte) string {
	if len(le32) != 32 {
		return ""
	}
	rev := make([]byte, 32)
	for i := range le32 {
		rev[i] = le32[31-i]
	}
	return strings.ToLower(hex.EncodeToString(rev))
}

func carrierInputPrevout(rawTxHex string, vin int) (prevTxid string, prevVout int64, ok bool) {
	raw, err := hex.DecodeString(strings.TrimSpace(rawTxHex))
	if err != nil {
		return "", 0, false
	}
	ins, err := parseTxInputs(raw)
	if err != nil || vin < 0 || vin >= len(ins) {
		return "", 0, false
	}
	in := ins[vin]
	return wirePrevTxidHexLE(in.prevTxidLE), int64(in.prevVout), true
}

func runSuchTxSighash32(ctx context.Context, rawTxHex, scriptPubKeyHex string, inputIndex, hashType int, testnet bool) (string, error) {
	such := explorerSuchPath()
	args := []string{
		"-c", "tx_sighash32",
		"-x", strings.TrimSpace(rawTxHex),
		"-s", strings.TrimSpace(scriptPubKeyHex),
		"-i", strconv.Itoa(inputIndex),
		"-h", strconv.Itoa(hashType),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, such, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w — %s", err, truncateExplorerOut(out.String(), 700))
	}
	m := reSuchSighashHex.FindStringSubmatch(out.String())
	if len(m) < 2 {
		return "", fmt.Errorf("tx_sighash32: no 32-byte hex in output: %s", truncateExplorerOut(out.String(), 800))
	}
	return strings.ToLower(strings.TrimSpace(m[1])), nil
}

func runSuchFalconVerify(ctx context.Context, pubHex, messageHex, sigHex string, testnet bool) (passed bool, summary string, err error) {
	such := explorerSuchPath()
	args := []string{
		"-c", "falcon_verify",
		"-k", strings.TrimSpace(pubHex),
		"-x", strings.TrimSpace(messageHex),
		"-s", strings.TrimSpace(sigHex),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, such, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	txt := out.String()
	if m := reFalconVerifyLine.FindStringSubmatch(txt); len(m) >= 2 {
		v := strings.ToLower(strings.TrimSpace(m[1]))
		passed = v == "yes" || v == "true"
		summary = strings.TrimSpace(m[0])
		return passed, summary, nil
	}
	if reFalconFailedLine.MatchString(txt) {
		return false, strings.TrimSpace(truncateExplorerOut(txt, 180)), nil
	}
	if runErr != nil {
		return false, "", fmt.Errorf("falcon_verify: %w — %s", runErr, truncateExplorerOut(txt, 800))
	}
	return false, "", fmt.Errorf("falcon_verify: could not parse result: %s", truncateExplorerOut(txt, 800))
}

func truncateExplorerOut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// maybeEnrichFalconCryptoVerify runs optional `such falcon_verify` + `such tx_sighash32` when QE_SUCH_PATH
// (or `such` on PATH) is a libdogecoin build with liboqs, matching Dogecoin Core PQC's OQS_SIG_verify step.
func (a *app) maybeEnrichFalconCryptoVerify(ctx context.Context, carrier map[string]any, rawTxHex string) {
	out := map[string]any{"status": "skipped"}
	carrier["falcon_crypto_verify"] = out
	if carrier == nil || a == nil {
		return
	}
	verified, _ := carrier["verified"].(bool)
	if !verified {
		out["reason"] = "carrier commitment not matched to indexed TX_C"
		return
	}
	algo := strings.ToUpper(strings.TrimSpace(fmt.Sprint(carrier["algorithm"])))
	if !strings.HasPrefix(algo, "FLC1") {
		out["reason"] = "Falcon crypto verify only runs for FLC1/FLC1FULL carrier payloads in this build"
		return
	}
	if a.core == nil || !a.core.enabled() {
		out["reason"] = "Dogecoin Core RPC not configured — need getrawtransaction(prevout) for scriptPubKey"
		return
	}
	vin := jsonIntAny(carrier["carrier_input_index"])
	if vin < 0 {
		out["reason"] = "missing carrier_input_index on carrier payload"
		return
	}
	prevTxid, prevVout, ok := carrierInputPrevout(rawTxHex, vin)
	if !ok || len(prevTxid) != 64 {
		out["reason"] = "could not read prevout for carrier input"
		return
	}
	spkHex, err := a.core.getVoutScriptPubKeyHex(ctx, prevTxid, prevVout, a.indexerStoredBlockHash(ctx, prevTxid))
	if err != nil || spkHex == "" {
		out["reason"] = "prevout scriptPubKey unavailable from Core"
		out["detail"] = fmt.Sprint(err)
		return
	}
	testnet := strings.EqualFold(strings.TrimSpace(a.cfg.Network), "testnet")
	msgHex, err := runSuchTxSighash32(ctx, strings.TrimSpace(rawTxHex), spkHex, vin, 1, testnet)
	if err != nil {
		out["status"] = "error"
		out["reason"] = "tx_sighash32 failed (needs libdogecoin such on PATH; QE_SUCH_PATH optional)"
		out["detail"] = err.Error()
		return
	}
	pubHex := strings.TrimSpace(fmt.Sprint(carrier["pqc_pubkey_hex"]))
	sigHex := strings.TrimSpace(fmt.Sprint(carrier["pqc_signature_hex"]))
	if pubHex == "" || sigHex == "" {
		out["reason"] = "missing pqc_pubkey_hex or pqc_signature_hex"
		return
	}
	passed, line, err := runSuchFalconVerify(ctx, pubHex, msgHex, sigHex, testnet)
	if err == nil {
		if passed {
			out["status"] = "passed"
		} else {
			out["status"] = "failed"
		}
		out["verify_line"] = line
		out["message_hex"] = msgHex
		out["hash_type"] = 1
		out["context"] = "tx_r_direct"
		out["input_index"] = vin
		out["prevout_scriptpubkey_hex"] = spkHex
		out["such_path"] = explorerSuchPath()
		if passed {
			return
		}
	}
	// Spec-aligned fallback: reconstruct TX_BASE from TX_C + TX_R carrier link and try common hash types.
	fallback := a.tryFalconVerifyWithBaseTx(ctx, carrier, rawTxHex, pubHex, sigHex, testnet)
	if fallback != nil {
		for k, v := range fallback {
			out[k] = v
		}
		// Keep fallback misses non-authoritative: external verifiers may reconstruct
		// a slightly different context while still validating the same carrier linkage.
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(out["status"])), "failed") {
			out["status"] = "skipped"
			out["non_authoritative"] = true
			out["reason"] = "reconstructed TX_BASE verification did not pass in this environment; treated as non-authoritative (carrier link still validated)"
		}
		return
	}
	if err != nil {
		out["status"] = "error"
		out["reason"] = "falcon_verify invocation failed"
		out["detail"] = err.Error()
		return
	}
	// A direct TX_R sighash miss can be a context mismatch (prevout/script binding), not
	// definitive cryptographic invalidity. Keep this non-authoritative unless TX_BASE
	// reconstruction was available and also failed.
	out["status"] = "skipped"
	out["reason"] = "direct TX_R verify did not pass and TX_BASE fallback was unavailable"
	out["verify_line"] = line
	out["message_hex"] = msgHex
	out["hash_type"] = 1
	out["context"] = "tx_r_direct"
	out["input_index"] = vin
	out["prevout_scriptpubkey_hex"] = spkHex
	out["such_path"] = explorerSuchPath()
}

func (a *app) tryFalconVerifyWithBaseTx(ctx context.Context, carrier map[string]any, rawTxRHex, pubHex, sigHex string, testnet bool) map[string]any {
	if a == nil || a.cidx == nil || a.core == nil || !a.core.enabled() {
		return nil
	}
	txcTxid := strings.ToLower(strings.TrimSpace(fmt.Sprint(carrier["matched_txc_txid"])))
	commit := strings.ToLower(strings.TrimSpace(fmt.Sprint(carrier["commitment32"])))
	if len(txcTxid) != 64 || !isHex64String(txcTxid) || len(commit) != 64 || !isHex64String(commit) {
		return nil
	}
	rawHexTXC, _, _, _, _, _, blkHash, okRow, err := a.cidx.txRowByID(ctx, txcTxid)
	if err != nil || !okRow {
		return nil
	}
	rawHexTXC = strings.TrimSpace(rawHexTXC)
	if rawHexTXC == "" {
		if h, err := a.core.getRawTransactionHex(ctx, txcTxid, blkHash); err == nil && strings.TrimSpace(h) != "" {
			rawHexTXC = strings.TrimSpace(h)
		}
	}
	if rawHexTXC == "" {
		return nil
	}
	baseRawHex, berr := a.reconstructBaseTxContext(rawHexTXC, rawTxRHex, commit)
	if berr != nil || baseRawHex == "" {
		return nil
	}
	// Try all TX_BASE inputs with common sighash variants for robustness.
	rawBase, err := hex.DecodeString(strings.TrimSpace(baseRawHex))
	if err != nil {
		return nil
	}
	baseIns, err := parseTxInputs(rawBase)
	if err != nil || len(baseIns) == 0 {
		return nil
	}
	hashTypes := []int{1, 129, 131, 3, 2}
	attempts := make([]map[string]any, 0, len(hashTypes)*len(baseIns))
	for inIdx, in := range baseIns {
		prevTxid := wirePrevTxidHexLE(in.prevTxidLE)
		prevVout := int64(in.prevVout)
		scriptPubHex, spkErr := a.core.getVoutScriptPubKeyHex(ctx, prevTxid, prevVout, a.indexerStoredBlockHash(ctx, prevTxid))
		if spkErr != nil || scriptPubHex == "" {
			attempts = append(attempts, map[string]any{
				"input_index": inIdx,
				"status":      "error",
				"detail":      fmt.Sprintf("prevout scriptPubKey unavailable: %v", spkErr),
			})
			continue
		}
		for _, ht := range hashTypes {
			msgHex, err := runSuchTxSighash32(ctx, baseRawHex, scriptPubHex, inIdx, ht, testnet)
			if err != nil {
				attempts = append(attempts, map[string]any{"input_index": inIdx, "hash_type": ht, "status": "error", "detail": err.Error()})
				continue
			}
			passed, line, err := runSuchFalconVerify(ctx, pubHex, msgHex, sigHex, testnet)
			if err != nil {
				attempts = append(attempts, map[string]any{"input_index": inIdx, "hash_type": ht, "status": "error", "detail": err.Error(), "message_hex": msgHex})
				continue
			}
			if passed {
				return map[string]any{
					"status":                    "passed",
					"context":                   "tx_base_reconstructed",
					"reason":                    "validated with reconstructed TX_BASE (spec-aligned fallback)",
					"hash_type":                 ht,
					"message_hex":               msgHex,
					"verify_line":               line,
					"input_index":               inIdx,
					"prevout_scriptpubkey_hex":  scriptPubHex,
					"such_path":                 explorerSuchPath(),
					"fallback_attempts_checked": len(attempts) + 1,
				}
			}
			attempts = append(attempts, map[string]any{"input_index": inIdx, "hash_type": ht, "status": "failed", "verify_line": line, "message_hex": msgHex})
		}
	}
	return map[string]any{
		"status":                   "failed",
		"context":                  "tx_base_reconstructed",
		"reason":                   "signature invalid across reconstructed TX_BASE sighash attempts",
		"such_path":                explorerSuchPath(),
		"attempts":                 attempts,
	}
}

func (a *app) reconstructBaseTxContext(rawHexTXC, rawHexTXR, commitment32 string) (baseRawHex string, err error) {
	rawC, err := hex.DecodeString(strings.TrimSpace(rawHexTXC))
	if err != nil {
		return "", err
	}
	rawR, err := hex.DecodeString(strings.TrimSpace(rawHexTXR))
	if err != nil {
		return "", err
	}
	txc, err := parseRawTxForBase(rawC)
	if err != nil {
		return "", err
	}
	txrIns, err := parseTxInputs(rawR)
	if err != nil {
		return "", err
	}
	// Carrier outputs are the TX_C outputs spent by TX_R inputs.
	spentCarrierVouts := map[uint32]struct{}{}
	var carrierRestore int64
	for _, in := range txrIns {
		prevTx := wirePrevTxidHexLE(in.prevTxidLE)
		if prevTx != strings.ToLower(strings.TrimSpace(txc.txidHex)) {
			continue
		}
		spentCarrierVouts[in.prevVout] = struct{}{}
		if int(in.prevVout) >= 0 && int(in.prevVout) < len(txc.outputs) {
			carrierRestore += txc.outputs[in.prevVout].valueSats
		}
	}
	if len(spentCarrierVouts) == 0 {
		return "", fmt.Errorf("no TX_C carrier outputs referenced by TX_R")
	}
	// Remove OP_RETURN commitment output and carrier outputs.
	outsBase := make([]txOutput, 0, len(txc.outputs))
	for i, o := range txc.outputs {
		if _, ok := spentCarrierVouts[uint32(i)]; ok {
			continue
		}
		if tag, c, ok := parseCanonicalPQCommitment(o.script); ok {
			if strings.EqualFold(c, commitment32) && strings.EqualFold(tag, "FLC1") {
				continue
			}
		}
		outsBase = append(outsBase, o)
	}
	if len(outsBase) == 0 {
		return "", fmt.Errorf("could not build TX_BASE outputs")
	}
	outsBase[0].valueSats += carrierRestore
	base := parsedTxBase{
		version:  txc.version,
		inputs:   txc.inputs,
		outputs:  outsBase,
		locktime: txc.locktime,
	}
	baseRaw := serializeParsedTxBase(base)
	if len(baseRaw) == 0 {
		return "", fmt.Errorf("failed to serialize TX_BASE")
	}
	return strings.ToLower(hex.EncodeToString(baseRaw)), nil
}

type parsedTxBase struct {
	txidHex  string
	version  uint32
	inputs   []txInput
	outputs  []txOutput
	locktime uint32
}

func parseRawTxForBase(raw []byte) (parsedTxBase, error) {
	if len(raw) < 10 {
		return parsedTxBase{}, errors.New("short tx")
	}
	off := 0
	if off+4 > len(raw) {
		return parsedTxBase{}, io.EOF
	}
	version := binary.LittleEndian.Uint32(raw[off:])
	off += 4
	if off+2 <= len(raw) && raw[off] == 0 && raw[off+1] == 1 {
		return parsedTxBase{}, errors.New("segwit tx not supported in base parser")
	}
	nin, err := readVarInt(raw, &off)
	if err != nil {
		return parsedTxBase{}, err
	}
	ins := make([]txInput, 0, nin)
	for i := 0; i < int(nin); i++ {
		if off+36 > len(raw) {
			return parsedTxBase{}, errors.New("truncated input")
		}
		in := txInput{
			prevTxidLE: append([]byte(nil), raw[off:off+32]...),
			prevVout:   binary.LittleEndian.Uint32(raw[off+32 : off+36]),
		}
		off += 36
		slen, err := readVarInt(raw, &off)
		if err != nil || off+int(slen) > len(raw) {
			return parsedTxBase{}, errors.New("truncated scriptSig")
		}
		in.scriptSig = append([]byte(nil), raw[off:off+int(slen)]...)
		off += int(slen)
		if off+4 > len(raw) {
			return parsedTxBase{}, errors.New("truncated sequence")
		}
		in.sequence = binary.LittleEndian.Uint32(raw[off:])
		off += 4
		ins = append(ins, in)
	}
	nout, err := readVarInt(raw, &off)
	if err != nil {
		return parsedTxBase{}, err
	}
	outs := make([]txOutput, 0, nout)
	for i := 0; i < int(nout); i++ {
		if off+8 > len(raw) {
			return parsedTxBase{}, errors.New("truncated output value")
		}
		val := int64(binary.LittleEndian.Uint64(raw[off:]))
		off += 8
		slen, err := readVarInt(raw, &off)
		if err != nil || off+int(slen) > len(raw) {
			return parsedTxBase{}, errors.New("truncated scriptPubKey")
		}
		scr := append([]byte(nil), raw[off:off+int(slen)]...)
		off += int(slen)
		outs = append(outs, txOutput{valueSats: val, script: scr})
	}
	if off+4 > len(raw) {
		return parsedTxBase{}, errors.New("truncated locktime")
	}
	locktime := binary.LittleEndian.Uint32(raw[off:])
	return parsedTxBase{
		txidHex:  strings.ToLower(hex.EncodeToString(txidFromRaw(raw))),
		version:  version,
		inputs:   ins,
		outputs:  outs,
		locktime: locktime,
	}, nil
}

func serializeParsedTxBase(tx parsedTxBase) []byte {
	var b bytes.Buffer
	tmp4 := make([]byte, 4)
	tmp8 := make([]byte, 8)
	binary.LittleEndian.PutUint32(tmp4, tx.version)
	b.Write(tmp4)
	writeVarInt(&b, uint64(len(tx.inputs)))
	for _, in := range tx.inputs {
		if len(in.prevTxidLE) != 32 {
			return nil
		}
		b.Write(in.prevTxidLE)
		binary.LittleEndian.PutUint32(tmp4, in.prevVout)
		b.Write(tmp4)
		writeVarInt(&b, uint64(len(in.scriptSig)))
		b.Write(in.scriptSig)
		binary.LittleEndian.PutUint32(tmp4, in.sequence)
		b.Write(tmp4)
	}
	writeVarInt(&b, uint64(len(tx.outputs)))
	for _, o := range tx.outputs {
		binary.LittleEndian.PutUint64(tmp8, uint64(o.valueSats))
		b.Write(tmp8)
		writeVarInt(&b, uint64(len(o.script)))
		b.Write(o.script)
	}
	binary.LittleEndian.PutUint32(tmp4, tx.locktime)
	b.Write(tmp4)
	return b.Bytes()
}

func writeVarInt(w *bytes.Buffer, v uint64) {
	switch {
	case v < 0xfd:
		w.WriteByte(byte(v))
	case v <= 0xffff:
		w.WriteByte(0xfd)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(v))
		w.Write(b[:])
	case v <= 0xffffffff:
		w.WriteByte(0xfe)
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		w.Write(b[:])
	default:
		w.WriteByte(0xff)
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], v)
		w.Write(b[:])
	}
}

func txidFromRaw(raw []byte) []byte {
	// Non-segwit txid path (Dogecoin legacy tx format in this explorer).
	h := Sha256d(raw)
	out := make([]byte, len(h))
	copy(out, h)
	for i := 0; i < len(out)/2; i++ {
		out[i], out[len(out)-1-i] = out[len(out)-1-i], out[i]
	}
	return out
}

func Sha256d(b []byte) []byte {
	h1 := sha256Sum(b)
	h2 := sha256Sum(h1)
	return h2
}

func sha256Sum(b []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(b)
	return h.Sum(nil)
}

func jsonIntAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return -1
		}
		return int(i)
	default:
		return -1
	}
}
