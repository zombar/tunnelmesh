package coord

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// relayPacketPool pools relay packet buffers to reduce GC pressure.
// Max packet size is ~65KB, plus header overhead.
var relayPacketPool = sync.Pool{
	New: func() interface{} {
		// Allocate buffer for max relay message: type(1) + name_len(1) + name(255) + data(65535)
		buf := make([]byte, 65792)
		return &buf
	},
}

// relayConn represents a peer's relay connection.
type relayConn struct {
	peerName   string
	targetPeer string
	conn       *websocket.Conn
	paired     chan struct{} // closed when paired
}

// persistentConn represents a peer's persistent relay connection (DERP-like).
type persistentConn struct {
	peerName  string
	conn      *websocket.Conn
	writeChan chan []byte   // buffered channel for async writes
	closeChan chan struct{} // signals writer goroutine to stop
	closed    bool
	closeMu   sync.Mutex
}

// relayManager handles relay connections between peers.
type relayManager struct {
	pending    map[string]*relayConn    // key: "peerA->peerB" (legacy pairing)
	persistent map[string]*persistentConn // key: peerName (DERP-like routing)
	mu         sync.Mutex
}

// Persistent relay message types
const (
	MsgTypeSendPacket      byte = 0x01 // Client -> Server: send packet to target peer
	MsgTypeRecvPacket      byte = 0x02 // Server -> Client: received packet from source peer
	MsgTypePing            byte = 0x03 // Keepalive ping
	MsgTypePong            byte = 0x04 // Keepalive pong
	MsgTypePeerReconnected byte = 0x05 // Server -> Client: peer reconnected (tunnel may be stale)
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16384,
	WriteBufferSize: 16384,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for relay connections
	},
}

// newRelayManager creates a new relay manager.
func newRelayManager() *relayManager {
	return &relayManager{
		pending:    make(map[string]*relayConn),
		persistent: make(map[string]*persistentConn),
	}
}

// RegisterPersistent registers a peer's persistent relay connection.
func (r *relayManager) RegisterPersistent(peerName string, conn *websocket.Conn) *persistentConn {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Close existing connection if any
	if existing, ok := r.persistent[peerName]; ok {
		existing.Close()
	}

	pc := &persistentConn{
		peerName:  peerName,
		conn:      conn,
		writeChan: make(chan []byte, 256), // Buffered channel to prevent blocking
		closeChan: make(chan struct{}),
	}
	r.persistent[peerName] = pc

	// Start the writer goroutine
	go pc.writeLoop()

	return pc
}

// writeLoop processes queued writes to avoid blocking senders.
func (pc *persistentConn) writeLoop() {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-pc.closeChan:
			return
		case <-pingTicker.C:
			if err := pc.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				log.Debug().Err(err).Str("peer", pc.peerName).Msg("persistent relay ping failed")
				return
			}
		case data := <-pc.writeChan:
			if err := pc.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Debug().Err(err).Str("peer", pc.peerName).Msg("persistent relay write failed")
				// Return buffer to pool
				relayPacketPool.Put(&data)
				return
			}
			// Return buffer to pool after successful write
			relayPacketPool.Put(&data)
		}
	}
}

// Close closes the persistent connection and stops the writer goroutine.
func (pc *persistentConn) Close() {
	pc.closeMu.Lock()
	defer pc.closeMu.Unlock()
	if pc.closed {
		return
	}
	pc.closed = true
	close(pc.closeChan)
	pc.conn.Close()
}

// UnregisterPersistent removes a peer's persistent relay connection.
func (r *relayManager) UnregisterPersistent(peerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.persistent, peerName)
}

// GetPersistent returns a peer's persistent connection if connected.
func (r *relayManager) GetPersistent(peerName string) (*persistentConn, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pc, ok := r.persistent[peerName]
	return pc, ok
}

