package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reSuchFalconPub = regexp.MustCompile(`(?i)public key:\s*([0-9a-f]+)`)
	reSuchFalconSec = regexp.MustCompile(`(?i)secret key:\s*([0-9a-f]+)`)
	reSuchSignedTx  = regexp.MustCompile(`(?i)signed TX:\s*([0-9a-f]+)`)

	reSuchPrivWIF     = regexp.MustCompile(`(?i)private key wif:\s*(\S+)`)
	reSuchPubKeyHex   = regexp.MustCompile(`(?i)public key hex:\s*([0-9a-f]+)`)
	reSuchP2PKHAddr   = regexp.MustCompile(`(?i)p2pkh address:\s*(\S+)`)
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

func (s *Server) suchPath() string {
	if p := strings.TrimSpace(os.Getenv("LIBDOGECOIN_SUCH")); p != "" {
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
	if p, err := exec.LookPath("sendtx"); err == nil {
		return p
	}
	return "sendtx"
}

func (s *Server) spvnodePath() string {
	if p := strings.TrimSpace(os.Getenv("LIBDOGECOIN_SPVNODE")); p != "" {
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

// runSendtx broadcasts a signed raw hex transaction via libdogecoin P2P.
func (s *Server) runSendtx(signedHex string, testnet bool, peers string) (string, error) {
	signedHex = strings.TrimSpace(signedHex)
	if signedHex == "" {
		return "", errors.New("empty transaction hex")
	}
	args := []string{}
	if testnet {
		args = append(args, "-t")
	}
	if peers != "" {
		args = append(args, "-i", peers)
	}
	args = append(args, signedHex)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.sendtxPath(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("sendtx: %w — %s", err, truncateStr(out.String(), 800))
	}
	// sendtx prints minimal output on success; return combined for debugging
	return strings.TrimSpace(out.String()), nil
}

func (s *Server) spvLogPath() string {
	return filepath.Join(s.storageDir, "spv.log")
}

func (s *Server) spvPidPath() string {
	return filepath.Join(s.storageDir, "spv.pid")
}

// startSPVNode launches spvnode in the background (one-shot; ignores error if already running).
func (s *Server) startSPVNode(w *WalletFile) {
	if strings.TrimSpace(os.Getenv("SPVNODE_ENABLE")) == "0" {
		return
	}
	if b, err := os.ReadFile(s.spvPidPath()); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil {
			return
		}
		_ = os.Remove(s.spvPidPath())
	}
	addr := strings.TrimSpace(w.P2PKHAddress)
	if addr == "" {
		return
	}
	testnet := strings.EqualFold(w.Network, "testnet")
	args := []string{
		"-f", "0", "-c", "-l",
		"-a", addr,
		"-w", filepath.Join(s.storageDir, "spv_wallet.db"),
		"-h", filepath.Join(s.storageDir, "headers.db"),
		"-b", "scan",
	}
	if testnet {
		args = append([]string{"-t"}, args...)
	}
	logPath := s.spvLogPath()
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("[pq-wallet] spv log: %v", err)
		return
	}
	cmd := exec.Command(s.spvnodePath(), args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		log.Printf("[pq-wallet] spvnode start: %v", err)
		return
	}
	_ = os.WriteFile(s.spvPidPath(), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
	}()
	log.Printf("[pq-wallet] spvnode pid=%d", cmd.Process.Pid)
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
	_ = exec.Command("kill", "-TERM", pid).Run()
	_ = os.Remove(s.spvPidPath())
}

func (s *Server) readSPVStatus() map[string]any {
	pidPath := s.spvPidPath()
	logPath := s.spvLogPath()
	out := map[string]any{
		"libdogecoin_spvnode": s.spvnodePath(),
		"pid_file":            pidPath,
		"log_file":            logPath,
	}
	// Expose spv.log tail whenever the file exists so peer / height parsing works even if pid is stale.
	if lb, err := readFileTail(logPath, 512*1024); err == nil {
		out["log_tail"] = lb
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
		if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
			out["running"] = false
		} else {
			out["running"] = true
		}
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
