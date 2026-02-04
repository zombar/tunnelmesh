package peer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/dns"
	"github.com/tunnelmesh/tunnelmesh/internal/peer/connection"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	sshtransport "github.com/tunnelmesh/tunnelmesh/internal/transport/ssh"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// networkBypassWindow is the duration after a network change during which
// we consider the node to be in a "recovery" state. Used for logging and debugging.
const networkBypassWindow = 10 * time.Second

// asymmetricDetectionGracePeriod is the duration after tunnel establishment during which
// we ignore relay packets for asymmetric failure detection. This prevents tearing down
// freshly established tunnels due to stale relay packets that were in-flight before
// the peer knew the direct tunnel was ready.
const asymmetricDetectionGracePeriod = 5 * time.Second

// MeshNode coordinates all mesh networking operations for a peer.
// It owns all the managers and handles lifecycle of connections.
type MeshNode struct {
	// Identity (immutable after construction)
	identity *PeerIdentity

	// Coordination server client
	client *coord.Client

	// Transport layer
	TransportRegistry   *transport.Registry
	TransportNegotiator *transport.Negotiator
	SSHTransport        *sshtransport.Transport // For incoming SSH and key management

	// Persistent relay for DERP-like instant connectivity
	PersistentRelay *tunnel.PersistentRelay

	// Tunnel and routing
	tunnelMgr *TunnelAdapter
	router    *routing.Router
	Forwarder *routing.Forwarder

	// Connection lifecycle management (FSM-based)
	Connections *connection.LifecycleManager

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

	// Peer cache for routing (mesh IP lookup when coord server unreachable)
	peerCacheMu sync.RWMutex
	peerCache   map[string]string // peer name -> mesh IP

	// Optional components
	Resolver *dns.Resolver
}

