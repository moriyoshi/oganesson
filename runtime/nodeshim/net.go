package nodeshim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/tlsroots"
)

// Sockets is the process-wide side of node:net and node:tls (js/net.js,
// js/tls.js): connections are Go's (net.Dial, crypto/tls), their events
// posted to the engine that opened them.
type Sockets struct {
	dialer net.Dialer
	mu     sync.Mutex
	idle   map[string][]*session // parked sessions by server (netpool.go)
}

// NewSockets returns the socket service.
func NewSockets() *Sockets { return &Sockets{} }

// sock is one connection of one engine. Its reader goroutine posts data
// while not paused; writes and the write-side shutdown go through one
// writer goroutine, in order.
type sock struct {
	conn   net.Conn
	emit   func(func(*sobek.Runtime) error)
	onEvt  sobek.Callable
	writes chan sockOp

	mu        sync.Mutex
	cond      *sync.Cond
	paused    bool
	closed    bool
	upgrading bool
	// shuttingDown is set while the write side is being shut down, until
	// its completion is queued: what the reader gets meanwhile (the peer's
	// answer to our FIN) waits, so "finish" comes before "end" as in Node,
	// where the shutdown completes before the peer can see it.
	shuttingDown bool
	readerOut    chan struct{} // closed when the reader goroutine returns
	pending      []byte        // read while a TLS upgrade interrupted the reader
}

type sockOp struct {
	data     []byte
	shutdown bool
	done     func(error)
}

type netBinding struct {
	svc   *Sockets
	e     *engine.Engine
	vm    *sobek.Runtime
	socks map[int64]*sock
	next  int64
}

