package replication

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRebalancer(t *testing.T) (*Rebalancer, *Replicator, *mockS3Store) {
	t.Helper()

	broker := newTestTransportBroker()
	transport := broker.newTransportFor("coord1")
	s3Store := newMockS3Store()
	registry := newMockChunkRegistry()
	logger := zerolog.Nop()

	replicator := NewReplicator(Config{
		NodeID:        "coord1",
		Transport:     transport,
		S3Store:       s3Store,
		ChunkRegistry: registry,
		Logger:        logger,
	})

	rb := NewRebalancer(replicator, s3Store, registry, logger)
	// Use zero debounce for tests
	rb.debounceDuration = 0

	return rb, replicator, s3Store
}

func TestRebalancer_TopologyChange_ChunkRedistribution(t *testing.T) {
	rb, replicator, s3Store := newTestRebalancer(t)

	// Add chunks and objects to coord1
	chunks := []string{"chunk_a", "chunk_b", "chunk_c", "chunk_d"}
	chunkData := map[string][]byte{
		"chunk_a": []byte("data_a"),
		"chunk_b": []byte("data_b"),
		"chunk_c": []byte("data_c"),
		"chunk_d": []byte("data_d"),
	}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	// Initially no peers
	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	// Now add a peer
	replicator.AddPeer("coord2")

	// Verify rebalanceObject can compute what needs moving
	policy := NewStripingPolicy([]string{"coord1", "coord2"})
	assignedToSelf := policy.ChunksForPeer("coord1", 4, 2)

	// With 2 coordinators and RF=2, each coordinator should have all 4 chunks
	// (since RF=2 >= coord count)
	assert.Len(t, assignedToSelf, 4)

	// Verify stats are initialized
	stats := rb.GetStats()
	assert.Equal(t, uint64(0), stats.ChunksRedistributed)
}

func TestRebalancer_CoordinatorLeaves(t *testing.T) {
	rb, replicator, s3Store := newTestRebalancer(t)

	// Start with 3 coordinators
	replicator.AddPeer("coord2")
	replicator.AddPeer("coord3")

	chunks := []string{"chunk_a", "chunk_b", "chunk_c"}
	chunkData := map[string][]byte{
		"chunk_a": []byte("data_a"),
		"chunk_b": []byte("data_b"),
		"chunk_c": []byte("data_c"),
	}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	// Initial run with 3 coordinators
	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	initialRuns := rb.GetStats().RunsTotal

	// Remove coord3
	replicator.RemovePeer("coord3")
	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	// Should have run another cycle
	assert.Greater(t, rb.GetStats().RunsTotal, initialRuns)
}

func TestRebalancer_RateLimiting(t *testing.T) {
	rb, _, s3Store := newTestRebalancer(t)
	rb.maxBytesPerCycle = 10 // Very low limit: 10 bytes

	// Add object with chunks totaling more than 10 bytes
	chunks := []string{"chunk_a", "chunk_b"}
	chunkData := map[string][]byte{
		"chunk_a": []byte("data_a_long_enough_to_exceed_limit"),
		"chunk_b": []byte("data_b_also_long"),
	}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	// Topology change should trigger, but rate limit should stop early
	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	// Verify the cycle ran (even if it stopped early)
	assert.Equal(t, uint64(1), rb.GetStats().RunsTotal)
}

func TestRebalancer_Idempotent(t *testing.T) {
	rb, replicator, s3Store := newTestRebalancer(t)

	replicator.AddPeer("coord2")

	chunks := []string{"chunk_a"}
	chunkData := map[string][]byte{
		"chunk_a": []byte("data_a"),
	}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	// Run once
	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	runsAfterFirst := rb.GetStats().RunsTotal

	// Run again with same topology — should not trigger
	rb.runRebalanceCycle(context.Background())

	assert.Equal(t, runsAfterFirst, rb.GetStats().RunsTotal)
}

func TestRebalancer_Debounce(t *testing.T) {
	rb, _, _ := newTestRebalancer(t)
	rb.debounceDuration = 50 * time.Millisecond

	// Rapid topology changes
	rb.NotifyTopologyChange()
	rb.NotifyTopologyChange()
	rb.NotifyTopologyChange()

	// The flag should be set
	assert.True(t, rb.topologyChanged.Load())
}

func TestRebalancer_NoOpOnStable(t *testing.T) {
	rb, _, _ := newTestRebalancer(t)

	// No topology change notification
	rb.runRebalanceCycle(context.Background())

	assert.Equal(t, uint64(0), rb.GetStats().RunsTotal)
}

func TestRebalancer_StartStop(t *testing.T) {
	rb, _, _ := newTestRebalancer(t)

	rb.Start()
	time.Sleep(10 * time.Millisecond) // Let goroutine start
	rb.Stop()

	// Should not panic or hang
}

