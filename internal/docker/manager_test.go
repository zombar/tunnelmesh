package docker

import (
	"context"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// mockDockerClient is a mock implementation for testing.
type mockDockerClient struct {
	containers []ContainerInfo
	err        error
}

func (m *mockDockerClient) ListContainers(ctx context.Context) ([]ContainerInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.containers, nil
}

func (m *mockDockerClient) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, c := range m.containers {
		if c.ID == id || c.ShortID == id {
			return &c, nil
		}
	}
	return nil, nil
}

func (m *mockDockerClient) GetContainerStats(ctx context.Context, id string) (*ContainerStats, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return mock stats for testing
	return &ContainerStats{
		ContainerID:   id,
		ContainerName: "test",
		Timestamp:     time.Now(),
		CPUPercent:    25.5,
		MemoryBytes:   268435456,
		MemoryLimit:   536870912,
		MemoryPercent: 50.0,
		DiskBytes:     1073741824,
		PIDs:          10,
	}, nil
}

func (m *mockDockerClient) ListNetworks(ctx context.Context) ([]NetworkInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return mock networks for testing
	return []NetworkInfo{
		{
			ID:       "net1",
			Name:     "bridge",
			Driver:   "bridge",
			Scope:    "local",
			Internal: false,
		},
	}, nil
}

func (m *mockDockerClient) WatchEvents(ctx context.Context, handler func(ContainerEvent)) error {
	// Mock implementation - just block until context cancelled
	<-ctx.Done()
	return ctx.Err()
}

func (m *mockDockerClient) Close() error {
	return m.err
}

func TestNewManager(t *testing.T) {
	autoForward := true
	cfg := &config.DockerConfig{
		Socket:          "unix:///var/run/docker.sock",
		AutoPortForward: &autoForward,
	}

	mgr := NewManager(cfg, "test-peer", nil, nil)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}

	if mgr.peerName != "test-peer" {
		t.Errorf("expected peerName 'test-peer', got %q", mgr.peerName)
	}

	if mgr.cfg != cfg {
		t.Error("config not set correctly")
	}
}

func TestListContainers(t *testing.T) {
	now := time.Now()
	mockContainers := []ContainerInfo{
		{
			ID:        "abc123def456",
			ShortID:   "abc123",
			Name:      "test-nginx",
			Image:     "nginx:latest",
			Status:    "running",
			State:     "running",
			CreatedAt: now.Add(-time.Hour),
			StartedAt: now.Add(-time.Hour),
			Ports: []PortBinding{
				{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
			},
			NetworkMode: "bridge",
			Labels:      map[string]string{"app": "web"},
		},
		{
			ID:          "xyz789",
			ShortID:     "xyz789",
			Name:        "test-redis",
			Image:       "redis:latest",
			Status:      "running",
			State:       "running",
			CreatedAt:   now.Add(-2 * time.Hour),
			StartedAt:   now.Add(-2 * time.Hour),
			Ports:       []PortBinding{{ContainerPort: 6379, HostPort: 6379, Protocol: "tcp"}},
			NetworkMode: "bridge",
			Labels:      map[string]string{"app": "cache"},
		},
	}

	mock := &mockDockerClient{containers: mockContainers}
	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)
	mgr.client = mock

	ctx := context.Background()
	containers, err := mgr.ListContainers(ctx)
	if err != nil {
		t.Fatalf("ListContainers failed: %v", err)
	}

	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	if containers[0].Name != "test-nginx" {
		t.Errorf("expected first container name 'test-nginx', got %q", containers[0].Name)
	}

	if containers[1].Name != "test-redis" {
		t.Errorf("expected second container name 'test-redis', got %q", containers[1].Name)
	}
}

func TestListContainersEmpty(t *testing.T) {
	mock := &mockDockerClient{containers: []ContainerInfo{}}
	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)
	mgr.client = mock

	ctx := context.Background()
	containers, err := mgr.ListContainers(ctx)
	if err != nil {
		t.Fatalf("ListContainers failed: %v", err)
	}

	if len(containers) != 0 {
		t.Fatalf("expected 0 containers, got %d", len(containers))
	}
}

