package udp

import (
	"bytes"
	"context"
	"encoding/binary"
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

// handshakeResponse is used to pass handshake responses from receiveLoop to initiators.
type handshakeResponse struct {
	data       []byte
	remoteAddr *net.UDPAddr
}

// Transport implements encrypted UDP transport.
type Transport struct {
	mu sync.RWMutex

	// Configuration
	config Config

	// Network - dual-stack support with separate IPv4 and IPv6 sockets
	conn     *net.UDPConn // IPv4 socket (or single socket if dual-stack disabled)
	conn6    *net.UDPConn // IPv6 socket (nil if not available)
	listener *Listener

	// Identity
	staticPrivate [32]byte
	staticPublic  [32]byte

	// Our discovered external addresses (set after registration)
	externalAddr  string // IPv4 external address
	externalAddr6 string // IPv6 external address

	// Sessions (indexed by local index)
	sessions map[uint32]*Session

	// Peer lookup (peer name -> session)
	peerSessions map[string]*Session

	// Pending handshakes (local index -> response channel)
	pendingHandshakes map[uint32]chan handshakeResponse

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

	// LocalPeerName is our own peer name (used for hole-punch coordination)
	LocalPeerName string

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

	// PeerResolver looks up peer name from their X25519 public key
	// Returns empty string if peer is unknown
	PeerResolver func(pubKey [32]byte) string

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
		config:            cfg,
		staticPrivate:     cfg.StaticPrivate,
		staticPublic:      cfg.StaticPublic,
		sessions:          make(map[uint32]*Session),
		peerSessions:      make(map[string]*Session),
		pendingHandshakes: make(map[uint32]chan handshakeResponse),
		closeCh:           make(chan struct{}),
	}

	return t, nil
}

// Type returns the transport type.
func (t *Transport) Type() transport.TransportType {
	return transport.TransportUDP
}

// selectSocketForPeer returns the appropriate socket for communicating with the peer.
// Returns the IPv4 socket for IPv4 addresses, IPv6 socket for IPv6 addresses.
func (t *Transport) selectSocketForPeer(peerAddr *net.UDPAddr) *net.UDPConn {
	if peerAddr.IP.To4() != nil {
		// Peer is IPv4
		return t.conn
	}
	// Peer is IPv6
	if t.conn6 != nil {
		return t.conn6
	}
	// Fall back to main connection if no IPv6 socket (might be dual-stack)
	return t.conn
}

// Start starts the UDP transport (listening for incoming packets).
// Creates both IPv4 and IPv6 sockets for dual-stack support.
func (t *Transport) Start() error {
	if t.running.Load() {
		return fmt.Errorf("transport already running")
	}

	port := t.config.Port
	if t.config.ListenAddr != "" {
		// If explicit address provided, use it
		udpAddr, err := net.ResolveUDPAddr("udp", t.config.ListenAddr)
		if err != nil {
			return fmt.Errorf("resolve address: %w", err)
		}
		conn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return fmt.Errorf("listen UDP: %w", err)
		}
		t.conn = conn
	} else {
		// Create IPv4 socket
		udpAddr4 := &net.UDPAddr{IP: net.IPv4zero, Port: port}
		conn4, err := net.ListenUDP("udp4", udpAddr4)
		if err != nil {
			log.Warn().Err(err).Msg("failed to create IPv4 UDP socket")
		} else {
			t.conn = conn4
			log.Debug().Str("addr", conn4.LocalAddr().String()).Msg("IPv4 UDP socket created")
		}

		// Create IPv6 socket
		udpAddr6 := &net.UDPAddr{IP: net.IPv6zero, Port: port}
		conn6, err := net.ListenUDP("udp6", udpAddr6)
		if err != nil {
			log.Warn().Err(err).Msg("failed to create IPv6 UDP socket")
		} else {
			t.conn6 = conn6
			log.Debug().Str("addr", conn6.LocalAddr().String()).Msg("IPv6 UDP socket created")
		}

		// At least one socket must succeed
		if t.conn == nil && t.conn6 == nil {
			return fmt.Errorf("failed to create any UDP socket")
		}
	}

	t.running.Store(true)

	// Start packet receivers for each socket
	if t.conn != nil {
		go t.receiveLoop(t.conn)
	}
	if t.conn6 != nil {
		go t.receiveLoop(t.conn6)
	}

	// Start keepalive sender
	go t.keepaliveLoop()

	addrs := []string{}
	if t.conn != nil {
		addrs = append(addrs, t.conn.LocalAddr().String())
	}
	if t.conn6 != nil {
		addrs = append(addrs, t.conn6.LocalAddr().String())
	}
	log.Info().
		Strs("addrs", addrs).
		Msg("UDP transport started")

	return nil
}

