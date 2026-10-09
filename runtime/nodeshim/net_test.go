package nodeshim_test

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
)

// netServers are the peers of the node:net / node:tls scenario: an echo
// server (greets, echoes upper-cased, says "bye" after the client's FIN), a
// silent one, the echo server over TLS (a self-signed certificate for
// localhost and 127.0.0.1, ALPN "og"), one that upgrades after "SSL?" as
// Postgres does after an SSLRequest, and a port nothing listens on.
type netServers struct {
	echo, silent, tls, starttls, closed int
	caPEM                               string
	cert                                tls.Certificate
}

func echo(c net.Conn) {
	defer c.Close()
	_, _ = c.Write([]byte("hello\n"))
	buf := make([]byte, 1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			_, _ = c.Write(bytes.ToUpper(buf[:n]))
		}
		if err != nil {
			_, _ = c.Write([]byte("bye"))
			return
		}
	}
}

func serve(t *testing.T, ln net.Listener, h func(net.Conn)) int {
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func startNetServers(t *testing.T) *netServers {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x0611),
		Subject:      pkix.Name{CommonName: "og-test", Organization: []string{"Oganesson"}},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"og"},
	}
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		return ln
	}
	s := &netServers{caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), cert: cfg.Certificates[0]}
	s.echo = serve(t, listen(), echo)
	s.silent = serve(t, listen(), func(c net.Conn) { _, _ = io.Copy(io.Discard, c); c.Close() })
	s.tls = serve(t, tls.NewListener(listen(), cfg), echo)
	s.starttls = serve(t, listen(), func(c net.Conn) {
		r := bufio.NewReader(c)
		req := make([]byte, 4)
		if _, err := io.ReadFull(r, req); err != nil || string(req) != "SSL?" {
			c.Close()
			return
		}
		_, _ = c.Write([]byte("S"))
		echo(tls.Server(c, cfg))
	})
	ln := listen()
	s.closed = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return s
}

// netScenario exercises node:net and node:tls against netServers (ports
// and the CA in globals). Its expected output is Node 24's.
const netScenario = `
const net = require("net");
const tls = require("tls");
const log = [];
// Each step ends within 3 s, logging TIMEOUT rather than hanging.
let step = 0;
const done = (fn) => new Promise((resolve) => {
  const n = ++step;
  const guard = setTimeout(() => { log.push("step " + n + ": TIMEOUT"); resolve(); }, 3000);
  fn(() => { clearTimeout(guard); resolve(); });
});
const certLine = (c) => c.subject.CN + "/" + c.subject.O + " " + c.subjectaltname + " " + c.valid_from + " " + c.serialNumber;

async function main() {
  await done((resolve) => {
    const s = net.connect(PORTS.echo, "127.0.0.1");
    const ev = [];
    let data = "";
    s.on("connect", () => ev.push("connect"));
    s.on("ready", () => { ev.push("ready"); s.write("abc"); s.end("def"); });
    s.on("data", (d) => (data += d));
    s.on("end", () => ev.push("end"));
    s.on("finish", () => ev.push("finish"));
    s.on("close", (hadError) => {
      ev.push("close:" + hadError);
      log.push("echo: " + ev.join(",") + " data=" + JSON.stringify(data) + " read=" + s.bytesRead + " written=" + s.bytesWritten +
        " remote=" + s.remoteAddress + ":" + (s.remotePort === PORTS.echo) + " " + s.remoteFamily + " state=" + s.readyState + " destroyed=" + s.destroyed);
      resolve();
    });
  });
  await done((resolve) => {
    const s = net.createConnection({ port: PORTS.closed, host: "127.0.0.1" });
    s.on("error", (e) => log.push("refused: " + [e.code, typeof e.errno, e.syscall, e.address, e.port === PORTS.closed, e.message.replace(String(PORTS.closed), "PORT")].join(" ")));
    s.on("close", (h) => { log.push("refused close: " + h); resolve(); });
  });
  await done((resolve) => {
    const s = new net.Socket();
    s.setTimeout(50);
    s.on("timeout", () => { log.push("timeout: " + s.timeout + " " + s.readyState); s.destroy(); });
    s.on("close", () => resolve());
    s.connect(PORTS.silent, "127.0.0.1");
  });
  log.push("isIP: " + ["127.0.0.1", "::1", "fe80::1%lo0", "1.2.3", "::ffff:1.2.3.4", "abc", "1.2.3.4/24", "01.2.3.4"].map((x) => net.isIP(x)).join(","));
  try { net.connect({ port: 70000 }); } catch (e) { log.push("bad port: " + e.code + " " + (e instanceof RangeError)); }
  await done((resolve) => {
    const s = tls.connect({ port: PORTS.tls, host: "localhost", ca: CA, ALPNProtocols: ["og"] }, () => {
      const c = s.getPeerCertificate();
      log.push("tls: " + [s.authorized, s.authorizationError, s.encrypted, s.alpnProtocol, s.getProtocol(), certLine(c), Buffer.isBuffer(c.raw)].join(" "));
      s.end("x");
    });
    let data = "";
    s.on("data", (d) => (data += d));
    s.on("close", (h) => { log.push("tls data: " + JSON.stringify(data) + " " + h); resolve(); });
  });
  await done((resolve) => {
    const s = tls.connect({ port: PORTS.tls, host: "localhost" });
    s.on("secureConnect", () => log.push("untrusted: connected?"));
    s.on("error", (e) => log.push("untrusted: " + e.code + " " + e.message));
    s.on("close", () => resolve());
  });
  await done((resolve) => {
    const s = tls.connect({ port: PORTS.tls, host: "localhost", rejectUnauthorized: false }, () => {
      log.push("unverified: " + s.authorized + " " + s.authorizationError);
      s.destroy();
    });
    s.on("close", () => resolve());
  });
  await done((resolve) => {
    const s = tls.connect({ port: PORTS.tls, host: "127.0.0.1", servername: "example.com", ca: CA });
    s.on("secureConnect", () => { log.push("mismatch: connected"); s.destroy(); });
    s.on("error", (e) => log.push("mismatch: " + e.code + " " + e.message + " " + e.host));
    s.on("close", () => resolve());
  });
  log.push("identity: " + [
    tls.checkServerIdentity("a.example.com", { subject: {}, subjectaltname: "DNS:*.example.com" }),
    tls.checkServerIdentity("a.b.example.com", { subject: {}, subjectaltname: "DNS:*.example.com" })?.code,
    tls.checkServerIdentity("Example.COM.", { subject: { CN: "example.com" } }),
    tls.checkServerIdentity("::1", { subject: {}, subjectaltname: "IP Address:0:0:0:0:0:0:0:1" }),
  ].join(","));
  await done((resolve) => {
    const plain = net.connect(PORTS.starttls, "127.0.0.1", () => plain.write("SSL?"));
    plain.once("data", (d) => {
      const s = tls.connect({ socket: plain, servername: "localhost", ca: CA }, () => {
        log.push("starttls: " + JSON.stringify(String(d)) + " " + s.authorized + " " + s.getPeerCertificate().subject.CN);
        s.end("q");
      });
      let data = "";
      s.on("data", (x) => (data += x));
      s.on("close", () => { log.push("starttls data: " + JSON.stringify(data)); });
    });
    plain.on("close", () => { log.push("starttls plain close"); resolve(); });
  });
  return log.join("\n");
}
globalThis.result = main();
`

