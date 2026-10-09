package nodeshim

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"time"
)

// The MySQL side of the pool (netpool.go). The server speaks first: its
// greeting carries a scramble, and the client's authentication answers it.
// A fresh session's handshake is recorded as a transcript of turns
// (server bytes, client bytes, ..., up to the server's OK); a parked
// session's transcript is replayed to the next client, whose turns must be
// the recorded ones byte for byte (with the same scramble and credentials
// they are: mysql_native_password and caching_sha2_password's fast path
// are deterministic). A client whose turn differs cannot be moved to
// another connection (it answered this session's scramble): it gets an ERR
// packet, and the parked session ends, so that a retry connects afresh
// (COM_CHANGE_USER on the parked session would serve it; TODO).
//
// While relaying, responses are followed to the end of each command (OK,
// ERR, result sets with or without EOF packets, prepared statements), so a
// session is parked only between commands; it is reset with
// COM_RESET_CONNECTION (which also rolls back). COM_QUIT is withheld.

const (
	myClientSSL        = 0x00000800
	myDeprecateEOF     = 0x01000000
	myMoreResults      = 0x0008
	myComQuit          = 0x01
	myComFieldList     = 0x04
	myComChangeUser    = 0x11
	myComStmtPrepare   = 0x16
	myComStmtSendLong  = 0x18
	myComStmtClose     = 0x19
	myComResetConn     = 0x1f
	myComStmtFetch     = 0x1c
	myMaxPacketPayload = 0xffffff
)

// myState is where a MySQL connection is in its current command.
type myState int

const (
	myIdle myState = iota
	myResp
	myCols
	myColsEOF
	myRows
	myPrepResp
	myPrepParams
	myPrepParamsEOF
	myPrepCols
	myPrepColsEOF
	myFieldList
)

type mySide struct {
	cbuf, sbuf []byte
	turn       int  // replay: the next transcript turn
	newTurn    bool // recording: the next bytes start a turn (after TLS)
	authDone   bool
	noPark     bool // relay, but never park (full authentication, LOCAL INFILE, COM_CHANGE_USER, ...)
	state      myState
	remaining  int
	prepCols   int
	contS      bool // the server's packet continues (a 16 MB payload)
	serverCaps uint32
}

// looksMySQL reports a MySQL greeting: the first packet (seq 0) of a
// protocol 10 handshake.
func looksMySQL(p []byte) bool {
	if len(p) < 5 || p[3] != 0 || p[4] != 0x0a {
		return false
	}
	n := int(p[0]) | int(p[1])<<8 | int(p[2])<<16
	return n > 20 && n < 1024
}

// recordTurn appends bytes to the transcript (pc.mu held).
func (s *session) recordTurn(dir byte, p []byte, force bool) {
	if len(s.turns) > 0 && s.turnDir[len(s.turnDir)-1] == dir && !force {
		s.turns[len(s.turns)-1] = append(s.turns[len(s.turns)-1], p...)
		return
	}
	s.turns = append(s.turns, append([]byte(nil), p...))
	s.turnDir = append(s.turnDir, dir)
}

// nextPacket takes one whole packet (header and payload) off *buf.
func nextPacket(buf *[]byte) ([]byte, bool) {
	b := *buf
	if len(b) < 4 {
		return nil, false
	}
	n := int(b[0]) | int(b[1])<<8 | int(b[2])<<16
	if len(b) < 4+n {
		return nil, false
	}
	*buf = b[4+n:]
	return b[: 4+n : 4+n], true
}

