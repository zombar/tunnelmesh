package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	udptransport "github.com/tunnelmesh/tunnelmesh/internal/transport/udp"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
)

// HandleIncomingUDP accepts incoming UDP connections from the listener.
func (m *MeshNode) HandleIncomingUDP(ctx context.Context, listener transport.Listener) {
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("UDP accept error")
			continue
		}

		go m.handleUDPConnection(ctx, conn)
	}
}

// handleUDPConnection handles an individual incoming UDP connection.
func (m *MeshNode) handleUDPConnection(ctx context.Context, conn transport.Connection) {
	peerName := conn.PeerName()
	if peerName == "" {
		log.Warn().Msg("UDP connection without peer name, rejecting")
		conn.Close()
		return
	}

	log.Info().
		Str("peer", peerName).
		Str("transport", string(conn.Type())).
		Msg("incoming UDP connection")

	// Cancel any outbound connection attempt to this peer
	m.Connections.CancelOutbound(peerName)

	// If we already have a healthy tunnel to this peer, reject the incoming connection
	// to avoid race conditions where both peers connect simultaneously
	if existing, ok := m.tunnelMgr.Get(peerName); ok {
		if hc, canCheck := existing.(tunnel.HealthChecker); canCheck && hc.IsHealthy() {
			log.Debug().
				Str("peer", peerName).
				Msg("already have healthy tunnel, rejecting incoming connection")
			conn.Close()
			return
		}
	}

	// Fetch peer info from coordination server to get mesh IP and add route
	// This ensures routing works immediately, without waiting for next discovery cycle
	meshIP := m.ensurePeerRoute(peerName)

	// Wrap connection as a tunnel
	tun := tunnel.NewTunnelFromTransport(conn)

	// Transition to Connected state (this adds tunnel via LifecycleManager observer)
	pc := m.Connections.GetOrCreate(peerName, meshIP)
	if err := pc.Connected(tun, "incoming UDP connection"); err != nil {
		log.Warn().Err(err).Str("peer", peerName).Msg("failed to transition to connected state")
		tun.Close()
		return
	}

	log.Info().Str("peer", peerName).Msg("tunnel established from incoming UDP connection")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		go func(name string, p *tunnel.Tunnel, peerConn interface{ Disconnect(string, error) error }) {
			m.Forwarder.HandleTunnel(ctx, name, p)
			// Disconnect when tunnel handler exits (removes tunnel via LifecycleManager observer)
			peerConn.Disconnect("tunnel handler exited", nil)
		}(peerName, tun, pc)
	}
}

// HandleUDPSessionInvalidated is called when a UDP session is invalidated by the remote peer
// (e.g., when we receive a rekey-required message). This removes the stale tunnel and
// triggers reconnection.
func (m *MeshNode) HandleUDPSessionInvalidated(peerName string) {
	log.Info().Str("peer", peerName).Msg("UDP session invalidated by peer, removing tunnel and triggering reconnection")

	// Disconnect the peer (removes tunnel via LifecycleManager observer)
	pc := m.Connections.Get(peerName)
	if pc != nil {
		pc.Disconnect("session invalidated by peer", nil)
	}

	// Trigger peer discovery to reconnect
	m.TriggerDiscovery()
}

// SetupUDPSessionInvalidCallback wires up the UDP transport's session invalid callback
// to handle remote session invalidation (rekey-required messages).
func (m *MeshNode) SetupUDPSessionInvalidCallback(udpTransport *udptransport.Transport) {
	udpTransport.SetSessionInvalidCallback(m.HandleUDPSessionInvalidated)
}
