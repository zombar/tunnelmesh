package peer

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/coord"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
)

// RunHeartbeat starts the heartbeat loop that maintains presence with the coordination server.
// It runs in fast mode (5s interval) for the first 60 seconds, then normal mode (30s interval).
func (m *MeshNode) RunHeartbeat(ctx context.Context) {
	// Fast phase: 5-second interval for first 60 seconds
	fastTicker := time.NewTicker(5 * time.Second)
	fastPhaseEnd := time.After(60 * time.Second)

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
			m.PerformHeartbeat(ctx)
		}
	}

	// Normal phase: 30-second interval
	normalTicker := time.NewTicker(30 * time.Second)
	defer normalTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-normalTicker.C:
			m.PerformHeartbeat(ctx)
		}
	}
}

// PerformHeartbeat performs a single heartbeat cycle.
func (m *MeshNode) PerformHeartbeat(ctx context.Context) {
	// Check if IPs have changed
	publicIPs, privateIPs, behindNAT := m.identity.GetLocalIPs()

	if m.IPsChanged(publicIPs, privateIPs, behindNAT) {
		oldPub, oldPriv, _ := m.GetHeartbeatIPs()
		if oldPub != nil {
			// IPs changed - handle the change
			log.Info().
				Strs("old_public", oldPub).
				Strs("new_public", publicIPs).
				Strs("old_private", oldPriv).
				Strs("new_private", privateIPs).
				Msg("IP addresses changed, re-registering...")

			m.HandleIPChange(publicIPs, privateIPs, behindNAT)

			// Re-register with server
			if _, err := m.client.Register(
				m.identity.Name, m.identity.PubKeyEncoded,
				publicIPs, privateIPs, m.identity.SSHPort, m.identity.UDPPort, behindNAT, m.identity.Version,
			); err != nil {
				log.Error().Err(err).Msg("failed to re-register after IP change")
			} else {
				log.Info().Msg("re-registered with new IP addresses")
			}
		} else {
			// First heartbeat - just record the IPs
			m.SetHeartbeatIPs(publicIPs, privateIPs, behindNAT)
		}
	}

	// Collect and send stats
	stats := m.CollectStats()
	heartbeatResp, err := m.client.HeartbeatWithStats(
		m.identity.Name, m.identity.PubKeyEncoded, stats,
	)
	if err != nil {
		m.handleHeartbeatError(err, publicIPs, privateIPs, behindNAT)
		return
	}

	// Handle reconnect signal from admin
	if heartbeatResp.Reconnect {
		log.Info().Msg("reconnect requested by admin, closing all tunnels")
		m.tunnelMgr.CloseAll()
		m.TriggerDiscovery()
	}

	// Handle relay requests
	m.HandleRelayRequests(ctx, heartbeatResp.RelayRequests)

	// Handle hole-punch requests
	m.HandleHolePunchRequests(ctx, heartbeatResp.HolePunchRequests)

	// Sync DNS
	m.syncDNS()
}

// HandleIPChange handles an IP address change by closing stale connections
// and triggering discovery.
func (m *MeshNode) HandleIPChange(publicIPs, privateIPs []string, behindNAT bool) {
	// Clear cached network state in transports (e.g., STUN-discovered addresses)
	if m.TransportRegistry != nil {
		m.TransportRegistry.ClearNetworkState()
	}

	// Close stale HTTP connections
	m.client.CloseIdleConnections()

	// Close all existing tunnels
	m.tunnelMgr.CloseAll()
	log.Debug().Msg("closed stale tunnels due to IP change")

	// Update stored IPs
	m.SetHeartbeatIPs(publicIPs, privateIPs, behindNAT)

	// Trigger discovery
	m.TriggerDiscovery()
}

// handleHeartbeatError handles errors from heartbeat.
func (m *MeshNode) handleHeartbeatError(err error, publicIPs, privateIPs []string, behindNAT bool) {
	if errors.Is(err, coord.ErrPeerNotFound) {
		// Server restarted or peer was removed - re-register
		log.Info().Msg("peer not found on server, re-registering...")
		ips := publicIPs
		privIPs := privateIPs
		nat := behindNAT
		if _, regErr := m.client.Register(
			m.identity.Name, m.identity.PubKeyEncoded,
			ips, privIPs, m.identity.SSHPort, m.identity.UDPPort, nat, m.identity.Version,
		); regErr != nil {
			log.Error().Err(regErr).Msg("failed to re-register")
		} else {
			log.Info().Msg("re-registered with coordination server")
			m.SetHeartbeatIPs(ips, privIPs, nat)
		}
	} else {
		log.Warn().Err(err).Msg("heartbeat failed")
	}
}

