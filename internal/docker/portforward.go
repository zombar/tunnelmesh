package docker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// syncPortForwards creates temporary filter rules for a container's published ports.
// Only applies to bridge network containers with published host ports.
func (m *Manager) syncPortForwards(ctx context.Context, containerID string) error {
	// Skip if auto port forwarding is disabled
	if m.cfg.AutoPortForward != nil && !*m.cfg.AutoPortForward {
		return nil
	}

	// Skip if no filter available
	if m.filter == nil {
		return nil
	}

	// Inspect container to get port mappings
	container, err := m.InspectContainer(ctx, containerID)
	if err != nil {
		return err
	}

	if container == nil {
		log.Warn().Str("container", containerID).Msg("Container not found for port forward sync")
		return nil
	}

	// Only forward bridge network containers
	// Host network containers already have direct access
	if container.NetworkMode != "bridge" {
		log.Debug().
			Str("container", container.Name).
			Str("network", container.NetworkMode).
			Msg("Skipping port forwards for non-bridge network")
		return nil
	}

	// Use container lifetime + 1 day buffer for TTL
	// This ensures rules persist for the container's lifetime
	ttl := 24 * time.Hour
	if container.UptimeSeconds > 0 {
		// If container has been running, extend TTL to cover expected lifetime
		ttl = 24 * time.Hour
	}
	expiresAt := time.Now().Add(ttl)

	// Create filter rules for each published port
	for _, port := range container.Ports {
		if port.HostPort == 0 {
			// Port not published to host
			continue
		}

		rule := routing.FilterRule{
			Port:       port.HostPort,
			Protocol:   parseProtocol(port.Protocol),
			Action:     routing.ActionAllow,
			Expires:    expiresAt.Unix(),
			SourcePeer: "", // Allow from any peer
		}

		m.filter.AddTemporaryRule(rule)

		log.Info().
			Str("container", container.Name).
			Uint16("port", port.HostPort).
			Str("protocol", port.Protocol).
			Str("expires", expiresAt.Format(time.RFC3339)).
			Msg("Created temporary port forward")

		// Record mapping for persistence
		if m.systemStore != nil {
			mapping := PortForwardMapping{
				ContainerID:   container.ID,
				ContainerName: container.Name,
				Port:          port.HostPort,
				Protocol:      port.Protocol,
				CreatedAt:     time.Now(),
				ExpiresAt:     expiresAt,
			}
			m.recordMapping(mapping)
		}
	}

	return nil
}

// recordMapping stores a port forward mapping for persistence.
func (m *Manager) recordMapping(mapping PortForwardMapping) {
	// TODO: Implement persistence to S3 SystemStore
	// This will be implemented when we add persistence support
	log.Debug().
		Str("container", mapping.ContainerName).
		Uint16("port", mapping.Port).
		Msg("Recorded port forward mapping")
}
