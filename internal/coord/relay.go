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

// relayConn represents a peer's relay connection.
type relayConn struct {
	peerName   string
	targetPeer string
	conn       *websocket.Conn
	paired     chan struct{} // closed when paired
}

// persistentConn represents a peer's persistent relay connection (DERP-like).
type persistentConn struct {
	peerName string
	conn     *websocket.Conn
	writeMu  sync.Mutex // protects writes to conn
}

// relayManager handles relay connections between peers.
type relayManager struct {
	pending    map[string]*relayConn    // key: "peerA->peerB" (legacy pairing)
	persistent map[string]*persistentConn // key: peerName (DERP-like routing)
	mu         sync.Mutex
}

// Persistent relay message types
const (
	MsgTypeSendPacket byte = 0x01 // Client -> Server: send packet to target peer
	MsgTypeRecvPacket byte = 0x02 // Server -> Client: received packet from source peer
	MsgTypePing       byte = 0x03 // Keepalive ping
	MsgTypePong       byte = 0x04 // Keepalive pong
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
		existing.conn.Close()
	}

	pc := &persistentConn{
		peerName: peerName,
		conn:     conn,
	}
	r.persistent[peerName] = pc
	return pc
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

// SendToPeer sends a packet to a peer via their persistent connection.
// Returns true if the peer is connected and the packet was sent.
func (pc *persistentConn) SendPacket(sourcePeer string, data []byte) error {
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	// Build message: [MsgTypeRecvPacket][source name len][source name][data]
	msg := make([]byte, 2+len(sourcePeer)+len(data))
	msg[0] = MsgTypeRecvPacket
	msg[1] = byte(len(sourcePeer))
	copy(msg[2:], sourcePeer)
	copy(msg[2+len(sourcePeer):], data)

	return pc.conn.WriteMessage(websocket.BinaryMessage, msg)
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

	// Register the connection
	pc := s.relay.RegisterPersistent(peerName, conn)
	defer func() {
		s.relay.UnregisterPersistent(peerName)
		conn.Close()
		log.Info().Str("peer", peerName).Msg("persistent relay connection closed")
	}()

	// Set up ping/pong handlers for keepalive
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Start ping ticker
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Handle messages in a separate goroutine and use channel to coordinate
	msgChan := make(chan []byte, 16)
	errChan := make(chan error, 1)

	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
			_, data, err := conn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			msgChan <- data
		}
	}()

	for {
		select {
		case <-pingTicker.C:
			pc.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			pc.writeMu.Unlock()
			if err != nil {
				log.Debug().Err(err).Str("peer", peerName).Msg("persistent relay ping failed")
				return
			}

		case data := <-msgChan:
			s.handlePersistentRelayMessage(peerName, data)

		case err := <-errChan:
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Debug().Err(err).Str("peer", peerName).Msg("persistent relay read error")
			}
			return
		}
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