// BroadcastPeerReconnected notifies all other connected peers that a peer has reconnected.
// Clients may use this to re-evaluate their connection state to that peer.
func (r *relayManager) BroadcastPeerReconnected(reconnectedPeer string) {
	r.mu.Lock()
	peers := make([]*persistentConn, 0, len(r.persistent))
	for name, pc := range r.persistent {
		if name != reconnectedPeer {
			peers = append(peers, pc)
		}
	}
	r.mu.Unlock()

	for _, pc := range peers {
		// Get buffer from pool for each peer (they'll be returned after write)
		bufPtr := relayPacketPool.Get().(*[]byte)
		buf := *bufPtr

		// Build message: [MsgTypePeerReconnected][name len][name]
		msgLen := 2 + len(reconnectedPeer)
		msg := buf[:msgLen]
		msg[0] = MsgTypePeerReconnected
		msg[1] = byte(len(reconnectedPeer))
		copy(msg[2:], reconnectedPeer)

		// Non-blocking send to write channel
		select {
		case pc.writeChan <- msg:
			log.Debug().
				Str("target", pc.peerName).
				Str("reconnected", reconnectedPeer).
				Msg("queued peer reconnected notification")
		default:
			relayPacketPool.Put(bufPtr)
			log.Debug().
				Str("target", pc.peerName).
				Str("reconnected", reconnectedPeer).
				Msg("failed to queue peer reconnected notification (channel full)")
		}
	}
}

// SendToPeer sends a packet to a peer via their persistent connection.
// Returns true if the peer is connected and the packet was sent.
func (pc *persistentConn) SendPacket(sourcePeer string, data []byte) error {
	pc.closeMu.Lock()
	if pc.closed {
		pc.closeMu.Unlock()
		return io.EOF
	}
	pc.closeMu.Unlock()

	// Get buffer from pool
	bufPtr := relayPacketPool.Get().(*[]byte)
	buf := *bufPtr

	// Build message: [MsgTypeRecvPacket][source name len][source name][data]
	msgLen := 2 + len(sourcePeer) + len(data)
	if msgLen > len(buf) {
		// Message too large for pooled buffer, allocate new one
		relayPacketPool.Put(bufPtr)
		buf = make([]byte, msgLen)
		bufPtr = &buf
	}

	msg := buf[:msgLen]
	msg[0] = MsgTypeRecvPacket
	msg[1] = byte(len(sourcePeer))
	copy(msg[2:], sourcePeer)
	copy(msg[2+len(sourcePeer):], data)

	// Send to write channel (non-blocking with timeout)
	select {
	case pc.writeChan <- msg:
		return nil
	default:
		// Channel full - drop packet to avoid blocking
		relayPacketPool.Put(bufPtr)
		log.Warn().Str("peer", pc.peerName).Msg("relay write channel full, dropping packet")
		return nil
	}
}

// GetPendingRequestsFor returns the list of peers waiting on relay for the given peer.
// For example, if "oldie" is waiting on relay for "roku", calling GetPendingRequestsFor("roku")
// returns ["oldie"].
func (r *relayManager) GetPendingRequestsFor(peerName string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var requests []string
	suffix := "->" + peerName
	for key, conn := range r.pending {
		if len(key) > len(suffix) && key[len(key)-len(suffix):] == suffix {
			requests = append(requests, conn.peerName)
		}
	}
	return requests
}

// setupRelayRoutes registers the relay WebSocket endpoint.
func (s *Server) setupRelayRoutes() {
	if s.relay == nil {
		s.relay = newRelayManager()
	}
	s.mux.HandleFunc("/api/v1/relay/persistent", s.handlePersistentRelay)
	s.mux.HandleFunc("/api/v1/relay/", s.handleRelay)
	s.mux.HandleFunc("/api/v1/relay-status", s.handleRelayStatus)
}

