package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

// realDockerClient wraps the official Docker SDK client.
type realDockerClient struct {
	cli *client.Client
}

// newRealDockerClient creates a new Docker client connected to the given socket.
func newRealDockerClient(socket string) (*realDockerClient, error) {
	opts := []client.Opt{
		client.WithHost(socket),
		client.WithAPIVersionNegotiation(),
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}

	return &realDockerClient{cli: cli}, nil
}

// ListContainers lists all containers (running and stopped).
func (c *realDockerClient) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	result := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		info := convertContainer(c)
		result = append(result, info)
	}

	return result, nil
}

// InspectContainer returns detailed information about a specific container.
func (c *realDockerClient) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	info := convertInspect(inspect)
	return &info, nil
}

// WatchEvents streams Docker events and calls the handler for each event.
// Blocks until context is cancelled.
func (c *realDockerClient) WatchEvents(ctx context.Context, handler func(ContainerEvent)) error {
	eventChan, errChan := c.cli.Events(ctx, events.ListOptions{})

	for {
		select {
		case event := <-eventChan:
			if event.Type == events.ContainerEventType {
				handler(ContainerEvent{
					Type:        string(event.Action),
					ContainerID: event.Actor.ID,
				})
			}
		case err := <-errChan:
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			log.Error().Err(err).Msg("Docker events stream error")
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// GetContainerStats returns resource usage statistics for a container.
func (c *realDockerClient) GetContainerStats(ctx context.Context, id string) (*ContainerStats, error) {
	stats, err := c.cli.ContainerStats(ctx, id, false) // false = one-shot, not streaming
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := stats.Body.Close(); err != nil {
			log.Warn().Err(err).Msg("Failed to close stats body")
		}
	}()

	var v container.StatsResponse
	if err := json.NewDecoder(stats.Body).Decode(&v); err != nil {
		return nil, err
	}

	// Calculate CPU percentage
	cpuPercent := calculateCPUPercent(&v)

	// Calculate memory percentage
	memPercent := 0.0
	if v.MemoryStats.Limit > 0 {
		memPercent = float64(v.MemoryStats.Usage) / float64(v.MemoryStats.Limit) * 100.0
	}

	// Get disk usage (sum of all storage layers)
	diskBytes := v.StorageStats.ReadSizeBytes

	return &ContainerStats{
		ContainerID:   v.ID,
		ContainerName: v.Name,
		Timestamp:     v.Read,
		CPUPercent:    cpuPercent,
		MemoryBytes:   v.MemoryStats.Usage,
		MemoryLimit:   v.MemoryStats.Limit,
		MemoryPercent: memPercent,
		DiskBytes:     diskBytes,
		PIDs:          v.PidsStats.Current,
	}, nil
}

// calculateCPUPercent calculates CPU usage percentage from stats.
func calculateCPUPercent(stats *container.StatsResponse) float64 {
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		return (cpuDelta / systemDelta) * float64(stats.CPUStats.OnlineCPUs) * 100.0
	}
	return 0.0
}

// ListNetworks returns all Docker networks.
func (c *realDockerClient) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	networks, err := c.cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}

	result := make([]NetworkInfo, 0, len(networks))
	for _, n := range networks {
		containerIDs := make([]string, 0, len(n.Containers))
		for id := range n.Containers {
			containerIDs = append(containerIDs, id)
		}

		result = append(result, NetworkInfo{
			ID:         n.ID,
			Name:       n.Name,
			Driver:     n.Driver,
			Scope:      n.Scope,
			Internal:   n.Internal,
			Containers: containerIDs,
			Labels:     n.Labels,
		})
	}

	return result, nil
}

// Close closes the Docker client connection.
func (c *realDockerClient) Close() error {
	return c.cli.Close()
}

// convertContainer converts Docker SDK container summary to our ContainerInfo.
func convertContainer(c container.Summary) ContainerInfo {
	info := ContainerInfo{
		ID:      c.ID,
		ShortID: shortID(c.ID),
		Image:   c.Image,
		Status:  c.Status,
		State:   c.State,
		Labels:  c.Labels,
		Ports:   make([]PortBinding, 0, len(c.Ports)),
	}

	// Remove leading slash from name
	if len(c.Names) > 0 {
		name := c.Names[0]
		if len(name) > 0 && name[0] == '/' {
			info.Name = name[1:]
		} else {
			info.Name = name
		}
	}

	// Parse network mode from HostConfig
	if c.HostConfig.NetworkMode != "" {
		info.NetworkMode = c.HostConfig.NetworkMode
	}

	// Convert ports
	for _, port := range c.Ports {
		binding := PortBinding{
			ContainerPort: port.PrivatePort,
			HostPort:      port.PublicPort,
			Protocol:      port.Type,
		}
		info.Ports = append(info.Ports, binding)
	}

	// Parse timestamps
	info.CreatedAt = parseDockerTimestamp(c.Created)
	// StartedAt not available in container list, will be populated in inspect

	return info
}

// convertInspect converts Docker SDK inspect response to our ContainerInfo.
func convertInspect(inspect container.InspectResponse) ContainerInfo {
	info := ContainerInfo{
		ID:      inspect.ID,
		ShortID: shortID(inspect.ID),
		Image:   inspect.Config.Image,
		Status:  inspect.State.Status,
		State:   inspect.State.Status,
		Labels:  inspect.Config.Labels,
		Ports:   make([]PortBinding, 0),
	}

	// Remove leading slash from name
	if len(inspect.Name) > 0 && inspect.Name[0] == '/' {
		info.Name = inspect.Name[1:]
	} else {
		info.Name = inspect.Name
	}

	// Network mode
	info.NetworkMode = string(inspect.HostConfig.NetworkMode)

	// Timestamps
	info.CreatedAt = parseDockerTime(inspect.Created)
	info.StartedAt = parseDockerTime(inspect.State.StartedAt)

	// Convert port bindings
	if inspect.NetworkSettings != nil && inspect.NetworkSettings.Ports != nil {
		for portProto, bindings := range inspect.NetworkSettings.Ports {
			for _, binding := range bindings {
				port, proto := parsePortProto(string(portProto))
				hostPort := parseHostPort(binding.HostPort)

				info.Ports = append(info.Ports, PortBinding{
					ContainerPort: port,
					HostPort:      hostPort,
					Protocol:      proto,
				})
			}
		}
	}

	return info
}
