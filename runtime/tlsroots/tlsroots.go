// Package tlsroots embeds Mozilla's CA trust store (via
// golang.org/x/crypto/x509roots/fallback) so TLS verification works on a
// minimal OS image that ships no system trust store — the blank import installs
// the roots through x509.SetFallbackRoots, which Go's crypto/x509 uses when it
// finds no system roots, covering fetch over HTTPS and the native pg/mysql TLS
// paths. The same roots are exposed as PEM for node:tls's rootCertificates,
// which is otherwise empty (unenv's stub), matching Node's bundled list.
package tlsroots

import (
	"crypto/x509"
	"encoding/pem"
	"sync"

	_ "golang.org/x/crypto/x509roots/fallback" // installs x509.SetFallbackRoots
	"golang.org/x/crypto/x509roots/fallback/bundle"
)

var (
	once sync.Once
	pems []string
	pool *x509.CertPool
)

func load() {
	once.Do(func() {
		pool = x509.NewCertPool()
		for r := range bundle.Roots() {
			pems = append(pems, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.Certificate})))
			cert, err := x509.ParseCertificate(r.Certificate)
			if err != nil {
				continue
			}
			if r.Constraint == nil {
				pool.AddCert(cert)
			} else {
				pool.AddCertWithConstraint(cert, r.Constraint)
			}
		}
	})
}

// PEMs returns the embedded root certificates, each a PEM-encoded block, for
// node:tls's rootCertificates. The slice is freshly copied so callers cannot
// mutate the cached list.
func PEMs() []string {
	load()
	return append([]string(nil), pems...)
}

// Pool returns a CertPool of the embedded roots (name constraints applied as the
// upstream bundle records them).
func Pool() *x509.CertPool {
	load()
	return pool
}
