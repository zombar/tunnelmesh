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

	// Check if we're in a network change bypass window
	bypassAlphaOrdering := m.InNetworkBypassWindow()

	for _, peer := range peers {
		if peer.Name == m.identity.Name {
			continue // Skip self
		}

		// Add peer's public key to authorized keys for incoming connections
		if peer.PublicKey != "" && m.SSHServer != nil {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				m.SSHServer.AddAuthorizedKey(pubKey)
			}
		}

		// Update routing table with peer's mesh IP
		m.router.AddRoute(peer.MeshIP, peer.Name)

		// Skip if tunnel already exists
		if existingSet[peer.Name] {
			continue
		}

		// Try to establish tunnel
		go m.EstablishTunnel(ctx, peer, bypassAlphaOrdering)
	}
}

// EstablishTunnel negotiates and establishes a tunnel to a peer.
func (m *MeshNode) EstablishTunnel(ctx context.Context, peer proto.Peer, bypassAlphaOrdering bool) {
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

	// For direct connections, apply alpha ordering to prevent duplicate tunnels
	// (both peers trying to connect to each other simultaneously)
	// Exception: bypass during network change recovery window
	if m.identity.Name > peer.Name && !bypassAlphaOrdering {
		log.Debug().Str("peer", peer.Name).Msg("waiting for peer to initiate connection (alpha ordering)")
		return
	}

	if bypassAlphaOrdering && m.identity.Name > peer.Name {
		log.Debug().Str("peer", peer.Name).Msg("bypassing alpha ordering due to recent network change")
	}

	// Set up dial options
	dialOpts := transport.DialOptions{
		PeerName:  peer.Name,
		ServerURL: m.client.BaseURL(),
		JWTToken:  m.client.JWTToken(),
	}

	// Try to negotiate a connection using the new transport layer
	result, err := m.TransportNegotiator.Negotiate(ctx, peerInfo, dialOpts)
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("transport negotiation failed")
		return
	}

	// Wrap the transport.Connection in a tunnel adapter
	tun := tunnel.NewConnectionAdapter(result.Connection, peer.Name)
	m.tunnelMgr.Add(peer.Name, tun)

	log.Info().
		Str("peer", peer.Name).
		Str("transport", string(result.Transport)).
		Msg("tunnel established via transport layer")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		m.Forwarder.HandleTunnel(ctx, peer.Name, tun)
	}
	m.tunnelMgr.RemoveIfMatch(peer.Name, tun)
}

// buildTransportPeerInfo builds a transport.PeerInfo from a proto.Peer.
func (m *MeshNode) buildTransportPeerInfo(peer proto.Peer) *transport.PeerInfo {
	return &transport.PeerInfo{
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
}

// RefreshAuthorizedKeys fetches peer keys from coordination server and adds them to SSH server.
func (m *MeshNode) RefreshAuthorizedKeys() {
	if m.SSHServer == nil {
		return
	}

	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Msg("failed to refresh peer keys")
		return
	}

	for _, peer := range peers {
		if peer.PublicKey != "" {
			pubKey, err := config.DecodePublicKey(peer.PublicKey)
			if err != nil {
				log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
			} else {
				m.SSHServer.AddAuthorizedKey(pubKey)
			}
		}
		m.router.AddRoute(peer.MeshIP, peer.Name)
	}
	log.Debug().Int("peers", len(peers)).Msg("refreshed authorized keys")
}