// InstallNet provides og.net (Symbol.for("og.net")), the primitives behind
// js/net.js and js/tls.js. Sockets are weak handles of the loop: a request
// awaiting a reply keeps waiting for them, and the drain after the response
// does not (an idle connection would hold every response for the grace
// period). They close with the engine.
func InstallNet(e *engine.Engine, svc *Sockets) error {
	b := &netBinding{svc: svc, e: e, vm: e.VM, socks: map[int64]*sock{}}
	o := b.vm.NewObject()
	_ = o.Set("connect", b.connect)
	_ = o.Set("write", b.write)
	_ = o.Set("shutdown", b.shutdown)
	_ = o.Set("close", b.close)
	_ = o.Set("pause", func(id int64) { b.setPaused(id, true) })
	_ = o.Set("resume", func(id int64) { b.setPaused(id, false) })
	_ = o.Set("setNoDelay", b.setNoDelay)
	_ = o.Set("setKeepAlive", b.setKeepAlive)
	_ = o.Set("startTLS", b.startTLS)
	_ = o.Set("isIP", isIP)
	_ = o.Set("canonicalizeIP", func(s string) sobek.Value {
		if a, err := netip.ParseAddr(s); err == nil {
			return b.vm.ToValue(a.WithZone("").String())
		}
		return sobek.Undefined()
	})
	// node:tls rootCertificates: the embedded Mozilla roots (tlsroots also
	// installs them as crypto/x509's fallback for TLS verification). Importing
	// the package here links that fallback into every og and app binary.
	_ = o.Set("rootCertificates", b.vm.ToValue(tlsroots.PEMs()))
	e.OnClose(b.closeAll)
	install, err := b.vm.RunString(`(function (o) { Object.defineProperty(globalThis, Symbol.for("og.net"), { value: o }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}

// isIP is node:net's isIP: 4, 6 (zone ids allowed, as in Node) or 0.
func isIP(s string) int {
	if strings.ContainsAny(s, "/") {
		return 0
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return 0
	}
	if a.Is4() {
		if strings.Count(s, ".") != 3 {
			return 0
		}
		return 4
	}
	return 6
}

// nodeErrno are libuv's negative errno values on Linux (og reports Linux).
var nodeErrno = map[string]int{
	"ECONNREFUSED": -111, "ECONNRESET": -104, "ETIMEDOUT": -110, "EPIPE": -32,
	"EHOSTUNREACH": -113, "ENETUNREACH": -101, "ENOTFOUND": -3008, "EADDRINUSE": -98,
	"ECONNABORTED": -103, "ENOENT": -2, "EACCES": -13, "EADDRNOTAVAIL": -99,
}

// errInfo describes err as Node's system errors do: code, errno, syscall
// and a message such as "connect ECONNREFUSED 127.0.0.1:5432".
func errInfo(err error, syscallName, address string, port int) map[string]any {
	code := "EIO"
	var dnsErr *net.DNSError
	var errno syscall.Errno
	switch {
	case errors.As(err, &dnsErr):
		code, syscallName = "ENOTFOUND", "getaddrinfo"
		if dnsErr.IsTemporary {
			code = "EAI_AGAIN"
		}
		address = dnsErr.Name
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		code = "ETIMEDOUT"
	case errors.As(err, &errno):
		switch errno {
		case syscall.ECONNREFUSED:
			code = "ECONNREFUSED"
		case syscall.ECONNRESET:
			code = "ECONNRESET"
		case syscall.EPIPE:
			code = "EPIPE"
		case syscall.ETIMEDOUT:
			code = "ETIMEDOUT"
		case syscall.EHOSTUNREACH:
			code = "EHOSTUNREACH"
		case syscall.ENETUNREACH:
			code = "ENETUNREACH"
		case syscall.ECONNABORTED:
			code = "ECONNABORTED"
		case syscall.ENOENT:
			code = "ENOENT"
		case syscall.EACCES:
			code = "EACCES"
		case syscall.EADDRNOTAVAIL:
			code = "EADDRNOTAVAIL"
		}
	case errors.Is(err, net.ErrClosed):
		code = "ERR_SOCKET_CLOSED"
	}
	info := map[string]any{"code": code, "syscall": syscallName}
	if n, ok := nodeErrno[code]; ok {
		info["errno"] = n
	}
	msg := syscallName + " " + code
	if code == "ENOTFOUND" || code == "EAI_AGAIN" {
		info["hostname"] = address
		msg += " " + address
	} else if address != "" {
		info["address"] = address
		if port > 0 {
			info["port"] = port
			msg += fmt.Sprintf(" %s:%d", address, port)
		} else {
			msg += " " + address
		}
	}
	if code == "EIO" || code == "ERR_SOCKET_CLOSED" {
		msg = err.Error()
	}
	info["message"] = msg
	return info
}

func addrInfo(a net.Addr) (string, int, string) {
	switch t := a.(type) {
	case *net.TCPAddr:
		fam := "IPv4"
		ip := t.IP.String()
		if t.IP.To4() == nil {
			fam = "IPv6"
		}
		return ip, t.Port, fam
	case *net.UnixAddr:
		return t.Name, 0, ""
	}
	return "", 0, ""
}

// connect(opts, onEvent) -> id. opts: host, port, path (a Unix socket),
// timeout (ms), localAddress, localPort, family. onEvent(type, a): "connect"
// with the endpoints, "data" (ArrayBuffer), "end", "error" (errInfo).
func (b *netBinding) connect(opts map[string]any, onEvent sobek.Callable) int64 {
	str := func(k string) string { s, _ := opts[k].(string); return s }
	num := func(k string) int64 {
		switch v := opts[k].(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		}
		return 0
	}
	b.next++
	id := b.next
	s := &sock{emit: b.e.Loop.Bind(), onEvt: onEvent, writes: make(chan sockOp, 64)}
	s.cond = sync.NewCond(&s.mu)
	b.socks[id] = s
	network, address := "tcp", ""
	host, port := str("host"), int(num("port"))
	if p := str("path"); p != "" {
		network, address = "unix", p
	} else {
		if host == "" {
			host = "localhost"
		}
		switch num("family") {
		case 4:
			network = "tcp4"
		case 6:
			network = "tcp6"
		}
		address = net.JoinHostPort(host, fmt.Sprint(port))
	}
	d := b.svc.dialer
	if t := num("timeout"); t > 0 {
		d.Timeout = time.Duration(t) * time.Millisecond
	}
	if la := str("localAddress"); la != "" || num("localPort") > 0 {
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(la), Port: int(num("localPort"))}
	}
	var t *tlsSetup
	if o, ok := opts["tls"].(map[string]any); ok {
		t = b.tlsSetup(o)
	}
	done := b.e.Loop.Async()
	go func() {
		var conn net.Conn
		var err error
		if t == nil {
			conn, err = b.svc.connect(d, network, address) // netpool.go
		} else {
			conn, err = d.Dial(network, address)
		}
		var tlsErr error
		if err == nil && t != nil {
			// tls.connect without a socket: the handshake comes first,
			// so no plaintext reaches JS.
			tc := tls.Client(conn, t.cfg)
			if tlsErr = tc.Handshake(); tlsErr != nil {
				_ = conn.Close()
			} else {
				conn = tc
			}
		}
		done(func(vm *sobek.Runtime) error {
			if s.closed {
				if conn != nil {
					_ = conn.Close()
				}
				return nil
			}
			if err != nil {
				errHost := host
				if network == "unix" {
					errHost, port = address, 0
				}
				_, cerr := onEvent(sobek.Undefined(), vm.ToValue("error"), vm.ToValue(errInfo(err, "connect", errHost, port)))
				return cerr
			}
			if tlsErr != nil {
				_, cerr := onEvent(sobek.Undefined(), vm.ToValue("error"), vm.ToValue(tlsErrInfo(tlsErr)))
				return cerr
			}
			s.conn = conn
			b.e.Loop.Weak(1)
			go s.writer()
			s.startReader()
			la, lp, lf := addrInfo(conn.LocalAddr())
			ra, rp, rf := addrInfo(conn.RemoteAddr())
			info := map[string]any{"localAddress": la, "localPort": lp, "localFamily": lf, "remoteAddress": ra, "remotePort": rp, "remoteFamily": rf}
			if _, cerr := onEvent(sobek.Undefined(), vm.ToValue("connect"), vm.ToValue(info)); cerr != nil {
				return cerr
			}
			if tc, ok := conn.(*tls.Conn); ok {
				_, cerr := onEvent(sobek.Undefined(), vm.ToValue("secure"), vm.ToValue(t.secureInfo(vm, tc.ConnectionState())))
				return cerr
			}
			return nil
		})
	}()
	return id
}

// startReader runs the reader goroutine for the socket's current conn.
func (s *sock) startReader() {
	s.mu.Lock()
	s.readerOut = make(chan struct{})
	out := s.readerOut
	conn := s.conn
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	go func() {
		defer close(out)
		buf := make([]byte, 64*1024)
		first := pending
		for {
			s.mu.Lock()
			for s.paused && !s.closed && !s.upgrading {
				s.cond.Wait()
			}
			stop := s.closed || s.upgrading
			s.mu.Unlock()
			if stop {
				return
			}
			var n int
			var err error
			if len(first) > 0 {
				n = copy(buf, first)
				first = first[n:]
			} else {
				n, err = conn.Read(buf)
			}
			s.mu.Lock()
			for s.shuttingDown && !s.closed {
				s.cond.Wait()
			}
			if s.upgrading {
				// The TLS upgrade interrupted this read: what arrived
				// belongs to the handshake.
				s.pending = append(s.pending, buf[:n]...)
				s.mu.Unlock()
				return
			}
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				s.post("data", func(vm *sobek.Runtime) sobek.Value { return vm.ToValue(vm.NewArrayBuffer(chunk)) })
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					s.post("end", nil)
				} else {
					info := errInfo(err, "read", "", 0)
					s.post("error", func(vm *sobek.Runtime) sobek.Value { return vm.ToValue(info) })
				}
				return
			}
		}
	}()
}

// post delivers an event to the socket's JS object, unless it was closed
// meanwhile.
func (s *sock) post(typ string, arg func(*sobek.Runtime) sobek.Value) {
	s.emit(func(vm *sobek.Runtime) error {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil
		}
		a := sobek.Undefined()
		if arg != nil {
			a = arg(vm)
		}
		_, err := s.onEvt(sobek.Undefined(), vm.ToValue(typ), a)
		return err
	})
}

// shutdownDelay delays a shutdown's completion (tests only).
var shutdownDelay time.Duration

func (s *sock) writer() {
	for op := range s.writes {
		var err error
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		switch {
		case op.shutdown:
			s.mu.Lock()
			s.shuttingDown = true
			s.mu.Unlock()
			// TLS: close_notify, then the TCP write side (as Node's
			// end() on a TLSSocket).
			if tc, ok := conn.(*tls.Conn); ok {
				err = tc.CloseWrite()
				conn = tc.NetConn()
				if pc, ok := conn.(*prefixConn); ok {
					conn = pc.Conn
				}
			}
			if cw, ok := conn.(interface{ CloseWrite() error }); ok && err == nil {
				err = cw.CloseWrite()
			}
		default:
			_, err = conn.Write(op.data)
		}
		if op.shutdown && shutdownDelay > 0 {
			time.Sleep(shutdownDelay) // tests: the peer answers the FIN first
		}
		op.done(err)
		if op.shutdown {
			s.mu.Lock()
			s.shuttingDown = false
			s.cond.Broadcast()
			s.mu.Unlock()
		}
	}
}

func (b *netBinding) sock(id int64) *sock {
	s := b.socks[id]
	if s == nil || s.conn == nil {
		panic(b.vm.NewTypeError("og: socket %d is not open", id))
	}
	return s
}

// write(id, bytes, cb): cb(null) once written, or cb(errInfo).
func (b *netBinding) write(id int64, data []byte, cb sobek.Callable) {
	s := b.sock(id)
	buf := make([]byte, len(data))
	copy(buf, data)
	b.queue(s, sockOp{data: buf}, cb, "write")
}

// shutdown(id, cb): the write side ends (FIN, or TLS close_notify) after
// the queued writes.
func (b *netBinding) shutdown(id int64, cb sobek.Callable) {
	b.queue(b.sock(id), sockOp{shutdown: true}, cb, "shutdown")
}

func (b *netBinding) queue(s *sock, op sockOp, cb sobek.Callable, sys string) {
	done := b.e.Loop.Async()
	op.done = func(err error) {
		done(func(vm *sobek.Runtime) error {
			arg := sobek.Null()
			if err != nil {
				arg = vm.ToValue(errInfo(err, sys, "", 0))
			}
			_, cerr := cb(sobek.Undefined(), arg)
			return cerr
		})
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		op.done(net.ErrClosed)
		return
	}
	select {
	case s.writes <- op:
	default:
		// A full queue: wait for room off the loop goroutine.
		go func() {
			defer func() {
				if recover() != nil { // closed meanwhile
					op.done(net.ErrClosed)
				}
			}()
			s.writes <- op
		}()
	}
}

// close(id, reset): the connection closes (reset: RST, as resetAndDestroy).
func (b *netBinding) close(id int64, reset bool) {
	s := b.socks[id]
	if s == nil {
		return
	}
	delete(b.socks, id)
	s.shut(reset)
	if s.conn != nil {
		b.e.Loop.Weak(-1)
	}
}

func (s *sock) shut(reset bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cond.Broadcast()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	if reset {
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
	}
	_ = conn.Close()
	close(s.writes)
}

func (b *netBinding) closeAll() {
	for id := range b.socks {
		b.close(id, false)
	}
}

func (b *netBinding) setPaused(id int64, paused bool) {
	s := b.socks[id]
	if s == nil {
		return
	}
	s.mu.Lock()
	s.paused = paused
	s.cond.Broadcast()
	s.mu.Unlock()
}

func tcpOf(c net.Conn) *net.TCPConn {
	switch t := c.(type) {
	case *net.TCPConn:
		return t
	case *tls.Conn:
		return tcpOf(t.NetConn())
	case *prefixConn:
		return tcpOf(t.Conn)
	case *pooledConn:
		t.mu.Lock()
		defer t.mu.Unlock()
		return tcpOf(t.s.raw)
	}
	return nil
}

func (b *netBinding) setNoDelay(id int64, on bool) {
	if s := b.socks[id]; s != nil && s.conn != nil {
		if tc := tcpOf(s.conn); tc != nil {
			_ = tc.SetNoDelay(on)
		}
	}
}

func (b *netBinding) setKeepAlive(id int64, on bool, delayMs int64) {
	if s := b.socks[id]; s != nil && s.conn != nil {
		if tc := tcpOf(s.conn); tc != nil {
			_ = tc.SetKeepAlive(on)
			if on && delayMs > 0 {
				_ = tc.SetKeepAlivePeriod(time.Duration(delayMs) * time.Millisecond)
			}
		}
	}
}

// prefixConn reads pending bytes before its conn's (what the reader took
// before a TLS upgrade stopped it).
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// tlsSetup is a client configuration from tls.connect's options:
// servername (SNI), ca (PEM strings; the system roots otherwise),
// cert / key, ALPNProtocols, minVersion / maxVersion. The chain is
// verified after the handshake (verdict, with OpenSSL's reason codes, as
// Node's verifyError); identity is left to tls.checkServerIdentity in JS,
// as Node does.
type tlsSetup struct {
	cfg     *tls.Config
	roots   *x509.CertPool
	authErr string
}

func (b *netBinding) tlsSetup(opts map[string]any) *tlsSetup {
	str := func(k string) string { v, _ := opts[k].(string); return v }
	strs := func(k string) []string {
		var out []string
		if arr, ok := opts[k].([]any); ok {
			for _, x := range arr {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		} else if s, ok := opts[k].(string); ok {
			out = append(out, s)
		}
		return out
	}
	var roots *x509.CertPool
	if ca := strs("ca"); len(ca) > 0 {
		roots = x509.NewCertPool()
		for _, p := range ca {
			roots.AppendCertsFromPEM([]byte(p))
		}
	}
	t := &tlsSetup{roots: roots}
	t.cfg = &tls.Config{
		ServerName:         str("servername"),
		InsecureSkipVerify: true, // verified in VerifyConnection; identity in JS
		NextProtos:         strs("ALPNProtocols"),
		MinVersion:         tlsVersion(str("minVersion"), tls.VersionTLS12),
		MaxVersion:         tlsVersion(str("maxVersion"), 0),
		VerifyConnection: func(cs tls.ConnectionState) error {
			t.authErr = verifyChain(cs.PeerCertificates, roots)
			return nil
		},
	}
	if c, k := str("cert"), str("key"); c != "" && k != "" {
		pair, err := tls.X509KeyPair([]byte(c), []byte(k))
		if err != nil {
			panic(b.vm.NewGoError(err))
		}
		t.cfg.Certificates = []tls.Certificate{pair}
	}
	return t
}

// secureInfo is the "secure" event's argument: the session and the
// chain's verdict.
func (t *tlsSetup) secureInfo(vm *sobek.Runtime, st tls.ConnectionState) map[string]any {
	info := map[string]any{
		"verifyError": t.authErr, "alpnProtocol": st.NegotiatedProtocol,
		"protocol": tlsVersionName(st.Version), "cipher": tls.CipherSuiteName(st.CipherSuite),
	}
	if len(st.PeerCertificates) > 0 {
		info["peerCertificate"] = certInfo(vm, st.PeerCertificates[0])
	}
	return info
}

func tlsErrInfo(err error) map[string]any {
	return map[string]any{"code": tlsErrCode(err), "message": err.Error(), "syscall": ""}
}

// startTLS(id, opts, onEvent): the connection becomes a TLS client in
// place, as tls.connect({ socket }) does with a connected socket. The
// reader stops first (bytes it took go to the handshake); onEvent then
// receives "secure" and the socket's events.
func (b *netBinding) startTLS(id int64, opts map[string]any, onEvent sobek.Callable) {
	s := b.sock(id)
	t := b.tlsSetup(opts)
	s.mu.Lock()
	s.upgrading = true
	s.cond.Broadcast()
	out := s.readerOut
	raw := s.conn
	s.mu.Unlock()
	_ = raw.SetReadDeadline(time.Now())
	done := b.e.Loop.Async()
	go func() {
		if out != nil {
			<-out
		}
		_ = raw.SetReadDeadline(time.Time{})
		s.mu.Lock()
		var base net.Conn = raw
		pending := s.pending
		if len(s.pending) > 0 {
			base = &prefixConn{Conn: raw, prefix: s.pending}
			s.pending = nil
		}
		s.mu.Unlock()
		var conn net.Conn
		var state tls.ConnectionState
		var err error
		if pc, ok := raw.(*pooledConn); ok {
			// The pool's session becomes TLS (or was, when parked); the
			// socket keeps reading through the pool.
			if len(pending) > 0 {
				err = errors.New("og: data arrived before the TLS handshake")
			} else {
				state, err = pc.startTLS(t.cfg)
				if err == nil {
					// A parked session's handshake ran under another
					// configuration: the chain is judged by this one.
					t.authErr = verifyChain(state.PeerCertificates, t.roots)
				}
			}
			conn = pc
		} else {
			tc := tls.Client(base, t.cfg)
			err = tc.Handshake()
			state, conn = tc.ConnectionState(), tc
		}
		done(func(vm *sobek.Runtime) error {
			s.mu.Lock()
			s.upgrading = false
			closed := s.closed
			s.onEvt = onEvent
			if err == nil && !closed {
				s.conn = conn
			}
			s.mu.Unlock()
			if closed {
				return nil
			}
			if err != nil {
				_, cerr := onEvent(sobek.Undefined(), vm.ToValue("error"), vm.ToValue(tlsErrInfo(err)))
				return cerr
			}
			s.startReader()
			_, cerr := onEvent(sobek.Undefined(), vm.ToValue("secure"), vm.ToValue(t.secureInfo(vm, state)))
			return cerr
		})
	}()
}

func tlsVersion(s string, def uint16) uint16 {
	switch s {
	case "TLSv1":
		return tls.VersionTLS10
	case "TLSv1.1":
		return tls.VersionTLS11
	case "TLSv1.2":
		return tls.VersionTLS12
	case "TLSv1.3":
		return tls.VersionTLS13
	}
	return def
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLSv1"
	case tls.VersionTLS11:
		return "TLSv1.1"
	case tls.VersionTLS12:
		return "TLSv1.2"
	case tls.VersionTLS13:
		return "TLSv1.3"
	}
	return "unknown"
}

func tlsErrCode(err error) string {
	var ae tls.AlertError
	if errors.As(err, &ae) {
		return "ERR_SSL_" + strings.ToUpper(strings.ReplaceAll(ae.Error(), " ", "_"))
	}
	if errors.Is(err, io.EOF) {
		return "ECONNRESET"
	}
	return "ERR_SSL_HANDSHAKE_FAILURE"
}

// verifyChain checks the peer's chain against roots (the system's when
// nil) and returns OpenSSL's reason code, as Node's verifyError, or ""
// when the chain is trusted. The reason comes from the chain itself, as
// OpenSSL reports it (the platform verifier's errors do not say).
func verifyChain(certs []*x509.Certificate, roots *x509.CertPool) string {
	if len(certs) == 0 {
		return "UNABLE_TO_GET_ISSUER_CERT"
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter}); err == nil {
		return ""
	}
	now := time.Now()
	for _, c := range certs {
		if now.After(c.NotAfter) {
			return "CERT_HAS_EXPIRED"
		}
		if now.Before(c.NotBefore) {
			return "CERT_NOT_YET_VALID"
		}
	}
	selfSigned := func(c *x509.Certificate) bool {
		return bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
	}
	switch {
	case len(certs) == 1 && selfSigned(certs[0]):
		return "DEPTH_ZERO_SELF_SIGNED_CERT"
	case len(certs) == 1:
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	case selfSigned(certs[len(certs)-1]):
		return "SELF_SIGNED_CERT_IN_CHAIN"
	}
	return "UNABLE_TO_GET_ISSUER_CERT_LOCALLY"
}

// certInfo is a certificate as getPeerCertificate() describes it.
func certInfo(vm *sobek.Runtime, c *x509.Certificate) map[string]any {
	name := func(n []string) any {
		if len(n) == 1 {
			return n[0]
		}
		if len(n) == 0 {
			return nil
		}
		return n
	}
	dn := func(cn string, o, ou, c, st, l []string) map[string]any {
		m := map[string]any{}
		for k, v := range map[string]any{"C": name(c), "ST": name(st), "L": name(l), "O": name(o), "OU": name(ou)} {
			if v != nil {
				m[k] = v
			}
		}
		if cn != "" {
			m["CN"] = cn
		}
		return m
	}
	var alt []string
	for _, d := range c.DNSNames {
		alt = append(alt, "DNS:"+d)
	}
	for _, ip := range c.IPAddresses {
		alt = append(alt, "IP Address:"+ip.String())
	}
	sum := sha256.Sum256(c.Raw)
	fp := strings.ToUpper(hex.EncodeToString(sum[:]))
	var pairs []string
	for i := 0; i < len(fp); i += 2 {
		pairs = append(pairs, fp[i:i+2])
	}
	const layout = "Jan _2 15:04:05 2006 GMT"
	info := map[string]any{
		"subject":        dn(c.Subject.CommonName, c.Subject.Organization, c.Subject.OrganizationalUnit, c.Subject.Country, c.Subject.Province, c.Subject.Locality),
		"issuer":         dn(c.Issuer.CommonName, c.Issuer.Organization, c.Issuer.OrganizationalUnit, c.Issuer.Country, c.Issuer.Province, c.Issuer.Locality),
		"valid_from":     c.NotBefore.UTC().Format(layout),
		"valid_to":       c.NotAfter.UTC().Format(layout),
		"fingerprint256": strings.Join(pairs, ":"),
		"serialNumber":   serialHex(c),
		"ca":             c.IsCA,
		"pem":            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})),
		"raw":            vm.NewArrayBuffer(append([]byte(nil), c.Raw...)),
	}
	if len(alt) > 0 {
		info["subjectaltname"] = strings.Join(alt, ", ")
	}
	return info
}

// serialHex is the serial number as OpenSSL prints it: upper-case hex of
// whole bytes.
func serialHex(c *x509.Certificate) string {
	h := strings.ToUpper(c.SerialNumber.Text(16))
	if len(h)%2 == 1 {
		h = "0" + h
	}
	return h
}