// myObserve follows the server (pc.mu held): the handshake while
// recording, then each command's response.
func (pc *pooledConn) myObserve(p []byte) {
	m, s := &pc.my, pc.s
	if pc.broken {
		return
	}
	if !m.authDone {
		s.recordTurn('s', p, m.newTurn)
		m.newTurn = false
	}
	m.sbuf = append(m.sbuf, p...)
	for {
		pkt, ok := nextPacket(&m.sbuf)
		if !ok {
			break
		}
		n := len(pkt) - 4
		if m.contS {
			m.contS = n == myMaxPacketPayload
			continue
		}
		m.contS = n == myMaxPacketPayload
		if n == 0 {
			continue
		}
		h := pkt[4]
		if !m.authDone {
			switch {
			case h == 0x0a && pkt[3] == 0:
				m.serverCaps = greetingCaps(pkt[4:])
			case h == 0x00:
				m.authDone = true
				s.deprecateEOF = s.clientCaps&m.serverCaps&myDeprecateEOF != 0
			case h == 0xff:
				pc.broken = true // authentication failed
			case h == 0x01 && n >= 2 && pkt[5] == 0x04:
				m.noPark = true // full authentication: the client's next turn is not deterministic
			}
			continue
		}
		pc.myResponse(pkt[4:])
	}
	m.sbuf = append([]byte(nil), m.sbuf...)
}

// greetingCaps reads the capability flags of a protocol 10 greeting.
func greetingCaps(p []byte) uint32 {
	i := bytes.IndexByte(p[1:], 0)
	if i < 0 {
		return 0
	}
	off := 1 + i + 1 + 4 + 8 + 1 // version, connection id, scramble part 1, filler
	if len(p) < off+2 {
		return 0
	}
	caps := uint32(binary.LittleEndian.Uint16(p[off:]))
	if len(p) >= off+7 {
		caps |= uint32(binary.LittleEndian.Uint16(p[off+5:])) << 16
	}
	return caps
}

// lenenc reads a length-encoded integer.
func lenenc(p []byte) (uint64, int) {
	if len(p) == 0 {
		return 0, 0
	}
	switch p[0] {
	case 0xfc:
		if len(p) >= 3 {
			return uint64(binary.LittleEndian.Uint16(p[1:])), 3
		}
	case 0xfd:
		if len(p) >= 4 {
			return uint64(p[1]) | uint64(p[2])<<8 | uint64(p[3])<<16, 4
		}
	case 0xfe:
		if len(p) >= 9 {
			return binary.LittleEndian.Uint64(p[1:]), 9
		}
	default:
		if p[0] < 0xfb {
			return uint64(p[0]), 1
		}
	}
	return 0, 0
}

// okStatus is the status flags of an OK packet (header 0x00 or 0xfe).
func okStatus(p []byte) uint16 {
	off := 1
	for i := 0; i < 2; i++ {
		_, n := lenenc(p[off:])
		if n == 0 {
			return 0
		}
		off += n
	}
	if len(p) < off+2 {
		return 0
	}
	return binary.LittleEndian.Uint16(p[off:])
}

// endOfRows reports the packet that ends rows: EOF, or the OK that
// replaces it (CLIENT_DEPRECATE_EOF), and the status it carries.
func (pc *pooledConn) endOfRows(p []byte) (uint16, bool) {
	if p[0] != 0xfe || len(p) >= myMaxPacketPayload {
		return 0, false
	}
	if pc.s.deprecateEOF {
		return okStatus(p), true
	}
	if len(p) < 9 {
		if len(p) >= 5 {
			return binary.LittleEndian.Uint16(p[3:]), true
		}
		return 0, true
	}
	return 0, false
}