func TestInspectContainer(t *testing.T) {
	now := time.Now()
	mockContainer := ContainerInfo{
		ID:        "abc123def456",
		ShortID:   "abc123",
		Name:      "test-nginx",
		Image:     "nginx:latest",
		Status:    "running",
		State:     "running",
		CreatedAt: now.Add(-time.Hour),
		StartedAt: now.Add(-time.Hour),
		Ports: []PortBinding{
			{ContainerPort: 80, HostPort: 8080, Protocol: "tcp"},
		},
		NetworkMode: "bridge",
		Labels:      map[string]string{"app": "web"},
	}

	mock := &mockDockerClient{containers: []ContainerInfo{mockContainer}}
	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)
	mgr.client = mock

	ctx := context.Background()

	// Test with full ID
	container, err := mgr.InspectContainer(ctx, "abc123def456")
	if err != nil {
		t.Fatalf("InspectContainer failed: %v", err)
	}
	if container == nil {
		t.Fatal("InspectContainer returned nil")
	}
	if container.Name != "test-nginx" {
		t.Errorf("expected name 'test-nginx', got %q", container.Name)
	}

	// Test with short ID
	container, err = mgr.InspectContainer(ctx, "abc123")
	if err != nil {
		t.Fatalf("InspectContainer with short ID failed: %v", err)
	}
	if container == nil {
		t.Fatal("InspectContainer with short ID returned nil")
	}
	if container.Name != "test-nginx" {
		t.Errorf("expected name 'test-nginx', got %q", container.Name)
	}
}

func TestInspectContainerNotFound(t *testing.T) {
	mock := &mockDockerClient{containers: []ContainerInfo{}}
	cfg := &config.DockerConfig{Socket: "unix:///var/run/docker.sock"}
	mgr := NewManager(cfg, "test-peer", nil, nil)
	mgr.client = mock

	ctx := context.Background()
	container, err := mgr.InspectContainer(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("InspectContainer failed: %v", err)
	}
	if container != nil {
		t.Error("expected nil for non-existent container")
	}
}

func TestCalculateUptime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		started  time.Time
		expected int64
	}{
		{
			name:     "1 hour ago",
			started:  now.Add(-time.Hour),
			expected: 3600,
		},
		{
			name:     "30 minutes ago",
			started:  now.Add(-30 * time.Minute),
			expected: 1800,
		},
		{
			name:     "just started",
			started:  now,
			expected: 0,
		},
		{
			name:     "24 hours ago",
			started:  now.Add(-24 * time.Hour),
			expected: 86400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uptime := calculateUptime(tt.started)
			// Allow 1 second tolerance for test execution time
			if uptime < tt.expected-1 || uptime > tt.expected+1 {
				t.Errorf("expected uptime ~%d, got %d", tt.expected, uptime)
			}
		})
	}
}

func TestShortID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected string
	}{
		{
			name:     "full docker ID",
			id:       "abc123def456789012345678901234567890123456789012345678901234",
			expected: "abc123def456",
		},
		{
			name:     "already short",
			id:       "abc123",
			expected: "abc123",
		},
		{
			name:     "empty",
			id:       "",
			expected: "",
		},
		{
			name:     "12 chars",
			id:       "abc123def456",
			expected: "abc123def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shortID(tt.id)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetComposeStack(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name:     "compose project",
			labels:   map[string]string{"com.docker.compose.project": "myapp"},
			expected: "myapp",
		},
		{
			name:     "no compose labels",
			labels:   map[string]string{"app": "web"},
			expected: "",
		},
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: "",
		},
		{
			name:     "nil labels",
			labels:   nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getComposeStack(tt.labels)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGetComposeService(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name:     "compose service",
			labels:   map[string]string{"com.docker.compose.service": "web"},
			expected: "web",
		},
		{
			name:     "no compose labels",
			labels:   map[string]string{"app": "web"},
			expected: "",
		},
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getComposeService(tt.labels)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
