package docker

import (
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestInitMetrics(t *testing.T) {
	metrics := initMetrics("test-peer")
	if metrics == nil {
		t.Fatal("initMetrics returned nil")
	}

	if metrics.cpuPercent == nil {
		t.Error("cpuPercent metric not initialized")
	}
	if metrics.memoryBytes == nil {
		t.Error("memoryBytes metric not initialized")
	}
	if metrics.memoryLimit == nil {
		t.Error("memoryLimit metric not initialized")
	}
	if metrics.memoryPercent == nil {
		t.Error("memoryPercent metric not initialized")
	}
	if metrics.diskBytes == nil {
		t.Error("diskBytes metric not initialized")
	}
	if metrics.pids == nil {
		t.Error("pids metric not initialized")
	}
	if metrics.containerInfo == nil {
		t.Error("containerInfo metric not initialized")
	}
	if metrics.containerStatus == nil {
		t.Error("containerStatus metric not initialized")
	}
}

func TestRecordStats(t *testing.T) {
	initMetrics("test-peer")

	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)

	stats := ContainerStats{
		ContainerID:   "abc123",
		ContainerName: "test-nginx",
		Timestamp:     time.Now(),
		CPUPercent:    25.5,
		MemoryBytes:   268435456,
		MemoryLimit:   536870912,
		MemoryPercent: 50.0,
		DiskBytes:     1073741824,
		PIDs:          24,
	}

	container := &ContainerInfo{
		ID:    "abc123",
		Name:  "test-nginx",
		Image: "nginx:latest",
	}

	// Should not panic
	mgr.recordStats(stats, container)
}

func TestRecordContainerInfo(t *testing.T) {
	initMetrics("test-peer")

	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)

	container := &ContainerInfo{
		ID:          "abc123",
		Name:        "test-nginx",
		Image:       "nginx:latest",
		Status:      "running",
		State:       "running",
		NetworkMode: "bridge",
	}

	// Should not panic
	mgr.recordContainerInfo(container)
}
