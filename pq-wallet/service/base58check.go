package main

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

// Dogecoin P2PKH address decoding uses Bitcoin-style base58check (no btcsuite dependency).

var (
	errBase58Checksum = errors.New("base58check: checksum mismatch")
	errBase58Format   = errors.New("base58check: invalid format")
	errBase58Char     = errors.New("base58check: invalid character")
)

const alphabetIdx0 = '1'

var b58 = [256]byte{
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 255, 255, 255, 255, 255, 255, 255,
	255, 0, 1, 2, 3, 4, 5, 6,
	7, 8, 255, 255, 255, 255, 255, 255,
	255, 9, 10, 11, 12, 13, 14, 15,
	16, 255, 17, 18, 19, 20, 21, 255,
	22, 23, 24, 25, 26, 27, 28, 29,
	30, 31, 32, 255, 255, 255, 255, 255,
	255, 33, 34, 35, 36, 37, 38, 39,
	40, 41, 42, 43, 255, 44, 45, 46,
	47, 48, 49, 50, 51, 52, 53, 54,
	55, 56, 57, 255, 255, 255, 255, 255,
}

var bigRadix = [...]*big.Int{
	big.NewInt(0),
	big.NewInt(58),
	big.NewInt(58 * 58),
	big.NewInt(58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
}

func base58Decode(b string) ([]byte, error) {
	answer := big.NewInt(0)
	scratch := new(big.Int)
	for t := b; len(t) > 0; {
		n := len(t)
		if n > 10 {
			n = 10
		}
		var total uint64
		for _, v := range t[:n] {
			if v > 255 {
				return nil, errBase58Char
			}
			tmp := b58[v]
			if tmp == 255 {
				return nil, errBase58Char
			}
			total = total*58 + uint64(tmp)
		}
		answer.Mul(answer, bigRadix[n])
		scratch.SetUint64(total)
		answer.Add(answer, scratch)
		t = t[n:]
	}
	tmpval := answer.Bytes()
	var numZeros int
	for numZeros = 0; numZeros < len(b); numZeros++ {
		if b[numZeros] != alphabetIdx0 {
			break
		}
	}
	flen := numZeros + len(tmpval)
	val := make([]byte, flen)
	copy(val[numZeros:], tmpval)
	return val, nil
}

func base58Checksum(versionAndPayload []byte) [4]byte {
	h := sha256.Sum256(versionAndPayload)
	h2 := sha256.Sum256(h[:])
	var c [4]byte
	copy(c[:], h2[:4])
	return c
}

// base58CheckDecode verifies base58check and returns the 20-byte pubkey hash and address version byte.
func base58CheckDecode(input string) (payload []byte, version byte, err error) {
	decoded, err := base58Decode(input)
	if err != nil {
		return nil, 0, err
	}
	if len(decoded) < 5 {
		return nil, 0, errBase58Format
	}
	version = decoded[0]
	var cksum [4]byte
	copy(cksum[:], decoded[len(decoded)-4:])
	if base58Checksum(decoded[:len(decoded)-4]) != cksum {
		return nil, 0, errBase58Checksum
	}
	payload = append(payload, decoded[1:len(decoded)-4]...)
	return payload, version, nil
}