// handlePersistentRelay handles persistent (DERP-like) relay connections.
// Peers connect once and stay connected. Packets are routed by peer name.
func (s *Server) handlePersistentRelay(w http.ResponseWriter, r *http.Request) {
	// Authenticate via JWT
	auth := r.Header.Get("Authorization")
	if auth == "" {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		http.Error(w, "invalid authorization header", http.StatusUnauthorized)
		return
	}

	claims, err := s.ValidateToken(parts[1])
	if err != nil {
		log.Debug().Err(err).Msg("persistent relay auth failed")
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	peerName := claims.PeerName

	// Verify peer is registered
	s.peersMu.RLock()
	_, exists := s.peers[peerName]
	s.peersMu.RUnlock()

	if !exists {
		http.Error(w, "peer not registered", http.StatusNotFound)
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Str("peer", peerName).Msg("persistent relay websocket upgrade failed")
		return
	}

	log.Info().Str("peer", peerName).Msg("persistent relay connection established")

	// Register the connection (starts writer goroutine with ping ticker)
	pc := s.relay.RegisterPersistent(peerName, conn)
	defer func() {
		s.relay.UnregisterPersistent(peerName)
		pc.Close()
		log.Info().Str("peer", peerName).Msg("persistent relay connection closed")
	}()

	// Notify other peers that this peer has (re)connected
	// This allows them to invalidate stale direct tunnels
	go s.relay.BroadcastPeerReconnected(peerName)

	// Set up ping/pong handlers for keepalive
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Read loop - handle messages directly (writes go through buffered channel)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Debug().Err(err).Str("peer", peerName).Msg("persistent relay read error")
			}
			return
		}
		s.handlePersistentRelayMessage(peerName, data)
	}
}

// handlePersistentRelayMessage processes a message from a persistent relay connection.
func (s *Server) handlePersistentRelayMessage(sourcePeer string, data []byte) {
	if len(data) < 3 {
		log.Debug().Str("peer", sourcePeer).Msg("persistent relay message too short")
		return
	}

	msgType := data[0]

	switch msgType {
	case MsgTypeSendPacket:
		// Format: [MsgTypeSendPacket][target name len][target name][packet data]
		targetLen := int(data[1])
		if len(data) < 2+targetLen {
			log.Debug().Str("peer", sourcePeer).Msg("persistent relay send packet too short")
			return
		}
		targetPeer := string(data[2 : 2+targetLen])
		packetData := data[2+targetLen:]

		// Route to target peer
		targetConn, ok := s.relay.GetPersistent(targetPeer)
		if !ok {
			log.Debug().
				Str("source", sourcePeer).
				Str("target", targetPeer).
				Msg("persistent relay target not connected")
			return
		}

		if err := targetConn.SendPacket(sourcePeer, packetData); err != nil {
			log.Debug().Err(err).
				Str("source", sourcePeer).
				Str("target", targetPeer).
				Msg("persistent relay forward failed")
		} else {
			log.Debug().
				Str("source", sourcePeer).
				Str("target", targetPeer).
				Int("len", len(packetData)).
				Msg("persistent relay forwarded packet")
		}

	case MsgTypePing:
		// Respond with pong (handled at protocol level via SetPongHandler)
		log.Debug().Str("peer", sourcePeer).Msg("persistent relay received ping")

	default:
		log.Debug().Str("peer", sourcePeer).Uint8("type", msgType).Msg("unknown persistent relay message type")
	}
}

