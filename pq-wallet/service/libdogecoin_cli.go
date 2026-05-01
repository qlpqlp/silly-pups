package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	reSuchFalconPub = regexp.MustCompile(`(?i)public key:\s*([0-9a-f]+)`)
	reSuchFalconSec = regexp.MustCompile(`(?i)secret key:\s*([0-9a-f]+)`)
	reSuchSignedTx  = regexp.MustCompile(`(?i)signed TX:\s*([0-9a-f]+)`)

	reSuchPrivWIF       = regexp.MustCompile(`(?i)private key wif:\s*(\S+)`)
	reSuchPubKeyHex     = regexp.MustCompile(`(?i)public key hex:\s*([0-9a-f]+)`)
	reSuchP2PKHAddr     = regexp.MustCompile(`(?i)p2pkh address:\s*(\S+)`)
	reSuchAnyHexValue   = regexp.MustCompile(`(?i)\b([0-9a-f]{64,})\b`)
	reSuchUTXOLine      = regexp.MustCompile(`(?i)\btxid[=: ]+([a-f0-9]{64})\b.*?\bvout[=: ]+(\d+)\b.*?\b(?:value|amount|koinu|satoshis)[=: ]+(-?\d+(?:\.\d+)?)`)
	reSendtxStartTxid   = regexp.MustCompile(`(?i)start broadcasting transaction:\s*([a-f0-9]{64})`)
	reSuchFlexibleTxHex = regexp.MustCompile(`(?i)(?:signed|unsigned|modified)\s+TX\s*:\s*([0-9a-f]+)`)
	reSuchCarrierSPK    = regexp.MustCompile(`(?i)carrier_p2sh_scriptpubkey:\s*([0-9a-f]+)`)
	reSuchCarrierMkSig  = regexp.MustCompile(`(?i)carrier_part_scriptsig\[(\d+)\]\s*:\s*([0-9a-f]+)`)
	reSuchLongHex       = regexp.MustCompile(`\b([0-9a-f]{200,})\b`)
)

// runSuchP2PKHWallet runs `such -c generate_private_key` then `such -c generate_public_key -p <WIF>` (libdogecoin ECC + base58).
func (s *Server) runSuchP2PKHWallet(testnet bool) (wif, pubHex, p2pkh string, err error) {
	args := []string{"-c", "generate_private_key"}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", "", "", fmt.Errorf("such generate_private_key: %w — %s", err, truncateStr(out.String(), 800))
	}
	text := out.String()
	m := reSuchPrivWIF.FindStringSubmatch(text)
	if len(m) < 2 {
		return "", "", "", fmt.Errorf("parse private key wif from such: %s", truncateStr(text, 1000))
	}
	wif = strings.TrimSpace(m[1])

	args2 := []string{"-c", "generate_public_key", "-p", wif}
	if testnet {
		args2 = append([]string{"-t"}, args2...)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel2()
	cmd2 := exec.CommandContext(ctx2, s.suchPath(), args2...)
	var out2 bytes.Buffer
	cmd2.Stdout = &out2
	cmd2.Stderr = &out2
	if err := cmd2.Run(); err != nil {
		return "", "", "", fmt.Errorf("such generate_public_key: %w — %s", err, truncateStr(out2.String(), 800))
	}
	t2 := out2.String()
	mp := reSuchPubKeyHex.FindStringSubmatch(t2)
	ma := reSuchP2PKHAddr.FindStringSubmatch(t2)
	if len(mp) < 2 || len(ma) < 2 {
		return "", "", "", fmt.Errorf("parse pubkey/address from such: %s", truncateStr(t2, 1000))
	}
	return wif, strings.TrimSpace(mp[1]), strings.TrimSpace(ma[1]), nil
}

// runSuchPubKeyFromWIF runs `such -c generate_public_key -p <WIF>` and returns pubkey hex + P2PKH address.
func (s *Server) runSuchPubKeyFromWIF(wif string, testnet bool) (pubHex, p2pkh string, err error) {
	wif = strings.TrimSpace(wif)
	if wif == "" {
		return "", "", fmt.Errorf("empty WIF")
	}
	args := []string{"-c", "generate_public_key", "-p", wif}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("such generate_public_key: %w — %s", err, truncateStr(out.String(), 800))
	}
	t := out.String()
	mp := reSuchPubKeyHex.FindStringSubmatch(t)
	ma := reSuchP2PKHAddr.FindStringSubmatch(t)
	if len(mp) < 2 || len(ma) < 2 {
		return "", "", fmt.Errorf("parse pubkey/address from such: %s", truncateStr(t, 1000))
	}
	return strings.TrimSpace(mp[1]), strings.TrimSpace(ma[1]), nil
}

// p2pkhScriptPubKeyHexForSign returns scriptPubKey hex for `such -c sign -s` using libdogecoin-derived pubkey.
func (s *Server) p2pkhScriptPubKeyHexForSign(wf *WalletFile) (string, error) {
	p := wf.PrimaryAddress()
	if p == nil {
		return "", fmt.Errorf("no primary address")
	}
	testnet := strings.EqualFold(wf.Network, "testnet")
	pub := strings.TrimSpace(p.PubHex)
	if pub == "" {
		var err error
		pub, _, err = s.runSuchPubKeyFromWIF(p.WIF, testnet)
		if err != nil {
			return "", err
		}
	}
	return p2pkhScriptPubKeyHexFromCompressedPubKeyHex(pub)
}

