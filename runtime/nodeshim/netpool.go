package nodeshim

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// The connection pool behind node:net. Request VMs are short-lived, so a
// driver's own pool (pg.Pool, postgres.js) lives for one request; og keeps
// the server connections instead, in Go, shared by every request of the
// process (the user's choice over per-request connections). It works at
// the protocol level, below the JS socket, which sees an ordinary new
// connection either way:
//
//   - A fresh connection is recorded: for PostgreSQL, the client's packets
//     before its StartupMessage (SSLRequest, with the server's one-byte
//     answers and whether TLS followed), the StartupMessage itself, and the
//     server's greeting from AuthenticationOk to the first ReadyForQuery.
//     TLS terminates in Go, so the pool sees the protocol in plaintext.
//   - When the JS socket closes (or its VM ends) with the session at rest
//     (no query unanswered, nothing half-read), the client's Terminate is
//     withheld and the session reset (ROLLBACK if a transaction is open,
//     then DISCARD ALL, as a pooler's reset query) and parked.
//   - A later connection to the same address gets a parked session: the
//     client's handshake is answered from the recording (TLS included,
//     without a new handshake), but only while its packets are the
//     recorded ones byte for byte (the same user, database and settings);
//     on the first difference the parked session goes back and a fresh
//     connection takes over, the client's steps so far replayed to it.
//
// A connection whose protocol is not PostgreSQL's is relayed as is and
// closed with its socket.

const (
	pgSSLRequest    = 80877103
	pgGSSENCRequest = 80877104
	pgCancelRequest = 80877102
	pgProtocol3     = 196608
)

// session is a server connection and the recording of its handshake.
type session struct {
	key      string
	proto    byte     // 'p' PostgreSQL, 'm' MySQL (netpool_mysql.go)
	raw      net.Conn // TCP
	conn     net.Conn // raw, or TLS over it
	tlsState *tls.ConnectionState
	pre      [][]byte // the client's packets before its StartupMessage
	preAns   []byte   // the server's one-byte answer to each
	tlsAfter int      // TLS followed the answer to pre[tlsAfter] (-1: none)
	startup  []byte
	greeting []byte
	// MySQL: the handshake's turns (server and client bytes, alternating),
	// the SSLRequest turn TLS followed (-1: none), the capabilities.
	turns        [][]byte
	turnDir      []byte
	tlsTurn      int
	clientCaps   uint32
	deprecateEOF bool
}

// poolKey identifies a server.
func poolKey(network, address string) string { return network + "|" + address }

// idleTimeout closes parked sessions nobody took (Node's pg.Pool idles out
// after 10 s by default; a process-wide pool keeps them longer).
var idleTimeout = 60 * time.Second

const maxIdlePerKey = 16

func (svc *Sockets) take(key string) *session {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for list := svc.idle[key]; len(list) > 0; list = svc.idle[key] {
		s := list[len(list)-1]
		svc.idle[key] = list[:len(list)-1]
		if alive(s) {
			return s
		}
		_ = s.raw.Close()
	}
	return nil
}

