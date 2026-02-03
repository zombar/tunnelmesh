package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/transport"
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

		go m.handleIncomingConnection(ctx, conn, "SSH")
	}
}