// receiveLoop processes incoming UDP packets from the given connection.
func (t *Transport) receiveLoop(conn *net.UDPConn) {
	buf := make([]byte, 2000) // Larger than MTU

	for t.running.Load() {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
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

		go t.handlePacket(packet, remoteAddr, conn)
	}
}

// handlePacket processes a single incoming packet.
// The conn parameter is the socket the packet was received on.
func (t *Transport) handlePacket(data []byte, remoteAddr *net.UDPAddr, conn *net.UDPConn) {
	if len(data) < 1 {
		return
	}

	// Check packet type from first byte
	packetType := data[0]

	switch packetType {
	case PacketTypeHandshakeInit:
		// Handshake packets have 1-byte type prefix + handshake message
		t.handleHandshakeInit(data[1:], remoteAddr, conn)
	case PacketTypeHandshakeResponse:
		t.handleHandshakeResponse(data[1:], remoteAddr)
	case PacketTypeData, PacketTypeKeepalive:
		// Data/keepalive packets have 16-byte header
		header, err := UnmarshalHeader(data)
		if err != nil {
			return
		}
		if packetType == PacketTypeData {
			t.handleDataPacket(header, data[HeaderSize:], remoteAddr)
		} else {
			t.handleKeepalive(header, remoteAddr)
		}
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
// The conn parameter is the socket the init was received on, used for responding.
func (t *Transport) handleHandshakeInit(data []byte, remoteAddr *net.UDPAddr, conn *net.UDPConn) {
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

	// Respond on the same socket that received the init
	if _, err := conn.WriteToUDP(packet, remoteAddr); err != nil {
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

	// Look up peer name from public key
	peerPubKey := hs.PeerStaticPublic()
	peerName := ""
	if t.config.PeerResolver != nil {
		peerName = t.config.PeerResolver(peerPubKey)
	}
	if peerName == "" {
		// If we can't identify the peer, log and return
		log.Debug().
			Hex("peer_pubkey", peerPubKey[:8]).
			Str("remote", remoteAddr.String()).
			Msg("unknown peer public key, rejecting connection")
		return
	}

	// Create session using the same socket that received the handshake
	session := NewSession(SessionConfig{
		LocalIndex: hs.LocalIndex(),
		PeerName:   peerName,
		PeerPublic: peerPubKey,
		RemoteAddr: remoteAddr,
		Conn:       conn,
	})
	session.SetCrypto(crypto, hs.RemoteIndex())

	t.mu.Lock()
	t.sessions[session.LocalIndex()] = session
	t.peerSessions[peerName] = session
	t.mu.Unlock()

	log.Debug().
		Str("remote", remoteAddr.String()).
		Str("peer", peerName).
		Uint32("local_index", session.LocalIndex()).
		Msg("incoming handshake completed")

	// Push connection to listener if active
	if t.listener != nil && !t.listener.closed.Load() {
		conn := &Connection{
			session: session,
			readBuf: new(bytes.Buffer),
		}
		select {
		case t.listener.acceptCh <- conn:
			log.Debug().Str("peer", peerName).Msg("incoming connection queued for accept")
		default:
			log.Warn().Str("peer", peerName).Msg("listener accept channel full, dropping connection")
		}
	}
}

// handleHandshakeResponse processes an incoming handshake response.
func (t *Transport) handleHandshakeResponse(data []byte, remoteAddr *net.UDPAddr) {
	if len(data) < 72 {
		return
	}

	// Extract receiver index (bytes 4-8) - this is the initiator's local index
	receiverIndex := binary.LittleEndian.Uint32(data[4:8])

	t.mu.RLock()
	respChan, ok := t.pendingHandshakes[receiverIndex]
	t.mu.RUnlock()

	if ok {
		// Non-blocking send - if channel is full or closed, we drop the response
		select {
		case respChan <- handshakeResponse{data: data, remoteAddr: remoteAddr}:
			log.Debug().
				Uint32("receiver_index", receiverIndex).
				Str("remote", remoteAddr.String()).
				Msg("handshake response routed to initiator")
		default:
			log.Debug().
				Uint32("receiver_index", receiverIndex).
				Msg("handshake response channel full or closed")
		}
	} else {
		log.Debug().
			Uint32("receiver_index", receiverIndex).
			Msg("no pending handshake for response")
	}
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

	// Select the appropriate socket based on peer's address family
	conn := t.selectSocketForPeer(peerAddr)
	if conn == nil {
		peerIsIPv4 := peerAddr.IP.To4() != nil
		return nil, fmt.Errorf("no %s socket available for peer %s",
			map[bool]string{true: "IPv4", false: "IPv6"}[peerIsIPv4],
			opts.PeerName)
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
	if err := t.holePunch(ctx, opts.PeerName, peerAddr, conn); err != nil {
		log.Debug().Err(err).Str("peer", opts.PeerName).Msg("hole-punch failed, trying direct")
	}

	// Perform handshake
	session, err := t.initiateHandshake(ctx, opts.PeerName, peerPublic, peerAddr, conn)
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
func (t *Transport) initiateHandshake(ctx context.Context, peerName string, peerPublic [32]byte, peerAddr *net.UDPAddr, conn *net.UDPConn) (*Session, error) {
	hs, err := NewInitiatorHandshake(t.staticPrivate, t.staticPublic, peerPublic)
	if err != nil {
		return nil, err
	}

	// Create initiation message
	initMsg, err := hs.CreateInitiation()
	if err != nil {
		return nil, err
	}

	// Register pending handshake before sending
	respChan := make(chan handshakeResponse, 1)
	localIndex := hs.LocalIndex()

	t.mu.Lock()
	t.pendingHandshakes[localIndex] = respChan
	t.mu.Unlock()

	// Clean up pending handshake when done
	defer func() {
		t.mu.Lock()
		delete(t.pendingHandshakes, localIndex)
		t.mu.Unlock()
	}()

	// Prepend packet type header
	packet := make([]byte, 1+len(initMsg))
	packet[0] = PacketTypeHandshakeInit
	copy(packet[1:], initMsg)

	// Send initiation
	if _, err := conn.WriteToUDP(packet, peerAddr); err != nil {
		return nil, err
	}

	// Wait for response with timeout
	timeout := t.config.HandshakeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	// Wait for response via channel (routed by receiveLoop)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("handshake timeout")
	case resp := <-respChan:
		// Verify it's from the expected peer
		if resp.remoteAddr.String() != peerAddr.String() {
			return nil, fmt.Errorf("response from unexpected peer: %s", resp.remoteAddr)
		}

		// Process response
		if err := hs.ConsumeResponse(resp.data); err != nil {
			return nil, fmt.Errorf("invalid response: %w", err)
		}
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
func (t *Transport) holePunch(ctx context.Context, peerName string, peerAddr *net.UDPAddr, conn *net.UDPConn) error {
	if t.config.CoordServerURL == "" {
		return nil // No coordination server, skip hole-punch
	}

	// Get our stored external address for the appropriate address family
	t.mu.RLock()
	externalAddr := t.externalAddr
	if peerAddr.IP.To4() == nil && t.externalAddr6 != "" {
		externalAddr = t.externalAddr6
	}
	t.mu.RUnlock()

	// Request hole-punch coordination from server
	reqBody := map[string]string{
		"from_peer":     t.config.LocalPeerName,
		"to_peer":       peerName,
		"local_addr":    conn.LocalAddr().String(),
		"external_addr": externalAddr,
	}

	log.Debug().
		Str("from", t.config.LocalPeerName).
		Str("to", peerName).
		Str("our_external", externalAddr).
		Str("peer_addr", peerAddr.String()).
		Msg("initiating hole-punch coordination")

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

	// Parse hole-punch response
	var hpResp struct {
		OK            bool   `json:"ok"`
		Ready         bool   `json:"ready"`
		PeerAddr      string `json:"peer_addr"`
		PeerLocalAddr string `json:"peer_local_addr"`
		Message       string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hpResp); err != nil {
		return fmt.Errorf("decode hole-punch response: %w", err)
	}

	if !hpResp.Ready {
		log.Debug().
			Str("peer", peerName).
			Str("message", hpResp.Message).
			Msg("peer not ready for hole-punch")
		return fmt.Errorf("peer not ready: %s", hpResp.Message)
	}

	log.Debug().
		Str("peer", peerName).
		Str("peer_external", hpResp.PeerAddr).
		Msg("hole-punch coordination successful, sending packets")

	// Send hole-punch packets
	for i := 0; i < t.config.HolePunchRetries; i++ {
		// Send a small packet to punch the hole
		_, _ = conn.WriteToUDP([]byte{0}, peerAddr)
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
	if t.conn6 != nil {
		t.conn6.Close()
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

// IsHealthy returns true if the UDP session is established and ready for data.
func (c *Connection) IsHealthy() bool {
	return c.session.State() == SessionStateEstablished
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

	// Store our external address for hole-punching
	t.mu.Lock()
	t.externalAddr = result.ExternalAddr
	t.mu.Unlock()

	log.Debug().
		Str("external_addr", result.ExternalAddr).
		Msg("UDP endpoint registered")

	return nil
}
