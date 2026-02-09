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
