package nodeshim_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// fakePG is a PostgreSQL server for the pool's tests: trust authentication,
// every query answered with CommandComplete (BEGIN / COMMIT / ROLLBACK
// change the transaction status; "SLOW" answers after 300 ms), and a log of
// what happened, per connection.
type fakePG struct {
	mu   sync.Mutex
	log  []string
	n    int
	tls  *tls.Config
	port int
}

func (f *fakePG) logf(format string, args ...any) {
	f.mu.Lock()
	f.log = append(f.log, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

// events is the log grouped by connection (each line starts with its
// number), in order within each: the pool may end one session while it
// dials the next, so lines of different connections interleave by chance.
func (f *fakePG) events() string {
	f.mu.Lock()
	lines := slices.Clone(f.log)
	f.mu.Unlock()
	conn := func(l string) int {
		n, _ := strconv.Atoi(strings.SplitN(l, " ", 2)[0])
		return n
	}
	slices.SortStableFunc(lines, func(a, b string) int { return conn(a) - conn(b) })
	return strings.Join(lines, "\n")
}

func pgFrame(typ byte, body []byte) []byte {
	b := []byte{typ, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[1:], uint32(len(body)+4))
	return append(b, body...)
}

func startFakePG(t *testing.T, cfg *tls.Config) *fakePG {
	f := &fakePG{tls: cfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.port = ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.n++
			id := f.n
			f.mu.Unlock()
			go f.serve(id, c)
		}
	}()
	return f
}

func (f *fakePG) serve(id int, c net.Conn) {
	defer func() { f.logf("%d close", id); c.Close() }()
	f.logf("%d accept", id)
	var r *bufio.Reader = bufio.NewReader(c)
	var w io.Writer = c
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		body := make([]byte, n-8)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		if binary.BigEndian.Uint32(hdr[4:]) == 80877103 {
			if f.tls == nil {
				_, _ = c.Write([]byte{'N'})
				continue
			}
			_, _ = c.Write([]byte{'S'})
			tc := tls.Server(c, f.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			f.logf("%d tls", id)
			r, w = bufio.NewReader(tc), tc
			continue
		}
		user := strings.Split(string(body), "\x00")
		f.logf("%d startup %s", id, user[1])
		break
	}
	var out []byte
	out = append(out, pgFrame('R', []byte{0, 0, 0, 0})...)
	out = append(out, pgFrame('S', []byte("server_version\x0018.0\x00"))...)
	out = append(out, pgFrame('K', binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, uint32(id)), 77))...)
	out = append(out, pgFrame('Z', []byte{'I'})...)
	_, _ = w.Write(out)
	status := byte('I')
	for {
		var hdr [5]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr[1:])-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		switch hdr[0] {
		case 'X':
			f.logf("%d terminate", id)
			return
		case 'Q':
			q := strings.TrimRight(string(body), "\x00")
			f.logf("%d Q %s", id, q)
			switch q {
			case "BEGIN":
				status = 'T'
			case "COMMIT", "ROLLBACK":
				status = 'I'
			case "SLOW":
				time.Sleep(300 * time.Millisecond)
			}
			_, _ = w.Write(append(pgFrame('C', []byte(q+"\x00")), pgFrame('Z', []byte{status})...))
		}
	}
}

