package transport

import (
	"fmt"
	"sync"
)

// PeerTransportConfig holds per-peer transport settings.
type PeerTransportConfig struct {
	Preferred    []TransportType // Ordered preference
	Disabled     []TransportType // Transports to never use
	AllowUpgrade bool            // Allow hot-swapping to better transport
}

// RegistryConfig holds configuration for the transport registry.
type RegistryConfig struct {
	DefaultOrder []TransportType
}

// Registry manages available transports and per-peer preferences.
type Registry struct {
	transports   map[TransportType]Transport
	peerConfigs  map[string]PeerTransportConfig
	defaultOrder []TransportType
	mu           sync.RWMutex
}

// NewRegistry creates a new transport registry.
func NewRegistry(cfg RegistryConfig) *Registry {
	defaultOrder := cfg.DefaultOrder
	if len(defaultOrder) == 0 {
		defaultOrder = []TransportType{TransportUDP, TransportSSH, TransportRelay}
	}

	return &Registry{
		transports:   make(map[TransportType]Transport),
		peerConfigs:  make(map[string]PeerTransportConfig),
		defaultOrder: defaultOrder,
	}
}

// Register adds a transport implementation.
func (r *Registry) Register(t Transport) error {
	if t == nil {
		return fmt.Errorf("transport cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	typ := t.Type()
	if _, exists := r.transports[typ]; exists {
		return fmt.Errorf("transport %s already registered", typ)
	}

	r.transports[typ] = t
	return nil
}

// Get returns a transport by type.
func (r *Registry) Get(typ TransportType) (Transport, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.transports[typ]
	return t, ok
}

// GetAll returns all registered transports.
func (r *Registry) GetAll() map[TransportType]Transport {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[TransportType]Transport, len(r.transports))
	for k, v := range r.transports {
		result[k] = v
	}
	return result
}

// GetPreferredOrder returns the preferred transport order for a peer.
// Returns the peer-specific order if configured, otherwise the default order.
func (r *Registry) GetPreferredOrder(peerName string) []TransportType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if cfg, ok := r.peerConfigs[peerName]; ok && len(cfg.Preferred) > 0 {
		return r.filterDisabled(cfg.Preferred, cfg.Disabled)
	}

	// Use default order, filtered by any peer-specific disabled transports
	if cfg, ok := r.peerConfigs[peerName]; ok && len(cfg.Disabled) > 0 {
		return r.filterDisabled(r.defaultOrder, cfg.Disabled)
	}

	return r.defaultOrder
}

// filterDisabled removes disabled transports from the order.
func (r *Registry) filterDisabled(order, disabled []TransportType) []TransportType {
	if len(disabled) == 0 {
		return order
	}

	disabledSet := make(map[TransportType]bool, len(disabled))
	for _, d := range disabled {
		disabledSet[d] = true
	}

	filtered := make([]TransportType, 0, len(order))
	for _, t := range order {
		if !disabledSet[t] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// SetPeerConfig sets per-peer transport preferences.
func (r *Registry) SetPeerConfig(peerName string, cfg PeerTransportConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.peerConfigs[peerName] = cfg
}

// GetPeerConfig returns the transport configuration for a peer.
func (r *Registry) GetPeerConfig(peerName string) (PeerTransportConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cfg, ok := r.peerConfigs[peerName]
	return cfg, ok
}

// RemovePeerConfig removes per-peer transport preferences.
func (r *Registry) RemovePeerConfig(peerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.peerConfigs, peerName)
}

// SetDefaultOrder sets the default transport order.
func (r *Registry) SetDefaultOrder(order []TransportType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.defaultOrder = order
}

// Close closes all registered transports.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lastErr error
	for _, t := range r.transports {
		if err := t.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// ClearNetworkState clears cached network state on all transports that support it.
// This should be called after network changes to ensure fresh address discovery.
func (r *Registry) ClearNetworkState() {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, t := range r.transports {
		if resetter, ok := t.(NetworkStateResetter); ok {
			resetter.ClearNetworkState()
		}
	}
}
