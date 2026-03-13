package crypto

import (
	"fmt"
	"math/big"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

var big62 = big.NewInt(62)

// Base62Encode encodes data as a fixed-length base62 string.
// The result is left-padded with '0' (index 0) to reach fixedLen.
func Base62Encode(data []byte, fixedLen int) string {
	n := new(big.Int).SetBytes(data)
	buf := make([]byte, fixedLen)
	for i := range buf {
		buf[i] = base62Alphabet[0]
	}
	pos := fixedLen - 1
	for n.Sign() > 0 {
		if pos < 0 {
			panic("base62: fixedLen too small for data")
		}
		var rem big.Int
		n.DivMod(n, big62, &rem)
		buf[pos] = base62Alphabet[rem.Int64()]
		pos--
	}
	return string(buf)
}

// Base62Decode decodes a base62 string back to bytes, left-padded to expectedByteLen.
func Base62Decode(s string, expectedByteLen int) ([]byte, error) {
	n := new(big.Int)
	for _, c := range s {
		idx := base62Index(byte(c))
		if idx < 0 {
			return nil, fmt.Errorf("invalid base62 character: %c", c)
		}
		n.Mul(n, big62)
		n.Add(n, big.NewInt(int64(idx)))
	}
	raw := n.Bytes()
	if len(raw) > expectedByteLen {
		return nil, fmt.Errorf("decoded length %d exceeds expected %d", len(raw), expectedByteLen)
	}
	if len(raw) == expectedByteLen {
		return raw, nil
	}
	padded := make([]byte, expectedByteLen)
	copy(padded[expectedByteLen-len(raw):], raw)
	return padded, nil
}

func base62Index(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 36
	default:
		return -1
	}
}