// libdogecoinVendoredToolCandidates returns possible paths for such/sendtx/spvnode shipped next to the
// service or under pq-wallet/vendors (PQ_LIBDOGECOIN_BIN, vendors/bin, or a local cmake build tree).
func libdogecoinVendoredToolCandidates(tool string) []string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	fn := tool + ext
	var out []string
	if d := strings.TrimSpace(os.Getenv("PQ_LIBDOGECOIN_BIN")); d != "" {
		out = append(out, filepath.Join(d, fn))
	}
	addRoots := func(root string) {
		if root == "" {
			return
		}
		out = append(out,
			filepath.Join(root, "vendors", "bin", fn),
			filepath.Join(root, "vendors", "libdogecoin", "build", fn),
			filepath.Join(root, "vendors", "libdogecoin", "build", "Release", fn),
		)
	}
	if ex, err := os.Executable(); err == nil && ex != "" {
		dir := filepath.Dir(ex)
		addRoots(dir)
		addRoots(filepath.Clean(filepath.Join(dir, "..")))
		addRoots(filepath.Clean(filepath.Join(dir, "..", "..")))
	}
	if wd, err := os.Getwd(); err == nil {
		addRoots(wd)
		addRoots(filepath.Clean(filepath.Join(wd, "..")))
		addRoots(filepath.Clean(filepath.Join(wd, "..", "..")))
	}
	return out
}

func firstExistingFile(paths []string) string {
	for _, p := range paths {
		p = filepath.Clean(p)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func (s *Server) suchPath() string {
	if p := strings.TrimSpace(os.Getenv("LIBDOGECOIN_SUCH")); p != "" {
		return p
	}
	if p := firstExistingFile(libdogecoinVendoredToolCandidates("such")); p != "" {
		return p
	}
	if p, err := exec.LookPath("such"); err == nil {
		return p
	}
	return "such"
}

func (s *Server) sendtxPath() string {
	if p := strings.TrimSpace(os.Getenv("LIBDOGECOIN_SENDTX")); p != "" {
		return p
	}
	if p := firstExistingFile(libdogecoinVendoredToolCandidates("sendtx")); p != "" {
		return p
	}
	if p, err := exec.LookPath("sendtx"); err == nil {
		return p
	}
	return "sendtx"
}

func (s *Server) spvnodePath() string {
	if p := strings.TrimSpace(os.Getenv("LIBDOGECOIN_SPVNODE")); p != "" {
		return p
	}
	if p := firstExistingFile(libdogecoinVendoredToolCandidates("spvnode")); p != "" {
		return p
	}
	if p, err := exec.LookPath("spvnode"); err == nil {
		return p
	}
	return "spvnode"
}

// runSuchFalconKeygen runs `such -c falcon_keygen` and parses Falcon-512 hex keys from stdout.
func (s *Server) runSuchFalconKeygen(testnet bool) (pubHex, privHex string, err error) {
	args := []string{"-c", "falcon_keygen"}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("such falcon_keygen: %w — output: %s", err, truncateStr(out.String(), 800))
	}
	text := out.String()
	m1 := reSuchFalconPub.FindStringSubmatch(text)
	m2 := reSuchFalconSec.FindStringSubmatch(text)
	if len(m1) < 2 || len(m2) < 2 {
		return "", "", fmt.Errorf("could not parse falcon_keygen output: %s", truncateStr(text, 1200))
	}
	return m1[1], m2[1], nil
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// runSuchSign runs `such -c sign` and returns signed raw transaction hex.
func (s *Server) runSuchSign(rawHex, scriptPubHex, wif string, inputIndex, sighashType int, testnet bool) (string, error) {
	args := []string{
		"-c", "sign",
		"-x", rawHex,
		"-s", scriptPubHex,
		"-i", strconv.Itoa(inputIndex),
		"-h", strconv.Itoa(sighashType),
		"-p", wif,
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such sign: %w — %s", err, truncateStr(out.String(), 800))
	}
	m := reSuchSignedTx.FindStringSubmatch(out.String())
	if len(m) < 2 {
		return "", fmt.Errorf("signed TX not found in such output: %s", truncateStr(out.String(), 1200))
	}
	return m[1], nil
}

func (s *Server) runSuchTxSighash32(rawHex, scriptPubHex string, inputIndex, hashType int, testnet bool) (string, error) {
	args := []string{
		"-c", "tx_sighash32",
		"-x", strings.TrimSpace(rawHex),
		"-s", strings.TrimSpace(scriptPubHex),
		"-i", strconv.Itoa(inputIndex),
		"-h", strconv.Itoa(hashType),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such tx_sighash32: %w — %s", err, truncateStr(out.String(), 800))
	}
	m := reSuchAnyHexValue.FindStringSubmatch(out.String())
	if len(m) < 2 {
		return "", fmt.Errorf("sighash32 not found in such output: %s", truncateStr(out.String(), 1200))
	}
	h := strings.ToLower(strings.TrimSpace(m[1]))
	if len(h) < 64 {
		return "", fmt.Errorf("invalid sighash length from such")
	}
	return h[:64], nil
}

func (s *Server) runSuchFalconSign(msgHex, privHex string, testnet bool) (string, error) {
	args := []string{
		"-c", "falcon_sign",
		"-x", strings.TrimSpace(msgHex),
		"-p", strings.TrimSpace(privHex),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such falcon_sign: %w — %s", err, truncateStr(out.String(), 800))
	}
	matches := reSuchAnyHexValue.FindAllStringSubmatch(out.String(), -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("falcon signature hex not found in such output: %s", truncateStr(out.String(), 1200))
	}
	// Keep longest hex value as signature.
	best := ""
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if len(m[1]) > len(best) {
			best = m[1]
		}
	}
	if best == "" {
		return "", fmt.Errorf("falcon signature parse failed")
	}
	return strings.ToLower(strings.TrimSpace(best)), nil
}

// parseSuchTransactionHex extracts raw transaction hex from such stdout (sign / set_scriptsig / falcon_add_*).
func parseSuchTransactionHex(text string) string {
	text = strings.TrimSpace(text)
	if m := reSuchFlexibleTxHex.FindStringSubmatch(text); len(m) >= 2 && len(m[1]) >= 120 {
		return strings.ToLower(m[1])
	}
	if m := reSuchSignedTx.FindStringSubmatch(text); len(m) >= 2 && len(m[1]) >= 120 {
		return strings.ToLower(m[1])
	}
	best := ""
	for _, m := range reSuchLongHex.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 && len(m[1]) > len(best) {
			best = strings.ToLower(m[1])
		}
	}
	return best
}

