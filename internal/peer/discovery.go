package peer

import (
	"context"
	"math/rand"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// RunPeerDiscovery periodically discovers peers and establishes tunnels.
func (m *MeshNode) RunPeerDiscovery(ctx context.Context) {
	// Add random jitter (0-3 seconds) before initial discovery
	jitter := time.Duration(rand.Intn(3000)) * time.Millisecond
	log.Debug().Dur("jitter", jitter).Msg("waiting before initial peer discovery")
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	// Initial peer discovery
	m.DiscoverAndConnectPeers(ctx)

	// Fast retry phase: discover every 5 seconds for the first 30 seconds
	fastTicker := time.NewTicker(5 * time.Second)
	fastPhaseEnd := time.After(30 * time.Second)

fastLoop:
	for {
		select {
		case <-ctx.Done():
			fastTicker.Stop()
			return
		case <-fastPhaseEnd:
			fastTicker.Stop()
			break fastLoop
		case <-fastTicker.C:
			m.DiscoverAndConnectPeers(ctx)
			m.CheckAndHandleRelayRequests(ctx)
		case <-m.triggerDiscovery:
			log.Debug().Msg("peer discovery triggered by network change")
			m.DiscoverAndConnectPeers(ctx)
			m.CheckAndHandleRelayRequests(ctx)
		}
	}

	// Normal phase: discover every 60 seconds
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.DiscoverAndConnectPeers(ctx)
		case <-m.triggerDiscovery:
			log.Debug().Msg("peer discovery triggered by network change")
			m.DiscoverAndConnectPeers(ctx)
		}
	}
}

// DiscoverAndConnectPeers lists peers from the coordination server and establishes tunnels.
func (m *MeshNode) DiscoverAndConnectPeers(ctx context.Context) {
	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("failed to list peers")
		return
	}

	existingTunnels := m.tunnelMgr.List()
	existingSet := make(map[string]bool)
	for _, name := range existingTunnels {
		existingSet[name] = true
	}

	// Build routes map for atomic update - only routes for peers known to coord server
	routes := make(map[string]string, len(peers))

	for _, peer := range peers {
		if peer.Name == m.identity.Name {
			continue // Skip self
		}

		// Add peer's public key to authorized keys for incoming connections
		if peer.PublicKey != "" && m.SSHTransport != nil {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				m.SSHTransport.AddAuthorizedKey(pubKey)
			}
		}

		// Collect route for atomic update
		routes[peer.MeshIP] = peer.Name
		// Cache peer mesh IP for use when coord server is unreachable
		m.CachePeerMeshIP(peer.Name, peer.MeshIP)

		// Skip if tunnel already exists
		if existingSet[peer.Name] {
			continue
		}

		// Skip if connection attempt already in progress
		if m.Connections.IsConnecting(peer.Name) {
			log.Debug().Str("peer", peer.Name).Msg("connection attempt already in progress, skipping")
			continue
		}

		// Try to establish tunnel
		go m.EstablishTunnel(ctx, peer)
	}

	// Atomically update all routes - this adds new peers and removes stale ones
	m.router.UpdateRoutes(routes)
}

