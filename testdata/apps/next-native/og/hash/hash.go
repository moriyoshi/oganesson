// Package hash is og:hash, Go functions the app calls.
package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Digest is a SHA-256 digest.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Hex       string `json:"hex"`
	Size      int    `json:"size"`
}

// Sum digests s.
//
//og:name sha256
func Sum(s string) (Digest, error) {
	if s == "" {
		return Digest{}, errors.New("nothing to digest")
	}
	h := sha256.Sum256([]byte(s))
	return Digest{Algorithm: "sha256", Hex: hex.EncodeToString(h[:]), Size: len(s)}, nil
}

// Raw is the digest of b as bytes.
func Raw(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
