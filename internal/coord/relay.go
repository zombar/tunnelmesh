package coord

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

// relayConn represents a peer's relay connection.
type relayConn struct {
	peerName   string
	targetPeer string
	conn       *websocket.Conn
	paired     chan struct{} // closed when paired
}

// relayManager handles relay connections between peers.
type relayManager struct {
	pending map[string]*relayConn // key: "peerA->peerB"
	mu      sync.Mutex
}

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
		pending: make(map[string]*relayConn),
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
	s.mux.HandleFunc("/api/v1/relay/", s.handleRelay)
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