// myResponse moves the command's state along one response packet.
func (pc *pooledConn) myResponse(p []byte) {
	m := &pc.my
	next := func(status uint16) {
		if status&myMoreResults != 0 {
			m.state = myResp
		} else {
			m.state = myIdle
		}
	}
	switch m.state {
	case myIdle:
		m.noPark = true // nothing was asked
	case myResp:
		switch p[0] {
		case 0x00:
			next(okStatus(p))
		case 0xff:
			m.state = myIdle
		case 0xfb:
			m.noPark = true // LOCAL INFILE: the client sends a file
		default:
			n, w := lenenc(p)
			if w == 0 || n == 0 {
				m.noPark = true
				return
			}
			m.state, m.remaining = myCols, int(n)
		}
	case myCols:
		if m.remaining--; m.remaining == 0 {
			if pc.s.deprecateEOF {
				m.state = myRows
			} else {
				m.state = myColsEOF
			}
		}
	case myColsEOF:
		m.state = myRows
	case myRows:
		if p[0] == 0xff {
			m.state = myIdle
		} else if st, end := pc.endOfRows(p); end {
			next(st)
		}
	case myPrepResp:
		if p[0] != 0x00 || len(p) < 9 {
			m.state = myIdle
			return
		}
		cols, params := int(binary.LittleEndian.Uint16(p[5:])), int(binary.LittleEndian.Uint16(p[7:]))
		m.prepCols = cols
		switch {
		case params > 0:
			m.state, m.remaining = myPrepParams, params
		case cols > 0:
			m.state, m.remaining = myPrepCols, cols
		default:
			m.state = myIdle
		}
	case myPrepParams:
		if m.remaining--; m.remaining == 0 {
			switch {
			case !pc.s.deprecateEOF:
				m.state = myPrepParamsEOF
			case m.prepCols > 0:
				m.state, m.remaining = myPrepCols, m.prepCols
			default:
				m.state = myIdle
			}
		}
	case myPrepParamsEOF:
		if m.prepCols > 0 {
			m.state, m.remaining = myPrepCols, m.prepCols
		} else {
			m.state = myIdle
		}
	case myPrepCols:
		if m.remaining--; m.remaining == 0 {
			if pc.s.deprecateEOF {
				m.state = myIdle
			} else {
				m.state = myPrepColsEOF
			}
		}
	case myPrepColsEOF:
		m.state = myIdle
	case myFieldList:
		if p[0] == 0xff {
			m.state = myIdle
		} else if _, end := pc.endOfRows(p); end {
			m.state = myIdle
		}
	}
}

