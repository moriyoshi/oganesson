package tlsroots

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// TestPEMs: the embedded Mozilla bundle is present and every entry is a
// parseable certificate; PEMs returns a copy callers cannot mutate.
func TestPEMs(t *testing.T) {
	pems := PEMs()
	if len(pems) < 100 {
		t.Fatalf("got %d root PEMs, want the full Mozilla bundle (>100)", len(pems))
	}
	for i, p := range pems {
		blk, _ := pem.Decode([]byte(p))
		if blk == nil || blk.Type != "CERTIFICATE" {
			t.Fatalf("PEM %d is not a CERTIFICATE block: %q", i, p)
		}
		if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
			t.Fatalf("PEM %d does not parse: %v", i, err)
		}
	}
	pems[0] = "mutated"
	if PEMs()[0] == "mutated" {
		t.Error("PEMs() handed out the cached slice, not a copy")
	}
}

// TestPoolVerifiesEmbeddedRoot: the embedded roots form a usable trust store —
// a root from the bundle verifies against Pool() (it is self-signed and in the
// pool). Proves the roots are wired for verification, not merely parseable.
func TestPoolVerifiesEmbeddedRoot(t *testing.T) {
	pool := Pool()
	if pool == nil {
		t.Fatal("Pool() is nil")
	}
	for _, p := range PEMs() {
		blk, _ := pem.Decode([]byte(p))
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil || !cert.IsCA {
			continue
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err == nil {
			return
		}
	}
	t.Fatal("no embedded root verified against Pool()")
}
