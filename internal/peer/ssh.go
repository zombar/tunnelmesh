package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
)

// HandleIncomingSSH accepts incoming SSH connections from the transport listener.
func (m *MeshNode) HandleIncomingSSH(ctx context.Context, listener transport.Listener) {
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("SSH accept error")
			continue
		}

		go m.handleSSHConnection(ctx, conn)
	}
}

// handleSSHConnection handles an individual incoming SSH connection.
func (m *MeshNode) handleSSHConnection(ctx context.Context, conn transport.Connection) {
	peerName := conn.PeerName()
	if peerName == "" {
		log.Warn().Msg("SSH connection without peer name, rejecting")
		conn.Close()
		return
	}

	log.Info().
		Str("peer", peerName).
		Str("transport", string(conn.Type())).
		Msg("incoming SSH connection")

	// Cancel any outbound connection attempt to this peer
	m.CancelOutboundConnection(peerName)

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
	m.ensurePeerRoute(peerName)

	// Wrap connection as a tunnel
	tun := tunnel.NewTunnelFromTransport(conn)

	// Add to tunnel manager
	m.tunnelMgr.Add(peerName, tun)

	log.Info().Str("peer", peerName).Msg("tunnel established from incoming SSH connection")

	// Handle incoming packets from this tunnel
	if m.Forwarder != nil {
		go func(name string, t *tunnel.Tunnel) {
			m.Forwarder.HandleTunnel(ctx, name, t)
			m.tunnelMgr.RemoveIfMatch(name, t)
		}(peerName, tun)
	}
}

// ensurePeerRoute fetches peer info from coordination server and ensures the route exists.
func (m *MeshNode) ensurePeerRoute(peerName string) {
	if m.client == nil {
		return
	}

	peers, err := m.client.ListPeers()
	if err != nil {
		log.Warn().Err(err).Str("peer", peerName).Msg("failed to fetch peer info for routing")
		return
	}

	for _, peer := range peers {
		if peer.Name == peerName {
			// Add route for this peer's mesh IP
			m.router.AddRoute(peer.MeshIP, peer.Name)
			log.Debug().
				Str("peer", peer.Name).
				Str("mesh_ip", peer.MeshIP).
				Msg("route added for incoming connection")

			// Also add authorized key if we have the SSH transport
			if peer.PublicKey != "" && m.SSHTransport != nil {
				pubKey, err := config.DecodePublicKey(peer.PublicKey)
				if err != nil {
					log.Warn().Err(err).Str("peer", peer.Name).Msg("failed to decode peer public key")
				} else {
					m.SSHTransport.AddAuthorizedKey(pubKey)
				}
			}
			return
		}
	}

	log.Warn().Str("peer", peerName).Msg("peer not found on coordination server")
}
