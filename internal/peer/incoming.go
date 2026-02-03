package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
)

// handleIncomingConnection handles an individual incoming connection (SSH or UDP).
// This is the common logic shared between handleSSHConnection and handleUDPConnection.
func (m *MeshNode) handleIncomingConnection(ctx context.Context, conn transport.Connection, transportName string) {
	peerName := conn.PeerName()
	if peerName == "" {
		log.Warn().Str("transport", transportName).Msg("connection without peer name, rejecting")
		conn.Close()
		return
	}

	log.Info().
		Str("peer", peerName).
		Str("transport", string(conn.Type())).
		Msg("incoming " + transportName + " connection")

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

	// Get cached mesh IP for FSM tracking
	// Routes are managed atomically by discovery via UpdateRoutes()
	meshIP, _ := m.GetCachedPeerMeshIP(peerName)

	// Wrap connection as a tunnel
	tun := tunnel.NewTunnelFromTransport(conn)

	// Transition to Connected state (this adds tunnel via LifecycleManager observer)
	pc := m.Connections.GetOrCreate(peerName, meshIP)
	if err := pc.Connected(tun, "incoming "+transportName+" connection"); err != nil {
		log.Warn().Err(err).Str("peer", peerName).Msg("failed to transition to connected state")
		tun.Close()
		return
	}

	log.Info().Str("peer", peerName).Msg("tunnel established from incoming " + transportName + " connection")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		go func(name string, p *tunnel.Tunnel, peerConn interface{ Disconnect(string, error) error }) {
			m.Forwarder.HandleTunnel(ctx, name, p)
			// Disconnect when tunnel handler exits (removes tunnel via LifecycleManager observer)
			_ = peerConn.Disconnect("tunnel handler exited", nil)
		}(peerName, tun, pc)
	}
}
