package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
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
	m.CancelOutboundConnection(peerName)

	// Fetch peer info from coordination server to get mesh IP and add route
	// This ensures routing works immediately, without waiting for next discovery cycle
	m.ensurePeerRoute(peerName)

	// Wrap connection as a tunnel
	tun := tunnel.NewTunnelFromTransport(conn)

	// Add to tunnel manager
	m.tunnelMgr.Add(peerName, tun)

	log.Info().Str("peer", peerName).Msg("tunnel established from incoming UDP connection")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		go func(name string, t *tunnel.Tunnel) {
			m.Forwarder.HandleTunnel(ctx, name, t)
			m.tunnelMgr.RemoveIfMatch(name, t)
		}(peerName, tun)
	}
}
