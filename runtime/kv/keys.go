package kv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
)

// KeyPart is one element of a tuple key: []byte, string, float64,
// *big.Int or bool (Deno KV's key part types).
type KeyPart any

// Type tags, ordered as Deno KV orders key part types.
const (
	tagBytes  = 0x01
	tagString = 0x02
	tagNumber = 0x03
	tagBigInt = 0x04
	tagFalse  = 0x05
	tagTrue   = 0x06
)

// EncodeKey maps a tuple key to bytes whose lexicographic order is the
// key order, and in which a key prefix encodes to a byte prefix.
func EncodeKey(parts []KeyPart) ([]byte, error) {
	var b bytes.Buffer
	for _, p := range parts {
		switch v := p.(type) {
		case []byte:
			b.WriteByte(tagBytes)
			writeEscaped(&b, v)
		case string:
			b.WriteByte(tagString)
			writeEscaped(&b, []byte(v))
		case float64:
			b.WriteByte(tagNumber)
			bits := math.Float64bits(v)
			if math.IsNaN(v) {
				bits = 0x7ff8000000000000 // canonical NaN
			}
			// Order-preserving float encoding: flip all bits of negatives,
			// only the sign bit of positives.
			if bits&(1<<63) != 0 {
				bits = ^bits
			} else {
				bits |= 1 << 63
			}
			var tmp [8]byte
			binary.BigEndian.PutUint64(tmp[:], bits)
			b.Write(tmp[:])
		case *big.Int:
			b.WriteByte(tagBigInt)
			mag := v.Bytes()
			if len(mag) > 255 {
				return nil, fmt.Errorf("kv: bigint key part too large")
			}
			if v.Sign() < 0 {
				// Negative: sign 0, shorter magnitudes sort after longer ones,
				// inverted magnitude bytes.
				b.WriteByte(0)
				b.WriteByte(^byte(len(mag)))
				for _, c := range mag {
					b.WriteByte(^c)
				}
			} else {
				b.WriteByte(1)
				b.WriteByte(byte(len(mag)))
				b.Write(mag)
			}
		case bool:
			if v {
				b.WriteByte(tagTrue)
			} else {
				b.WriteByte(tagFalse)
			}
		default:
			return nil, fmt.Errorf("kv: unsupported key part type %T", p)
		}
	}
	return b.Bytes(), nil
}

// writeEscaped writes data terminated by 0x00, escaping embedded zeros as
// 0x00 0xFF so that ordering and prefixes are preserved.
func writeEscaped(b *bytes.Buffer, data []byte) {
	for _, c := range data {
		b.WriteByte(c)
		if c == 0 {
			b.WriteByte(0xFF)
		}
	}
	b.WriteByte(0)
}