func (s *Server) runSuchFalconAddCommitAndCarrierTx(unsignedHex, commit32Hex, pubHex, sigHex string, carrierKoinu int64, testnet bool) (string, error) {
	unsignedHex = strings.TrimSpace(unsignedHex)
	commit32Hex = strings.TrimSpace(commit32Hex)
	pubHex = strings.TrimSpace(pubHex)
	sigHex = strings.TrimSpace(sigHex)
	if unsignedHex == "" || commit32Hex == "" || pubHex == "" || sigHex == "" {
		return "", fmt.Errorf("missing falcon_add_commit_and_carrier_tx argument")
	}
	if carrierKoinu <= 0 {
		carrierKoinu = 100_000_000
	}
	args := []string{
		"-c", "falcon_add_commit_and_carrier_tx",
		"-x", unsignedHex,
		"-m", commit32Hex,
		"-k", pubHex,
		"-s", sigHex,
		"-h", strconv.FormatInt(carrierKoinu, 10),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such falcon_add_commit_and_carrier_tx: %w — %s", err, truncateStr(out.String(), 800))
	}
	h := parseSuchTransactionHex(out.String())
	if h == "" {
		return "", fmt.Errorf("falcon_add_commit_and_carrier_tx: no transaction hex in output: %s", truncateStr(out.String(), 1200))
	}
	return h, nil
}

func (s *Server) runSuchPqcCarrierScriptPubkey(testnet bool) (string, error) {
	args := []string{"-c", "pqc_carrier_scriptpubkey"}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such pqc_carrier_scriptpubkey: %w — %s", err, truncateStr(out.String(), 600))
	}
	if m := reSuchCarrierSPK.FindStringSubmatch(out.String()); len(m) >= 2 && len(m[1]) >= 4 {
		return strings.ToLower(strings.TrimSpace(m[1])), nil
	}
	return "", fmt.Errorf("pqc_carrier_scriptpubkey: parse failed: %s", truncateStr(out.String(), 800))
}

func (s *Server) runSuchPqcCarrierMkpart(tag4Hex, pubHex, sigHex string, partIndex int, testnet bool) (string, error) {
	tag4Hex = strings.TrimSpace(tag4Hex)
	pubHex = strings.TrimSpace(pubHex)
	sigHex = strings.TrimSpace(sigHex)
	if tag4Hex == "" || pubHex == "" || sigHex == "" {
		return "", fmt.Errorf("missing pqc_carrier_mkpart argument")
	}
	args := []string{
		"-c", "pqc_carrier_mkpart",
		"-k", tag4Hex,
		"-p", pubHex,
		"-s", sigHex,
		"-i", strconv.Itoa(partIndex),
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such pqc_carrier_mkpart: %w — %s", err, truncateStr(out.String(), 800))
	}
	txt := out.String()
	if m := reSuchCarrierMkSig.FindStringSubmatch(txt); len(m) >= 3 {
		if pi, err := strconv.Atoi(m[1]); err == nil && pi == partIndex {
			return strings.ToLower(strings.TrimSpace(m[2])), nil
		}
	}
	return "", fmt.Errorf("pqc_carrier_mkpart: parse failed: %s", truncateStr(txt, 1200))
}

func (s *Server) runSuchSetScriptSig(rawHex string, vin int, scriptSigHex string, testnet bool) (string, error) {
	rawHex = strings.TrimSpace(rawHex)
	scriptSigHex = strings.TrimSpace(scriptSigHex)
	if rawHex == "" || scriptSigHex == "" {
		return "", fmt.Errorf("empty set_scriptsig argument")
	}
	args := []string{
		"-c", "set_scriptsig",
		"-x", rawHex,
		"-i", strconv.Itoa(vin),
		"-s", scriptSigHex,
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("such set_scriptsig: %w — %s", err, truncateStr(out.String(), 800))
	}
	h := parseSuchTransactionHex(out.String())
	if h == "" {
		return "", fmt.Errorf("set_scriptsig: no transaction hex in output: %s", truncateStr(out.String(), 1200))
	}
	return h, nil
}

// runSuchSetScriptSigMulti applies set_scriptsig for vin 0..len(sigs)-1 in order.
func (s *Server) runSuchSetScriptSigMulti(rawHex string, sigs []string, testnet bool) (string, error) {
	h := strings.TrimSpace(rawHex)
	for i, sg := range sigs {
		var err error
		h, err = s.runSuchSetScriptSig(h, i, sg, testnet)
		if err != nil {
			return "", fmt.Errorf("set_scriptsig vin %d: %w", i, err)
		}
	}
	return h, nil
}

