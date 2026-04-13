package main

import (
	"crypto/sha256"
	"errors"
	"math/big"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(b []byte) string {
	x := new(big.Int).SetBytes(b)
	if x.Sign() == 0 {
		return string(base58Alphabet[0])
	}
	base := big.NewInt(58)
	zero := big.NewInt(0)
	var out []byte
	for x.Cmp(zero) > 0 {
		mod := new(big.Int)
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, v := range b {
		if v != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58CheckEncode(version byte, payload []byte) string {
	buf := make([]byte, 0, 1+len(payload)+4)
	buf = append(buf, version)
	buf = append(buf, payload...)
	h := sha256.Sum256(buf)
	h2 := sha256.Sum256(h[:])
	buf = append(buf, h2[0:4]...)
	return base58Encode(buf)
}

func dogeAddressFromScript(script []byte, network string) (kind string, address string) {
	test := network == "testnet"
	switch {
	case len(script) == 25 && script[0] == 0x76 && script[1] == 0xa9 && script[2] == 0x14 && script[23] == 0x88 && script[24] == 0xac:
		ver := byte(0x1e)
		if test {
			ver = 0x71
		}
		return "p2pkh", base58CheckEncode(ver, script[3:23])
	case len(script) == 23 && script[0] == 0xa9 && script[1] == 0x14 && script[22] == 0x87:
		ver := byte(0x16)
		if test {
			ver = 0xc4
		}
		return "p2sh", base58CheckEncode(ver, script[2:22])
	case len(script) == 22 && script[0] == 0x00 && script[1] == 0x14:
		return "p2wpkh", "" // bech32 not implemented; show hash
	default:
		return "other", ""
	}
}

func satsToDoge(sats int64) string {
	if sats < 0 {
		return "0"
	}
	whole := sats / 100000000
	frac := sats % 100000000
	if frac == 0 {
		return formatInt(whole)
	}
	return formatInt(whole) + "." + pad9(frac)
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append(b, byte('0'+n%10))
		n /= 10
	}
	if neg {
		b = append(b, '-')
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func pad9(n int64) string {
	s := formatInt(n)
	for len(s) < 8 {
		s = "0" + s
	}
	return s
}

var errShort = errors.New("truncated transaction bytes")
