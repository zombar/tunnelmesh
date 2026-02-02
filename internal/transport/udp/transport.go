package udp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
)

// Transport implements encrypted UDP transport.
type Transport struct {
	mu sync.RWMutex

	// Configuration
	config Config

	// Network
	conn     *net.UDPConn
	listener *Listener

	// Identity
	staticPrivate [32]byte
	staticPublic  [32]byte

	// Sessions (indexed by local index)
	sessions map[uint32]*Session

	// Peer lookup (peer name -> session)
	peerSessions map[string]*Session

	// State
	running atomic.Bool
	closeCh chan struct{}
}

// Config holds UDP transport configuration.
type Config struct {
	// ListenAddr is the UDP address to listen on
	ListenAddr string

	// Port is the UDP port (if ListenAddr not specified)
	Port int

	// StaticPrivate is our X25519 private key
	StaticPrivate [32]byte

	// StaticPublic is our X25519 public key
	StaticPublic [32]byte

	// CoordServerURL is the coordination server URL for hole-punching
	CoordServerURL string

	// AuthToken is the shared auth token for coordination server endpoints
	AuthToken string

	// JWTToken for authenticating with relay (not used for hole-punch)
	JWTToken string

	// KeepaliveInterval for NAT keepalive
	KeepaliveInterval time.Duration

	// HandshakeTimeout for establishing sessions
	HandshakeTimeout time.Duration

	// HolePunchTimeout for NAT traversal
	HolePunchTimeout time.Duration

	// HolePunchRetries is the number of hole-punch attempts
	HolePunchRetries int
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Port:              51820,
		KeepaliveInterval: 25 * time.Second,
		HandshakeTimeout:  10 * time.Second,
		HolePunchTimeout:  10 * time.Second,
		HolePunchRetries:  5,
	}
}

// New creates a new UDP transport.
func New(cfg Config) (*Transport, error) {
	if cfg.KeepaliveInterval == 0 {
		cfg.KeepaliveInterval = 25 * time.Second
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.HolePunchTimeout == 0 {
		cfg.HolePunchTimeout = 10 * time.Second
	}
	if cfg.HolePunchRetries == 0 {
		cfg.HolePunchRetries = 5
	}

	t := &Transport{
		config:       cfg,
		staticPrivate: cfg.StaticPrivate,
		staticPublic:  cfg.StaticPublic,
		sessions:     make(map[uint32]*Session),
		peerSessions: make(map[string]*Session),
		closeCh:      make(chan struct{}),
	}

	return t, nil
}

// Type returns the transport type.
func (t *Transport) Type() transport.TransportType {
	return transport.TransportUDP
}

// Start starts the UDP transport (listening for incoming packets).
func (t *Transport) Start() error {
	if t.running.Load() {
		return fmt.Errorf("transport already running")
	}

	addr := t.config.ListenAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", t.config.Port)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve address: %w", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}

	t.conn = conn
	t.running.Store(true)

	// Start packet receiver
	go t.receiveLoop()

	// Start keepalive sender
	go t.keepaliveLoop()

	log.Info().
		Str("addr", conn.LocalAddr().String()).
		Msg("UDP transport started")

	return nil
}

// receiveLoop processes incoming UDP packets.
func (t *Transport) receiveLoop() {
	buf := make([]byte, 2000) // Larger than MTU

	for t.running.Load() {
		n, remoteAddr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if t.running.Load() {
				log.Debug().Err(err).Msg("UDP read error")
			}
			continue
		}

		if n < MinPacketSize {
			continue // Too short
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])

		go t.handlePacket(packet, remoteAddr)
	}
}

// handlePacket processes a single incoming packet.
func (t *Transport) handlePacket(data []byte, remoteAddr *net.UDPAddr) {
	header, err := UnmarshalHeader(data)
	if err != nil {
		return
	}

	switch header.Type {
	case PacketTypeHandshakeInit:
		t.handleHandshakeInit(data, remoteAddr)
	case PacketTypeHandshakeResponse:
		t.handleHandshakeResponse(data, remoteAddr)
	case PacketTypeData:
		t.handleDataPacket(header, data[HeaderSize:], remoteAddr)
	case PacketTypeKeepalive:
		t.handleKeepalive(header, remoteAddr)
	}
}