// collectCarrierScriptSigs calls pqc_carrier_mkpart for part indices 0,1,... until the tool errors (single-part payloads stop at i=1).
func (s *Server) collectCarrierScriptSigs(tag4Hex, pubHex, sigHex string, testnet bool) ([]string, error) {
	var out []string
	for i := 0; i < 48; i++ {
		sg, err := s.runSuchPqcCarrierMkpart(tag4Hex, pubHex, sigHex, i, testnet)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			break
		}
		out = append(out, sg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no carrier scriptSig parts from pqc_carrier_mkpart")
	}
	return out, nil
}

// probeSuchPQC runs lightweight such probes (short timeout) to report whether the liboqs/PQC CLI surface is present.
func (s *Server) probeSuchPQC(ctx context.Context) map[string]any {
	out := map[string]any{"such_path": s.suchPath()}
	if ctx == nil {
		ctx = context.Background()
	}
	args := []string{"-c", "pqc_carrier_scriptpubkey"}
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	txt := buf.String()
	if err != nil {
		out["pqc_carrier_scriptpubkey_ok"] = false
		out["pqc_carrier_scriptpubkey_error"] = truncateStr(strings.TrimSpace(txt+" | "+err.Error()), 500)
	} else if m := reSuchCarrierSPK.FindStringSubmatch(txt); len(m) >= 2 {
		out["pqc_carrier_scriptpubkey_ok"] = true
		out["carrier_p2sh_scriptpubkey_hex"] = strings.ToLower(strings.TrimSpace(m[1]))
	} else {
		out["pqc_carrier_scriptpubkey_ok"] = false
		out["pqc_carrier_scriptpubkey_error"] = truncateStr(txt, 500)
	}
	// Missing-args invocation distinguishes "unknown command" (non-OQS build) from a usage / missing-parameter error.
	args2 := []string{"-c", "falcon_add_commit_and_carrier_tx"}
	cmd2 := exec.CommandContext(ctx, s.suchPath(), args2...)
	var b2 bytes.Buffer
	cmd2.Stdout = &b2
	cmd2.Stderr = &b2
	_ = cmd2.Run()
	t2 := strings.ToLower(b2.String())
	if strings.Contains(t2, "unknown command") {
		out["falcon_add_commit_and_carrier_tx"] = false
	} else {
		out["falcon_add_commit_and_carrier_tx"] = true
	}
	return out
}

func (s *Server) cachedSuchPQCProbe(ctx context.Context) map[string]any {
	s.suchProbeMu.Lock()
	defer s.suchProbeMu.Unlock()
	if s.suchProbeData != nil && time.Since(s.suchProbeAt) < 2*time.Minute {
		return s.suchProbeData
	}
	c := ctx
	if c == nil {
		c = context.Background()
	}
	c, cancel := context.WithTimeout(c, 6*time.Second)
	defer cancel()
	s.suchProbeData = s.probeSuchPQC(c)
	s.suchProbeAt = time.Now()
	return s.suchProbeData
}

// runSuchListUnspent tries to use a native libdogecoin/such unspent query command.
// If the current such build does not support it, caller should fallback to local sqlite parsing.
func (s *Server) runSuchListUnspent(address string, testnet bool) ([]ExplorerUTXO, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("empty address")
	}
	spvWalletDB := filepath.Join(s.storageDir, "spv_wallet.db")
	args := []string{
		"-c", "list_unspent",
		"-a", address,
		"-w", spvWalletDB,
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.suchPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("such list_unspent unavailable: %w — %s", err, truncateStr(out.String(), 600))
	}
	return parseSuchListUnspentOutput(out.String())
}

func parseSuchListUnspentOutput(raw string) ([]ExplorerUTXO, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty such list_unspent output")
	}
	if strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "[") {
		if utxos, err := parseSuchListUnspentJSON(raw); err == nil && len(utxos) > 0 {
			return utxos, nil
		}
	}
	var out []ExplorerUTXO
	for _, ln := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		m := reSuchUTXOLine.FindStringSubmatch(ln)
		if len(m) < 4 {
			continue
		}
		txid := normalizeTxid(m[1])
		if txid == "" {
			continue
		}
		vout, err := strconv.ParseUint(strings.TrimSpace(m[2]), 10, 32)
		if err != nil {
			continue
		}
		val, err := parseValueKoinu(m[3])
		if err != nil || val <= 0 {
			continue
		}
		out = append(out, ExplorerUTXO{
			TxID:  txid,
			Vout:  uint32(vout),
			Value: val,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("could not parse such list_unspent output")
	}
	return out, nil
}

func parseSuchListUnspentJSON(raw string) ([]ExplorerUTXO, error) {
	var anyRoot any
	if err := json.Unmarshal([]byte(raw), &anyRoot); err != nil {
		return nil, err
	}
	candidates := []any{anyRoot}
	if m, ok := anyRoot.(map[string]any); ok {
		for _, k := range []string{"utxos", "unspent", "rows", "data"} {
			if v, ok := m[k]; ok {
				candidates = append(candidates, v)
			}
		}
	}
	var out []ExplorerUTXO
	for _, c := range candidates {
		arr, ok := c.([]any)
		if !ok {
			continue
		}
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			txid := normalizeTxid(jsonStringAny(m["txid"]))
			if txid == "" {
				txid = normalizeTxid(jsonStringAny(m["tx_hash"]))
			}
			if txid == "" {
				continue
			}
			vout := uint32(parseIntDefault(jsonStringAny(m["vout"]), 0))
			val, err := parseValueKoinu(jsonStringAny(m["value"]))
			if err != nil || val <= 0 {
				val, err = parseValueKoinu(jsonStringAny(m["amount"]))
				if err != nil || val <= 0 {
					continue
				}
			}
			out = append(out, ExplorerUTXO{
				TxID:         txid,
				Vout:         vout,
				Value:        val,
				ScriptPubHex: strings.TrimSpace(jsonStringAny(m["script_pubkey"])),
			})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no json utxos parsed")
	}
	return out, nil
}

func sendtxExecTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("PUP_SENDTX_TIMEOUT_SEC")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 15 && n <= 300 {
			return time.Duration(n) * time.Second
		}
	}
	// Shorter default avoids the UI waiting minutes; libdogecoin still connects to many peers in parallel.
	return 90 * time.Second
}

// runSendtx broadcasts a signed raw hex transaction via libdogecoin P2P.
func (s *Server) runSendtx(signedHex string, testnet bool, peers string) (string, error) {
	signedHex = strings.TrimSpace(signedHex)
	if signedHex == "" {
		return "", errors.New("empty transaction hex")
	}
	txDeadline := sendtxExecTimeout()
	args := []string{}
	if testnet {
		args = append(args, "-t")
	}
	if peers != "" {
		args = append(args, "-i", peers)
	}
	args = append(args, signedHex)
	ctx, cancel := context.WithTimeout(context.Background(), txDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.sendtxPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("sendtx: %w — %s", err, truncateStr(out.String(), 800))
	}
	firstOut := strings.TrimSpace(out.String())
	sum := summarizeSendtxOutput(firstOut)
	if strings.TrimSpace(peers) == "" && sum.ConnectedNodes == 0 {
		fallbackPeers := sendtxFallbackPeers(testnet)
		if fallbackPeers != "" {
			args2 := []string{}
			if testnet {
				args2 = append(args2, "-t")
			}
			args2 = append(args2, "-i", fallbackPeers, signedHex)
			ctx2, cancel2 := context.WithTimeout(context.Background(), txDeadline)
			defer cancel2()
			cmd2 := exec.CommandContext(ctx2, s.sendtxPath(), args2...)
			var out2 bytes.Buffer
			cmd2.Stdout = &out2
			cmd2.Stderr = &out2
			if err2 := cmd2.Run(); err2 == nil {
				second := strings.TrimSpace(out2.String())
				if summarizeSendtxOutput(second).ConnectedNodes > 0 {
					return second, nil
				}
				return strings.TrimSpace(firstOut + "\n\n[retry-with-fallback-peers]\n" + second), nil
			}
		}
	}
	// sendtx prints minimal output on success; return combined for debugging
	return firstOut, nil
}

func sendtxFallbackPeers(testnet bool) string {
	if p := strings.TrimSpace(os.Getenv("PUP_SENDTX_PEERS")); p != "" {
		return p
	}
	if testnet {
		return ""
	}
	return strings.Join([]string{
		"54.224.206.12",
		"159.203.65.214",
		"138.201.221.250",
		"178.63.139.172",
		"93.243.63.136",
		"95.217.56.57",
	}, ",")
}

