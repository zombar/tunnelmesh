package simulator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

func TestMeshOrchestrator_JoinPeer(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
		JoinTime:   0,
		LeaveTime:  0,
	}

	ctx := context.Background()

	// Join peer
	err := orch.JoinPeer(ctx, alice)
	if err != nil {
		t.Fatalf("JoinPeer() error = %v", err)
	}

	// Verify peer state
	state, err := orch.GetPeerState(alice.ID)
	if err != nil {
		t.Fatalf("GetPeerState() error = %v", err)
	}

	if state.Status != PeerOnline {
		t.Errorf("Peer status = %v, want %v", state.Status, PeerOnline)
	}

	if state.ContainerID == "" {
		t.Error("Peer should have container ID")
	}

	if state.JoinedAt.IsZero() {
		t.Error("Peer should have join time")
	}
}

func TestMeshOrchestrator_JoinPeerAlreadyOnline(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
		JoinTime:   0,
		LeaveTime:  0,
	}

	ctx := context.Background()

	// Join peer first time
	if err := orch.JoinPeer(ctx, alice); err != nil {
		t.Fatalf("First JoinPeer() error = %v", err)
	}

	// Try to join again
	err := orch.JoinPeer(ctx, alice)
	if err == nil {
		t.Error("Expected error when joining peer that is already online")
	}
}

func TestMeshOrchestrator_LeavePeer(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	bob := story.Character{
		ID:         "bob",
		Name:       "Bob",
		Role:       "Analyst",
		Department: "intel",
		Clearance:  3,
		Alignment:  "good",
		DockerPeer: "bob",
		JoinTime:   0,
		LeaveTime:  10 * time.Hour,
	}

	ctx := context.Background()

	// Join peer
	if err := orch.JoinPeer(ctx, bob); err != nil {
		t.Fatalf("JoinPeer() error = %v", err)
	}

	// Verify online
	state, _ := orch.GetPeerState(bob.ID)
	if state.Status != PeerOnline {
		t.Errorf("Peer status after join = %v, want %v", state.Status, PeerOnline)
	}

	// Leave peer
	if err := orch.LeavePeer(ctx, bob); err != nil {
		t.Fatalf("LeavePeer() error = %v", err)
	}

	// Verify offline
	state, _ = orch.GetPeerState(bob.ID)
	if state.Status != PeerOffline {
		t.Errorf("Peer status after leave = %v, want %v", state.Status, PeerOffline)
	}

	if state.ContainerID != "" {
		t.Error("Peer should not have container ID after leaving")
	}
}

func TestMeshOrchestrator_LeaveNotFound(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	eve := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
		JoinTime:   12 * time.Hour,
		LeaveTime:  30 * time.Hour,
	}

	ctx := context.Background()

	// Try to leave peer that never joined
	err := orch.LeavePeer(ctx, eve)
	if err == nil {
		t.Error("Expected error when leaving peer that doesn't exist")
	}
}

func TestMeshOrchestrator_GetOnlinePeers(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	bob := story.Character{
		ID:         "bob",
		Name:       "Bob",
		Role:       "Analyst",
		Department: "intel",
		Clearance:  3,
		Alignment:  "good",
		DockerPeer: "bob",
	}

	eve := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
	}

	ctx := context.Background()

	// Initially no peers online
	online := orch.GetOnlinePeers()
	if len(online) != 0 {
		t.Errorf("Initial online peers = %d, want 0", len(online))
	}

	// Join alice and bob
	if err := orch.JoinPeer(ctx, alice); err != nil {
		t.Fatalf("JoinPeer(alice) error = %v", err)
	}
	if err := orch.JoinPeer(ctx, bob); err != nil {
		t.Fatalf("JoinPeer(bob) error = %v", err)
	}

	// Should have 2 online
	online = orch.GetOnlinePeers()
	if len(online) != 2 {
		t.Errorf("Online peers after join = %d, want 2", len(online))
	}

	// Eve joins
	if err := orch.JoinPeer(ctx, eve); err != nil {
		t.Fatalf("JoinPeer(eve) error = %v", err)
	}

	// Should have 3 online
	online = orch.GetOnlinePeers()
	if len(online) != 3 {
		t.Errorf("Online peers after eve joins = %d, want 3", len(online))
	}

	// Bob leaves
	if err := orch.LeavePeer(ctx, bob); err != nil {
		t.Fatalf("LeavePeer(bob) error = %v", err)
	}

	// Should have 2 online (alice and eve)
	online = orch.GetOnlinePeers()
	if len(online) != 2 {
		t.Errorf("Online peers after bob leaves = %d, want 2", len(online))
	}
}

