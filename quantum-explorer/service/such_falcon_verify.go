package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
)

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
		if runErr != nil && !passed {
			return false, summary, fmt.Errorf("%w — %s", runErr, truncateExplorerOut(txt, 600))
		}
		return passed, summary, nil
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
	if strings.ToUpper(strings.TrimSpace(fmt.Sprint(carrier["algorithm"]))) != "FLC1" {
		out["reason"] = "Falcon crypto verify only runs for FLC1 carrier payloads in this build"
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
	spkHex, err := a.core.getVoutScriptPubKeyHex(ctx, prevTxid, prevVout)
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
	if err != nil {
		out["status"] = "error"
		out["reason"] = "falcon_verify invocation failed"
		out["detail"] = err.Error()
		return
	}
	if passed {
		out["status"] = "passed"
	} else {
		out["status"] = "failed"
	}
	out["verify_line"] = line
	out["message_hex"] = msgHex
	out["input_index"] = vin
	out["prevout_scriptpubkey_hex"] = spkHex
	out["such_path"] = explorerSuchPath()
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
