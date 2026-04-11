// P2PKH scriptPubKey hex for `such -c sign -s` from libdogecoin pubkey hex (SHA256 + RIPEMD-160, standard template).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/ripemd160"
)

// p2pkhScriptPubKeyHexFromCompressedPubKeyHex builds the legacy P2PKH scriptPubKey hex for signing
// (same bytes libdogecoin uses with `such -c sign -s`). The pubkey bytes must be the ones libdogecoin
// produced (`such -c generate_public_key`), not a re-derived key from another stack.
func p2pkhScriptPubKeyHexFromCompressedPubKeyHex(pubHex string) (string, error) {
	pubHex = strings.TrimSpace(strings.ToLower(pubHex))
	if pubHex == "" {
		return "", fmt.Errorf("empty public key hex")
	}
	b, err := hex.DecodeString(pubHex)
	if err != nil {
		return "", err
	}
	if len(b) != 33 && len(b) != 65 {
		return "", fmt.Errorf("pubkey must be 33 (compressed) or 65 (uncompressed) bytes, got %d", len(b))
	}
	first := sha256.Sum256(b)
	r := ripemd160.New()
	_, _ = r.Write(first[:])
	h := r.Sum(nil)
	return fmt.Sprintf("76a914%x88ac", h), nil
}
