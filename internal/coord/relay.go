package coord

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	pending        map[string]*relayConn      // key: "peerA->peerB" (legacy pairing)
	persistent     map[string]*persistentConn // key: peerName (DERP-like routing)
	wgConcentrator string                     // peerName of WireGuard concentrator (if any)
	mu             sync.Mutex

	// API request tracking
	apiRequests   map[uint32]chan []byte // reqID -> response channel
	apiRequestsMu sync.Mutex
	nextReqID     uint32
}

// Persistent relay message types
const (
	MsgTypeSendPacket      byte = 0x01 // Client -> Server: send packet to target peer
	MsgTypeRecvPacket      byte = 0x02 // Server -> Client: received packet from source peer
	MsgTypePing            byte = 0x03 // Keepalive ping
	MsgTypePong            byte = 0x04 // Keepalive pong
	MsgTypePeerReconnected byte = 0x05 // Server -> Client: peer reconnected (tunnel may be stale)
	MsgTypeWGAnnounce      byte = 0x10 // Client -> Server: announce as WireGuard concentrator
	MsgTypeAPIRequest      byte = 0x11 // Server -> Client: API request to concentrator
	MsgTypeAPIResponse     byte = 0x12 // Client -> Server: API response from concentrator

	// Heartbeat and push notification message types
	MsgTypeHeartbeat       byte = 0x20 // Client -> Server: stats update
	MsgTypeHeartbeatAck    byte = 0x21 // Server -> Client: heartbeat acknowledged
	MsgTypeRelayNotify     byte = 0x22 // Server -> Client: relay request notification
	MsgTypeHolePunchNotify byte = 0x23 // Server -> Client: hole-punch request notification
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
		pending:     make(map[string]*relayConn),
		persistent:  make(map[string]*persistentConn),
		apiRequests: make(map[uint32]chan []byte),
	}
}

// SetWGConcentrator sets the peer that acts as WireGuard concentrator.
func (r *relayManager) SetWGConcentrator(peerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.wgConcentrator
	r.wgConcentrator = peerName
	if old != peerName {
		log.Info().Str("peer", peerName).Str("old", old).Msg("WireGuard concentrator updated")
	}
}

// GetWGConcentrator returns the current WireGuard concentrator peer name.
func (r *relayManager) GetWGConcentrator() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wgConcentrator
}

// ClearWGConcentrator clears the concentrator if it matches the given peer.
func (r *relayManager) ClearWGConcentrator(peerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wgConcentrator == peerName {
		r.wgConcentrator = ""
		log.Info().Str("peer", peerName).Msg("WireGuard concentrator disconnected")
	}
}

// SendAPIRequest sends an API request to the WireGuard concentrator and waits for response.
// Returns the response body or error if timeout/no concentrator.
func (r *relayManager) SendAPIRequest(method string, body []byte, timeout time.Duration) ([]byte, error) {
	r.mu.Lock()
	concentrator := r.wgConcentrator
	pc, ok := r.persistent[concentrator]
	r.mu.Unlock()

	if concentrator == "" || !ok {
		return nil, fmt.Errorf("no WireGuard concentrator connected")
	}

	// Allocate request ID and response channel
	r.apiRequestsMu.Lock()
	reqID := r.nextReqID
	r.nextReqID++
	respChan := make(chan []byte, 1)
	r.apiRequests[reqID] = respChan
	r.apiRequestsMu.Unlock()

	defer func() {
		r.apiRequestsMu.Lock()
		delete(r.apiRequests, reqID)
		r.apiRequestsMu.Unlock()
	}()

	// Build request: [MsgTypeAPIRequest][reqID:4][method_len:1][method][body]
	msg := make([]byte, 1+4+1+len(method)+len(body))
	msg[0] = MsgTypeAPIRequest
	msg[1] = byte(reqID >> 24)
	msg[2] = byte(reqID >> 16)
	msg[3] = byte(reqID >> 8)
	msg[4] = byte(reqID)
	msg[5] = byte(len(method))
	copy(msg[6:], method)
	copy(msg[6+len(method):], body)

	// Send request via async write channel
	select {
	case pc.writeChan <- msg:
		// Message queued
	default:
		return nil, fmt.Errorf("send API request: write channel full")
	}

	// Wait for response
	select {
	case resp := <-respChan:
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("API request timeout")
	}
}