// HandleRelayRequests connects to relay for peers that are waiting for us.
func (m *MeshNode) HandleRelayRequests(ctx context.Context, relayRequests []string) {
	if len(relayRequests) == 0 {
		return
	}

	existingTunnels := m.tunnelMgr.List()
	existingSet := make(map[string]bool)
	for _, t := range existingTunnels {
		existingSet[t] = true
	}

	for _, peerName := range relayRequests {
		// Skip if we already have a tunnel to this peer
		if existingSet[peerName] {
			continue
		}

		log.Info().Str("peer", peerName).Msg("peer is waiting on relay for us, connecting...")

		jwtToken := m.client.JWTToken()
		if jwtToken == "" {
			log.Warn().Str("peer", peerName).Msg("no JWT token available for relay")
			continue
		}

		// Connect to relay in a goroutine to not block
		go m.connectRelay(ctx, peerName, jwtToken)
	}
}

// connectRelay connects to a relay for the given peer.
func (m *MeshNode) connectRelay(ctx context.Context, peerName, jwtToken string) {
	// Cancel any outbound connection attempt to this peer (relay is server-mediated inbound)
	m.CancelOutboundConnection(peerName)

	relayTunnel, err := tunnel.NewRelayTunnel(ctx, m.client.BaseURL(), peerName, jwtToken)
	if err != nil {
		log.Warn().Err(err).Str("peer", peerName).Msg("relay connection failed")
		return
	}

	m.tunnelMgr.Add(peerName, relayTunnel)
	log.Info().Str("peer", peerName).Msg("relay tunnel established via notification")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		m.Forwarder.HandleTunnel(ctx, peerName, relayTunnel)
	}
	m.tunnelMgr.RemoveIfMatch(peerName, relayTunnel)
}

// syncDNS syncs DNS records from the coordination server.
func (m *MeshNode) syncDNS() {
	if m.Resolver == nil {
		return
	}

	records, err := m.client.GetDNSRecords()
	if err != nil {
		log.Warn().Err(err).Msg("DNS sync failed")
		return
	}

	recordMap := make(map[string]string, len(records))
	for _, r := range records {
		recordMap[r.Hostname] = r.MeshIP
	}

	m.Resolver.UpdateRecords(recordMap)
}

// CheckAndHandleRelayRequests polls for relay requests and handles them.
func (m *MeshNode) CheckAndHandleRelayRequests(ctx context.Context) {
	relayRequests, err := m.client.CheckRelayRequests()
	if err != nil {
		log.Debug().Err(err).Msg("failed to check relay requests")
		return
	}
	m.HandleRelayRequests(ctx, relayRequests)
}

// HandleHolePunchRequests initiates hole-punching to peers that have requested it.
// When peer A tries to hole-punch to peer B, the server notifies B so it can
// simultaneously punch back to A, enabling NAT traversal.
func (m *MeshNode) HandleHolePunchRequests(ctx context.Context, holePunchRequests []string) {
	if len(holePunchRequests) == 0 {
		return
	}

	existingTunnels := m.tunnelMgr.List()
	existingSet := make(map[string]bool)
	for _, t := range existingTunnels {
		existingSet[t] = true
	}

	for _, peerName := range holePunchRequests {
		// Skip if we already have a tunnel to this peer
		if existingSet[peerName] {
			log.Debug().Str("peer", peerName).Msg("skipping hole-punch request, tunnel already exists")
			continue
		}

		// Skip if we're already connecting to this peer
		if m.IsConnecting(peerName) {
			log.Debug().Str("peer", peerName).Msg("skipping hole-punch request, already connecting")
			continue
		}

		log.Info().Str("peer", peerName).Msg("peer wants to hole-punch with us, initiating connection")

		// Get the peer info and start a connection attempt
		// This will use the negotiator which will try UDP hole-punching
		go m.ConnectToPeerByName(ctx, peerName)
	}
}
