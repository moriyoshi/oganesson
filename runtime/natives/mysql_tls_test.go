package natives_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/moriyoshi/oganesson/runtime/natives"
)

// genCA makes a self-signed CA, returning its parsed cert, key and PEM.
func genCA(t *testing.T, cn string, serial int64) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: cn},
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return cert, key, caPEM
}

// mysqlServerTLS makes a server cert for 127.0.0.1/localhost signed by caCert,
// returning a server TLS config.
func mysqlServerTLS(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) *tls.Config {
	t.Helper()
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(100),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}}
}

// startMySQLTLS serves go-mysql-server with the given server TLS config.
func startMySQLTLS(t *testing.T, tlsCfg *tls.Config) string {
	t.Helper()
	db := memory.NewDatabase("og")
	db.EnablePrimaryKeyIndexes()
	pro := memory.NewDBProvider(db)
	eng := sqle.NewDefault(pro)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(
		server.Config{Protocol: "tcp", Address: ln.Addr().String(), Listener: ln, TLSConfig: tlsCfg},
		eng, sql.NewContext, memory.NewSessionBuilder(pro), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Start() }()
	t.Cleanup(func() { _ = s.Close() })
	return "mysql://root@" + ln.Addr().String() + "/og"
}

// TestMySQLTLSVerify drives mysql2's ssl: { ca, rejectUnauthorized: true }
// against a TLS go-mysql-server: the server's own CA verifies and connects; an
// unrelated (valid but wrong) CA fails the handshake, so the connection is
// rejected rather than silently trusted.
func TestMySQLTLSVerify(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	caCert, caKey, goodCA := genCA(t, "og good CA", 1)
	_, _, otherCA := genCA(t, "og other CA", 2)
	url := startMySQLTLS(t, mysqlServerTLS(t, caCert, caKey))
	got := run(t, svc, map[string]any{"URL": url, "GOOD": goodCA, "OTHER": otherCA}, `
import mysql from "mysql2/promise";
globalThis.result = (async () => {
  const out = [];
  const ok = await mysql.createConnection({ uri: URL, ssl: { ca: GOOD, rejectUnauthorized: true } });
  const [r] = await ok.query("SELECT 1 AS one");
  out.push("good: " + r[0].one);
  await ok.end();
  try {
    const bad = await mysql.createConnection({ uri: URL, ssl: { ca: OTHER, rejectUnauthorized: true } });
    await bad.query("SELECT 1");
    out.push("bad: connected");
  } catch (e) {
    out.push("bad: rejected");
  }
  return out.join("|");
})();
`)
	want := "good: 1|bad: rejected"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
