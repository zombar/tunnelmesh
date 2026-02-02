package peer

import (
	"context"
	"net"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// HandleIncomingSSH accepts incoming SSH connections on the listener.
func (m *MeshNode) HandleIncomingSSH(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("accept error")
			continue
		}

		go m.handleSSHConnection(ctx, conn)
	}
}

// handleSSHConnection handles an individual incoming SSH connection.
func (m *MeshNode) handleSSHConnection(ctx context.Context, conn net.Conn) {
	if m.SSHServer == nil {
		log.Warn().Str("remote", conn.RemoteAddr().String()).Msg("SSH server not configured, rejecting connection")
		conn.Close()
		return
	}

	sshConn, err := m.SSHServer.Accept(conn)
	if err != nil {
		// If handshake failed, try refreshing authorized keys and log for retry
		log.Warn().Err(err).Str("remote", conn.RemoteAddr().String()).Msg("SSH handshake failed, refreshing peer keys")
		m.RefreshAuthorizedKeys()
		conn.Close()
		return
	}

	log.Info().
		Str("remote", conn.RemoteAddr().String()).
		Str("user", sshConn.Conn.User()).
		Msg("incoming SSH connection")

	// Handle channels
	for newChannel := range sshConn.Channels {
		if newChannel.ChannelType() != tunnel.ChannelType {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, _, err := newChannel.Accept()
		if err != nil {
			log.Warn().Err(err).Msg("failed to accept channel")
			continue
		}

		// Get peer name from extra data (sent by client when opening channel)
		peerName := string(newChannel.ExtraData())
		if peerName == "" {
			peerName = sshConn.Conn.User() // Fallback to user name
		}

		tun := tunnel.NewTunnel(channel, peerName)

		// Add to tunnel manager
		m.tunnelMgr.Add(peerName, tun)

		log.Info().Str("peer", peerName).Msg("tunnel established from incoming connection")

		// Handle incoming packets from this tunnel
		if m.Forwarder != nil {
			go func(name string, t *tunnel.Tunnel) {
				m.Forwarder.HandleTunnel(ctx, name, t)
				m.tunnelMgr.RemoveIfMatch(name, t)
			}(peerName, tun)
		}
	}
}
