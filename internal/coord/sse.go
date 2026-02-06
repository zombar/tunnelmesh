package coord

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/rs/zerolog/log"
)

// sseClient represents a connected SSE client.
type sseClient struct {
	events chan string
	done   chan struct{}
}

// sseHub manages SSE client connections and broadcasts.
type sseHub struct {
	clients map[*sseClient]bool
	mu      sync.RWMutex
}

// newSSEHub creates a new SSE hub.
func newSSEHub() *sseHub {
	return &sseHub{
		clients: make(map[*sseClient]bool),
	}
}

// register adds a new client to the hub.
func (h *sseHub) register(client *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = true
	log.Debug().Int("clients", len(h.clients)).Msg("SSE client connected")
}

// unregister removes a client from the hub.
func (h *sseHub) unregister(client *sseClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		close(client.events)
		log.Debug().Int("clients", len(h.clients)).Msg("SSE client disconnected")
	}
}

// broadcast sends an event to all connected clients.
func (h *sseHub) broadcast(event string) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for client := range h.clients {
		select {
		case client.events <- event:
		default:
			// Client buffer full, skip
			log.Debug().Msg("SSE client buffer full, skipping event")
		}
	}
}

// clientCount returns the number of connected clients.
func (h *sseHub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// handleSSE handles SSE connections for the admin dashboard.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Check if we can flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Create client
	client := &sseClient{
		events: make(chan string, 10), // Buffer up to 10 events
		done:   make(chan struct{}),
	}

	// Register client
	s.sseHub.register(client)
	defer s.sseHub.unregister(client)

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Stream events until client disconnects
	for {
		select {
		case event, ok := <-client.events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "%s\n\n", event)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// notifyHeartbeat broadcasts a heartbeat event to all SSE clients.
func (s *Server) notifyHeartbeat(peerName string) {
	if s.sseHub == nil {
		return
	}

	event := fmt.Sprintf("event: heartbeat\ndata: {\"peer\":\"%s\"}", peerName)
	s.sseHub.broadcast(event)
}
