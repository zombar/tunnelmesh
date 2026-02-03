package peer

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/netmon"
)

// RunNetworkMonitor monitors for network changes and handles them.
func (m *MeshNode) RunNetworkMonitor(ctx context.Context, events <-chan netmon.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			m.HandleNetworkChange(event)
		}
	}
}

// HandleNetworkChange handles a network change event by re-registering with
// the coordination server and triggering peer discovery.
func (m *MeshNode) HandleNetworkChange(event netmon.Event) {
	log.Info().
		Str("type", event.Type.String()).
		Str("interface", event.Interface).
		Msg("network change detected")

	// Clear cached network state in transports (e.g., STUN-discovered addresses)
	if m.TransportRegistry != nil {
		m.TransportRegistry.ClearNetworkState()
	}

	log.Debug().Msg("getting local IPs...")

	// Get new IP addresses, excluding mesh network IPs
	publicIPs, privateIPs, behindNAT := m.identity.GetLocalIPs()
	log.Debug().
		Strs("public", publicIPs).
		Strs("private", privateIPs).
		Bool("behind_nat", behindNAT).
		Msg("updated local IPs")

	// Close stale HTTP connections from the old network before re-registering
	m.client.CloseIdleConnections()

	// Re-register with coordination server
	resp, err := m.client.Register(
		m.identity.Name, m.identity.PubKeyEncoded,
		publicIPs, privateIPs, m.identity.SSHPort, m.identity.UDPPort, behindNAT, m.identity.Version,
	)
	if err != nil {
		log.Error().Err(err).Msg("failed to re-register after network change")
		// Still disconnect all peers and record change even if re-register fails
		// Use DisconnectAll to properly transition FSM states and trigger observers
		m.Connections.DisconnectAll("network change (re-register failed)")
		// Also close any orphaned tunnels not tracked by FSM (belt and suspenders)
		m.tunnelMgr.CloseAll()
		log.Debug().Msg("disconnected all peers after network change")
		m.RecordNetworkChange()
		// Reconnect persistent relay even if re-register failed
		if m.PersistentRelay != nil {
			go m.ReconnectPersistentRelay(context.Background())
		}
		m.TriggerDiscovery()
		return
	}

	log.Info().
		Str("mesh_ip", resp.MeshIP).
		Msg("re-registered with coordination server")

	// Disconnect all peers (they may be using stale IPs)
	// Use DisconnectAll to properly transition FSM states and trigger observers
	m.Connections.DisconnectAll("network change")
	// Also close any orphaned tunnels not tracked by FSM (belt and suspenders)
	m.tunnelMgr.CloseAll()
	log.Debug().Msg("disconnected all peers after network change")

	// Record network change time for bypass window
	m.RecordNetworkChange()

	// Reconnect persistent relay (non-blocking)
	if m.PersistentRelay != nil {
		go m.ReconnectPersistentRelay(context.Background())
	}

	// Trigger immediate peer discovery
	m.TriggerDiscovery()
}
