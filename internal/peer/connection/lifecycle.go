package connection

import (
	"context"
	"io"
	"sync"

	"github.com/rs/zerolog/log"
)

// RouteManager is the interface for adding/removing routes.
type RouteManager interface {
	AddRoute(ip string, peerID string)
	RemoveRoute(ip string)
}

// TunnelProvider is the interface for managing tunnels.
type TunnelProvider interface {
	Add(name string, tunnel io.ReadWriteCloser)
	Remove(name string)
	Get(name string) (io.ReadWriteCloser, bool)
}

// LifecycleManager manages PeerConnection instances and coordinates
// route and tunnel lifecycle based on state transitions.
type LifecycleManager struct {
	mu          sync.RWMutex
	connections map[string]*PeerConnection

	router  RouteManager
	tunnels TunnelProvider

	// Global observers applied to all connections
	globalObservers []Observer
}

// LifecycleConfig holds configuration for creating a LifecycleManager.
type LifecycleConfig struct {
	Router  RouteManager
	Tunnels TunnelProvider
}

// NewLifecycleManager creates a new LifecycleManager.
func NewLifecycleManager(cfg LifecycleConfig) *LifecycleManager {
	lm := &LifecycleManager{
		connections: make(map[string]*PeerConnection),
		router:      cfg.Router,
		tunnels:     cfg.Tunnels,
	}

	// Add ourselves as a global observer to handle route/tunnel lifecycle
	lm.globalObservers = append(lm.globalObservers, ObserverFunc(lm.onTransition))

	return lm
}

// AddObserver adds a global observer that receives notifications for all connections.
func (lm *LifecycleManager) AddObserver(o Observer) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.globalObservers = append(lm.globalObservers, o)
}

// GetOrCreate returns an existing connection or creates a new one.
func (lm *LifecycleManager) GetOrCreate(peerName, meshIP string) *PeerConnection {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if pc, ok := lm.connections[peerName]; ok {
		return pc
	}

	// Create new connection with global observers
	observers := make([]Observer, len(lm.globalObservers))
	copy(observers, lm.globalObservers)

	pc := NewPeerConnection(PeerConnectionConfig{
		PeerName:  peerName,
		MeshIP:    meshIP,
		Observers: observers,
	})

	lm.connections[peerName] = pc

	log.Debug().
		Str("peer", peerName).
		Str("mesh_ip", meshIP).
		Msg("created new peer connection")

	return pc
}

// Get returns an existing connection or nil if not found.
func (lm *LifecycleManager) Get(peerName string) *PeerConnection {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.connections[peerName]
}

// Remove removes a connection from management.
// The connection is closed before removal.
func (lm *LifecycleManager) Remove(peerName string) {
	lm.mu.RLock()
	pc := lm.connections[peerName]
	lm.mu.RUnlock()

	// Close first (this triggers observers while connection is still in map)
	if pc != nil {
		pc.Close()
	}

	// Then remove from map
	lm.mu.Lock()
	delete(lm.connections, peerName)
	lm.mu.Unlock()
}

// Close closes a connection but keeps it in the manager.
// Use this when you want to track the closed state.
func (lm *LifecycleManager) Close(peerName string) {
	lm.mu.RLock()
	pc := lm.connections[peerName]
	lm.mu.RUnlock()

	if pc != nil {
		pc.Close()
	}
}

// CloseAll closes all connections and clears the manager.
func (lm *LifecycleManager) CloseAll() {
	// Get all connections
	lm.mu.RLock()
	connections := make([]*PeerConnection, 0, len(lm.connections))
	for _, pc := range lm.connections {
		connections = append(connections, pc)
	}
	lm.mu.RUnlock()

	// Close all connections first (triggers observers while still in map)
	for _, pc := range connections {
		pc.Close()
	}

	// Then clear the map
	lm.mu.Lock()
	lm.connections = make(map[string]*PeerConnection)
	lm.mu.Unlock()
}

// List returns all peer names in the manager.
func (lm *LifecycleManager) List() []string {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	names := make([]string, 0, len(lm.connections))
	for name := range lm.connections {
		names = append(names, name)
	}
	return names
}

// ListByState returns all peer names in the given state.
func (lm *LifecycleManager) ListByState(state State) []string {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	var names []string
	for name, pc := range lm.connections {
		if pc.State() == state {
			names = append(names, name)
		}
	}
	return names
}

