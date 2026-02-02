package udp

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// SessionState represents the current state of a UDP session.
type SessionState int

const (
	SessionStateNew SessionState = iota
	SessionStateHandshaking
	SessionStateEstablished
	SessionStateClosed
)

// Session represents an encrypted UDP session with a peer.
type Session struct {
	mu sync.RWMutex

	// Identity
	localIndex  uint32 // Our session index
	remoteIndex uint32 // Peer's session index
	peerName    string
	peerPublic  [32]byte // Peer's static X25519 public key

	// Network
	remoteAddr *net.UDPAddr
	conn       *net.UDPConn // Shared connection (not owned)

	// Crypto
	crypto     *CryptoState
	sendNonce  atomic.Uint64
	recvWindow *ReplayWindow

	// State
	state        SessionState
	established  time.Time
	lastSend     time.Time
	lastRecv     time.Time
	bytesIn      atomic.Uint64
	bytesOut     atomic.Uint64
	packetsIn    atomic.Uint64
	packetsOut   atomic.Uint64

	// Channels
	recvChan chan []byte // Decrypted data packets
	closeCh  chan struct{}
}

// SessionConfig holds configuration for creating a session.
type SessionConfig struct {
	LocalIndex  uint32
	PeerName    string
	PeerPublic  [32]byte
	RemoteAddr  *net.UDPAddr
	Conn        *net.UDPConn
	WindowSize  int
	RecvBufSize int
}

// NewSession creates a new UDP session.
func NewSession(cfg SessionConfig) *Session {
	if cfg.WindowSize == 0 {
		cfg.WindowSize = DefaultWindowSize
	}
	if cfg.RecvBufSize == 0 {
		cfg.RecvBufSize = 64 // Default receive channel buffer
	}

	return &Session{
		localIndex:  cfg.LocalIndex,
		peerName:    cfg.PeerName,
		peerPublic:  cfg.PeerPublic,
		remoteAddr:  cfg.RemoteAddr,
		conn:        cfg.Conn,
		recvWindow:  NewReplayWindow(cfg.WindowSize),
		state:       SessionStateNew,
		recvChan:    make(chan []byte, cfg.RecvBufSize),
		closeCh:     make(chan struct{}),
	}
}

// SetCrypto sets the crypto state after handshake completes.
func (s *Session) SetCrypto(crypto *CryptoState, remoteIndex uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.crypto = crypto
	s.remoteIndex = remoteIndex
	s.state = SessionStateEstablished
	s.established = time.Now()
}

// State returns the current session state.
func (s *Session) State() SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// SetState updates the session state.
func (s *Session) SetState(state SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

// LocalIndex returns the local session index.
func (s *Session) LocalIndex() uint32 {
	return s.localIndex
}

// RemoteIndex returns the remote session index.
func (s *Session) RemoteIndex() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteIndex
}

// PeerName returns the peer name.
func (s *Session) PeerName() string {
	return s.peerName
}

// RemoteAddr returns the remote address.
func (s *Session) RemoteAddr() *net.UDPAddr {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.remoteAddr
}

// UpdateRemoteAddr updates the remote address (for NAT roaming).
func (s *Session) UpdateRemoteAddr(addr *net.UDPAddr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteAddr = addr
}

// Send encrypts and sends a data packet.
func (s *Session) Send(data []byte) error {
	s.mu.RLock()
	if s.state != SessionStateEstablished {
		s.mu.RUnlock()
		return ErrSessionNotEstablished
	}
	crypto := s.crypto
	remoteIndex := s.remoteIndex
	remoteAddr := s.remoteAddr
	s.mu.RUnlock()

	// Get next nonce
	counter := s.sendNonce.Add(1)

	// Build header
	header := PacketHeader{
		Type:     PacketTypeData,
		Receiver: remoteIndex,
		Counter:  counter,
	}
	headerBytes := header.Marshal()

	// Encrypt data with header as additional data
	ciphertext := crypto.Encrypt(counter, data, headerBytes)

	// Build packet
	packet := make([]byte, len(headerBytes)+len(ciphertext))
	copy(packet, headerBytes)
	copy(packet[len(headerBytes):], ciphertext)

	// Send
	_, err := s.conn.WriteToUDP(packet, remoteAddr)
	if err != nil {
		return err
	}

	s.lastSend = time.Now()
	s.bytesOut.Add(uint64(len(data)))
	s.packetsOut.Add(1)

	return nil
}

// HandlePacket processes an incoming encrypted packet.
func (s *Session) HandlePacket(header *PacketHeader, data []byte) error {
	s.mu.RLock()
	if s.state != SessionStateEstablished {
		s.mu.RUnlock()
		return ErrSessionNotEstablished
	}
	crypto := s.crypto
	s.mu.RUnlock()

	// Check replay window
	if !s.recvWindow.Check(header.Counter) {
		return ErrReplayDetected
	}

	// Decrypt
	headerBytes := header.Marshal()
	plaintext, err := crypto.Decrypt(header.Counter, data, headerBytes)
	if err != nil {
		return err
	}

	s.lastRecv = time.Now()
	s.bytesIn.Add(uint64(len(plaintext)))
	s.packetsIn.Add(1)

	// Deliver to receive channel (non-blocking)
	select {
	case s.recvChan <- plaintext:
	default:
		// Channel full, drop packet
		return ErrReceiveBufferFull
	}

	return nil
}

// Recv returns the channel for receiving decrypted data.
func (s *Session) Recv() <-chan []byte {
	return s.recvChan
}

// SendKeepalive sends a keepalive packet.
func (s *Session) SendKeepalive() error {
	s.mu.RLock()
	if s.state != SessionStateEstablished {
		s.mu.RUnlock()
		return ErrSessionNotEstablished
	}
	remoteIndex := s.remoteIndex
	remoteAddr := s.remoteAddr
	s.mu.RUnlock()

	pkt := NewKeepalivePacket(remoteIndex)
	data := pkt.Header.Marshal()

	_, err := s.conn.WriteToUDP(data, remoteAddr)
	if err != nil {
		return err
	}

	s.lastSend = time.Now()
	return nil
}

// Close closes the session.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == SessionStateClosed {
		return nil
	}

	s.state = SessionStateClosed
	close(s.closeCh)
	close(s.recvChan)

	return nil
}

// Stats returns session statistics.
func (s *Session) Stats() SessionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return SessionStats{
		BytesIn:     s.bytesIn.Load(),
		BytesOut:    s.bytesOut.Load(),
		PacketsIn:   s.packetsIn.Load(),
		PacketsOut:  s.packetsOut.Load(),
		Established: s.established,
		LastSend:    s.lastSend,
		LastRecv:    s.lastRecv,
	}
}

// SessionStats contains session statistics.
type SessionStats struct {
	BytesIn     uint64
	BytesOut    uint64
	PacketsIn   uint64
	PacketsOut  uint64
	Established time.Time
	LastSend    time.Time
	LastRecv    time.Time
}

// Errors
var (
	ErrSessionNotEstablished = &sessionError{"session not established"}
	ErrReplayDetected        = &sessionError{"replay detected"}
	ErrReceiveBufferFull     = &sessionError{"receive buffer full"}
)

type sessionError struct {
	msg string
}

func (e *sessionError) Error() string {
	return e.msg
}
