package natives

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestPGRegisterExtractTLS verifies the Go side of pg's ssl: { ca } support:
// registerTLS builds a verifying TLS config keyed by a content hash, and
// extractTLS pulls the og-internal `ogtls` parameter back out (leaving pgx a
// clean DSN) and returns that config. No server is contacted — this pins the
// config construction, as PGlite cannot run here to exercise the handshake.
func TestPGRegisterExtractTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "pg ca"},
		NotBefore:             time.Unix(0, 0),
		NotAfter:              time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	s := NewPostgres()
	id, err := s.registerTLS(caPEM, "db.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "og-") {
		t.Fatalf("key %q, want an og- prefix", id)
	}
	if id2, _ := s.registerTLS(caPEM, "db.example.com"); id2 != id {
		t.Errorf("content-hash key not stable: %q vs %q", id2, id)
	}

	clean, cfg := s.extractTLS("postgres://u@h:5432/db?sslmode=require&ogtls=" + id)
	if strings.Contains(clean, "ogtls") {
		t.Errorf("ogtls not stripped from the DSN pgx parses: %q", clean)
	}
	if !strings.Contains(clean, "sslmode=require") {
		t.Errorf("sslmode dropped: %q", clean)
	}
	if cfg == nil || cfg.RootCAs == nil || cfg.ServerName != "db.example.com" || cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("tls config built wrong: %+v", cfg)
	}

	// No ogtls: DSN unchanged, nil config.
	if c, tc := s.extractTLS("postgres://u@h/db?sslmode=require"); tc != nil || c != "postgres://u@h/db?sslmode=require" {
		t.Errorf("no-ogtls extract changed things: %q %v", c, tc)
	}
	// An invalid CA is rejected.
	if _, err := s.registerTLS("not a certificate", ""); err == nil {
		t.Error("registerTLS accepted an invalid CA PEM")
	}
}