// CountByState returns the count of connections in the given state.
func (lm *LifecycleManager) CountByState(state State) int {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	count := 0
	for _, pc := range lm.connections {
		if pc.State() == state {
			count++
		}
	}
	return count
}

// AllInfo returns information about all connections.
func (lm *LifecycleManager) AllInfo() []ConnectionInfo {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	infos := make([]ConnectionInfo, 0, len(lm.connections))
	for _, pc := range lm.connections {
		infos = append(infos, pc.Info())
	}
	return infos
}

// onTransition handles state transitions for route and tunnel lifecycle.
// This is called as an observer for all managed connections.
func (lm *LifecycleManager) onTransition(t Transition) {
	pc := lm.Get(t.PeerName)
	if pc == nil {
		return
	}

	meshIP := pc.MeshIP()

	switch t.To {
	case StateConnected:
		// Add route and tunnel
		if lm.router != nil && meshIP != "" {
			lm.router.AddRoute(meshIP, t.PeerName)
			log.Debug().
				Str("peer", t.PeerName).
				Str("mesh_ip", meshIP).
				Msg("added route on connection")
		}
		if lm.tunnels != nil {
			tunnel := pc.Tunnel()
			if tunnel != nil {
				lm.tunnels.Add(t.PeerName, tunnel)
				log.Debug().
					Str("peer", t.PeerName).
					Msg("added tunnel on connection")
			}
		}

	case StateDisconnected, StateClosed:
		// Remove route and tunnel
		if lm.router != nil && meshIP != "" {
			lm.router.RemoveRoute(meshIP)
			log.Debug().
				Str("peer", t.PeerName).
				Str("mesh_ip", meshIP).
				Str("reason", t.Reason).
				Msg("removed route on disconnect")
		}
		if lm.tunnels != nil {
			lm.tunnels.Remove(t.PeerName)
			log.Debug().
				Str("peer", t.PeerName).
				Str("reason", t.Reason).
				Msg("removed tunnel on disconnect")
		}

	case StateReconnecting:
		// Keep route but remove tunnel (will be re-added on reconnect)
		if lm.tunnels != nil {
			lm.tunnels.Remove(t.PeerName)
			log.Debug().
				Str("peer", t.PeerName).
				Str("reason", t.Reason).
				Msg("removed tunnel for reconnection (keeping route)")
		}
	}
}

// IsConnecting returns true if the peer is currently connecting.
func (lm *LifecycleManager) IsConnecting(peerName string) bool {
	pc := lm.Get(peerName)
	if pc == nil {
		return false
	}
	return pc.State() == StateConnecting
}

// IsConnected returns true if the peer is currently connected.
func (lm *LifecycleManager) IsConnected(peerName string) bool {
	pc := lm.Get(peerName)
	if pc == nil {
		return false
	}
	return pc.State() == StateConnected
}

// State returns the current state for a peer, or StateDisconnected if not found.
func (lm *LifecycleManager) State(peerName string) State {
	pc := lm.Get(peerName)
	if pc == nil {
		return StateDisconnected
	}
	return pc.State()
}

// StartConnecting transitions a peer to Connecting state with an optional cancel function.
// Returns true if the transition was successful, false if already connecting or invalid state.
func (lm *LifecycleManager) StartConnecting(peerName, meshIP string, cancel context.CancelFunc) bool {
	pc := lm.GetOrCreate(peerName, meshIP)

	// Only allow starting to connect from Disconnected state
	if pc.State() != StateDisconnected {
		return false
	}

	if cancel != nil {
		pc.SetCancelFunc(cancel)
	}

	if err := pc.StartConnecting("outbound dial"); err != nil {
		return false
	}

	return true
}

// CancelOutbound cancels any pending outbound connection attempt to the peer.
// Returns true if a connection was cancelled.
func (lm *LifecycleManager) CancelOutbound(peerName string) bool {
	pc := lm.Get(peerName)
	if pc == nil {
		return false
	}

	cancelled := pc.CancelOutbound()
	if cancelled {
		log.Debug().Str("peer", peerName).Msg("cancelled outbound connection")
	}
	return cancelled
}

// ClearConnecting transitions a peer from Connecting back to Disconnected.
// This is called when a connection attempt fails or is cancelled.
func (lm *LifecycleManager) ClearConnecting(peerName string) {
	pc := lm.Get(peerName)
	if pc == nil {
		return
	}

	// Only transition if currently connecting
	if pc.State() == StateConnecting {
		pc.Disconnect("connection attempt ended", nil)
	}
}