// pgClient is a minimal PostgreSQL client over node:net / node:tls: it
// connects (TLS when asked), starts up as user, runs queries, and ends with
// Terminate unless told to leave mid-query. It returns the backend key's
// process id and the statuses it saw.
const pgClient = `
const net = require("net");
const tls = require("tls");
function frame(type, body) {
  const b = Buffer.alloc(5 + body.length);
  b[0] = type.charCodeAt(0); b.writeUInt32BE(4 + body.length, 1); body.copy(b, 5);
  return b;
}
function startup(user) {
  const body = Buffer.from("\0\x03\0\0user\0" + user + "\0database\0og\0\0", "binary");
  const b = Buffer.alloc(4 + body.length); b.writeUInt32BE(4 + body.length, 0); body.copy(b, 4);
  return b;
}
function run({ port, user, tlsOn, queries, leave }) {
  return new Promise((resolve, reject) => {
    const out = [];
    let sock = net.connect(port, "127.0.0.1");
    let buf = Buffer.alloc(0);
    let pending = 0;
    const onData = (d) => {
      buf = Buffer.concat([buf, d]);
      while (buf.length >= 5) {
        const n = buf.readUInt32BE(1);
        if (buf.length < 1 + n) break;
        const type = String.fromCharCode(buf[0]);
        const body = buf.subarray(5, 1 + n);
        buf = buf.subarray(1 + n);
        if (type === "K") out.push("pid=" + body.readUInt32BE(0));
        if (type === "Z") {
          out.push("Z" + String.fromCharCode(body[0]));
          if (pending === 0 && queries.length) {
            pending++;
            const q = queries.shift();
            // leave: gone once the query is sent, before its answer.
            sock.write(frame("Q", Buffer.from(q + "\0")), () => { if (leave && !queries.length) { sock.destroy(); resolve(out.join(" ") + " left"); } });
          }
          else if (pending > 0) { pending--; if (queries.length) { pending++; sock.write(frame("Q", Buffer.from(queries.shift() + "\0"))); } else { sock.write(frame("X", Buffer.alloc(0))); sock.end(); } }
          else { sock.write(frame("X", Buffer.alloc(0))); sock.end(); }
        }
      }
    };
    sock.on("error", reject);
    sock.on("connect", () => {
      if (!tlsOn) { sock.on("data", onData); sock.write(startup(user)); return; }
      const ssl = Buffer.alloc(8); ssl.writeUInt32BE(8, 0); ssl.writeUInt32BE(80877103, 4);
      sock.write(ssl);
      sock.once("data", (d) => {
        out.push("ssl=" + String(d));
        const plain = sock;
        sock = tls.connect({ socket: plain, servername: "localhost", ca: CA }, () => {
          out.push("authorized=" + sock.authorized);
          sock.on("data", onData);
          sock.write(startup(user));
        });
        sock.on("error", reject);
        sock.on("close", () => resolve(out.join(" ")));
      });
      return;
    });
    sock.on("close", () => { if (!tlsOn) resolve(out.join(" ")); });
  });
}
`

// runEngines runs each script in its own engine, one after the other (as
// requests do), all sharing svc; the next starts once settle reports the
// previous one's session was handled by the pool.
func runEngines(t *testing.T, svc *nodeshim.Sockets, scripts []string, settle func(i int)) []string {
	t.Helper()
	var outs []string
	for i, src := range scripts {
		bo := api.BuildOptions{
			Stdin:   &api.StdinOptions{Contents: "import " + jsonQuote(nodeshim.GlobalsModule) + ";\n" + src, Loader: api.LoaderJS},
			Bundle:  true,
			Write:   false,
			Plugins: []api.Plugin{nodeshim.Plugin(nodeshim.Options{})},
			Define:  map[string]string{"__dirname": `"/"`},
		}
		gojatarget.Apply(&bo)
		bo.Platform = api.PlatformBrowser
		res := api.Build(bo)
		if len(res.Errors) > 0 {
			t.Fatal(res.Errors[0].Text)
		}
		e := engine.New(engine.Options{})
		if _, err := webapi.Install(e, webapi.Options{Stdout: io.Discard}); err != nil {
			t.Fatal(err)
		}
		if err := nodeshim.InstallNet(e, svc); err != nil {
			t.Fatal(err)
		}
		if _, err := e.VM.RunString(string(res.OutputFiles[0].Contents)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		v, err := e.Await(ctx, e.VM.Get("result"))
		cancel()
		e.Close()
		if err != nil {
			t.Fatalf("script %d: %v", i, err)
		}
		outs = append(outs, v.String())
		if settle != nil {
			settle(i)
		}
	}
	return outs
}

// waitFor polls the server's log until it contains s.
func waitFor(t *testing.T, f *fakePG, s string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(f.events(), s) {
			return
		}
	}
	t.Fatalf("no %q in\n%s", s, f.events())
}