func (s *netServers) globals() string {
	return fmt.Sprintf("const PORTS = { echo: %d, silent: %d, tls: %d, starttls: %d, closed: %d };\nconst CA = %s;\n",
		s.echo, s.silent, s.tls, s.starttls, s.closed, strconv.Quote(s.caPEM))
}

// TestNetAndTLS: node:net and node:tls on Go's sockets (net.go, js/net.js,
// js/tls.js): events and their order, half-close, counters, system errors,
// idle timeouts, isIP; TLS with a CA, without (OpenSSL's verify codes),
// unverified, a host mismatch (Node's checkServerIdentity), and an upgrade
// of a connected socket. The expectations are Node 24's output for the
// same script against the same servers.
func TestNetAndTLS(t *testing.T) {
	s := startNetServers(t)
	got := runWith(t, nodeshim.Options{}, s.globals()+netScenario, func(e *engine.Engine) error {
		return nodeshim.InstallNet(e, nodeshim.NewSockets())
	})
	want := strings.TrimSpace(netScenarioNode)
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestNetShutdownOrder: "finish" (our write side shut down) comes before
// "end" (the peer's FIN, sent in answer to ours) even when the shutdown's
// completion is slow to reach the loop, as under load (it raced: Node
// completes the shutdown before the peer can see it).
func TestNetShutdownOrder(t *testing.T) {
	nodeshim.SetShutdownDelay(50 * time.Millisecond)
	defer nodeshim.SetShutdownDelay(0)
	s := startNetServers(t)
	got := runWith(t, nodeshim.Options{}, s.globals()+netScenario, func(e *engine.Engine) error {
		return nodeshim.InstallNet(e, nodeshim.NewSockets())
	})
	first, _, _ := strings.Cut(got, "\n")
	if want := `echo: connect,ready,finish,end,close:false`; !strings.HasPrefix(first, want) {
		t.Errorf("got %s, want %s...", first, want)
	}
}

// netScenarioNode is Node 24's output for netScenario (zone ids,
// errno values and ports aside, the same on any platform).
const netScenarioNode = `
echo: connect,ready,finish,end,close:false data="hello\nABCDEFbye" read=15 written=6 remote=undefined:false undefined state=closed destroyed=true
refused: ECONNREFUSED number connect 127.0.0.1 true connect ECONNREFUSED 127.0.0.1:PORT
refused close: true
timeout: 50 open
isIP: 4,6,6,0,6,0,0,0
bad port: ERR_SOCKET_BAD_PORT true
tls: true  true og TLSv1.3 og-test/Oganesson DNS:localhost, IP Address:127.0.0.1 Jan  1 00:00:00 2026 GMT 0611 true
tls data: "hello\nXbye" false
untrusted: DEPTH_ZERO_SELF_SIGNED_CERT self-signed certificate; if the root CA is installed locally, try running Node.js with --use-system-ca
unverified: false DEPTH_ZERO_SELF_SIGNED_CERT
mismatch: ERR_TLS_CERT_ALTNAME_INVALID Hostname/IP does not match certificate's altnames: Host: example.com. is not in the cert's altnames: DNS:localhost, IP Address:127.0.0.1 example.com
identity: ,ERR_TLS_CERT_ALTNAME_INVALID,,
starttls: "S" true og-test
starttls plain close
starttls data: "hello\nQbye"
`