// takeMatch takes a parked session whose recording begins with the
// client's packets so far (pre) and, when given, the same StartupMessage.
func (svc *Sockets) takeMatch(key string, pre [][]byte, startup []byte) *session {
	svc.mu.Lock()
	var found *session
	list := svc.idle[key]
	for i := len(list) - 1; i >= 0; i-- {
		s := list[i]
		if len(s.pre) < len(pre) || startup != nil && (len(s.pre) != len(pre) || !bytes.Equal(s.startup, startup)) {
			continue
		}
		same := true
		for j := range pre {
			same = same && bytes.Equal(s.pre[j], pre[j])
		}
		if same {
			found = s
			svc.idle[key] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	svc.mu.Unlock()
	if found != nil && !alive(found) {
		_ = found.raw.Close()
		return svc.takeMatch(key, pre, startup)
	}
	return found
}

// alive reports whether a parked session's server is still there: nothing
// to read (the server sent nothing unasked) and no EOF.
func alive(s *session) bool {
	_ = s.raw.SetReadDeadline(time.Now())
	var b [1]byte
	_, err := s.raw.Read(b[:])
	_ = s.raw.SetReadDeadline(time.Time{})
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (svc *Sockets) park(s *session) {
	svc.mu.Lock()
	if svc.idle == nil {
		svc.idle = map[string][]*session{}
	}
	if len(svc.idle[s.key]) >= maxIdlePerKey {
		svc.mu.Unlock()
		_ = s.raw.Close()
		return
	}
	svc.idle[s.key] = append(svc.idle[s.key], s)
	svc.mu.Unlock()
	time.AfterFunc(idleTimeout, func() {
		svc.mu.Lock()
		list := svc.idle[s.key]
		for i, x := range list {
			if x == s {
				svc.idle[s.key] = append(list[:i:i], list[i+1:]...)
				svc.mu.Unlock()
				_ = s.raw.Close()
				return
			}
		}
		svc.mu.Unlock()
	})
}

// Close closes every parked session.
func (svc *Sockets) Close() error {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, list := range svc.idle {
		for _, s := range list {
			_ = s.raw.Close()
		}
	}
	svc.idle = nil
	return nil
}

// pgPhase is where a PostgreSQL connection is.
type pgPhase int

const (
	pgPre   pgPhase = iota // before the StartupMessage
	pgAuth                 // authentication, until the first ReadyForQuery
	pgReady                // relaying
)

// pooledConn is the net.Conn of a JS socket over a pooled session.
type pooledConn struct {
	svc  *Sockets
	key  string
	dial func() (net.Conn, error)

	mu         sync.Mutex
	cond       *sync.Cond
	s          *session
	replaying  bool   // answering the client's handshake from s's recording
	out        []byte // answers for the client, before anything from the server
	phase      pgPhase
	notPG      bool   // the first client bytes were not PostgreSQL's
	cbuf       []byte // the client's bytes not yet framed
	sbuf       []byte // the server's bytes not yet framed
	preIdx     int    // replay: the next recorded pre-startup packet
	clientTLS  bool   // the client asked for TLS (done, or answered from the recording)
	tlsCfg     *tls.Config
	awaitAns   bool // recording: the server's one-byte answer comes next
	inGreet    bool // recording: AuthenticationOk seen, greeting being recorded
	pending    int  // Query / Sync / FunctionCall not yet answered by ReadyForQuery
	status     byte // the last ReadyForQuery's transaction status
	broken     bool // never park this session
	interrupt  bool // a read deadline in the past was set
	terminated bool // the client sent Terminate: it reads EOF, as the server would close
	closed     bool
	busy       int  // reads and writes in progress on the session
	wrote      bool // the client has written
	my         mySide
}

// connect returns the connection a JS socket gets to network/address: a
// parked session when there is one (its handshake answered from the
// recording), else a new connection, recorded.
func (svc *Sockets) connect(d net.Dialer, network, address string) (net.Conn, error) {
	key := poolKey(network, address)
	pc := &pooledConn{svc: svc, key: key, dial: func() (net.Conn, error) { return d.Dial(network, address) }}
	pc.cond = sync.NewCond(&pc.mu)
	if s := svc.take(key); s != nil {
		pc.s, pc.replaying = s, true
		if s.proto == 'm' { // the server speaks first: its greeting
			pc.out = append(pc.out, s.turns[0]...)
			pc.my.turn = 1
		}
		return pc, nil
	}
	raw, err := pc.dial()
	if err != nil {
		return nil, err
	}
	pc.s = &session{key: key, raw: raw, conn: raw, tlsAfter: -1, tlsTurn: -1}
	return pc, nil
}

func (pc *pooledConn) LocalAddr() net.Addr  { return pc.s.raw.LocalAddr() }
func (pc *pooledConn) RemoteAddr() net.Addr { return pc.s.raw.RemoteAddr() }
func (pc *pooledConn) SetDeadline(t time.Time) error {
	_ = pc.SetWriteDeadline(t)
	return pc.SetReadDeadline(t)
}
func (pc *pooledConn) SetWriteDeadline(t time.Time) error {
	pc.mu.Lock()
	s := pc.s
	pc.mu.Unlock()
	return s.raw.SetWriteDeadline(t)
}

// SetReadDeadline interrupts a pending Read when t has passed (net.go
// stops its reader this way before a TLS upgrade), whether it waits for the
// server or for answers from the recording.
func (pc *pooledConn) SetReadDeadline(t time.Time) error {
	pc.mu.Lock()
	pc.interrupt = !t.IsZero() && !t.After(time.Now())
	pc.cond.Broadcast()
	s := pc.s
	pc.mu.Unlock()
	return s.raw.SetReadDeadline(t)
}

func (pc *pooledConn) Read(b []byte) (int, error) {
	pc.mu.Lock()
	for {
		if pc.closed {
			pc.mu.Unlock()
			return 0, net.ErrClosed
		}
		if len(pc.out) > 0 {
			n := copy(b, pc.out)
			pc.out = pc.out[n:]
			pc.mu.Unlock()
			return n, nil
		}
		if pc.terminated {
			pc.mu.Unlock()
			return 0, io.EOF
		}
		if pc.interrupt {
			pc.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		if !pc.replaying {
			break
		}
		pc.cond.Wait() // the recording answers what the client sends
	}
	s := pc.s
	pc.busy++
	pc.mu.Unlock()
	n, err := s.conn.Read(b)
	pc.mu.Lock()
	pc.busy--
	if n > 0 && pc.s == s {
		switch {
		case s.proto == 'm':
			pc.myObserve(b[:n])
		case s.proto == 0 && !pc.wrote && looksMySQL(b[:n]):
			s.proto = 'm' // the server spoke first: a MySQL greeting
			pc.myObserve(b[:n])
		default:
			pc.observe(b[:n])
		}
	}
	if pc.terminated && n == 0 {
		err = io.EOF // the read Terminate interrupted
	}
	pc.cond.Broadcast()
	pc.mu.Unlock()
	return n, err
}

// observe follows the server's messages (pc.mu held): the answer to an
// SSLRequest, the greeting while recording, ReadyForQuery afterwards.
func (pc *pooledConn) observe(p []byte) {
	if pc.notPG || pc.broken {
		return
	}
	s := pc.s
	if pc.awaitAns {
		pc.awaitAns = false
		s.preAns = append(s.preAns, p[0])
		p = p[1:]
		if len(p) > 0 {
			pc.broken = true // nothing may follow the answer unasked
			return
		}
	}
	if pc.phase == pgPre {
		return
	}
	pc.sbuf = append(pc.sbuf, p...)
	for len(pc.sbuf) >= 5 {
		n := int(binary.BigEndian.Uint32(pc.sbuf[1:5]))
		if n < 4 {
			pc.broken = true
			return
		}
		if len(pc.sbuf) < 1+n {
			return
		}
		msg := pc.sbuf[:1+n]
		pc.sbuf = pc.sbuf[1+n:]
		switch pc.phase {
		case pgAuth:
			if msg[0] == 'R' && n == 8 && binary.BigEndian.Uint32(msg[5:9]) == 0 {
				pc.inGreet = true
			}
			if msg[0] == 'E' {
				pc.broken = true // authentication failed: nothing to keep
			}
			if pc.inGreet {
				s.greeting = append(s.greeting, msg...)
			}
			if msg[0] == 'Z' {
				pc.phase = pgReady
				pc.status = msg[5]
			}
		case pgReady:
			if msg[0] == 'Z' {
				pc.pending--
				pc.status = msg[5]
			}
		}
	}
	pc.sbuf = append([]byte(nil), pc.sbuf...)
}

func (pc *pooledConn) Write(b []byte) (int, error) {
	pc.mu.Lock()
	if pc.closed {
		pc.mu.Unlock()
		return 0, net.ErrClosed
	}
	pc.wrote = true
	if pc.s.proto == 'm' && !pc.broken {
		return pc.myWrite(b)
	}
	if pc.notPG || pc.broken && !pc.replaying {
		s := pc.s
		pc.busy++
		pc.mu.Unlock()
		n, err := s.conn.Write(b)
		pc.done()
		return n, err
	}
	pc.cbuf = append(pc.cbuf, b...)
	var send []byte
	for {
		msg, ok := pc.nextClientMessage()
		if !ok {
			break
		}
		if pc.notPG {
			send = append(send, msg...)
			send = append(send, pc.cbuf...)
			pc.cbuf = nil
			break
		}
		fwd, err := pc.clientMessage(msg)
		if err != nil {
			pc.mu.Unlock()
			return 0, err
		}
		send = append(send, fwd...)
	}
	s := pc.s
	if len(send) == 0 {
		pc.mu.Unlock()
		return len(b), nil
	}
	pc.busy++
	pc.mu.Unlock()
	_, err := s.conn.Write(send)
	pc.done()
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (pc *pooledConn) done() {
	pc.mu.Lock()
	pc.busy--
	pc.cond.Broadcast()
	pc.mu.Unlock()
}

// nextClientMessage takes one whole message off cbuf (untyped before the
// StartupMessage, typed after), or reports there is none yet. A first
// packet that is not PostgreSQL's marks the connection as another protocol.
func (pc *pooledConn) nextClientMessage() ([]byte, bool) {
	if pc.phase == pgPre {
		if len(pc.cbuf) < 8 {
			// PostgreSQL clients write their first packet whole (8 bytes
			// at least); anything shorter is another protocol, relayed at
			// once (its peer may be waiting for it).
			if len(pc.s.pre) == 0 && !pc.replaying {
				pc.notPG = true
				pc.broken = true
				return nil, true
			}
			return nil, false
		}
		n := int(binary.BigEndian.Uint32(pc.cbuf[:4]))
		code := binary.BigEndian.Uint32(pc.cbuf[4:8])
		if n < 8 || n > 10000 || (code != pgSSLRequest && code != pgGSSENCRequest && code != pgCancelRequest && code>>16 != 3) {
			pc.notPG = true
			pc.broken = true
			return nil, true
		}
		if len(pc.cbuf) < n {
			return nil, false
		}
		msg := pc.cbuf[:n:n]
		pc.cbuf = pc.cbuf[n:]
		return msg, true
	}
	if len(pc.cbuf) < 5 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint32(pc.cbuf[1:5]))
	if n < 4 {
		pc.broken = true
		msg := pc.cbuf
		pc.cbuf = nil
		return msg, true
	}
	if len(pc.cbuf) < 1+n {
		return nil, false
	}
	msg := pc.cbuf[: 1+n : 1+n]
	pc.cbuf = pc.cbuf[1+n:]
	return msg, true
}

// clientMessage handles one message from the client (pc.mu held) and
// returns what goes to the server.
func (pc *pooledConn) clientMessage(msg []byte) ([]byte, error) {
	s := pc.s
	if pc.phase == pgPre {
		code := binary.BigEndian.Uint32(msg[4:8])
		if pc.replaying {
			if code != pgProtocol3 && pc.preIdx < len(s.pre) && bytes.Equal(msg, s.pre[pc.preIdx]) {
				pc.out = append(pc.out, s.preAns[pc.preIdx])
				pc.preIdx++
				pc.cond.Broadcast()
				return nil, nil
			}
			if code == pgProtocol3 && pc.preIdx == len(s.pre) && bytes.Equal(msg, s.startup) {
				pc.out = append(pc.out, s.greeting...)
				pc.replaying = false
				pc.phase = pgReady
				pc.status = 'I'
				pc.cond.Broadcast()
				return nil, nil
			}
			var startup []byte
			if code == pgProtocol3 {
				startup = msg
			}
			if err := pc.fallback(startup); err != nil {
				return nil, err
			}
			s = pc.s
			if pc.replaying { // another parked session matches
				return pc.clientMessage(msg)
			}
		}
		switch code {
		case pgSSLRequest, pgGSSENCRequest:
			s.pre = append(s.pre, append([]byte(nil), msg...))
			pc.awaitAns = true
		case pgProtocol3:
			s.startup = append([]byte(nil), msg...)
			s.proto = 'p'
			pc.phase = pgAuth
		default: // CancelRequest: a one-off connection
			pc.broken = true
		}
		return msg, nil
	}
	if pc.phase == pgReady {
		switch msg[0] {
		case 'X': // Terminate: withheld, the session goes back to the pool
			pc.cbuf = nil
			// The server would close the connection: the client reads EOF
			// (postgres.js waits for it). A reader blocked on the server
			// returns.
			pc.terminated = true
			pc.cond.Broadcast()
			_ = s.raw.SetReadDeadline(time.Now())
			return nil, nil
		case 'Q', 'S', 'F':
			pc.pending++
		}
	}
	return msg, nil
}

// fallback leaves the parked session the client's handshake no longer
// matches (pc.mu held): it ends (Terminate), so a server that serves one
// session at a time is free for the next. Another parked session whose
// recording matches the client's packets so far (and startup, the
// StartupMessage that did not match, if that was it) takes over; else a
// fresh connection, to which the client's steps so far are replayed.
func (pc *pooledConn) fallback(startup []byte) error {
	old := pc.s
	terminatePG(old)
	if s := pc.svc.takeMatch(pc.key, old.pre[:pc.preIdx], startup); s != nil && (!pc.clientTLS || s.tlsState != nil && s.tlsAfter == old.tlsAfter) {
		pc.s = s
		return nil
	} else if s != nil {
		pc.svc.park(s)
	}
	raw, err := pc.dial()
	if err != nil {
		pc.broken = true
		return err
	}
	s := &session{key: pc.key, raw: raw, conn: raw, tlsAfter: -1, tlsTurn: -1}
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	defer raw.SetDeadline(time.Time{})
	for i := 0; i < pc.preIdx; i++ {
		if _, err := raw.Write(old.pre[i]); err != nil {
			return err
		}
		var ans [1]byte
		if _, err := io.ReadFull(raw, ans[:]); err != nil {
			return err
		}
		if ans[0] != old.preAns[i] {
			_ = raw.Close()
			return fmt.Errorf("og: the server answered %q where the pooled session had %q", ans[0], old.preAns[i])
		}
		s.pre = append(s.pre, old.pre[i])
		s.preAns = append(s.preAns, ans[0])
		if old.tlsAfter == i && pc.clientTLS {
			tc := tls.Client(raw, pc.tlsCfg)
			if err := tc.Handshake(); err != nil {
				_ = raw.Close()
				return err
			}
			st := tc.ConnectionState()
			s.conn, s.tlsState, s.tlsAfter = tc, &st, i
		}
	}
	pc.s = s
	pc.replaying = false
	return nil
}

// terminatePG ends a parked session: Terminate, then the connection.
func terminatePG(s *session) {
	_ = s.raw.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = s.conn.Write([]byte{'X', 0, 0, 0, 4})
	_ = s.raw.Close()
}

// startTLS makes the session TLS for the client: a real handshake while
// recording, or, on a parked session that had it after the same exchange,
// nothing new (its state is reported). net.go has stopped its reader.
func (pc *pooledConn) startTLS(cfg *tls.Config) (tls.ConnectionState, error) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.clientTLS, pc.tlsCfg = true, cfg
	s := pc.s
	if s.proto == 'm' {
		return pc.myStartTLS(cfg)
	}
	if pc.replaying {
		if s.tlsState != nil && s.tlsAfter == pc.preIdx-1 {
			return *s.tlsState, nil
		}
		if err := pc.fallback(nil); err != nil {
			return tls.ConnectionState{}, err
		}
		s = pc.s
		if s.tlsState != nil {
			return *s.tlsState, nil
		}
	}
	if s.tlsState != nil {
		return tls.ConnectionState{}, errors.New("og: TLS was already started")
	}
	tc := tls.Client(s.raw, cfg)
	if err := tc.Handshake(); err != nil {
		pc.broken = true
		return tls.ConnectionState{}, err
	}
	st := tc.ConnectionState()
	s.conn, s.tlsState, s.tlsAfter = tc, &st, len(s.pre)-1
	if len(s.pre) == 0 {
		pc.broken = true // TLS before any PostgreSQL packet: another protocol
	}
	return st, nil
}

// CloseWrite is the client's FIN: for a PostgreSQL session at rest it ends
// nothing (the session goes back to the pool on Close).
func (pc *pooledConn) CloseWrite() error {
	pc.mu.Lock()
	s, keep := pc.s, !pc.notPG && !pc.broken
	pc.mu.Unlock()
	if keep {
		return nil
	}
	if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Close ends the client's use: a session at rest is reset and parked, any
// other closes.
func (pc *pooledConn) Close() error {
	pc.mu.Lock()
	if pc.closed {
		pc.mu.Unlock()
		return nil
	}
	pc.closed = true
	s := pc.s
	pc.cond.Broadcast()
	_ = s.raw.SetDeadline(time.Now()) // a read or write blocked on the server returns
	for pc.busy > 0 {
		pc.cond.Wait()
	}
	untouched := pc.replaying && !pc.broken // the parked session never left its rest
	atRest := !pc.notPG && !pc.broken && pc.phase == pgReady && pc.pending == 0 && len(pc.cbuf) == 0 && len(pc.sbuf) == 0
	if s.proto == 'm' {
		atRest = !pc.broken && pc.myAtRest()
	}
	status := pc.status
	pc.mu.Unlock()
	_ = s.raw.SetDeadline(time.Time{})
	switch {
	case untouched:
		pc.svc.park(s)
	case atRest:
		go func() {
			reset := resetPG
			if s.proto == 'm' {
				reset = func(s *session, _ byte) bool { return resetMySQL(s) }
			}
			if reset(s, status) {
				pc.svc.park(s)
			} else {
				_ = s.raw.Close()
			}
		}()
	default:
		_ = s.raw.Close()
	}
	return nil
}

// resetPG returns a session to its state after the greeting: ROLLBACK if
// a transaction is open, then DISCARD ALL (prepared statements, portals,
// temporary tables, settings, LISTEN), as a pooler's reset query.
func resetPG(s *session, status byte) bool {
	_ = s.raw.SetDeadline(time.Now().Add(5 * time.Second))
	defer s.raw.SetDeadline(time.Time{})
	if status != 'I' {
		if st, ok := simpleQueryPG(s.conn, "ROLLBACK"); !ok || st != 'I' {
			return false
		}
	}
	st, ok := simpleQueryPG(s.conn, "DISCARD ALL")
	return ok && st == 'I'
}

// simpleQueryPG runs sql by the simple query protocol and reads to
// ReadyForQuery: its status, and whether no error came.
func simpleQueryPG(c net.Conn, sql string) (byte, bool) {
	q := []byte{'Q', 0, 0, 0, 0}
	q = append(q, sql...)
	q = append(q, 0)
	binary.BigEndian.PutUint32(q[1:5], uint32(len(q)-1))
	if _, err := c.Write(q); err != nil {
		return 0, false
	}
	ok := true
	var hdr [5]byte
	for {
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return 0, false
		}
		n := int(binary.BigEndian.Uint32(hdr[1:]))
		if n < 4 {
			return 0, false
		}
		body := make([]byte, n-4)
		if _, err := io.ReadFull(c, body); err != nil {
			return 0, false
		}
		switch hdr[0] {
		case 'E':
			ok = false
		case 'Z':
			if len(body) != 1 {
				return 0, false
			}
			return body[0], ok
		}
	}
}
