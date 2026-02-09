// Package docker provides Docker container orchestration for TunnelMesh peers.
package docker

import (
	"time"
)

// ContainerInfo represents basic container information.
type ContainerInfo struct {
	ID            string            `json:"id"`
	ShortID       string            `json:"short_id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Status        string            `json:"status"` // "running", "exited", "paused", etc.
	State         string            `json:"state"`  // "running", "exited", "paused", etc.
	CreatedAt     time.Time         `json:"created_at"`
	StartedAt     time.Time         `json:"started_at"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Ports         []PortBinding     `json:"ports"`
	NetworkMode   string            `json:"network_mode"` // "bridge", "host", "none", etc.
	Labels        map[string]string `json:"labels"`
	// Resource usage stats (populated for running containers)
	CPUPercent    float64 `json:"cpu_percent,omitempty"`    // CPU usage percentage
	MemoryBytes   uint64  `json:"memory_bytes,omitempty"`   // Current memory usage in bytes
	MemoryPercent float64 `json:"memory_percent,omitempty"` // Memory usage percentage
	DiskBytes     uint64  `json:"disk_bytes,omitempty"`     // Disk space used by container
}

// PortBinding represents a container port binding to the host.
type PortBinding struct {
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Protocol      string `json:"protocol"` // "tcp" or "udp"
	HostIP        string `json:"host_ip"`  // Usually empty or "0.0.0.0"
}

// ContainerStats represents container resource usage statistics.
type ContainerStats struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    float64   `json:"cpu_percent"`    // CPU usage percentage (0-100+ for multi-core)
	MemoryBytes   uint64    `json:"memory_bytes"`   // Current memory usage in bytes
	MemoryLimit   uint64    `json:"memory_limit"`   // Memory limit in bytes
	MemoryPercent float64   `json:"memory_percent"` // Memory usage percentage
	DiskBytes     uint64    `json:"disk_bytes"`     // Disk space used by container
	PIDs          uint64    `json:"pids"`           // Number of processes
}

// ContainerEvent represents a Docker container lifecycle event.
type ContainerEvent struct {
	Type        string // "start", "stop", "die", "destroy"
	ContainerID string
	Timestamp   time.Time
}

// NetworkInfo represents Docker network information.
type NetworkInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Internal   bool              `json:"internal"`
	Containers []string          `json:"containers"` // Container IDs
	Labels     map[string]string `json:"labels"`
}
