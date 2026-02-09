package simulator

import (
	"context"
	"fmt"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/s3bench/story"
)

// PeerStatus represents the state of a mesh peer.
type PeerStatus string

const (
	PeerOffline PeerStatus = "offline" // Container stopped
	PeerJoining PeerStatus = "joining" // Container starting, not yet stable
	PeerOnline  PeerStatus = "online"  // Container running, mesh stable
	PeerLeaving PeerStatus = "leaving" // Container stopping
)

// PeerState tracks the state of a mesh peer.
type PeerState struct {
	Character   story.Character // The character this peer represents
	ContainerID string          // Docker container ID
	Status      PeerStatus      // Current status
	JoinedAt    time.Time       // When the peer joined the mesh
	LastSeen    time.Time       // Last health check time
}

// MeshController is an interface for controlling mesh peer lifecycle.
// This allows for different implementations (Docker, mock, etc.)
type MeshController interface {
	// StartPeer starts a Docker container for the given character.
	// Returns the container ID and any error.
	StartPeer(ctx context.Context, character story.Character) (string, error)

	// StopPeer stops the Docker container for the given character.
	StopPeer(ctx context.Context, character story.Character) error

	// GetPeerStatus checks if a peer's container is running.
	GetPeerStatus(ctx context.Context, character story.Character) (PeerStatus, error)

	// WaitForMeshStable waits for the mesh to stabilize after peer changes.
	// Returns when all online peers have stable routes, or timeout.
	WaitForMeshStable(ctx context.Context, timeout time.Duration) error
}

// MeshOrchestrator manages peer join/leave lifecycle based on story timeline.
type MeshOrchestrator struct {
	controller MeshController
	peers      map[string]*PeerState // Map of character ID to peer state
	timeScale  float64
}

// NewMeshOrchestrator creates a new mesh orchestrator.
func NewMeshOrchestrator(controller MeshController, timeScale float64) *MeshOrchestrator {
	return &MeshOrchestrator{
		controller: controller,
		peers:      make(map[string]*PeerState),
		timeScale:  timeScale,
	}
}

// JoinPeer brings a peer online at the specified story time.
func (m *MeshOrchestrator) JoinPeer(ctx context.Context, character story.Character) error {
	// Check if already online
	if state, exists := m.peers[character.ID]; exists && state.Status == PeerOnline {
		return fmt.Errorf("peer %s already online", character.ID)
	}

	// Mark as joining
	m.peers[character.ID] = &PeerState{
		Character: character,
		Status:    PeerJoining,
	}

	// Start Docker container
	containerID, err := m.controller.StartPeer(ctx, character)
	if err != nil {
		m.peers[character.ID].Status = PeerOffline
		return fmt.Errorf("starting peer %s: %w", character.ID, err)
	}

	m.peers[character.ID].ContainerID = containerID

	// Wait for peer to join mesh (with scaled timeout)
	joinTimeout := story.ScaledDuration(30*time.Second, m.timeScale)
	joinCtx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()

	// Poll until peer is online
	ticker := time.NewTicker(story.ScaledDuration(1*time.Second, m.timeScale))
	defer ticker.Stop()

	for {
		select {
		case <-joinCtx.Done():
			m.peers[character.ID].Status = PeerOffline
			return fmt.Errorf("peer %s failed to join within %v: %w", character.ID, joinTimeout, joinCtx.Err())

		case <-ticker.C:
			status, err := m.controller.GetPeerStatus(joinCtx, character)
			if err != nil {
				return fmt.Errorf("checking peer %s status: %w", character.ID, err)
			}

			if status == PeerOnline {
				// Peer is online, wait for mesh to stabilize
				stableTimeout := story.ScaledDuration(15*time.Second, m.timeScale)
				if err := m.controller.WaitForMeshStable(joinCtx, stableTimeout); err != nil {
					return fmt.Errorf("waiting for mesh stable after %s joined: %w", character.ID, err)
				}

				// Mark as online
				now := time.Now()
				m.peers[character.ID].Status = PeerOnline
				m.peers[character.ID].JoinedAt = now
				m.peers[character.ID].LastSeen = now

				return nil
			}
		}
	}
}

// LeavePeer takes a peer offline at the specified story time.
func (m *MeshOrchestrator) LeavePeer(ctx context.Context, character story.Character) error {
	state, exists := m.peers[character.ID]
	if !exists {
		return fmt.Errorf("peer %s not found", character.ID)
	}

	if state.Status == PeerOffline {
		return fmt.Errorf("peer %s already offline", character.ID)
	}

	// Mark as leaving
	state.Status = PeerLeaving

	// Stop Docker container
	if err := m.controller.StopPeer(ctx, character); err != nil {
		return fmt.Errorf("stopping peer %s: %w", character.ID, err)
	}

	// Wait for mesh to stabilize after departure
	stableTimeout := story.ScaledDuration(15*time.Second, m.timeScale)
	stableCtx, cancel := context.WithTimeout(ctx, stableTimeout)
	defer cancel()

	// Ignore stabilization errors - peer is already stopped
	// In real scenario, remaining peers should adapt
	_ = m.controller.WaitForMeshStable(stableCtx, stableTimeout)

	// Mark as offline
	state.Status = PeerOffline
	state.ContainerID = ""

	return nil
}