// myWrite handles the client's bytes (pc.mu held; it is released).
func (pc *pooledConn) myWrite(b []byte) (int, error) {
	m, s := &pc.my, pc.s
	if pc.replaying {
		m.cbuf = append(m.cbuf, b...)
		if err := pc.myReplay(); err != nil || pc.replaying || len(m.cbuf) == 0 {
			pc.mu.Unlock()
			return len(b), err
		}
		b, m.cbuf = m.cbuf, nil // what followed the handshake: commands
	}
	var send []byte
	if !m.authDone {
		s.recordTurn('c', b, m.newTurn)
		m.newTurn = false
		m.cbuf = append(m.cbuf, b...)
		for {
			pkt, ok := nextPacket(&m.cbuf)
			if !ok {
				break
			}
			if len(pkt) >= 8 && pkt[3] >= 1 && s.clientCaps == 0 {
				s.clientCaps = binary.LittleEndian.Uint32(pkt[4:])
			}
		}
		send = b
	} else {
		m.cbuf = append(m.cbuf, b...)
		for {
			pkt, ok := nextPacket(&m.cbuf)
			if !ok {
				break
			}
			if pkt[3] == 0 && len(pkt) > 4 {
				if m.state != myIdle {
					m.noPark = true // a command before the last one's answer
				}
				switch pkt[4] {
				case myComQuit: // withheld: the session goes back to the pool
					m.cbuf = nil
					pc.terminated = true
					pc.cond.Broadcast()
					_ = s.raw.SetReadDeadline(time.Now())
					break
				case myComStmtClose, myComStmtSendLong:
				case myComStmtPrepare:
					m.state = myPrepResp
				case myComStmtFetch:
					m.state = myRows
				case myComFieldList:
					m.state = myFieldList
				case myComChangeUser:
					m.noPark = true
					m.state = myResp
				default:
					m.state = myResp
				}
				if pc.terminated {
					break
				}
			} else if m.state == myIdle {
				m.noPark = true // client data outside a command (LOCAL INFILE)
			}
			send = append(send, pkt...)
		}
	}
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

// myReplay matches the client's bytes against the parked session's
// transcript and answers each matched turn with the server's next ones.
func (pc *pooledConn) myReplay() error {
	m, s := &pc.my, pc.s
	for m.turn < len(s.turns) && s.turnDir[m.turn] == 'c' {
		want := s.turns[m.turn]
		if len(m.cbuf) < len(want) {
			if !bytes.HasPrefix(want, m.cbuf) {
				return pc.myMismatch()
			}
			return nil
		}
		if !bytes.Equal(m.cbuf[:len(want)], want) {
			return pc.myMismatch()
		}
		m.cbuf = m.cbuf[len(want):]
		m.turn++
		for m.turn < len(s.turns) && s.turnDir[m.turn] == 's' {
			pc.out = append(pc.out, s.turns[m.turn]...)
			m.turn++
		}
		pc.cond.Broadcast()
	}
	if m.turn == len(s.turns) {
		pc.replaying = false
		m.authDone = true
		m.state = myIdle
	}
	return nil
}

// myMismatch answers a client whose handshake is not the parked session's
// with an ERR packet (after which it reads EOF) and ends the session.
func (pc *pooledConn) myMismatch() error {
	m := &pc.my
	seq := byte(1)
	if len(m.cbuf) >= 4 {
		seq = m.cbuf[3] + 1
	}
	msg := "og: a pooled connection to this server was opened with other credentials or options; it was closed, connect again"
	payload := append([]byte{0xff, 0x51, 0x04, '#'}, "HY000"+msg...) // 1105, ER_UNKNOWN_ERROR
	pkt := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	pc.out = append(pc.out, append(pkt, payload...)...)
	pc.terminated = true
	pc.broken = true
	pc.replaying = false
	m.cbuf = nil
	pc.cond.Broadcast()
	quitMySQL(pc.s)
	return nil
}

// myStartTLS: the client's TLS after its SSLRequest. Recording: a real
// handshake; replaying a session that had TLS after the same turn: nothing
// new.
func (pc *pooledConn) myStartTLS(cfg *tls.Config) (tls.ConnectionState, error) {
	m, s := &pc.my, pc.s
	if pc.replaying {
		if s.tlsState != nil && s.tlsTurn == m.turn-1 {
			return *s.tlsState, nil
		}
		_ = pc.myMismatch()
		return tls.ConnectionState{}, errors.New("og: the pooled MySQL session had no TLS at this point")
	}
	tc := tls.Client(s.raw, cfg)
	if err := tc.Handshake(); err != nil {
		pc.broken = true
		return tls.ConnectionState{}, err
	}
	st := tc.ConnectionState()
	s.conn, s.tlsState, s.tlsTurn = tc, &st, len(s.turns)-1
	m.newTurn = true
	return st, nil
}

// myAtRest reports a session between commands, handshake done.
func (pc *pooledConn) myAtRest() bool {
	m := &pc.my
	return m.authDone && !m.noPark && m.state == myIdle && len(m.cbuf) == 0 && len(m.sbuf) == 0 && !m.contS
}

// resetMySQL resets a session with COM_RESET_CONNECTION (rolling back,
// dropping temporary tables, user variables and prepared statements).
func resetMySQL(s *session) bool {
	_ = s.raw.SetDeadline(time.Now().Add(5 * time.Second))
	defer s.raw.SetDeadline(time.Time{})
	if _, err := s.conn.Write([]byte{1, 0, 0, 0, myComResetConn}); err != nil {
		return false
	}
	var hdr [4]byte
	if _, err := io.ReadFull(s.conn, hdr[:]); err != nil {
		return false
	}
	n := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	body := make([]byte, n)
	if _, err := io.ReadFull(s.conn, body); err != nil || n == 0 {
		return false
	}
	return hdr[3] == 1 && body[0] == 0x00
}

// quitMySQL ends a session: COM_QUIT, then the connection.
func quitMySQL(s *session) {
	_ = s.raw.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = s.conn.Write([]byte{1, 0, 0, 0, myComQuit})
	_ = s.raw.Close()
}