func TestRebalancer_ConcurrentWrites(t *testing.T) {
	rb, replicator, s3Store := newTestRebalancer(t)

	replicator.AddPeer("coord2")

	// Initial object
	chunks := []string{"chunk_a", "chunk_b"}
	chunkData := map[string][]byte{
		"chunk_a": []byte("data_a"),
		"chunk_b": []byte("data_b"),
	}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	// Run rebalance
	rb.NotifyTopologyChange()

	// Simulate concurrent write by modifying the object during rebalance
	go func() {
		time.Sleep(1 * time.Millisecond)
		newChunks := []string{"chunk_c", "chunk_d"}
		newChunkData := map[string][]byte{
			"chunk_c": []byte("data_c"),
			"chunk_d": []byte("data_d"),
		}
		s3Store.addObjectWithChunks("bucket1", "file1", newChunks, newChunkData)
	}()

	rb.runRebalanceCycle(context.Background())
	// Should complete without panic or data corruption
}

func TestRebalancer_OnCycleCompleteCallback(t *testing.T) {
	rb, _, s3Store := newTestRebalancer(t)

	var callbackStats RebalancerStats
	callbackCalled := false
	rb.OnCycleComplete = func(stats RebalancerStats) {
		callbackStats = stats
		callbackCalled = true
	}

	chunks := []string{"chunk_a"}
	chunkData := map[string][]byte{"chunk_a": []byte("data_a")}
	s3Store.addObjectWithChunks("bucket1", "file1", chunks, chunkData)

	rb.NotifyTopologyChange()
	rb.runRebalanceCycle(context.Background())

	assert.True(t, callbackCalled)
	assert.Equal(t, uint64(1), callbackStats.RunsTotal)
}

// TestReplicateObjectMeta_IncludesVersionHistory verifies that sendReplicateObjectMeta
// includes version history in the payload.
func TestReplicateObjectMeta_IncludesVersionHistory(t *testing.T) {
	broker := newTestTransportBroker()
	transport1 := broker.newTransportFor("coord1")
	transport2 := broker.newTransportFor("coord2")
	s3Store1 := newMockS3Store()
	s3Store2 := newMockS3Store()
	logger := zerolog.Nop()

	r1 := NewReplicator(Config{
		NodeID:    "coord1",
		Transport: transport1,
		S3Store:   s3Store1,
		Logger:    logger,
	})

	r2 := NewReplicator(Config{
		NodeID:    "coord2",
		Transport: transport2,
		S3Store:   s3Store2,
		Logger:    logger,
	})
	require.NoError(t, r2.Start())
	defer func() { _ = r2.Stop() }()

	// Add version history to store 1
	s3Store1.versionHistory = map[string][]VersionEntry{
		"bucket1/file1:versions": {
			{VersionID: "v1", MetaJSON: json.RawMessage(`{"key":"file1","version_id":"v1"}`)},
			{VersionID: "v2", MetaJSON: json.RawMessage(`{"key":"file1","version_id":"v2"}`)},
		},
	}

	// Add the object to store 1
	s3Store1.mu.Lock()
	s3Store1.objects["bucket1/file1"] = mockS3Object{
		data:        []byte("data"),
		contentType: "text/plain",
	}
	s3Store1.mu.Unlock()

	// Send object metadata with version history
	metaJSON := json.RawMessage(`{"key":"file1","size":4}`)
	err := r1.sendReplicateObjectMeta(context.Background(), "coord2", "bucket1", "file1", metaJSON)
	require.NoError(t, err)

	// Verify coord2 received the version history
	time.Sleep(50 * time.Millisecond) // Allow async processing

	s3Store2.mu.Lock()
	versions := s3Store2.versionHistory["bucket1/file1:versions"]
	s3Store2.mu.Unlock()

	assert.Len(t, versions, 2)
	assert.Equal(t, "v1", versions[0].VersionID)
	assert.Equal(t, "v2", versions[1].VersionID)
}

// TestHandleReplicateObjectMeta_ImportsVersions verifies that the receiver imports versions.
func TestHandleReplicateObjectMeta_ImportsVersions(t *testing.T) {
	broker := newTestTransportBroker()
	transport := broker.newTransportFor("coord1")
	s3Store := newMockS3Store()
	logger := zerolog.Nop()

	r := NewReplicator(Config{
		NodeID:    "coord1",
		Transport: transport,
		S3Store:   s3Store,
		Logger:    logger,
	})
	require.NoError(t, r.Start())
	defer func() { _ = r.Stop() }()

	// Create payload with versions
	payload := ReplicateObjectMetaPayload{
		Bucket:   "bucket1",
		Key:      "file1",
		MetaJSON: json.RawMessage(`{"key":"file1","size":4}`),
		Versions: []VersionEntry{
			{VersionID: "v1", MetaJSON: json.RawMessage(`{"key":"file1","version_id":"v1"}`)},
			{VersionID: "v2", MetaJSON: json.RawMessage(`{"key":"file1","version_id":"v2"}`)},
		},
	}

	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeReplicateObjectMeta,
		ID:      "test-msg",
		From:    "coord2",
		Payload: json.RawMessage(payloadJSON),
	}

	err = r.handleReplicateObjectMeta(msg)
	require.NoError(t, err)

	// Check versions were imported
	s3Store.mu.Lock()
	versions := s3Store.versionHistory["bucket1/file1:versions"]
	s3Store.mu.Unlock()

	assert.Len(t, versions, 2)
}