// GetPeerState returns the current state of a peer.
func (m *MeshOrchestrator) GetPeerState(characterID string) (*PeerState, error) {
	state, exists := m.peers[characterID]
	if !exists {
		return nil, fmt.Errorf("peer %s not found", characterID)
	}
	return state, nil
}

// GetOnlinePeers returns a list of currently online peers.
func (m *MeshOrchestrator) GetOnlinePeers() []story.Character {
	var online []story.Character
	for _, state := range m.peers {
		if state.Status == PeerOnline {
			online = append(online, state.Character)
		}
	}
	return online
}

// Stats returns mesh orchestrator statistics.
func (m *MeshOrchestrator) Stats() map[string]interface{} {
	onlineCount := 0
	offlineCount := 0
	joiningCount := 0
	leavingCount := 0

	for _, state := range m.peers {
		switch state.Status {
		case PeerOnline:
			onlineCount++
		case PeerOffline:
			offlineCount++
		case PeerJoining:
			joiningCount++
		case PeerLeaving:
			leavingCount++
		}
	}

	return map[string]interface{}{
		"total_peers":  len(m.peers),
		"online":       onlineCount,
		"offline":      offlineCount,
		"joining":      joiningCount,
		"leaving":      leavingCount,
		"peer_details": m.peerDetails(),
	}
}

// peerDetails returns a summary of each peer's state.
func (m *MeshOrchestrator) peerDetails() []map[string]interface{} {
	details := make([]map[string]interface{}, 0, len(m.peers))
	for _, state := range m.peers {
		detail := map[string]interface{}{
			"character":   state.Character.Name,
			"id":          state.Character.ID,
			"status":      string(state.Status),
			"docker_peer": state.Character.DockerPeer,
		}
		if state.Status == PeerOnline {
			detail["online_duration"] = time.Since(state.JoinedAt).String()
		}
		details = append(details, detail)
	}
	return details
}

// MockMeshController is a mock implementation of MeshController for testing.
type MockMeshController struct {
	startDelay  time.Duration               // Delay before peer starts
	stableDelay time.Duration               // Delay for mesh stabilization
	containers  map[string]string           // Map character ID to container ID
	statuses    map[string]PeerStatus       // Map character ID to status
	startError  func(story.Character) error // Optional error on start
	stopError   func(story.Character) error // Optional error on stop
}

// NewMockMeshController creates a new mock mesh controller.
func NewMockMeshController() *MockMeshController {
	return &MockMeshController{
		startDelay:  10 * time.Millisecond, // Fast for testing
		stableDelay: 5 * time.Millisecond,  // Fast for testing
		containers:  make(map[string]string),
		statuses:    make(map[string]PeerStatus),
	}
}

// StartPeer implements MeshController for mock.
func (m *MockMeshController) StartPeer(ctx context.Context, character story.Character) (string, error) {
	if m.startError != nil {
		if err := m.startError(character); err != nil {
			return "", err
		}
	}

	// Simulate startup delay
	select {
	case <-time.After(m.startDelay):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Generate mock container ID
	containerID := fmt.Sprintf("mock_%s_%d", character.DockerPeer, time.Now().Unix())
	m.containers[character.ID] = containerID
	m.statuses[character.ID] = PeerOnline

	return containerID, nil
}

// StopPeer implements MeshController for mock.
func (m *MockMeshController) StopPeer(ctx context.Context, character story.Character) error {
	if m.stopError != nil {
		if err := m.stopError(character); err != nil {
			return err
		}
	}

	// Simulate stop delay
	select {
	case <-time.After(m.startDelay):
	case <-ctx.Done():
		return ctx.Err()
	}

	delete(m.containers, character.ID)
	m.statuses[character.ID] = PeerOffline

	return nil
}

// GetPeerStatus implements MeshController for mock.
func (m *MockMeshController) GetPeerStatus(ctx context.Context, character story.Character) (PeerStatus, error) {
	if status, exists := m.statuses[character.ID]; exists {
		return status, nil
	}
	return PeerOffline, nil
}

// WaitForMeshStable implements MeshController for mock.
func (m *MockMeshController) WaitForMeshStable(ctx context.Context, timeout time.Duration) error {
	// Simulate stabilization delay
	select {
	case <-time.After(m.stableDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetStartError allows tests to inject start errors.
func (m *MockMeshController) SetStartError(fn func(story.Character) error) {
	m.startError = fn
}

// SetStopError allows tests to inject stop errors.
func (m *MockMeshController) SetStopError(fn func(story.Character) error) {
	m.stopError = fn
}
