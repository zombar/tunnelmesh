package peer

import (
	"context"
	"math/rand"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/negotiate"
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
	// Check if negotiator is available
	if m.Negotiator == nil {
		log.Debug().Str("peer", peer.Name).Msg("no negotiator available, skipping connection attempt")
		return
	}

	// Build peer info for negotiation
	peerInfo := m.buildPeerInfo(peer)

	// Log detailed peer info for debugging
	log.Debug().
		Str("peer", peer.Name).
		Str("public_ip", peerInfo.PublicIP).
		Strs("private_ips", peerInfo.PrivateIPs).
		Int("ssh_port", peerInfo.SSHPort).
		Bool("connectable", peerInfo.Connectable).
		Msg("attempting to establish tunnel")

	// Try to negotiate a connection
	result, err := m.Negotiator.Negotiate(ctx, peerInfo)
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("negotiation failed")
		return
	}

	// Handle based on negotiation result
	switch result.Strategy {
	case negotiate.StrategyReverse:
		log.Info().Str("peer", peer.Name).Msg("peer requires reverse connection, waiting for incoming")
		return

	case negotiate.StrategyRelay:
		m.handleRelayConnection(ctx, peer)

	case negotiate.StrategyDirect:
		// For direct SSH connections, apply alpha ordering to prevent duplicate tunnels
		// (both peers trying to SSH to each other simultaneously)
		// Exception: bypass during network change recovery window
		if m.identity.Name > peer.Name && !bypassAlphaOrdering {
			log.Debug().Str("peer", peer.Name).Msg("waiting for peer to initiate direct connection")
			return
		}

		if bypassAlphaOrdering && m.identity.Name > peer.Name {
			log.Debug().Str("peer", peer.Name).Msg("bypassing alpha ordering due to recent network change")
		}

		m.handleDirectConnection(ctx, peer, result)
	}
}

// buildPeerInfo builds a negotiate.PeerInfo from a proto.Peer.
func (m *MeshNode) buildPeerInfo(peer proto.Peer) *negotiate.PeerInfo {
	var publicIP string
	if len(peer.PublicIPs) > 0 {
		publicIP = peer.PublicIPs[0]
	}

	return &negotiate.PeerInfo{
		ID:          peer.Name,
		PublicIP:    publicIP,
		PrivateIPs:  peer.PrivateIPs,
		SSHPort:     peer.SSHPort,
		Connectable: peer.Connectable,
	}
}

// handleDirectConnection handles establishing a direct SSH tunnel.
func (m *MeshNode) handleDirectConnection(ctx context.Context, peer proto.Peer, result *negotiate.NegotiationResult) {
	if m.SSHClient == nil {
		log.Debug().Str("peer", peer.Name).Msg("no SSH client available")
		return
	}

	// Direct SSH connection
	log.Info().
		Str("peer", peer.Name).
		Str("addr", result.Address).
		Msg("connecting to peer via direct SSH")

	sshConn, err := m.SSHClient.Connect(result.Address)
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("SSH connection failed")
		return
	}

	// Open data channel with our name as extra data so the peer knows who we are
	channel, _, err := sshConn.OpenChannel(tunnel.ChannelType, []byte(m.identity.Name))
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to open channel")
		sshConn.Close()
		return
	}

	// Create tunnel and add to manager
	// Use NewTunnelWithClient to keep SSH connection alive (prevents GC from closing it)
	tun := tunnel.NewTunnelWithClient(channel, peer.Name, sshConn)
	m.tunnelMgr.Add(peer.Name, tun)

	log.Info().Str("peer", peer.Name).Msg("tunnel established")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		m.Forwarder.HandleTunnel(ctx, peer.Name, tun)
	}
	m.tunnelMgr.RemoveIfMatch(peer.Name, tun)
}

// handleRelayConnection handles establishing a relay tunnel.
func (m *MeshNode) handleRelayConnection(ctx context.Context, peer proto.Peer) {
	log.Info().Str("peer", peer.Name).Msg("using relay connection through coordination server")

	jwtToken := m.client.JWTToken()
	if jwtToken == "" {
		log.Warn().Str("peer", peer.Name).Msg("no JWT token available for relay")
		return
	}

	relayTunnel, err := tunnel.NewRelayTunnel(ctx, m.client.BaseURL(), peer.Name, jwtToken)
	if err != nil {
		log.Warn().Err(err).Str("peer", peer.Name).Msg("relay connection failed")
		return
	}

	m.tunnelMgr.Add(peer.Name, relayTunnel)
	log.Info().Str("peer", peer.Name).Msg("relay tunnel established")

	if m.Forwarder != nil {
		m.Forwarder.HandleTunnel(ctx, peer.Name, relayTunnel)
	}
	m.tunnelMgr.RemoveIfMatch(peer.Name, relayTunnel)
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
