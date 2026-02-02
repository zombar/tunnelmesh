package peer

import (
	"io"
	"sync"

	"github.com/rs/zerolog/log"
)

// TunnelAdapter wraps tunnel management to implement routing.TunnelProvider.
// It manages active tunnels to peer nodes and provides thread-safe access.
type TunnelAdapter struct {
	tunnels  map[string]io.ReadWriteCloser
	mu       sync.RWMutex
	onRemove func() // Called when a tunnel is removed (for triggering reconnection)
}

// NewTunnelAdapter creates a new TunnelAdapter.
func NewTunnelAdapter() *TunnelAdapter {
	return &TunnelAdapter{
		tunnels: make(map[string]io.ReadWriteCloser),
	}
}

// SetOnRemove sets a callback that is called when a tunnel is removed.
func (t *TunnelAdapter) SetOnRemove(callback func()) {
	t.onRemove = callback
}

// Get returns the tunnel for the given peer name.
func (t *TunnelAdapter) Get(name string) (io.ReadWriteCloser, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tunnel, ok := t.tunnels[name]
	return tunnel, ok
}

// Add adds or replaces a tunnel for the given peer.
// If a tunnel already exists for this peer, it is closed before replacement.
func (t *TunnelAdapter) Add(name string, tunnel io.ReadWriteCloser) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Close existing tunnel if present
	if existing, ok := t.tunnels[name]; ok {
		existing.Close()
	}
	t.tunnels[name] = tunnel
	log.Debug().Str("peer", name).Msg("tunnel added")
}

// Remove removes and closes the tunnel for the given peer.
func (t *TunnelAdapter) Remove(name string) {
	t.mu.Lock()
	callback := t.onRemove
	if tunnel, ok := t.tunnels[name]; ok {
		tunnel.Close()
		delete(t.tunnels, name)
		log.Debug().Str("peer", name).Msg("tunnel removed")
	}
	t.mu.Unlock()

	// Trigger reconnection callback outside the lock
	if callback != nil {
		callback()
	}
}

// RemoveIfMatch removes a tunnel only if the current tunnel for this peer matches
// the provided tunnel. This prevents removing a replacement tunnel when the old
// tunnel's handler goroutine exits.
func (t *TunnelAdapter) RemoveIfMatch(name string, tun io.ReadWriteCloser) {
	t.mu.Lock()
	callback := t.onRemove
	removed := false
	if existing, ok := t.tunnels[name]; ok && existing == tun {
		// Don't close - it's already closed (that's why we're here)
		delete(t.tunnels, name)
		log.Debug().Str("peer", name).Msg("tunnel removed")
		removed = true
	}
	t.mu.Unlock()

	// Only trigger reconnection if we actually removed the tunnel
	if removed && callback != nil {
		callback()
	}
}

// CloseAll closes and removes all tunnels.
func (t *TunnelAdapter) CloseAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for name, tunnel := range t.tunnels {
		tunnel.Close()
		delete(t.tunnels, name)
	}
	log.Debug().Msg("all tunnels closed")
}

// List returns the names of all connected peers.
func (t *TunnelAdapter) List() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.tunnels))
	for name := range t.tunnels {
		names = append(names, name)
	}
	return names
}

// HealthChecker is an optional interface for tunnels that can report health.
type HealthChecker interface {
	IsHealthy() bool
}

// CountHealthy returns the number of healthy tunnels.
// A tunnel is considered healthy if it implements HealthChecker and returns true,
// or if it doesn't implement HealthChecker (assumed healthy).
func (t *TunnelAdapter) CountHealthy() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, tun := range t.tunnels {
		if hc, ok := tun.(HealthChecker); ok {
			if hc.IsHealthy() {
				count++
			}
		} else {
			// Tunnels without health check capability are assumed healthy
			count++
		}
	}
	return count
}

// ListHealthy returns the names of all healthy peers.
func (t *TunnelAdapter) ListHealthy() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.tunnels))
	for name, tun := range t.tunnels {
		if hc, ok := tun.(HealthChecker); ok {
			if hc.IsHealthy() {
				names = append(names, name)
			}
		} else {
			// Tunnels without health check capability are assumed healthy
			names = append(names, name)
		}
	}
	return names
}
