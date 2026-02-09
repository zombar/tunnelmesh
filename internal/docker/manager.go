package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
)

// packetFilter is an interface for packet filtering to enable testing.
type packetFilter interface {
	AddTemporaryRule(rule routing.FilterRule)
}

// dockerClient is an interface for Docker operations to enable testing.
type dockerClient interface {
	ListContainers(ctx context.Context) ([]ContainerInfo, error)
	InspectContainer(ctx context.Context, id string) (*ContainerInfo, error)
	WatchEvents(ctx context.Context, handler func(ContainerEvent)) error
	Close() error
}

// Manager manages Docker container integration for a TunnelMesh peer.
type Manager struct {
	cfg         *config.DockerConfig
	peerName    string
	client      dockerClient
	filter      packetFilter
	systemStore *s3.SystemStore
}

// NewManager creates a new Docker manager.
// If filter is nil, port forwarding will be disabled.
// If systemStore is nil, port forward mappings will not be persisted.
func NewManager(cfg *config.DockerConfig, peerName string, filter packetFilter, systemStore *s3.SystemStore) *Manager {
	return &Manager{
		cfg:         cfg,
		peerName:    peerName,
		filter:      filter,
		systemStore: systemStore,
	}
}

// Start initializes the Docker manager and connects to the Docker daemon.
// Returns error if connection fails.
func (m *Manager) Start(ctx context.Context) error {
	// Check if Docker socket exists
	if !m.isDockerAvailable() {
		log.Debug().
			Str("socket", m.cfg.Socket).
			Str("peer", m.peerName).
			Msg("Docker socket not found, Docker integration disabled")
		return nil
	}

	log.Info().
		Str("socket", m.cfg.Socket).
		Str("peer", m.peerName).
		Msg("Starting Docker manager")

	// Create Docker client
	client, err := newRealDockerClient(m.cfg.Socket)
	if err != nil {
		log.Error().Err(err).
			Str("socket", m.cfg.Socket).
			Msg("Failed to connect to Docker daemon")
		return err
	}
	m.client = client

	// Sync port forwards for existing containers
	if err := m.syncAllPortForwards(ctx); err != nil {
		log.Warn().Err(err).Msg("Failed to sync initial port forwards")
	}

	// Start event watcher in background
	go func() {
		if err := m.watchEvents(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error().Err(err).Msg("Docker event watcher stopped with error")
		}
	}()

	return nil
}

// syncAllPortForwards syncs port forwards for all running containers.
func (m *Manager) syncAllPortForwards(ctx context.Context) error {
	containers, err := m.ListContainers(ctx)
	if err != nil {
		return err
	}

	for _, container := range containers {
		if container.State == "running" {
			if err := m.syncPortForwards(ctx, container.ID); err != nil {
				log.Warn().Err(err).
					Str("container", container.Name).
					Msg("Failed to sync port forwards for container")
			}
		}
	}

	log.Info().Int("count", len(containers)).Msg("Synced port forwards for existing containers")
	return nil
}

// isDockerAvailable checks if Docker socket exists.
func (m *Manager) isDockerAvailable() bool {
	// For Unix sockets, check file existence
	if strings.HasPrefix(m.cfg.Socket, "unix://") {
		socketPath := strings.TrimPrefix(m.cfg.Socket, "unix://")
		if _, err := os.Stat(socketPath); err == nil {
			return true
		}
	}
	// For TCP sockets, assume available (will fail on connect if not)
	if strings.HasPrefix(m.cfg.Socket, "tcp://") {
		return true
	}
	return false
}

// Stop gracefully stops the Docker manager.
func (m *Manager) Stop() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// ListContainers returns all containers on this peer.
func (m *Manager) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	if m.client == nil {
		return []ContainerInfo{}, nil
	}

	containers, err := m.client.ListContainers(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list Docker containers")
		return nil, err
	}

	// Calculate uptime for running containers
	for i := range containers {
		if containers[i].State == "running" && !containers[i].StartedAt.IsZero() {
			containers[i].UptimeSeconds = calculateUptime(containers[i].StartedAt)
		}
		// Set short ID
		if containers[i].ShortID == "" {
			containers[i].ShortID = shortID(containers[i].ID)
		}
	}

	return containers, nil
}

// InspectContainer returns detailed information about a specific container.
// Returns nil if container not found.
func (m *Manager) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	if m.client == nil {
		return nil, nil
	}

	container, err := m.client.InspectContainer(ctx, id)
	if err != nil {
		log.Error().Err(err).Str("id", id).Msg("Failed to inspect Docker container")
		return nil, err
	}

	if container != nil {
		// Calculate uptime
		if container.State == "running" && !container.StartedAt.IsZero() {
			container.UptimeSeconds = calculateUptime(container.StartedAt)
		}
		// Set short ID
		if container.ShortID == "" {
			container.ShortID = shortID(container.ID)
		}
	}

	return container, nil
}

// calculateUptime returns the number of seconds since the container started.
func calculateUptime(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return int64(time.Since(startedAt).Seconds())
}

// shortID returns the first 12 characters of a Docker ID.
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// getComposeStack extracts the Docker Compose project name from labels.
func getComposeStack(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	return labels["com.docker.compose.project"]
}

// getComposeService extracts the Docker Compose service name from labels.
func getComposeService(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	return labels["com.docker.compose.service"]
}

// parseProtocol converts protocol string to number for filter rules.
func parseProtocol(proto string) uint8 {
	proto = strings.ToLower(proto)
	switch proto {
	case "tcp":
		return routing.ProtoTCP
	case "udp":
		return routing.ProtoUDP
	default:
		return routing.ProtoTCP // Default to TCP
	}
}