// EstablishTunnel negotiates and establishes a tunnel to a peer.
// Both peers race to connect - first successful connection wins.
// The connection attempt can be cancelled if an inbound connection arrives.
func (m *MeshNode) EstablishTunnel(ctx context.Context, peer proto.Peer) {
	// Create a cancellable context for this outbound connection
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Mark as connecting with cancel function - if already connecting, bail out
	if !m.Connections.StartConnecting(peer.Name, peer.MeshIP, cancel) {
		log.Debug().Str("peer", peer.Name).Msg("connection attempt already in progress")
		return
	}
	defer m.Connections.ClearConnecting(peer.Name)

	// Check if transport negotiator is available
	if m.TransportNegotiator == nil {
		log.Debug().Str("peer", peer.Name).Msg("no transport negotiator available, skipping connection attempt")
		return
	}

	// Build peer info for negotiation
	peerInfo := m.buildTransportPeerInfo(peer)

	// Log detailed peer info for debugging
	log.Debug().
		Str("peer", peer.Name).
		Strs("public_ips", peerInfo.PublicIPs).
		Strs("private_ips", peerInfo.PrivateIPs).
		Int("ssh_port", peerInfo.SSHPort).
		Int("udp_port", peerInfo.UDPPort).
		Bool("connectable", peerInfo.Connectable).
		Msg("attempting to establish tunnel")

	// Set up dial options
	dialOpts := transport.DialOptions{
		LocalName: m.identity.Name,
		PeerName:  peer.Name,
		ServerURL: m.client.BaseURL(),
		JWTToken:  m.client.JWTToken(),
	}

	// Try to negotiate a connection using the transport layer
	result, err := m.TransportNegotiator.Negotiate(connCtx, peerInfo, dialOpts)
	if err != nil {
		// Check if we were cancelled due to inbound connection
		if connCtx.Err() == context.Canceled {
			log.Debug().Str("peer", peer.Name).Msg("outbound connection cancelled (inbound connection established)")
			return
		}
		log.Warn().Err(err).Str("peer", peer.Name).Msg("transport negotiation failed")
		return
	}

	// Check if a tunnel was established while we were negotiating (by the other peer)
	if _, exists := m.tunnelMgr.Get(peer.Name); exists {
		log.Debug().Str("peer", peer.Name).Msg("tunnel already established by peer, closing our connection")
		result.Connection.Close()
		return
	}

	// Wrap the transport.Connection in a tunnel adapter
	tun := tunnel.NewConnectionAdapter(result.Connection, peer.Name)

	// Transition to Connected state (this adds tunnel via LifecycleManager observer)
	pc := m.Connections.Get(peer.Name)
	if pc == nil {
		log.Warn().Str("peer", peer.Name).Msg("peer connection not found after negotiation")
		tun.Close()
		return
	}
	if err := pc.Connected(tun, string(result.Transport), "transport negotiated: "+string(result.Transport)); err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to transition to connected state")
		tun.Close()
		return
	}

	log.Info().
		Str("peer", peer.Name).
		Str("transport", string(result.Transport)).
		Msg("tunnel established via transport layer")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		m.Forwarder.HandleTunnel(connCtx, peer.Name, tun)
	}

	// Disconnect when tunnel handler exits (this removes tunnel via LifecycleManager observer)
	_ = pc.Disconnect("tunnel handler exited", nil)
}

// buildTransportPeerInfo builds a transport.PeerInfo from a proto.Peer.
func (m *MeshNode) buildTransportPeerInfo(peer proto.Peer) *transport.PeerInfo {
	info := &transport.PeerInfo{
		Name:             peer.Name,
		PublicIPs:        peer.PublicIPs,
		PrivateIPs:       peer.PrivateIPs,
		SSHPort:          peer.SSHPort,
		UDPPort:          peer.UDPPort,
		Connectable:      peer.Connectable,
		BehindNAT:        !peer.Connectable,
		PublicKey:        peer.PublicKey,
		ExternalEndpoint: peer.ExternalEndpoint,
	}

	return info
}

// ConnectToPeerByName fetches the peer info from the coordination server and establishes a tunnel.
// This is used when we're notified that a peer wants to connect to us (e.g., hole-punch notification).
func (m *MeshNode) ConnectToPeerByName(ctx context.Context, peerName string) {
	// Skip if tunnel already exists
	if _, exists := m.tunnelMgr.Get(peerName); exists {
		log.Debug().Str("peer", peerName).Msg("tunnel already exists, skipping connection attempt")
		return
	}

	// Fetch peer info from coordination server
	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Str("peer", peerName).Msg("failed to fetch peer info for connection")
		return
	}

	// Find the target peer
	var targetPeer *proto.Peer
	for _, p := range peers {
		if p.Name == peerName {
			targetPeer = &p
			break
		}
	}

	if targetPeer == nil {
		log.Warn().Str("peer", peerName).Msg("peer not found on coordination server")
		return
	}

	// Establish tunnel
	m.EstablishTunnel(ctx, *targetPeer)
}

// RefreshAuthorizedKeys fetches peer keys from coordination server and adds them to SSH transport.
func (m *MeshNode) RefreshAuthorizedKeys() {
	if m.SSHTransport == nil {
		return
	}

	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("failed to refresh peer keys")
		return
	}

	// Build routes map for atomic update
	routes := make(map[string]string, len(peers))

	for _, peer := range peers {
		if peer.Name == m.identity.Name {
			continue // Skip self
		}
		if peer.PublicKey != "" {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				m.SSHTransport.AddAuthorizedKey(pubKey)
			}
		}
		routes[peer.MeshIP] = peer.Name
		m.CachePeerMeshIP(peer.Name, peer.MeshIP)
	}

	// Atomically update all routes
	m.router.UpdateRoutes(routes)
	log.Debug().Int("peers", len(peers)).Msg("refreshed authorized keys and routes")
}