func TestMeshOrchestrator_Stats(t *testing.T) {
	controller := NewMockMeshController()
	orch := NewMeshOrchestrator(controller, 1.0)

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	bob := story.Character{
		ID:         "bob",
		Name:       "Bob",
		Role:       "Analyst",
		Department: "intel",
		Clearance:  3,
		Alignment:  "good",
		DockerPeer: "bob",
	}

	ctx := context.Background()

	// Join alice
	if err := orch.JoinPeer(ctx, alice); err != nil {
		t.Fatalf("JoinPeer(alice) error = %v", err)
	}

	// Join bob
	if err := orch.JoinPeer(ctx, bob); err != nil {
		t.Fatalf("JoinPeer(bob) error = %v", err)
	}

	// Get stats
	stats := orch.Stats()

	// Verify counts
	if stats["total_peers"] != 2 {
		t.Errorf("Stats total_peers = %v, want 2", stats["total_peers"])
	}

	if stats["online"] != 2 {
		t.Errorf("Stats online = %v, want 2", stats["online"])
	}

	if stats["offline"] != 0 {
		t.Errorf("Stats offline = %v, want 0", stats["offline"])
	}

	// Leave alice
	if err := orch.LeavePeer(ctx, alice); err != nil {
		t.Fatalf("LeavePeer(alice) error = %v", err)
	}

	// Get updated stats
	stats = orch.Stats()

	if stats["online"] != 1 {
		t.Errorf("Stats online after leave = %v, want 1", stats["online"])
	}

	if stats["offline"] != 1 {
		t.Errorf("Stats offline after leave = %v, want 1", stats["offline"])
	}

	t.Logf("Mesh stats: %+v", stats)
}

func TestMeshOrchestrator_StartError(t *testing.T) {
	controller := NewMockMeshController()

	// Inject start error for specific character
	controller.SetStartError(func(char story.Character) error {
		if char.ID == "eve" {
			return errors.New("container start failed")
		}
		return nil
	})

	orch := NewMeshOrchestrator(controller, 1.0)

	eve := story.Character{
		ID:         "eve",
		Name:       "Eve",
		Role:       "Attacker",
		Department: "none",
		Clearance:  1,
		Alignment:  "dark",
		DockerPeer: "eve",
	}

	ctx := context.Background()

	// Try to join - should fail
	err := orch.JoinPeer(ctx, eve)
	if err == nil {
		t.Error("Expected error when container start fails")
	}

	// Verify peer is offline
	state, _ := orch.GetPeerState(eve.ID)
	if state.Status != PeerOffline {
		t.Errorf("Peer status after failed start = %v, want %v", state.Status, PeerOffline)
	}
}

func TestMeshOrchestrator_TimeScaling(t *testing.T) {
	controller := NewMockMeshController()

	testCases := []struct {
		name      string
		timeScale float64
	}{
		{"realtime", 1.0},
		{"10x", 10.0},
		{"100x", 100.0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			orch := NewMeshOrchestrator(controller, tc.timeScale)

			alice := story.Character{
				ID:         "alice",
				Name:       "Alice",
				Role:       "Commander",
				Department: "command",
				Clearance:  5,
				Alignment:  "good",
				DockerPeer: "alice",
			}

			ctx := context.Background()
			start := time.Now()

			// Join should complete faster with higher time scale
			err := orch.JoinPeer(ctx, alice)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("JoinPeer() error = %v", err)
			}

			t.Logf("Join completed in %v with timeScale %v", elapsed, tc.timeScale)

			// Verify peer is online
			state, _ := orch.GetPeerState(alice.ID)
			if state.Status != PeerOnline {
				t.Errorf("Peer status = %v, want %v", state.Status, PeerOnline)
			}
		})
	}
}

func TestMockMeshController(t *testing.T) {
	controller := NewMockMeshController()

	alice := story.Character{
		ID:         "alice",
		Name:       "Alice",
		Role:       "Commander",
		Department: "command",
		Clearance:  5,
		Alignment:  "good",
		DockerPeer: "alice",
	}

	ctx := context.Background()

	// Start peer
	containerID, err := controller.StartPeer(ctx, alice)
	if err != nil {
		t.Fatalf("StartPeer() error = %v", err)
	}

	if containerID == "" {
		t.Error("Expected container ID from StartPeer")
	}

	// Check status
	status, err := controller.GetPeerStatus(ctx, alice)
	if err != nil {
		t.Fatalf("GetPeerStatus() error = %v", err)
	}

	if status != PeerOnline {
		t.Errorf("Peer status = %v, want %v", status, PeerOnline)
	}

	// Wait for stable
	if err := controller.WaitForMeshStable(ctx, 1*time.Second); err != nil {
		t.Fatalf("WaitForMeshStable() error = %v", err)
	}

	// Stop peer
	if err := controller.StopPeer(ctx, alice); err != nil {
		t.Fatalf("StopPeer() error = %v", err)
	}

	// Check status after stop
	status, err = controller.GetPeerStatus(ctx, alice)
	if err != nil {
		t.Fatalf("GetPeerStatus() after stop error = %v", err)
	}

	if status != PeerOffline {
		t.Errorf("Peer status after stop = %v, want %v", status, PeerOffline)
	}
}
