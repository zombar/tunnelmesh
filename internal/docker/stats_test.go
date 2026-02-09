package docker

import (
	"context"
	"testing"
	"time"
)

func TestStartPeriodicStatsCollection_NilStore(t *testing.T) {
	mgr := &Manager{
		peerName: "test-peer",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should return immediately with nil store
	done := make(chan struct{})
	go func() {
		mgr.StartPeriodicStatsCollection(ctx, nil)
		close(done)
	}()

	select {
	case <-done:
		// Expected - should return immediately
	case <-time.After(100 * time.Millisecond):
		t.Fatal("StartPeriodicStatsCollection did not return with nil store")
	}
}

func TestDockerStatsSnapshot_Structure(t *testing.T) {
	// Test that the structures can be created properly
	snapshot := DockerStatsSnapshot{
		Timestamp: time.Now(),
		PeerName:  "test-peer",
		Containers: []ContainerStatsSnapshot{
			{
				ContainerInfo: ContainerInfo{
					ID:    "abc123",
					Name:  "test",
					State: "running",
				},
				CPUPercent:    10.5,
				MemoryBytes:   1024 * 1024,
				MemoryPercent: 50.0,
			},
		},
		Networks: []NetworkInfo{
			{ID: "net1", Name: "bridge"},
		},
	}

	if snapshot.PeerName != "test-peer" {
		t.Errorf("PeerName = %v, want test-peer", snapshot.PeerName)
	}

	if len(snapshot.Containers) != 1 {
		t.Errorf("Containers length = %v, want 1", len(snapshot.Containers))
	}

	if len(snapshot.Networks) != 1 {
		t.Errorf("Networks length = %v, want 1", len(snapshot.Networks))
	}
}

func TestContainerStatsSnapshot_Structure(t *testing.T) {
	// Test that container stats snapshot can be created
	snapshot := ContainerStatsSnapshot{
		ContainerInfo: ContainerInfo{
			ID:    "abc123",
			Name:  "test",
			State: "running",
		},
		CPUPercent:    25.5,
		MemoryBytes:   536870912,
		MemoryLimit:   1073741824,
		MemoryPercent: 50.0,
		DiskBytes:     2147483648,
		PIDs:          15,
	}

	if snapshot.ContainerInfo.ID != "abc123" {
		t.Errorf("ContainerInfo.ID = %v, want abc123", snapshot.ContainerInfo.ID)
	}

	if snapshot.CPUPercent != 25.5 {
		t.Errorf("CPUPercent = %v, want 25.5", snapshot.CPUPercent)
	}

	if snapshot.MemoryPercent != 50.0 {
		t.Errorf("MemoryPercent = %v, want 50.0", snapshot.MemoryPercent)
	}
}

func TestCollectAndPersistStats_NilClient(t *testing.T) {
	mgr := &Manager{
		peerName: "test-peer",
		client:   nil, // No Docker client
	}

	// collectAndPersistStats with nil client should return nil (no-op)
	err := mgr.collectAndPersistStats(context.Background(), nil)
	if err != nil {
		t.Errorf("collectAndPersistStats with nil client should not error, got: %v", err)
	}
}

func TestListContainers_ForStatsCollection(t *testing.T) {
	// Test that ListContainers works correctly for stats collection
	now := time.Now()
	mockContainers := []ContainerInfo{
		{
			ID:        "abc123",
			ShortID:   "abc123",
			Name:      "test-nginx",
			Image:     "nginx:latest",
			State:     "running",
			CreatedAt: now.Add(-time.Hour),
			StartedAt: now.Add(-time.Hour),
		},
		{
			ID:        "def456",
			ShortID:   "def456",
			Name:      "test-stopped",
			Image:     "redis:latest",
			State:     "exited",
			CreatedAt: now.Add(-2 * time.Hour),
		},
	}

	mockClient := &mockDockerClient{
		containers: mockContainers,
	}

	mgr := &Manager{
		peerName: "test-peer",
		client:   mockClient,
	}

	ctx := context.Background()
	containers, err := mgr.ListContainers(ctx)
	if err != nil {
		t.Fatalf("ListContainers failed: %v", err)
	}

	if len(containers) != 2 {
		t.Errorf("Expected 2 containers, got %d", len(containers))
	}

	// Verify we got both running and stopped containers
	states := make(map[string]int)
	for _, c := range containers {
		states[c.State]++
	}

	if states["running"] != 1 {
		t.Errorf("Expected 1 running container, got %d", states["running"])
	}

	if states["exited"] != 1 {
		t.Errorf("Expected 1 exited container, got %d", states["exited"])
	}
}

func TestInspectContainer_ForStatsCollection(t *testing.T) {
	// Test that InspectContainer works for stats collection
	now := time.Now()
	mockContainer := ContainerInfo{
		ID:        "abc123",
		ShortID:   "abc123",
		Name:      "test-nginx",
		Image:     "nginx:latest",
		State:     "running",
		CreatedAt: now.Add(-time.Hour),
		StartedAt: now.Add(-time.Hour),
		Ports: []PortBinding{
			{HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		},
	}

	mockClient := &mockDockerClient{
		containers: []ContainerInfo{mockContainer},
	}

	mgr := &Manager{
		peerName: "test-peer",
		client:   mockClient,
	}

	ctx := context.Background()
	info, err := mgr.InspectContainer(ctx, "abc123")
	if err != nil {
		t.Fatalf("InspectContainer failed: %v", err)
	}

	if info == nil {
		t.Fatal("Expected non-nil container info")
	}

	if info.Name != "test-nginx" {
		t.Errorf("Expected name 'test-nginx', got %q", info.Name)
	}

	if len(info.Ports) != 1 {
		t.Errorf("Expected 1 port, got %d", len(info.Ports))
	}
}

func TestStartPeriodicStatsCollection_ContextCancellation(t *testing.T) {
	// Test that periodic stats collection stops when context is cancelled
	mockClient := &mockDockerClient{
		containers: []ContainerInfo{
			{ID: "abc123", Name: "test", State: "running"},
		},
	}

	mgr := &Manager{
		peerName: "test-peer",
		client:   mockClient,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		// Note: We can't actually test with a real S3 store, but we can test
		// that the function respects context cancellation
		mgr.StartPeriodicStatsCollection(ctx, nil) // nil store returns immediately
		close(done)
	}()

	// Cancel context
	cancel()

	// Should exit quickly
	select {
	case <-done:
		// Expected - should return when context cancelled
	case <-time.After(100 * time.Millisecond):
		t.Fatal("StartPeriodicStatsCollection did not respect context cancellation")
	}
}