// handleDataPacket processes an incoming data packet.
func (t *Transport) handleDataPacket(header *PacketHeader, ciphertext []byte, remoteAddr *net.UDPAddr) {
	t.mu.RLock()
	session, ok := t.sessions[header.Receiver]
	t.mu.RUnlock()

	if !ok {
		return // Unknown session
	}

	// Update remote address (NAT roaming)
	currentAddr := session.RemoteAddr()
	if currentAddr == nil || currentAddr.String() != remoteAddr.String() {
		session.UpdateRemoteAddr(remoteAddr)
	}

	if err := session.HandlePacket(header, ciphertext); err != nil {
		log.Debug().
			Err(err).
			Str("peer", session.PeerName()).
			Msg("packet handling error")
	}
}

// handleKeepalive processes a keepalive packet.
func (t *Transport) handleKeepalive(header *PacketHeader, remoteAddr *net.UDPAddr) {
	t.mu.RLock()
	session, ok := t.sessions[header.Receiver]
	t.mu.RUnlock()

	if ok {
		session.UpdateRemoteAddr(remoteAddr)
	}
}

// handleHandshakeInit processes an incoming handshake initiation.
func (t *Transport) handleHandshakeInit(data []byte, remoteAddr *net.UDPAddr) {
	if len(data) < 128 {
		return
	}

	// Create responder handshake state
	// Responder doesn't know initiator's static key until they decrypt it from the message
	hs, err := NewResponderHandshake(t.staticPrivate, t.staticPublic)
	if err != nil {
		log.Debug().Err(err).Msg("failed to create handshake state")
		return
	}

	if err := hs.ConsumeInitiation(data); err != nil {
		log.Debug().Err(err).Msg("failed to consume initiation")
		return
	}

	// Create and send response
	response, err := hs.CreateResponse()
	if err != nil {
		log.Debug().Err(err).Msg("failed to create response")
		return
	}

	// Prepend packet type header
	packet := make([]byte, 1+len(response))
	packet[0] = PacketTypeHandshakeResponse
	copy(packet[1:], response)

	if _, err := t.conn.WriteToUDP(packet, remoteAddr); err != nil {
		log.Debug().Err(err).Msg("failed to send response")
		return
	}

	// Derive keys (DeriveKeys handles initiator/responder key ordering)
	sendKey, recvKey, err := hs.DeriveKeys()
	if err != nil {
		log.Debug().Err(err).Msg("failed to derive keys")
		return
	}

	crypto, err := NewCryptoState(sendKey, recvKey)
	if err != nil {
		log.Debug().Err(err).Msg("failed to create crypto state")
		return
	}

	// Create session
	session := NewSession(SessionConfig{
		LocalIndex: hs.LocalIndex(),
		PeerPublic: hs.PeerStaticPublic(),
		RemoteAddr: remoteAddr,
		Conn:       t.conn,
	})
	session.SetCrypto(crypto, hs.RemoteIndex())

	t.mu.Lock()
	t.sessions[session.LocalIndex()] = session
	t.mu.Unlock()

	log.Debug().
		Str("remote", remoteAddr.String()).
		Uint32("local_index", session.LocalIndex()).
		Msg("incoming handshake completed")
}

// handleHandshakeResponse processes an incoming handshake response.
func (t *Transport) handleHandshakeResponse(data []byte, remoteAddr *net.UDPAddr) {
	// This is handled by the dial goroutine waiting for response
	// For now, we'll use a simple callback mechanism
}

// keepaliveLoop sends periodic keepalives to maintain NAT mappings.
func (t *Transport) keepaliveLoop() {
	ticker := time.NewTicker(t.config.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.sendKeepalives()
		case <-t.closeCh:
			return
		}
	}
}