// NewMeshNode creates a new MeshNode with the given identity and client.
func NewMeshNode(identity *PeerIdentity, client *coord.Client) *MeshNode {
	router := routing.NewRouter()
	tunnelMgr := NewTunnelAdapter()

	node := &MeshNode{
		identity:         identity,
		client:           client,
		tunnelMgr:        tunnelMgr,
		router:           router,
		triggerDiscovery: make(chan struct{}, 1),
		peerCache:        make(map[string]string),
	}

	// Initialize connection lifecycle manager with tunnel integration
	// Routes are managed separately by discovery (via UpdateRoutes) to stay in sync
	// with the coordination server's peer list
	node.Connections = connection.NewLifecycleManager(connection.LifecycleConfig{
		Tunnels: tunnelMgr,
		OnConnect: func(peerName string) {
			// Trigger discovery to refresh routes immediately on connect
			// This ensures routes are current even if discovery hasn't run recently
			log.Debug().Str("peer", peerName).Msg("peer connected, triggering discovery for route refresh")
			node.TriggerDiscovery()
		},
		OnDisconnect: func(peerName string) {
			// Trigger discovery to refresh routes and attempt reconnection
			log.Debug().Str("peer", peerName).Msg("peer disconnected, triggering discovery")
			node.TriggerDiscovery()
		},
	})

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
// This is used to track network recovery state for logging and debugging.
func (m *MeshNode) RecordNetworkChange() {
	m.lastNetworkChange.Store(time.Now())
}

// InNetworkBypassWindow returns true if we're within the recovery window
// after a network change. Used for logging and debugging purposes.
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

// CachePeerMeshIP caches the mesh IP for a peer.
func (m *MeshNode) CachePeerMeshIP(peerName, meshIP string) {
	m.peerCacheMu.Lock()
	defer m.peerCacheMu.Unlock()
	m.peerCache[peerName] = meshIP
}

// GetCachedPeerMeshIP returns the cached mesh IP for a peer.
func (m *MeshNode) GetCachedPeerMeshIP(peerName string) (string, bool) {
	m.peerCacheMu.RLock()
	defer m.peerCacheMu.RUnlock()
	meshIP, ok := m.peerCache[peerName]
	return meshIP, ok
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
		ActiveTunnels: m.tunnelMgr.CountHealthy(),
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

// setupRelayHandlers configures the packet and peer reconnection handlers for a relay.
// This is extracted to ensure consistent behavior between initial connect and reconnect.
func (m *MeshNode) setupRelayHandlers(relay *tunnel.PersistentRelay) {
	// Set up packet handler to route incoming relay packets to forwarder
	relay.SetPacketHandler(func(sourcePeer string, data []byte) {
		if m.Forwarder != nil {
			log.Debug().Str("source", sourcePeer).Int("len", len(data)).Msg("routing relay packet to forwarder")
			m.Forwarder.HandleRelayPacket(sourcePeer, data)
		} else {
			log.Warn().Str("source", sourcePeer).Int("len", len(data)).Msg("dropping relay packet: forwarder not set")
		}

		// If we're receiving relay packets from a peer, they can't reach us directly.
		// Our direct tunnel to them is also likely broken (asymmetric path failure).
		// However, we apply a grace period after tunnel establishment to avoid
		// tearing down freshly established tunnels due to stale relay packets.
		// Also, if our tunnel to this peer is itself a relay tunnel, receiving
		// relay packets is expected behavior - don't invalidate.
		if pc := m.Connections.Get(sourcePeer); pc != nil {
			if pc.HasTunnel() {
				// If our tunnel to this peer is also a relay tunnel, receiving relay packets
				// is expected behavior - don't invalidate our relay tunnel
				if pc.IsRelayTunnel() {
					log.Debug().
						Str("peer", sourcePeer).
						Msg("receiving relay packet on relay tunnel - expected behavior")
					return
				}
				connectedSince := pc.ConnectedSince()
				tunnelAge := time.Since(connectedSince)
				if tunnelAge < asymmetricDetectionGracePeriod {
					log.Debug().
						Str("peer", sourcePeer).
						Dur("tunnel_age", tunnelAge).
						Msg("ignoring relay packet during grace period after tunnel establishment")
				} else {
					log.Info().
						Str("peer", sourcePeer).
						Dur("tunnel_age", tunnelAge).
						Msg("received relay packet from peer with active tunnel, invalidating stale tunnel")
					_ = pc.Disconnect("peer using relay (asymmetric tunnel failure)", nil)
				}
			}
		}
	})

	// Set up handler for peer reconnection notifications
	// NOTE: We no longer invalidate direct tunnels when a peer reconnects to relay.
	// The peer maintains a persistent relay connection for fallback - reconnecting to it
	// doesn't mean our direct tunnel is broken. We only rely on actual relay packet
	// reception to detect asymmetric situations (handled in SetPacketHandler above).
	relay.SetPeerReconnectedHandler(func(peerName string) {
		if pc := m.Connections.Get(peerName); pc != nil {
			if !pc.HasTunnel() {
				// No tunnel yet (e.g., still connecting) - ignore notification
				log.Debug().
					Str("peer", peerName).
					Msg("ignoring peer reconnect notification - no tunnel to invalidate")
				return
			}
			// Don't invalidate direct tunnels (UDP/SSH) when peer reconnects to relay.
			// Peer reconnecting to relay doesn't mean our direct path is broken -
			// they may just be refreshing their persistent relay connection.
			// We rely on actual relay packet reception to detect asymmetric situations.
			log.Debug().
				Str("peer", peerName).
				Str("transport", pc.TransportType()).
				Msg("ignoring peer reconnect notification - relying on packet-based detection")
		}
	})

	// Set up handler for reconnection errors to detect when we need to re-register
	relay.SetReconnectErrorHandler(func(err error) {
		if errors.Is(err, coord.ErrPeerNotFound) {
			log.Info().Msg("peer not registered on server, re-registering...")
			publicIPs, privateIPs, behindNAT := m.identity.GetLocalIPs()
			if _, regErr := m.client.Register(
				m.identity.Name, m.identity.PubKeyEncoded,
				publicIPs, privateIPs, m.identity.SSHPort, m.identity.UDPPort, behindNAT, m.identity.Version,
			); regErr != nil {
				log.Error().Err(regErr).Msg("failed to re-register after peer not found")
			} else {
				log.Info().Msg("re-registered with coordination server")
				m.SetHeartbeatIPs(publicIPs, privateIPs, behindNAT)
			}
		}
	})

	// Set up push notification handlers for relay and hole-punch requests.
	// These must be set here (not just in RunHeartbeat) to ensure they're
	// re-registered after relay reconnection.
	// Note: Uses background context since handlers spawn their own goroutines
	// and should continue processing even during shutdown.
	relay.SetRelayNotifyHandler(func(peers []string) {
		log.Debug().Strs("peers", peers).Msg("received relay notification via WebSocket")
		m.HandleRelayRequests(context.Background(), peers)
	})
	relay.SetHolePunchNotifyHandler(func(peers []string) {
		log.Debug().Strs("peers", peers).Msg("received hole-punch notification via WebSocket")
		m.HandleHolePunchRequests(context.Background(), peers)
	})
}

// ConnectPersistentRelay establishes the persistent relay connection.
// This should be called after registration when we have a JWT token.
func (m *MeshNode) ConnectPersistentRelay(ctx context.Context) error {
	jwtToken := m.client.JWTToken()
	if jwtToken == "" {
		log.Warn().Msg("no JWT token available for persistent relay")
		return nil
	}

	m.PersistentRelay = tunnel.NewPersistentRelay(m.client.BaseURL(), jwtToken)

	// Set up handlers BEFORE connecting
	m.setupRelayHandlers(m.PersistentRelay)

	if err := m.PersistentRelay.Connect(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to connect persistent relay")
		return err
	}

	// Update forwarder with new relay reference
	if m.Forwarder != nil {
		m.Forwarder.SetRelay(m.PersistentRelay)
	}

	log.Info().Msg("persistent relay connected for DERP-like routing")
	return nil
}

// ReconnectPersistentRelay reconnects the persistent relay after a network change.
// It uses atomic swap to keep the old relay active until the new one connects,
// preventing packet loss during the reconnection window.
func (m *MeshNode) ReconnectPersistentRelay(ctx context.Context) {
	oldRelay := m.PersistentRelay
	// Note: We intentionally do NOT close or clear the old relay here.
	// The forwarder continues using it until the new relay is ready.
	// We start reconnection immediately to minimize the window where
	// the old relay's connection might die before new one is ready.

	backoff := 200 * time.Millisecond // Start with fast retries
	maxBackoff := 10 * time.Second    // Cap backoff lower for faster recovery
	maxAttempts := 15                 // More attempts with faster backoff

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get fresh JWT token for new connection
		jwtToken := m.client.JWTToken()
		if jwtToken == "" {
			log.Warn().Int("attempt", attempt).Msg("no JWT token for relay reconnection")
			if attempt < maxAttempts {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}

		// Create new relay (don't touch old yet)
		newRelay := tunnel.NewPersistentRelay(m.client.BaseURL(), jwtToken)

		// Set up handlers BEFORE connecting (uses shared handler setup)
		m.setupRelayHandlers(newRelay)

		if err := newRelay.Connect(ctx); err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Msg("relay reconnection failed")
			newRelay.Close()

			if attempt < maxAttempts {
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}

		// SUCCESS! Atomically swap relay references
		// Forwarder will immediately start using new relay for outbound packets
		m.PersistentRelay = newRelay
		if m.Forwarder != nil {
			m.Forwarder.SetRelay(newRelay)
		}

		// NOW close old relay (packets already routing to new)
		if oldRelay != nil {
			oldRelay.Close()
		}

		log.Info().Int("attempt", attempt).Msg("persistent relay reconnected after network change")
		return
	}

	// All attempts failed - now clear references as last resort
	if oldRelay != nil {
		oldRelay.Close()
	}
	m.PersistentRelay = nil
	if m.Forwarder != nil {
		m.Forwarder.SetRelay(nil)
	}
	log.Error().Msg("gave up reconnecting persistent relay after max attempts")
}

// IsPersistentRelayConnected returns true if the persistent relay is connected.
func (m *MeshNode) IsPersistentRelayConnected() bool {
	return m.PersistentRelay != nil && m.PersistentRelay.IsConnected()
}
