package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

const (
	minRelayFeeKoinu = int64(100000) // 0.001 DOGE — rough minimum fee for relay
	dustLimitKoinu   = int64(100000)
)

func dogeAmountStringToKoinu(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok {
		return 0, fmt.Errorf("invalid amount")
	}
	k := new(big.Rat).Mul(r, big.NewRat(1e8, 1))
	if !k.IsInt() {
		return 0, fmt.Errorf("amount has too many decimal places")
	}
	i := k.Num().Int64()
	if i <= 0 {
		return 0, fmt.Errorf("amount must be positive")
	}
	return i, nil
}

func selectUTXOs(utxos []ExplorerUTXO, need int64) ([]ExplorerUTXO, int64, error) {
	if need <= 0 {
		return nil, 0, fmt.Errorf("invalid need")
	}
	sort.Slice(utxos, func(i, j int) bool { return utxos[i].Value > utxos[j].Value })
	var pick []ExplorerUTXO
	var sum int64
	for _, u := range utxos {
		pick = append(pick, u)
		sum += u.Value
		if sum >= need {
			return pick, sum, nil
		}
	}
	return nil, sum, fmt.Errorf("insufficient balance: need %d koinu, have %d", need, sum)
}

// buildUnsignedDogeP2PKH creates an unsigned legacy transaction spending selected P2PKH UTXOs.
// recipientValue and changeValue are in koinu. pqCommitment adds optional OP_RETURN output when non-empty (hex pubkey material to hash).
func buildUnsignedDogeP2PKH(
	selected []ExplorerUTXO,
	recipientScript []byte,
	recipientValue int64,
	changeScript []byte,
	changeValue int64,
	pqCommitmentPubHex string,
) ([]byte, error) {
	if recipientValue <= 0 {
		return nil, fmt.Errorf("recipient value must be positive")
	}
	var outputs []txOutWire
	outputs = append(outputs, txOutWire{Value: recipientValue, PkScript: recipientScript})
	if changeValue > dustLimitKoinu {
		outputs = append(outputs, txOutWire{Value: changeValue, PkScript: changeScript})
	}
	pqHex := strings.TrimSpace(strings.ToLower(pqCommitmentPubHex))
	if pqHex != "" {
		b, err := hex.DecodeString(pqHex)
		if err == nil && len(b) > 0 {
			h := sha256.Sum256(b)
			scr := append([]byte{0x6a, 0x20}, h[:]...)
			outputs = append(outputs, txOutWire{Value: 0, PkScript: scr})
		}
	}
	return serializeUnsignedLegacyP2PKHTx(selected, outputs, 0)
}

func hexMsgTx(txb []byte) string {
	return hex.EncodeToString(txb)
}
