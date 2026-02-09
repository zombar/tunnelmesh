package docker

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

// DockerStatsSnapshot represents a comprehensive snapshot of Docker state.
type DockerStatsSnapshot struct {
	Timestamp  time.Time                `json:"timestamp"`
	PeerName   string                   `json:"peer_name"`
	Containers []ContainerStatsSnapshot `json:"containers"`
	Networks   []NetworkInfo            `json:"networks"`
}

// ContainerStatsSnapshot contains full container info and stats.
type ContainerStatsSnapshot struct {
	// Full inspect data
	ContainerInfo ContainerInfo `json:"info"`

	// Runtime stats
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   uint64  `json:"memory_bytes"`
	MemoryLimit   uint64  `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	DiskBytes     uint64  `json:"disk_bytes"`
	PIDs          uint64  `json:"pids"`
}

// StartPeriodicStatsCollection starts a goroutine that collects and persists Docker stats every 30s.
func (m *Manager) StartPeriodicStatsCollection(ctx context.Context, s3Store *s3.SystemStore) {
	if s3Store == nil {
		log.Debug().Msg("S3 store not available, Docker stats persistence disabled")
		return
	}

	log.Info().
		Dur("interval", statsCollectionInterval).
		Msg("Starting Docker stats collection")

	ticker := time.NewTicker(statsCollectionInterval)
	defer ticker.Stop()

	// Collect immediately on start
	if err := m.collectAndPersistStats(ctx, s3Store); err != nil {
		log.Warn().Err(err).Msg("Initial Docker stats collection failed")
	}

	for {
		select {
		case <-ticker.C:
			if err := m.collectAndPersistStats(ctx, s3Store); err != nil {
				log.Warn().Err(err).Msg("Docker stats collection failed")
			}
		case <-ctx.Done():
			log.Info().Msg("Stopping Docker stats collection")
			return
		}
	}
}

// collectAndPersistStats collects comprehensive Docker stats and saves to S3.
func (m *Manager) collectAndPersistStats(ctx context.Context, s3Store *s3.SystemStore) error {
	if m.client == nil {
		return nil // Docker not available
	}

	snapshot := DockerStatsSnapshot{
		Timestamp:  time.Now(),
		PeerName:   m.peerName,
		Containers: []ContainerStatsSnapshot{},
		Networks:   []NetworkInfo{},
	}

	// Collect container info and stats with timeout
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	containers, err := m.ListContainers(listCtx)
	cancel()
	if err != nil {
		return err
	}

	for _, container := range containers {
		// Get full inspect data with timeout per container
		inspectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		fullInfo, err := m.InspectContainer(inspectCtx, container.ID)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("container", container.Name).Msg("Failed to inspect container")
			continue
		}

		if fullInfo == nil {
			continue
		}

		// Get runtime stats (only for running containers)
		containerSnapshot := ContainerStatsSnapshot{
			ContainerInfo: *fullInfo,
		}

		if fullInfo.State == "running" {
			// Use timeout per container to prevent hung stats calls from blocking entire collection
			statsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			stats, err := m.client.GetContainerStats(statsCtx, container.ID)
			cancel()
			if err != nil {
				log.Warn().Err(err).Str("container", container.Name).Msg("Failed to get container stats")
			} else if stats != nil {
				containerSnapshot.CPUPercent = stats.CPUPercent
				containerSnapshot.MemoryBytes = stats.MemoryBytes
				containerSnapshot.MemoryLimit = stats.MemoryLimit
				containerSnapshot.MemoryPercent = stats.MemoryPercent
				containerSnapshot.DiskBytes = stats.DiskBytes
				containerSnapshot.PIDs = stats.PIDs
			}
		}

		snapshot.Containers = append(snapshot.Containers, containerSnapshot)
	}

	// Collect Docker networks with timeout
	networksCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	networks, err := m.client.ListNetworks(networksCtx)
	cancel()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to list Docker networks")
	} else {
		snapshot.Networks = networks
	}

	// Save to S3 as stats/{peer_name}.docker.json
	path := "stats/" + m.peerName + ".docker.json"
	if err := s3Store.SaveJSON(path, snapshot); err != nil {
		return err
	}

	log.Debug().
		Str("peer", m.peerName).
		Int("containers", len(snapshot.Containers)).
		Msg("Saved Docker stats to S3")

	return nil
}