// TestSocketPool: og's Go-side pool of server connections (netpool.go), on
// the PostgreSQL protocol. Requests are separate engines sharing one
// Sockets, as in a packaged app. A session left in a transaction is rolled
// back and reset (DISCARD ALL), parked, and taken over by the next client
// with the same startup, which sees the recorded greeting (the first
// backend key); a client with another user gets a fresh connection, the
// mismatched session ending; a TLS session is taken over without a second
// handshake; a session left with a query unanswered is closed, not parked.
func TestSocketPool(t *testing.T) {
	s := startNetServers(t) // the certificate and CA
	plain := startFakePG(t, nil)
	secure := startFakePG(t, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	svc := nodeshim.NewSockets()
	defer svc.Close()
	script := func(port int, user string, tlsOn bool, queries []string, leave bool) string {
		q := ""
		for _, x := range queries {
			q += fmt.Sprintf("%q,", x)
		}
		return s.globals() + pgClient + fmt.Sprintf("globalThis.result = run({ port: %d, user: %q, tlsOn: %v, queries: [%s], leave: %v });", port, user, tlsOn, q, leave)
	}
	outs := runEngines(t, svc, []string{
		script(plain.port, "a", false, []string{"BEGIN"}, false),
		script(plain.port, "a", false, []string{"SELECT 1"}, false),
		script(plain.port, "b", false, []string{"SELECT 2"}, false),
		script(secure.port, "a", true, []string{"SELECT 3"}, false),
		script(secure.port, "a", true, []string{"SELECT 4"}, false),
		script(plain.port, "a", false, []string{"SLOW"}, true),
	}, func(i int) {
		switch i {
		case 0:
			waitFor(t, plain, "1 Q DISCARD ALL")
		case 1:
			waitFor(t, plain, "1 Q SELECT 1\n1 Q DISCARD ALL")
		case 2:
			waitFor(t, plain, "2 Q SELECT 2\n2 Q DISCARD ALL")
		case 3:
			waitFor(t, secure, "1 Q SELECT 3\n1 Q DISCARD ALL")
		case 4:
			waitFor(t, secure, "1 Q SELECT 4\n1 Q DISCARD ALL")
		case 5:
			waitFor(t, plain, "3 close")
		}
	})
	got := strings.Join(outs, "\n")
	want := strings.Join([]string{
		"pid=1 ZI ZT",
		"pid=1 ZI ZI",
		"pid=2 ZI ZI",
		"ssl=S authorized=true pid=1 ZI ZI",
		"ssl=S authorized=true pid=1 ZI ZI",
		"pid=3 ZI left",
	}, "\n")
	if got != want {
		t.Errorf("clients:\n%s\nwant\n%s", got, want)
	}
	// The plain server: one session for user a across two clients (the
	// transaction rolled back between them), a second for user b (a's
	// session ending when b did not match it), a third for a (b's ending
	// likewise), closed with its query unanswered.
	wantPlain := strings.Join([]string{
		"1 accept", "1 startup a", "1 Q BEGIN", "1 Q ROLLBACK", "1 Q DISCARD ALL", "1 Q SELECT 1", "1 Q DISCARD ALL",
		"1 terminate", "1 close", "2 accept", "2 startup b", "2 Q SELECT 2", "2 Q DISCARD ALL",
		"2 terminate", "2 close", "3 accept", "3 startup a", "3 Q SLOW", "3 close",
	}, "\n")
	if got := plain.events(); got != wantPlain {
		t.Errorf("plain server:\n%s\nwant\n%s", got, wantPlain)
	}
	// The TLS server: one connection, one handshake, both clients.
	wantSecure := strings.Join([]string{"1 accept", "1 tls", "1 startup a", "1 Q SELECT 3", "1 Q DISCARD ALL", "1 Q SELECT 4", "1 Q DISCARD ALL"}, "\n")
	if got := secure.events(); got != wantSecure {
		t.Errorf("TLS server:\n%s\nwant\n%s", got, wantSecure)
	}
}