// sendKeepalives sends keepalive packets to all active sessions.
func (t *Transport) sendKeepalives() {
	t.mu.RLock()
	sessions := make([]*Session, 0, len(t.sessions))
	for _, s := range t.sessions {
		if s.State() == SessionStateEstablished {
			sessions = append(sessions, s)
		}
	}
	t.mu.RUnlock()

	for _, s := range sessions {
		if err := s.SendKeepalive(); err != nil {
			log.Debug().
				Err(err).
				Str("peer", s.PeerName()).
				Msg("keepalive failed")
		}
	}
}

// Dial creates an outbound connection to a peer.
func (t *Transport) Dial(ctx context.Context, opts transport.DialOptions) (transport.Connection, error) {
	log.Debug().
		Str("peer", opts.PeerName).
		Bool("has_peer_info", opts.PeerInfo != nil).
		Msg("UDP Dial called")

	if !t.running.Load() {
		// Start transport if not already running
		if err := t.Start(); err != nil {
			return nil, err
		}
	}

	// Get peer's UDP endpoint via coordination server
	peerEndpoint, err := t.getPeerEndpoint(ctx, opts.PeerName)
	if err != nil {
		return nil, fmt.Errorf("get peer endpoint: %w", err)
	}

	// Resolve peer address
	peerAddr, err := net.ResolveUDPAddr("udp", peerEndpoint)
	if err != nil {
		return nil, fmt.Errorf("resolve peer address: %w", err)
	}

	// Get peer's X25519 public key from ED25519 public key
	var peerPublic [32]byte
	if opts.PeerInfo != nil && opts.PeerInfo.PublicKey != "" {
		sshPubKey, err := config.DecodePublicKey(opts.PeerInfo.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("decode peer public key: %w", err)
		}

		// Extract the raw ED25519 key and convert to X25519
		ed25519Key := sshPubKey.Marshal()
		// SSH format: 4 bytes length + "ssh-ed25519" + 4 bytes length + 32 bytes key
		// Skip the SSH wire format header to get raw ED25519 key
		keyLen := len(ed25519Key)
		if keyLen < 32 {
			return nil, fmt.Errorf("invalid ED25519 key length: %d", keyLen)
		}
		rawED25519 := ed25519Key[keyLen-32:]

		log.Debug().
			Int("ssh_key_len", keyLen).
			Hex("raw_ed25519", rawED25519).
			Str("key_type", sshPubKey.Type()).
			Msg("extracting ED25519 public key for X25519 conversion")

		x25519Key, err := config.ED25519PublicToX25519(rawED25519)
		if err != nil {
			return nil, fmt.Errorf("convert ED25519 to X25519: %w", err)
		}

		log.Debug().
			Hex("x25519_pub", x25519Key).
			Str("peer", opts.PeerName).
			Msg("converted ED25519 to X25519 public key")

		copy(peerPublic[:], x25519Key)
	} else {
		return nil, fmt.Errorf("peer public key not available")
	}

	// Attempt hole-punch if needed
	if err := t.holePunch(ctx, opts.PeerName, peerAddr); err != nil {
		log.Debug().Err(err).Str("peer", opts.PeerName).Msg("hole-punch failed, trying direct")
	}

	// Perform handshake
	session, err := t.initiateHandshake(ctx, opts.PeerName, peerPublic, peerAddr)
	if err != nil {
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	// Create connection wrapper
	return &Connection{
		session: session,
		readBuf: new(bytes.Buffer),
	}, nil
}

// initiateHandshake performs the Noise IK handshake as initiator.
func (t *Transport) initiateHandshake(ctx context.Context, peerName string, peerPublic [32]byte, peerAddr *net.UDPAddr) (*Session, error) {
	hs, err := NewInitiatorHandshake(t.staticPrivate, t.staticPublic, peerPublic)
	if err != nil {
		return nil, err
	}

	// Create initiation message
	initMsg, err := hs.CreateInitiation()
	if err != nil {
		return nil, err
	}

	// Prepend packet type header
	packet := make([]byte, 1+len(initMsg))
	packet[0] = PacketTypeHandshakeInit
	copy(packet[1:], initMsg)

	// Send initiation
	if _, err := t.conn.WriteToUDP(packet, peerAddr); err != nil {
		return nil, err
	}

	// Wait for response with timeout
	timeout := t.config.HandshakeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	// Set read deadline for receiving response
	_ = t.conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = t.conn.SetReadDeadline(time.Time{}) }()

	// Read response
	buf := make([]byte, 256)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		n, from, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return nil, fmt.Errorf("handshake timeout")
			}
			return nil, err
		}

		// Verify it's from the expected peer and is a response
		if from.String() != peerAddr.String() {
			continue // Ignore packets from other sources
		}

		if n < 73 || buf[0] != PacketTypeHandshakeResponse {
			continue // Not a valid response
		}

		// Process response
		if err := hs.ConsumeResponse(buf[1:n]); err != nil {
			return nil, fmt.Errorf("invalid response: %w", err)
		}

		break
	}

	// Derive keys
	sendKey, recvKey, err := hs.DeriveKeys()
	if err != nil {
		return nil, err
	}

	crypto, err := NewCryptoState(sendKey, recvKey)
	if err != nil {
		return nil, err
	}

	// Create session
	session := NewSession(SessionConfig{
		LocalIndex: hs.LocalIndex(),
		PeerName:   peerName,
		PeerPublic: peerPublic,
		RemoteAddr: peerAddr,
		Conn:       t.conn,
	})
	session.SetCrypto(crypto, hs.RemoteIndex())

	t.mu.Lock()
	t.sessions[session.LocalIndex()] = session
	t.peerSessions[peerName] = session
	t.mu.Unlock()

	log.Debug().
		Str("peer", peerName).
		Str("addr", peerAddr.String()).
		Msg("handshake completed")

	return session, nil
}

