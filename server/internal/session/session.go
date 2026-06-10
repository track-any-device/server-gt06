package session

import (
	"gt06-server/pkg/protocol"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// State tracks the GT06 device connection lifecycle.
// GT06 has no separate auth-token step: a successful Login (0x01) moves the
// session directly from StateConnected to StateLoggedIn.
type State int32

const (
	StateConnected State = iota
	StateLoggedIn
	StateClosing
)

// Session represents one active TCP connection from a GT06 device.
type Session struct {
	conn    net.Conn
	writeMu sync.Mutex

	IMEI string

	state  atomic.Int32
	outSeq atomic.Uint32

	ConnectedAt   time.Time
	LastHeartbeat atomic.Int64
	LastLocation  atomic.Int64
}

func NewSession(conn net.Conn) *Session {
	s := &Session{
		conn:        conn,
		ConnectedAt: time.Now(),
	}
	s.state.Store(int32(StateConnected))
	s.LastHeartbeat.Store(time.Now().UnixNano())
	return s
}

func (s *Session) State() State {
	return State(s.state.Load())
}

func (s *Session) SetState(st State) {
	s.state.Store(int32(st))
}

// WriteACK sends a GT06 ACK frame for the given protocol code and serial number.
func (s *Session) WriteACK(proto uint8, serial uint16, deadline time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(deadline))
	return protocol.WriteACK(s.conn, proto, serial)
}

// Send writes raw bytes to the TCP connection (used for outbound commands).
func (s *Session) Send(data []byte, deadline time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(deadline))
	_, err := s.conn.Write(data)
	return err
}

func (s *Session) Touch() {
	s.LastHeartbeat.Store(time.Now().UnixNano())
}

func (s *Session) TouchLocation() {
	s.LastLocation.Store(time.Now().UnixNano())
}

func (s *Session) IsStale(ttl time.Duration) bool {
	last := time.Unix(0, s.LastHeartbeat.Load())
	return time.Since(last) > ttl
}

func (s *Session) RemoteAddr() string {
	return s.conn.RemoteAddr().String()
}

func (s *Session) SetReadDeadline(d time.Time) error {
	return s.conn.SetReadDeadline(d)
}

func (s *Session) Close() {
	s.state.Store(int32(StateClosing))
	s.conn.Close()
}