// handleAPIResponse processes an API response from the concentrator.
func (r *relayManager) handleAPIResponse(data []byte) {
	if len(data) < 5 {
		log.Debug().Msg("API response too short")
		return
	}

	reqID := uint32(data[1])<<24 | uint32(data[2])<<16 | uint32(data[3])<<8 | uint32(data[4])
	body := data[5:]

	r.apiRequestsMu.Lock()
	respChan, ok := r.apiRequests[reqID]
	r.apiRequestsMu.Unlock()

	if ok {
		select {
		case respChan <- body:
		default:
			log.Debug().Uint32("req_id", reqID).Msg("API response channel full")
		}
	} else {
		log.Debug().Uint32("req_id", reqID).Msg("API response for unknown request")
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
				// Only return buffer to pool if it's a pool-sized buffer
				// (small buffers like heartbeat ack are allocated inline)
				if cap(data) >= 65792 {
					relayPacketPool.Put(&data)
				}
				return
			}
			// Only return buffer to pool if it's a pool-sized buffer
			if cap(data) >= 65792 {
				relayPacketPool.Put(&data)
			}
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

// NotifyRelayRequest pushes a relay notification to a peer via their persistent connection.
// This tells the peer that other peers are waiting for relay connections.
func (r *relayManager) NotifyRelayRequest(peerName string, waitingPeers []string) {
	if len(waitingPeers) == 0 {
		return
	}

	r.mu.Lock()
	pc, ok := r.persistent[peerName]
	r.mu.Unlock()

	if !ok {
		log.Debug().Str("peer", peerName).Msg("cannot notify relay request: peer not connected")
		return
	}

	// Build message: [MsgTypeRelayNotify][count:1][name_len:1][name]...
	msgLen := 2 // type + count
	for _, p := range waitingPeers {
		msgLen += 1 + len(p) // name_len + name
	}

	msg := make([]byte, msgLen)
	msg[0] = MsgTypeRelayNotify
	msg[1] = byte(len(waitingPeers))
	offset := 2
	for _, p := range waitingPeers {
		msg[offset] = byte(len(p))
		copy(msg[offset+1:], p)
		offset += 1 + len(p)
	}

	// Non-blocking send to write channel
	select {
	case pc.writeChan <- msg:
		log.Debug().
			Str("target", peerName).
			Strs("waiting", waitingPeers).
			Msg("sent relay notification")
	default:
		log.Debug().
			Str("target", peerName).
			Msg("failed to send relay notification: channel full")
	}
}

// NotifyHolePunch pushes a hole-punch notification to a peer via their persistent connection.
// This tells the peer that other peers want to hole-punch with them.
func (r *relayManager) NotifyHolePunch(peerName string, requestingPeers []string) {
	if len(requestingPeers) == 0 {
		return
	}

	r.mu.Lock()
	pc, ok := r.persistent[peerName]
	r.mu.Unlock()

	if !ok {
		log.Debug().Str("peer", peerName).Msg("cannot notify hole-punch: peer not connected")
		return
	}

	// Build message: [MsgTypeHolePunchNotify][count:1][name_len:1][name]...
	msgLen := 2 // type + count
	for _, p := range requestingPeers {
		msgLen += 1 + len(p) // name_len + name
	}

	msg := make([]byte, msgLen)
	msg[0] = MsgTypeHolePunchNotify
	msg[1] = byte(len(requestingPeers))
	offset := 2
	for _, p := range requestingPeers {
		msg[offset] = byte(len(p))
		copy(msg[offset+1:], p)
		offset += 1 + len(p)
	}

	// Non-blocking send to write channel
	select {
	case pc.writeChan <- msg:
		log.Debug().
			Str("target", peerName).
			Strs("requesting", requestingPeers).
			Msg("sent hole-punch notification")
	default:
		log.Debug().
			Str("target", peerName).
			Msg("failed to send hole-punch notification: channel full")
	}
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
		s.relay.ClearWGConcentrator(peerName) // Clear if this was the concentrator
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
	if len(data) < 1 {
		log.Debug().Str("peer", sourcePeer).Msg("persistent relay message too short")
		return
	}

	msgType := data[0]

	switch msgType {
	case MsgTypeHeartbeat:
		// Format: [MsgTypeHeartbeat][stats_len:2][stats JSON]
		if len(data) < 3 {
			log.Debug().Str("peer", sourcePeer).Msg("heartbeat message too short")
			return
		}
		statsLen := int(data[1])<<8 | int(data[2])
		if len(data) < 3+statsLen {
			log.Debug().Str("peer", sourcePeer).Int("expected", statsLen).Int("got", len(data)-3).Msg("heartbeat stats truncated")
			return
		}
		statsJSON := data[3 : 3+statsLen]

		// Parse stats
		var stats proto.PeerStats
		if err := json.Unmarshal(statsJSON, &stats); err != nil {
			log.Debug().Err(err).Str("peer", sourcePeer).Msg("failed to parse heartbeat stats")
		}

		// Update peer info
		s.peersMu.Lock()
		if peer, exists := s.peers[sourcePeer]; exists {
			peer.peer.LastSeen = time.Now()
			peer.stats = &stats
			peer.prevStats = peer.stats
			peer.lastStatsTime = time.Now()
			peer.heartbeatCount++
		}
		atomic.AddUint64(&s.serverStats.totalHeartbeats, 1)
		s.peersMu.Unlock()

		// Send ack via the persistent connection
		if pc, ok := s.relay.GetPersistent(sourcePeer); ok {
			select {
			case pc.writeChan <- []byte{MsgTypeHeartbeatAck}:
				log.Debug().Str("peer", sourcePeer).Msg("sent heartbeat ack")
			default:
				log.Debug().Str("peer", sourcePeer).Msg("failed to send heartbeat ack: channel full")
			}
		}

	case MsgTypeSendPacket:
		// Format: [MsgTypeSendPacket][target name len][target name][packet data]
		if len(data) < 3 {
			log.Debug().Str("peer", sourcePeer).Msg("persistent relay send packet too short")
			return
		}
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

	case MsgTypeWGAnnounce:
		// Peer announces itself as WireGuard concentrator
		log.Info().Str("peer", sourcePeer).Msg("peer announced as WireGuard concentrator")
		s.relay.SetWGConcentrator(sourcePeer)

	case MsgTypeAPIResponse:
		// API response from concentrator
		s.relay.handleAPIResponse(data)

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

	// Push notification to target peer that someone is waiting for relay
	s.relay.NotifyRelayRequest(targetPeer, []string{fromPeer})

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