// holePunch attempts to establish NAT traversal with the peer.
func (t *Transport) holePunch(ctx context.Context, peerName string, peerAddr *net.UDPAddr) error {
	if t.config.CoordServerURL == "" {
		return nil // No coordination server, skip hole-punch
	}

	// Request hole-punch coordination from server
	reqBody := map[string]string{
		"from_peer":     "", // TODO: get our peer name
		"to_peer":       peerName,
		"local_addr":    t.conn.LocalAddr().String(),
		"external_addr": "", // TODO: get our external addr
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST",
		t.config.CoordServerURL+"/api/v1/udp/holepunch",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if t.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.AuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Send hole-punch packets
	for i := 0; i < t.config.HolePunchRetries; i++ {
		// Send a small packet to punch the hole
		_, _ = t.conn.WriteToUDP([]byte{0}, peerAddr)
		time.Sleep(100 * time.Millisecond)

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}

	return nil
}

// getPeerEndpoint retrieves the peer's UDP endpoint from coordination server.
func (t *Transport) getPeerEndpoint(ctx context.Context, peerName string) (string, error) {
	if t.config.CoordServerURL == "" {
		return "", fmt.Errorf("coordination server URL not configured")
	}

	url := t.config.CoordServerURL + "/api/v1/udp/endpoint/" + peerName
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	if t.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.AuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get endpoint: %s", resp.Status)
	}

	var endpoint struct {
		ExternalAddr string `json:"external_addr"`
		LocalAddr    string `json:"local_addr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&endpoint); err != nil {
		return "", err
	}

	if endpoint.ExternalAddr != "" {
		return endpoint.ExternalAddr, nil
	}
	return endpoint.LocalAddr, nil
}

// Listen returns a listener for incoming connections.
func (t *Transport) Listen(ctx context.Context, opts transport.ListenOptions) (transport.Listener, error) {
	if !t.running.Load() {
		if err := t.Start(); err != nil {
			return nil, err
		}
	}

	t.listener = &Listener{
		transport: t,
		acceptCh:  make(chan *Connection, 16),
	}

	return t.listener, nil
}

// Probe tests if the peer is reachable via UDP.
func (t *Transport) Probe(ctx context.Context, opts transport.ProbeOptions) (time.Duration, error) {
	// For UDP, we need to check if the peer has registered their endpoint
	if t.config.CoordServerURL == "" {
		return 0, fmt.Errorf("coordination server not configured")
	}

	if opts.PeerInfo == nil {
		return 0, fmt.Errorf("peer info required")
	}

	start := time.Now()

	// Check if peer has UDP endpoint registered
	_, err := t.getPeerEndpoint(ctx, opts.PeerInfo.Name)
	if err != nil {
		return 0, err
	}

	// Return estimated latency (we don't actually probe the peer yet)
	// A real implementation would send a probe packet and measure RTT
	return time.Since(start) + 10*time.Millisecond, nil
}

// Close shuts down the transport.
func (t *Transport) Close() error {
	if !t.running.Load() {
		return nil
	}

	t.running.Store(false)
	close(t.closeCh)

	t.mu.Lock()
	for _, s := range t.sessions {
		s.Close()
	}
	t.sessions = make(map[uint32]*Session)
	t.peerSessions = make(map[string]*Session)
	t.mu.Unlock()

	if t.conn != nil {
		t.conn.Close()
	}

	return nil
}

// Connection wraps a UDP session as a transport.Connection.
type Connection struct {
	session *Session
	readBuf *bytes.Buffer
	mu      sync.Mutex
}

// Read reads data from the connection.
func (c *Connection) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Return buffered data first
	if c.readBuf.Len() > 0 {
		return c.readBuf.Read(p)
	}

	// Wait for new data
	data, ok := <-c.session.Recv()
	if !ok {
		return 0, io.EOF
	}
	c.readBuf.Write(data)
	return c.readBuf.Read(p)
}

// Write writes data to the connection.
func (c *Connection) Write(p []byte) (int, error) {
	if err := c.session.Send(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes the connection.
func (c *Connection) Close() error {
	return c.session.Close()
}

// PeerName returns the peer name.
func (c *Connection) PeerName() string {
	return c.session.PeerName()
}

// Type returns the transport type.
func (c *Connection) Type() transport.TransportType {
	return transport.TransportUDP
}

// LocalAddr returns the local address.
func (c *Connection) LocalAddr() net.Addr {
	return nil // UDP doesn't have a specific local address per connection
}

// RemoteAddr returns the remote address.
func (c *Connection) RemoteAddr() net.Addr {
	return c.session.RemoteAddr()
}

// Listener accepts incoming UDP connections.
type Listener struct {
	transport *Transport
	acceptCh  chan *Connection
	closed    atomic.Bool
}

// Accept waits for and returns the next connection.
func (l *Listener) Accept(ctx context.Context) (transport.Connection, error) {
	select {
	case conn := <-l.acceptCh:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Addr returns the listener's address.
func (l *Listener) Addr() net.Addr {
	if l.transport.conn != nil {
		return l.transport.conn.LocalAddr()
	}
	return nil
}

// Close closes the listener.
func (l *Listener) Close() error {
	l.closed.Store(true)
	close(l.acceptCh)
	return nil
}

// RegisterUDPEndpoint registers our UDP endpoint with the coordination server.
func (t *Transport) RegisterUDPEndpoint(ctx context.Context, peerName string) error {
	if t.config.CoordServerURL == "" {
		return fmt.Errorf("coordination server URL not configured")
	}

	localAddr := ""
	if t.conn != nil {
		localAddr = t.conn.LocalAddr().String()
	}

	// Get local port
	port := t.config.Port
	if t.conn != nil {
		if addr, ok := t.conn.LocalAddr().(*net.UDPAddr); ok {
			port = addr.Port
		}
	}

	reqBody := map[string]interface{}{
		"peer_name":  peerName,
		"local_addr": localAddr,
		"udp_port":   port,
	}

	bodyBytes, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST",
		t.config.CoordServerURL+"/api/v1/udp/register",
		bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	if t.config.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.config.AuthToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register failed: %s - %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var result struct {
		OK           bool   `json:"ok"`
		ExternalAddr string `json:"external_addr"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	log.Debug().
		Str("external_addr", result.ExternalAddr).
		Msg("UDP endpoint registered")

	return nil
}