// handleRelayStatus returns pending relay requests for the authenticated peer.
// This allows peers to poll for relay requests without waiting for the heartbeat interval.
func (s *Server) handleRelayStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authenticate via JWT
	auth := r.Header.Get("Authorization")
	if auth == "" {
		s.jsonError(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		s.jsonError(w, "invalid authorization header", http.StatusUnauthorized)
		return
	}

	claims, err := s.ValidateToken(parts[1])
	if err != nil {
		log.Debug().Err(err).Msg("relay-status auth failed")
		s.jsonError(w, "invalid token", http.StatusUnauthorized)
		return
	}

	peerName := claims.PeerName

	// Get pending relay requests for this peer
	requests := s.relay.GetPendingRequestsFor(peerName)

	resp := proto.RelayStatusResponse{RelayRequests: requests}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleRelay handles WebSocket relay connections.
// URL format: /api/v1/relay/{target_peer}
// The connecting peer is identified via JWT token in Authorization header.
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	// Extract target peer from path
	targetPeer := strings.TrimPrefix(r.URL.Path, "/api/v1/relay/")
	if targetPeer == "" {
		http.Error(w, "target peer required", http.StatusBadRequest)
		return
	}

	// Authenticate via JWT
	auth := r.Header.Get("Authorization")
	if auth == "" {
		http.Error(w, "missing authorization header", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		http.Error(w, "invalid authorization header", http.StatusUnauthorized)
		return
	}

	claims, err := s.ValidateToken(parts[1])
	if err != nil {
		log.Debug().Err(err).Msg("relay auth failed")
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	fromPeer := claims.PeerName

	// Verify both peers are registered
	s.peersMu.RLock()
	_, fromExists := s.peers[fromPeer]
	_, toExists := s.peers[targetPeer]
	s.peersMu.RUnlock()

	if !fromExists || !toExists {
		http.Error(w, "peer not registered", http.StatusNotFound)
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("websocket upgrade failed")
		return
	}

	log.Info().
		Str("from", fromPeer).
		Str("to", targetPeer).
		Msg("relay connection established")

	// Try to pair with existing connection
	s.relay.mu.Lock()

	// Check if there's a matching pending connection (targetPeer wanting to connect to fromPeer)
	reverseKey := targetPeer + "->" + fromPeer
	if pending, ok := s.relay.pending[reverseKey]; ok {
		// Found a match - pair them
		delete(s.relay.pending, reverseKey)
		s.relay.mu.Unlock()

		// Signal the waiting peer that pairing is complete
		close(pending.paired)

		// Bridge the connections (only this goroutine does it, not the waiting one)
		s.bridgeConnections(pending.conn, conn, pending.peerName, fromPeer)
		return
	}

	// No match found - wait for peer to connect
	forwardKey := fromPeer + "->" + targetPeer
	rc := &relayConn{
		peerName:   fromPeer,
		targetPeer: targetPeer,
		conn:       conn,
		paired:     make(chan struct{}),
	}
	s.relay.pending[forwardKey] = rc
	s.relay.mu.Unlock()

	// Wait for peer with timeout
	pairTimeout := 30 * time.Second
	if s.cfg.Relay.PairTimeout != "" {
		if d, err := time.ParseDuration(s.cfg.Relay.PairTimeout); err == nil {
			pairTimeout = d
		}
	}

	select {
	case <-rc.paired:
		// Successfully paired - the other goroutine handles bridging
		// Just return, our connection is being handled
		return

	case <-time.After(pairTimeout):
		// Timeout - clean up
		s.relay.mu.Lock()
		delete(s.relay.pending, forwardKey)
		s.relay.mu.Unlock()

		log.Debug().
			Str("from", fromPeer).
			Str("to", targetPeer).
			Msg("relay pairing timeout")

		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "pairing timeout"),
			time.Now().Add(5*time.Second))
		conn.Close()
	}
}

// bridgeConnections relays data between two WebSocket connections.
func (s *Server) bridgeConnections(conn1, conn2 *websocket.Conn, peer1, peer2 string) {
	log.Info().
		Str("peer1", peer1).
		Str("peer2", peer2).
		Msg("relay bridge started")

	var wg sync.WaitGroup
	wg.Add(2)

	// conn1 -> conn2
	go func() {
		defer wg.Done()
		s.relayMessages(conn1, conn2)
	}()

	// conn2 -> conn1
	go func() {
		defer wg.Done()
		s.relayMessages(conn2, conn1)
	}()

	wg.Wait()

	conn1.Close()
	conn2.Close()

	log.Info().
		Str("peer1", peer1).
		Str("peer2", peer2).
		Msg("relay bridge closed")
}

// relayMessages copies WebSocket messages from src to dst.
func (s *Server) relayMessages(src, dst *websocket.Conn) {
	for {
		messageType, data, err := src.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Debug().Err(err).Msg("relay read error")
			}
			// Signal the other direction to stop by closing the write side
			_ = dst.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(5*time.Second))
			return
		}

		if err := dst.WriteMessage(messageType, data); err != nil {
			if err != io.EOF {
				log.Debug().Err(err).Msg("relay write error")
			}
			return
		}
	}
}