// extractSendtxDiagnosticLines returns non-progress lines from sendtx stdout/stderr merge:
// tool Error:/Warning: lines and any line that looks like a P2P/policy rejection (if printed).
func extractSendtxDiagnosticLines(txt string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, ln := range strings.Split(strings.ReplaceAll(txt, "\r\n", "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		low := strings.ToLower(ln)
		if strings.HasPrefix(low, "error:") || strings.HasPrefix(low, "warning:") {
			if _, ok := seen[ln]; !ok {
				seen[ln] = struct{}{}
				out = append(out, ln)
			}
			continue
		}
		if strings.Contains(low, "reject") ||
			strings.Contains(low, "misbehaving") ||
			strings.Contains(low, "non-standard") ||
			strings.Contains(low, "nonstandard") ||
			strings.Contains(low, "insufficient fee") ||
			strings.Contains(low, "bad-txns") ||
			strings.Contains(low, "txn-") ||
			strings.Contains(low, "too many") ||
			strings.Contains(low, "dust") {
			if _, ok := seen[ln]; !ok {
				seen[ln] = struct{}{}
				out = append(out, ln)
			}
		}
	}
	return out
}

func relayHeuristicErrorLine(diagnosticLines []string) string {
	for _, ln := range diagnosticLines {
		low := strings.ToLower(strings.TrimSpace(ln))
		if strings.HasPrefix(low, "error:") && strings.Contains(low, "relay") {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

type sendtxOutputSummary struct {
	BroadcastTxID         string   `json:"broadcast_txid,omitempty"`
	ConnectedNodes        int      `json:"connected_nodes"`
	InformedNodes         int      `json:"informed_nodes"`
	RequestedFromNodes    int      `json:"requested_from_nodes"`
	SeenOnOtherNodes      int      `json:"seen_on_other_nodes"`
	RelayBackReceived     bool     `json:"relay_back_received"`
	LikelyBroadcasted     bool     `json:"likely_broadcasted"`
	Status                string   `json:"status"` // success | warning | unknown
	HumanNote             string   `json:"human_note"`
	SendtxDiagnosticLines []string `json:"sendtx_diagnostic_lines,omitempty"`
	RelayHeuristicError   string   `json:"relay_heuristic_error,omitempty"`
	SendtxDiagnosticsNote string   `json:"sendtx_diagnostics_note,omitempty"`
}

func summarizeSendtxOutput(raw string) sendtxOutputSummary {
	txt := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	s := sendtxOutputSummary{
		Status:    "unknown",
		HumanNote: "sendtx output unavailable",
	}
	if txt == "" {
		return s
	}
	s.SendtxDiagnosticLines = extractSendtxDiagnosticLines(txt)
	s.RelayHeuristicError = relayHeuristicErrorLine(s.SendtxDiagnosticLines)
	if len(s.SendtxDiagnosticLines) > 0 {
		s.SendtxDiagnosticsNote = "Verbatim lines from sendtx combined stdout/stderr (not JSON-RPC). Use relay_heuristic_error to separate sendtx relay-back guesses from any real reject lines, if present."
	}
	lower := strings.ToLower(txt)
	connected := strings.Count(lower, "successfully connected to peer")
	sent := strings.Count(lower, "tx successfully sent to node")
	if m := reSendtxStartTxid.FindStringSubmatch(txt); len(m) >= 2 {
		s.BroadcastTxID = normalizeTxid(m[1])
	}
	s.ConnectedNodes = connected
	s.InformedNodes = parseCountAfterLabel(txt, "Informed nodes")
	s.RequestedFromNodes = parseCountAfterLabel(txt, "Requested from nodes")
	s.SeenOnOtherNodes = parseCountAfterLabel(txt, "Seen on other nodes")
	notRelayedBack := strings.Contains(lower, "transaction was not relayed back")
	s.RelayBackReceived = !notRelayedBack && (s.SeenOnOtherNodes > 0)
	s.LikelyBroadcasted = sent > 0 || s.InformedNodes > 0 || s.RequestedFromNodes > 0
	peerAccepted := s.InformedNodes > 0 && s.RequestedFromNodes > 0
	switch {
	case peerAccepted:
		s.Status = "success"
		s.HumanNote = "Broadcast accepted by peers (inv/getdata observed). Relay-back was not observed in this short sendtx window."
		if s.RelayHeuristicError != "" {
			s.HumanNote += " The sendtx tool also printed a relay-back heuristic (see relay_heuristic_error); that is not a Dogecoin Core reject string — peers already requested the tx (getdata)."
		}
	case s.LikelyBroadcasted && notRelayedBack:
		s.Status = "warning"
		s.HumanNote = "Broadcast reached peers, but no relay-back was observed in this short window. This often happens with already-seen or delayed-propagation transactions."
	case s.LikelyBroadcasted:
		s.Status = "success"
		s.HumanNote = "Broadcast reached peers over libdogecoin P2P."
	default:
		s.Status = "unknown"
		s.HumanNote = "sendtx did not confirm peer relay in output."
	}
	return s
}

func parseCountAfterLabel(text, label string) int {
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(ln), strings.ToLower(label)) {
			continue
		}
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		n := parseIntDefault(strings.TrimSpace(ln[i+1:]), 0)
		if n < 0 {
			return 0
		}
		return n
	}
	return 0
}

func (s *Server) spvLogPath() string {
	return filepath.Join(s.storageDir, "spv.log")
}

func (s *Server) spvPidPath() string {
	return filepath.Join(s.storageDir, "spv.pid")
}

func (s *Server) spvWatchAddrPath() string {
	return filepath.Join(s.storageDir, "spv_watch_addrs.txt")
}

func (s *Server) spvHTTPBaseURL() string {
	addr := strings.TrimSpace(os.Getenv("SPV_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + strings.TrimRight(addr, "/")
}

// spvnodeArgs builds argv for libdogecoin spvnode. We intentionally omit -f:
// with -f 0, spvnode treats headers as in-memory only and ignores -h, so
// headers.db never appears on disk and SQLite rollback cannot run.
func spvnodeArgs(testnet bool, addrs []string, storageDir string, useCheckpoint bool, httpAddr string) []string {
	args := []string{"-c", "-l"}
	if useCheckpoint {
		args = append(args, "-p")
	}
	httpAddr = strings.TrimSpace(httpAddr)
	if httpAddr != "" {
		args = append(args, "-u", httpAddr)
	}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		args = append(args, "-a", a)
	}
	args = append(args,
		"-w", filepath.Join(storageDir, "spv_wallet.db"),
		"-h", filepath.Join(storageDir, "headers.db"),
		"-b", "scan",
	)
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	return args
}

// startSPVNode launches spvnode in the background. Restarts when the watch address set changes.
func (s *Server) startSPVNode(w *WalletFile) {
	if strings.TrimSpace(os.Getenv("SPVNODE_ENABLE")) == "0" {
		return
	}
	addrs := w.AllDistinctP2PKHAddresses()
	if len(addrs) == 0 {
		return
	}
	if !s.readServicePrefs().SpvEnabled {
		s.spvStartMu.Lock()
		s.stopSPVNode()
		s.spvStartMu.Unlock()
		return
	}
	s.spvStartMu.Lock()
	defer s.spvStartMu.Unlock()
	want := strings.Join(addrs, "\n")
	prev, _ := os.ReadFile(s.spvWatchAddrPath())
	pidRunning := false
	if b, err := os.ReadFile(s.spvPidPath()); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && spvProcessAlive(pid) {
			pidRunning = true
		} else {
			_ = os.Remove(s.spvPidPath())
		}
	}
	hdb := filepath.Join(s.storageDir, "headers.db")
	headersOK := false
	if st, err := os.Stat(hdb); err == nil && !st.IsDir() {
		headersOK = true
	}
	if pidRunning && strings.TrimSpace(string(prev)) == want && headersOK {
		return
	}
	s.repairHeadersDBForSPVStart()
	if migrated, backup, err := s.migrateLegacyHeadersDB(); err != nil {
		log.Printf("[pq-wallet] legacy headers.db migrate failed: %v", err)
		return
	} else if migrated {
		log.Printf("[pq-wallet] migrated non-SQLite headers.db to %s", backup)
	}
	s.stopSPVNode()
	testnet := strings.EqualFold(w.Network, "testnet")
	var used []string
	list := addrs
	if len(list) == 0 {
		return
	}
	sort.Strings(list)
	logPath := s.spvLogPath()
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("[pq-wallet] spv log %s: %v", logPath, err)
		lf, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0600)
		if err != nil {
			log.Printf("[pq-wallet] spv output sink: %v", err)
			return
		}
	}
	prefs := s.readSPVSyncPrefs()
	httpAddr := strings.TrimSpace(os.Getenv("SPV_HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = "127.0.0.1:8080"
	}
	args := spvnodeArgs(testnet, list, s.storageDir, prefs.UseCheckpoint, httpAddr)
	cmd := exec.Command(s.spvnodePath(), args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		log.Printf("[pq-wallet] spvnode start addrs=%d: %v", len(list), err)
		return
	}
	started := cmd.Process
	used = list
	_, _ = fmt.Fprintf(lf, "\n--- spvnode started %s pid=%d watch_addrs=%d ---\n", time.Now().UTC().Format(time.RFC3339), started.Pid, len(used))
	_ = os.WriteFile(s.spvPidPath(), []byte(strconv.Itoa(started.Pid)), 0600)
	_ = os.WriteFile(s.spvWatchAddrPath(), []byte(strings.Join(used, "\n")), 0600)
	go func(proc *os.Process, logf *os.File) {
		_, _ = proc.Wait()
		_ = logf.Close()
	}(started, lf)
	log.Printf("[pq-wallet] spvnode pid=%d watch_addrs=%d", started.Pid, len(used))
}

func (s *Server) startSPVNodeFromWatchState() {
	st, err := s.loadSPVWatchState()
	if err != nil || st == nil || len(st.Addresses) == 0 {
		return
	}
	w := &WalletFile{
		Network: st.Network,
	}
	w.Addresses = make([]WalletAddress, 0, len(st.Addresses))
	for _, a := range st.Addresses {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		w.Addresses = append(w.Addresses, WalletAddress{P2PKH: a})
	}
	if len(w.Addresses) == 0 {
		return
	}
	s.startSPVNode(w)
}

func (s *Server) stopSPVNode() {
	b, err := os.ReadFile(s.spvPidPath())
	if err != nil {
		return
	}
	pid := strings.TrimSpace(string(b))
	if pid == "" {
		return
	}
	pidInt, _ := strconv.Atoi(pid)
	spvProcessTerminate(pidInt)
	_ = os.Remove(s.spvPidPath())
}

func (s *Server) readSPVStatus() map[string]any {
	pidPath := s.spvPidPath()
	logPath := s.spvLogPath()
	out := map[string]any{
		"libdogecoin_spvnode": s.spvnodePath(),
		"libdogecoin_such":    s.suchPath(),
		"libdogecoin_sendtx":  s.sendtxPath(),
		"pq_libdogecoin_bin":  strings.TrimSpace(os.Getenv("PQ_LIBDOGECOIN_BIN")),
		"pid_file":            pidPath,
		"log_file":            logPath,
		"storage_dir":         s.storageDir,
		"spv_http_url":        s.spvHTTPBaseURL(),
	}
	// Use a larger tail window so we can parse structured raw tx hex for older spends
	// and keep direction/amount enrichment stable across refreshes.
	if tail, err := readFileTail(logPath, 2*1024*1024); err == nil && strings.TrimSpace(tail) != "" {
		out["log_tail"] = tail
	}
	hdb := filepath.Join(s.storageDir, "headers.db")
	wdb := filepath.Join(s.storageDir, "spv_wallet.db")
	if _, err := os.Stat(hdb); err == nil {
		out["headers_db"] = hdb
		out["headers_db_present"] = true
		if isSQLiteDBFile(hdb) {
			out["headers_db_format"] = "sqlite"
		} else {
			out["headers_db_format"] = "legacy_non_sqlite"
		}
	} else {
		out["headers_db_present"] = false
	}
	prefs := s.readSPVSyncPrefs()
	out["use_checkpoint"] = prefs.UseCheckpoint
	out["spv_checkpoints"] = map[string]any{
		"mainnet": spvMainnetCheckpoints,
		"testnet": spvTestnetCheckpoints,
		"note":    "With use_checkpoint true, spvnode -p lets libdogecoin pick one row from this table by timestamp (wallet scan window), not a single fixed menu index.",
	}
	if _, err := os.Stat(wdb); err == nil {
		out["spv_wallet_db"] = wdb
		out["spv_wallet_db_present"] = true
	} else {
		out["spv_wallet_db_present"] = false
	}
	if rawTip, err := s.fetchSPVREST("/getChaintip"); err == nil {
		h, bh := parseSPVRESTChaintip(rawTip)
		if h > 0 {
			out["header_height"] = h
		}
		if bh != "" {
			out["best_block_hash"] = bh
		}
	}
	if rawTS, err := s.fetchSPVREST("/getTimestamp"); err == nil {
		if ts := parseSPVRESTTimestamp(rawTS); ts > 0 {
			out["header_unix_time"] = ts
		}
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		out["running"] = false
		out["hint"] = "SPV process not started yet. It starts automatically after the wallet is created (or restart the pup)."
		return out
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	out["pid"] = pid
	if pid > 0 {
		out["running"] = spvProcessAlive(pid)
	}
	return out
}

func (s *Server) broadcastLogPath() string {
	return filepath.Join(s.storageDir, "broadcast.log")
}

func (s *Server) appendBroadcastLogLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	p := s.broadcastLogPath()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("[pq-wallet] broadcast log: %v", err)
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

// logBroadcastDetails appends signed raw hex (optional) and every line of sendtx output to broadcast.log.
// Set PUP_BROADCAST_LOG_SIGNED_HEX=0 to omit hex (privacy / huge txs). Default logs full hex in chunks.
func (s *Server) logBroadcastDetails(sourceTag, txid string, signedHex string, sendOut string, execErr error) {
	if s == nil || s.storageDir == "" {
		return
	}
	tag := strings.TrimSpace(sourceTag)
	if tag == "" {
		tag = "broadcast"
	}
	hex := strings.TrimSpace(signedHex)
	txid = normalizeTxid(strings.TrimSpace(txid))
	if execErr != nil {
		s.appendBroadcastLogLine(fmt.Sprintf("%s txid=%s signed_hex_len=%d exec_err=%q", tag, txid, len(hex), execErr.Error()))
	} else {
		s.appendBroadcastLogLine(fmt.Sprintf("%s txid=%s signed_hex_len=%d", tag, txid, len(hex)))
	}
	if strings.TrimSpace(os.Getenv("PUP_BROADCAST_LOG_SIGNED_HEX")) != "0" && hex != "" {
		const chunk = 12000
		for off := 0; off < len(hex); off += chunk {
			end := off + chunk
			if end > len(hex) {
				end = len(hex)
			}
			s.appendBroadcastLogLine(fmt.Sprintf("%s SIGNED_RAW_HEX off=%d len=%d %s", tag, off, end-off, hex[off:end]))
		}
	}
	for _, ln := range strings.Split(strings.ReplaceAll(sendOut, "\r\n", "\n"), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.TrimSpace(ln) == "" {
			continue
		}
		s.appendBroadcastLogLine(tag + " sendtx| " + ln)
	}
}

// logBroadcastPaymentHint stores wallet-known destination info for an outgoing tx.
// This lets tx list rendering show Dogecoin Wallet-style "paid to X amount Y"
// without requiring full transaction fetches.
func (s *Server) logBroadcastPaymentHint(sourceTag, txid, toAddress string, amountDOGE float64) {
	if s == nil || s.storageDir == "" {
		return
	}
	txid = normalizeTxid(strings.TrimSpace(txid))
	toAddress = strings.TrimSpace(toAddress)
	if txid == "" || toAddress == "" || amountDOGE <= 0 {
		return
	}
	tag := strings.TrimSpace(sourceTag)
	if tag == "" {
		tag = "broadcast"
	}
	s.appendBroadcastLogLine(fmt.Sprintf("%s PAYMENT_HINT txid=%s to=%s amount_doge=%.8f", tag, txid, toAddress, amountDOGE))
}

// logBroadcastSpentPrevoutHints maps each consumed wallet UTXO (prev txid:vout) to
// the final spending tx + destination metadata. This allows SPV /getTransactions spent rows
// (which are keyed by prevout txid:vout) to be shown as OUT with correct pay-to in UI.
func (s *Server) logBroadcastSpentPrevoutHints(sourceTag, spendTxid, toAddress string, amountDOGE float64, used []ExplorerUTXO) {
	if s == nil || s.storageDir == "" {
		return
	}
	spendTxid = normalizeTxid(strings.TrimSpace(spendTxid))
	toAddress = strings.TrimSpace(toAddress)
	if spendTxid == "" || toAddress == "" || amountDOGE <= 0 || len(used) == 0 {
		return
	}
	tag := strings.TrimSpace(sourceTag)
	if tag == "" {
		tag = "broadcast"
	}
	for _, u := range used {
		prevTxid := normalizeTxid(strings.TrimSpace(u.TxID))
		if prevTxid == "" {
			continue
		}
		s.appendBroadcastLogLine(fmt.Sprintf("%s SPENT_PREVOUT_HINT prev_txid=%s prev_vout=%d spend_txid=%s to=%s amount_doge=%.8f",
			tag, prevTxid, u.Vout, spendTxid, toAddress, amountDOGE))
	}
}

// logBroadcastPQSafeSummary records PQ carrier / TX_R outcome on broadcast.log for later diagnosis
// (same fields as the send_pq_safe JSON response, without needing the HTTP client).
func (s *Server) logBroadcastPQSafeSummary(
	sourceTag, txCTxid, pqMode string,
	pqCommitment, carrierFlow, pqRevealRequested, carrierEnvDisabled, falconSigPresent, econDowngraded bool,
	pqCarrierExtendErr, txRID, txRErr, pqRevealSkipReason string,
	pqMkParts int,
) {
	if s == nil || s.storageDir == "" {
		return
	}
	tag := strings.TrimSpace(sourceTag)
	if tag == "" {
		tag = "broadcast"
	}
	oneLine := func(s string) string {
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.TrimSpace(s)
		if len(s) > 500 {
			return s[:500] + "…"
		}
		return s
	}
	txCTxid = normalizeTxid(strings.TrimSpace(txCTxid))
	txRID = normalizeTxid(strings.TrimSpace(txRID))
	pqMode = strings.TrimSpace(pqMode)
	s.appendBroadcastLogLine(fmt.Sprintf(
		"%s PQ_STATUS tx_c_txid=%s pq_mode=%s pq_commitment=%t pq_carrier_flow=%t pq_reveal_requested=%t carrier_env_disabled=%t falcon_sig_present=%t econ_downgraded=%t carrier_extend_err=%q tx_r_txid=%s tx_r_mkpart_parts=%d tx_r_err=%q pq_reveal_skip_reason=%q",
		tag, txCTxid, pqMode, pqCommitment, carrierFlow, pqRevealRequested, carrierEnvDisabled, falconSigPresent, econDowngraded,
		oneLine(pqCarrierExtendErr), txRID, pqMkParts, oneLine(txRErr), oneLine(pqRevealSkipReason),
	))
}

func readFileTail(path string, max int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) <= max {
		return string(b), nil
	}
	return string(b[len(b)-max:]), nil
}
