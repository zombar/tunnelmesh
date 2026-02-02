package transport

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// Manager manages active connections with hot-swap capability.
type Manager struct {
	connections map[string]*ManagedConnection
	mu          sync.RWMutex
	onRemove    func(peerName string)
}

// NewManager creates a new connection manager.
func NewManager() *Manager {
	return &Manager{
		connections: make(map[string]*ManagedConnection),
	}
}

// SetOnRemove sets a callback for when connections are removed.
func (m *Manager) SetOnRemove(fn func(peerName string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRemove = fn
}

// Add adds or replaces a connection for a peer.
func (m *Manager) Add(peerName string, conn Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing connection if present
	if existing, ok := m.connections[peerName]; ok {
		existing.closeInternal()
	}

	m.connections[peerName] = &ManagedConnection{
		current:  conn,
		peerName: peerName,
	}

	log.Debug().
		Str("peer", peerName).
		Str("transport", string(conn.Type())).
		Msg("connection added to manager")
}

// Get returns the connection for a peer.
func (m *Manager) Get(peerName string) (io.ReadWriteCloser, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	mc, ok := m.connections[peerName]
	if !ok {
		return nil, false
	}
	return mc, true
}

// GetManaged returns the managed connection wrapper for a peer.
func (m *Manager) GetManaged(peerName string) (*ManagedConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	mc, ok := m.connections[peerName]
	return mc, ok
}

// Remove removes and closes a connection.
func (m *Manager) Remove(peerName string) {
	m.mu.Lock()
	var onRemove func(string)
	if mc, ok := m.connections[peerName]; ok {
		mc.closeInternal()
		delete(m.connections, peerName)
		onRemove = m.onRemove
	}
	m.mu.Unlock()

	if onRemove != nil {
		onRemove(peerName)
	}

	log.Debug().Str("peer", peerName).Msg("connection removed from manager")
}

// RemoveIfMatch removes a connection only if it matches the given connection.
// This prevents removing a newer connection when an old handler exits.
func (m *Manager) RemoveIfMatch(peerName string, conn Connection) bool {
	m.mu.Lock()
	var onRemove func(string)
	var removed bool
	if mc, ok := m.connections[peerName]; ok && mc.current == conn {
		mc.closeInternal()
		delete(m.connections, peerName)
		onRemove = m.onRemove
		removed = true
	}
	m.mu.Unlock()

	if onRemove != nil && removed {
		onRemove(peerName)
	}

	return removed
}

// List returns the names of all active connections.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.connections))
	for name := range m.connections {
		names = append(names, name)
	}
	return names
}

// Count returns the number of active connections.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}

// CloseAll closes all connections.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, mc := range m.connections {
		mc.closeInternal()
		delete(m.connections, name)
	}

	log.Debug().Msg("all connections closed")
}

// GetConnectionInfo returns info about all connections.
func (m *Manager) GetConnectionInfo() map[string]TransportType {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info := make(map[string]TransportType, len(m.connections))
	for name, mc := range m.connections {
		mc.mu.RLock()
		if mc.current != nil {
			info[name] = mc.current.Type()
		}
		mc.mu.RUnlock()
	}
	return info
}

// ManagedConnection wraps a connection with upgrade capability.
type ManagedConnection struct {
	current   Connection
	peerName  string
	upgrading atomic.Bool
	closed    atomic.Bool
	mu        sync.RWMutex
}

// Read delegates to current connection.
func (mc *ManagedConnection) Read(p []byte) (int, error) {
	mc.mu.RLock()
	conn := mc.current
	mc.mu.RUnlock()

	if conn == nil {
		return 0, io.EOF
	}
	return conn.Read(p)
}

// Write delegates to current connection.
func (mc *ManagedConnection) Write(p []byte) (int, error) {
	mc.mu.RLock()
	conn := mc.current
	mc.mu.RUnlock()

	if conn == nil {
		return 0, io.ErrClosedPipe
	}
	return conn.Write(p)
}

// Close closes the managed connection.
func (mc *ManagedConnection) Close() error {
	if mc.closed.Swap(true) {
		return nil // Already closed
	}
	return mc.closeInternal()
}

// closeInternal closes without checking the closed flag.
func (mc *ManagedConnection) closeInternal() error {
	mc.mu.Lock()
	conn := mc.current
	mc.current = nil
	mc.mu.Unlock()

	if conn != nil {
		return conn.Close()
	}
	return nil
}

// PeerName returns the peer name.
func (mc *ManagedConnection) PeerName() string {
	return mc.peerName
}

// Type returns the current transport type.
func (mc *ManagedConnection) Type() TransportType {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.current != nil {
		return mc.current.Type()
	}
	return ""
}

// Swap atomically swaps the underlying connection.
// The old connection is NOT closed - caller must handle that.
func (mc *ManagedConnection) Swap(newConn Connection) Connection {
	mc.mu.Lock()
	old := mc.current
	mc.current = newConn
	mc.mu.Unlock()

	log.Debug().
		Str("peer", mc.peerName).
		Str("old_transport", string(old.Type())).
		Str("new_transport", string(newConn.Type())).
		Msg("connection swapped")

	return old
}

// IsUpgrading returns true if an upgrade is in progress.
func (mc *ManagedConnection) IsUpgrading() bool {
	return mc.upgrading.Load()
}

// SetUpgrading sets the upgrading flag.
func (mc *ManagedConnection) SetUpgrading(v bool) {
	mc.upgrading.Store(v)
}

// GetConnection returns the underlying connection (for advanced use).
func (mc *ManagedConnection) GetConnection() Connection {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.current
}
