package peer

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/dns"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// networkBypassWindow is the duration after a network change during which
// alpha ordering is bypassed to speed up reconnection.
const networkBypassWindow = 10 * time.Second

// MeshNode coordinates all mesh networking operations for a peer.
// It owns all the managers and handles lifecycle of connections.
type MeshNode struct {
	// Identity (immutable after construction)
	identity *PeerIdentity

	// Coordination server client
	client *coord.Client

	// SSH components (legacy, used by HandleIncomingSSH)
	SSHClient *tunnel.SSHClient
	SSHServer *tunnel.SSHServer

	// Transport layer (new pluggable transport system)
	TransportRegistry   *transport.Registry
	TransportNegotiator *transport.Negotiator

	// Tunnel and routing
	tunnelMgr *TunnelAdapter
	router    *routing.Router
	Forwarder *routing.Forwarder

	// Signals
	triggerDiscovery chan struct{}

	// Network change state
	lastNetworkChange atomic.Value // stores time.Time

	// Heartbeat state
	heartbeatMu       sync.RWMutex
	lastPublicIPs     []string
	lastPrivateIPs    []string
	lastBehindNAT     bool
	heartbeatIPsIsSet bool

	// Optional components
	Resolver *dns.Resolver
}

// NewMeshNode creates a new MeshNode with the given identity and client.
func NewMeshNode(identity *PeerIdentity, client *coord.Client) *MeshNode {
	node := &MeshNode{
		identity:         identity,
		client:           client,
		tunnelMgr:        NewTunnelAdapter(),
		router:           routing.NewRouter(),
		triggerDiscovery: make(chan struct{}, 1),
	}
	node.lastNetworkChange.Store(time.Time{})
	return node
}

// Identity returns the peer's identity.
func (m *MeshNode) Identity() *PeerIdentity {
	return m.identity
}

// Client returns the coordination client.
func (m *MeshNode) Client() *coord.Client {
	return m.client
}

// TunnelMgr returns the tunnel adapter.
func (m *MeshNode) TunnelMgr() *TunnelAdapter {
	return m.tunnelMgr
}

// Router returns the routing table.
func (m *MeshNode) Router() *routing.Router {
	return m.router
}

// DiscoveryChan returns the channel used to trigger peer discovery.
func (m *MeshNode) DiscoveryChan() <-chan struct{} {
	return m.triggerDiscovery
}

// TriggerDiscovery triggers a peer discovery cycle.
// Returns true if the trigger was sent, false if a discovery is already pending.
func (m *MeshNode) TriggerDiscovery() bool {
	select {
	case m.triggerDiscovery <- struct{}{}:
		return true
	default:
		return false
	}
}

// RecordNetworkChange records that a network change has occurred.
// This enables the network bypass window for alpha ordering.
func (m *MeshNode) RecordNetworkChange() {
	m.lastNetworkChange.Store(time.Now())
}

// InNetworkBypassWindow returns true if we're within the network bypass window
// after a network change, during which alpha ordering should be bypassed.
func (m *MeshNode) InNetworkBypassWindow() bool {
	lastChange := m.lastNetworkChange.Load().(time.Time)
	if lastChange.IsZero() {
		return false
	}
	return time.Since(lastChange) < networkBypassWindow
}

// GetHeartbeatIPs returns the last known IPs from heartbeat detection.
func (m *MeshNode) GetHeartbeatIPs() (publicIPs, privateIPs []string, behindNAT bool) {
	m.heartbeatMu.RLock()
	defer m.heartbeatMu.RUnlock()
	return m.lastPublicIPs, m.lastPrivateIPs, m.lastBehindNAT
}

// SetHeartbeatIPs updates the last known IPs from heartbeat detection.
func (m *MeshNode) SetHeartbeatIPs(publicIPs, privateIPs []string, behindNAT bool) {
	m.heartbeatMu.Lock()
	defer m.heartbeatMu.Unlock()
	m.lastPublicIPs = publicIPs
	m.lastPrivateIPs = privateIPs
	m.lastBehindNAT = behindNAT
	m.heartbeatIPsIsSet = true
}

// IPsChanged returns true if the provided IPs differ from the last known IPs.
// If no IPs have been set yet (first heartbeat), returns true only if we have
// recorded IPs before (to avoid re-registering on first heartbeat).
func (m *MeshNode) IPsChanged(publicIPs, privateIPs []string, behindNAT bool) bool {
	m.heartbeatMu.RLock()
	defer m.heartbeatMu.RUnlock()

	if !m.heartbeatIPsIsSet {
		// First time - not a "change" per se, but caller should set initial IPs
		return true
	}

	return !slicesEqual(publicIPs, m.lastPublicIPs) ||
		!slicesEqual(privateIPs, m.lastPrivateIPs) ||
		behindNAT != m.lastBehindNAT
}

// CollectStats collects stats from the forwarder and tunnel manager.
func (m *MeshNode) CollectStats() *proto.PeerStats {
	stats := &proto.PeerStats{
		ActiveTunnels: len(m.tunnelMgr.List()),
	}

	if m.Forwarder != nil {
		fwdStats := m.Forwarder.Stats()
		stats.PacketsSent = fwdStats.PacketsSent
		stats.PacketsReceived = fwdStats.PacketsReceived
		stats.BytesSent = fwdStats.BytesSent
		stats.BytesReceived = fwdStats.BytesReceived
		stats.DroppedNoRoute = fwdStats.DroppedNoRoute
		stats.DroppedNoTunnel = fwdStats.DroppedNoTunnel
		stats.Errors = fwdStats.Errors
	}

	return stats
}

// slicesEqual compares two string slices for equality.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
