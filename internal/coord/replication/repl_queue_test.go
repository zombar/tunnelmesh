package replication

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestReplicatorWithQueue(t *testing.T) (*Replicator, *mockTransport, *mockS3Store) {
	t.Helper()
	transport := newMockTransport()
	s3Store := newMockS3Store()
	registry := newMockChunkRegistry()

	r := NewReplicator(Config{
		NodeID:              "coord-test",
		Transport:           transport,
		S3Store:             s3Store,
		ChunkRegistry:       registry,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0, // Disable auto-sync for unit tests
	})
	t.Cleanup(func() { _ = r.Stop() })
	return r, transport, s3Store
}

func TestEnqueueReplication_Dedup(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	// Enqueue same key multiple times
	r.EnqueueReplication("bucket1", "key1", "put")
	r.EnqueueReplication("bucket1", "key1", "put")
	r.EnqueueReplication("bucket1", "key1", "put")

	// Count entries in the pending map
	count := 0
	r.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})

	assert.Equal(t, 1, count, "Duplicate keys should be deduped to 1 entry")
}

func TestEnqueueReplication_DeleteSupersedesPut(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	// Enqueue a PUT then a DELETE for the same key
	r.EnqueueReplication("bucket1", "key1", "put")
	r.EnqueueReplication("bucket1", "key1", "delete")

	// The DELETE should supersede the PUT
	compositeKey := "bucket1\x00key1"
	val, ok := r.replPending.Load(compositeKey)
	require.True(t, ok, "Entry should exist")

	entry := val.(*replQueueEntry)
	assert.Equal(t, "delete", entry.op, "DELETE should supersede PUT")
}

func TestEnqueueReplication_DifferentKeys(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	r.EnqueueReplication("bucket1", "key1", "put")
	r.EnqueueReplication("bucket1", "key2", "put")
	r.EnqueueReplication("bucket2", "key1", "put")

	count := 0
	r.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})

	assert.Equal(t, 3, count, "Different keys should all be queued")
}

func TestDrainReplicationQueue_NoPeers(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	// Enqueue without adding peers — drain should be a no-op
	r.EnqueueReplication("bucket1", "key1", "put")

	r.drainReplicationQueue(context.Background())

	// Entry should be consumed (removed from pending map)
	count := 0
	r.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "Pending map should be empty after drain")
}

func TestDrainReplicationQueue_Put(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()
	registryB := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:        "coord-b",
		Transport:     broker.newTransportFor("coord-b"),
		S3Store:       s3b,
		ChunkRegistry: registryB,
		Logger:        zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Add object on coord-a
	chunks := []string{"hash1", "hash2"}
	chunkData := map[string][]byte{
		"hash1": []byte("data1"),
		"hash2": []byte("data2"),
	}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Enqueue and drain
	rA.EnqueueReplication("bucket1", "file.txt", "put")
	rA.drainReplicationQueue(context.Background())

	// Wait for async processing
	time.Sleep(100 * time.Millisecond)

	// Verify chunks were replicated to coord-b
	for _, hash := range chunks {
		exists := s3b.ChunkExists(context.Background(), hash)
		assert.True(t, exists, "Chunk %s should be replicated to coord-b", hash)
	}
}

func TestDrainReplicationQueue_Delete(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()

	rA := NewReplicator(Config{
		NodeID:           "coord-a",
		Transport:        broker.newTransportFor("coord-a"),
		S3Store:          s3a,
		Logger:           zerolog.Nop(),
		AutoSyncInterval: 0,
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:    "coord-b",
		Transport: broker.newTransportFor("coord-b"),
		S3Store:   s3b,
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Add object on coord-b to be deleted
	_ = s3b.Put(context.Background(), "bucket1", "file.txt", []byte("data"), "text/plain", nil)

	// Enqueue delete and drain
	rA.EnqueueReplication("bucket1", "file.txt", "delete")
	rA.drainReplicationQueue(context.Background())

	// Wait for async processing
	time.Sleep(100 * time.Millisecond)

	// Verify object was deleted on coord-b
	_, _, err := s3b.Get(context.Background(), "bucket1", "file.txt")
	assert.Error(t, err, "Object should be deleted on coord-b")
}

func TestReEnqueueOnFailure_MaxRetries(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	// Create an entry that has already been retried 3 times
	entry := &replQueueEntry{
		bucket:  "bucket1",
		key:     "key1",
		op:      "put",
		retries: 3,
	}

	r.reEnqueueOnFailure(entry)

	// Should NOT be re-enqueued (max retries exceeded)
	compositeKey := "bucket1\x00key1"
	_, loaded := r.replPending.Load(compositeKey)
	assert.False(t, loaded, "Entry should not be re-enqueued after max retries")
}

func TestReEnqueueOnFailure_IncrementRetry(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	entry := &replQueueEntry{
		bucket:  "bucket1",
		key:     "key1",
		op:      "put",
		retries: 1,
	}

	r.reEnqueueOnFailure(entry)

	compositeKey := "bucket1\x00key1"
	val, loaded := r.replPending.Load(compositeKey)
	require.True(t, loaded, "Entry should be re-enqueued")

	reEntry := val.(*replQueueEntry)
	assert.Equal(t, 2, reEntry.retries, "Retry count should be incremented")
}

func TestReEnqueueOnFailure_NewerEntryPreserved(t *testing.T) {
	r, _, _ := createTestReplicatorWithQueue(t)

	// Add a newer entry first
	r.EnqueueReplication("bucket1", "key1", "delete")

	// Try to re-enqueue an older failed PUT
	entry := &replQueueEntry{
		bucket:  "bucket1",
		key:     "key1",
		op:      "put",
		retries: 0,
	}

	r.reEnqueueOnFailure(entry)

	// The newer DELETE should still be there
	compositeKey := "bucket1\x00key1"
	val, loaded := r.replPending.Load(compositeKey)
	require.True(t, loaded)

	reEntry := val.(*replQueueEntry)
	assert.Equal(t, "delete", reEntry.op, "Newer entry should be preserved")
}

func TestDrainReplicationQueue_BoundedConcurrency(t *testing.T) {
	// Verify that drain processes entries with bounded concurrency (5 concurrent).
	// We enqueue many delete operations (which don't need chunk ACKs)
	// and verify they all get processed.
	transport := newMockTransport()
	s3Store := newMockS3Store()

	r := NewReplicator(Config{
		NodeID:           "coord-test",
		Transport:        transport,
		S3Store:          s3Store,
		Logger:           zerolog.Nop(),
		AutoSyncInterval: 0,
	})
	t.Cleanup(func() { _ = r.Stop() })

	r.AddPeer("coord-peer")

	// Enqueue 20 delete operations
	for i := 0; i < 20; i++ {
		key := "key" + string(rune('a'+i))
		r.EnqueueReplication("bucket", key, "delete")
	}

	// The drain function uses a semaphore of size 5, so we verify that
	// all 20 entries get processed (drain completes without hanging)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.drainReplicationQueue(ctx)

	// Verify all entries were consumed
	count := 0
	r.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "All entries should be consumed by drain")
}

func TestShutdownDrain(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	require.NoError(t, rA.Start())

	rB := NewReplicator(Config{
		NodeID:    "coord-b",
		Transport: broker.newTransportFor("coord-b"),
		S3Store:   s3b,
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Add object and enqueue
	chunks := []string{"hash1"}
	chunkData := map[string][]byte{"hash1": []byte("data1")}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)
	rA.EnqueueReplication("bucket1", "file.txt", "put")

	// Stop should drain the queue
	err := rA.Stop()
	require.NoError(t, err)

	// Wait for async processing
	time.Sleep(100 * time.Millisecond)

	// Verify the object was replicated during shutdown drain
	exists := s3b.ChunkExists(context.Background(), "hash1")
	assert.True(t, exists, "Chunk should be replicated during shutdown drain")
}

func TestRunReplicationQueueWorker_SignalWakeup(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:    "coord-b",
		Transport: broker.newTransportFor("coord-b"),
		S3Store:   s3b,
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Add object
	chunks := []string{"hash1"}
	chunkData := map[string][]byte{"hash1": []byte("data1")}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Enqueue — the worker should wake up via the signal channel
	rA.EnqueueReplication("bucket1", "file.txt", "put")

	// Wait for the worker to process (should be fast since it's signal-driven)
	assert.Eventually(t, func() bool {
		return s3b.ChunkExists(context.Background(), "hash1")
	}, 5*time.Second, 50*time.Millisecond, "Worker should process enqueued replication via signal")
}

func TestAutoSyncCycle(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0, // We'll call runAutoSyncCycle manually
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:    "coord-b",
		Transport: broker.newTransportFor("coord-b"),
		S3Store:   s3b,
		Logger:    zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Add multiple objects
	for _, key := range []string{"file1.txt", "file2.txt"} {
		chunks := []string{"hash-" + key}
		chunkData := map[string][]byte{
			"hash-" + key: []byte("data-" + key),
		}
		s3a.addObjectWithChunks("bucket1", key, chunks, chunkData)
	}

	// Run auto-sync cycle manually
	rA.runAutoSyncCycle()

	// Drain the queue (since worker might not process immediately in test)
	rA.drainReplicationQueue(context.Background())

	// Wait for async processing
	time.Sleep(200 * time.Millisecond)

	// Verify all objects were replicated
	for _, key := range []string{"file1.txt", "file2.txt"} {
		hash := "hash-" + key
		exists := s3b.ChunkExists(context.Background(), hash)
		assert.True(t, exists, "Chunk %s should be replicated via auto-sync", hash)
	}
}

func TestAutoSyncCycle_NoPeers(t *testing.T) {
	r, _, s3Store := createTestReplicatorWithQueue(t)

	// Add objects but no peers
	s3Store.addObjectWithChunks("bucket1", "file.txt", []string{"hash1"}, map[string][]byte{"hash1": []byte("data")})

	// Should be a no-op
	r.runAutoSyncCycle()

	// Verify nothing was enqueued
	count := 0
	r.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})
	assert.Equal(t, 0, count, "Nothing should be enqueued when no peers")
}

func TestChunkPipelining_MultipleChunks(t *testing.T) {
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()
	registryB := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		ChunkPipelineWindow: 3, // Pipeline window of 3
		Logger:              zerolog.Nop(),
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:        "coord-b",
		Transport:     broker.newTransportFor("coord-b"),
		S3Store:       s3b,
		ChunkRegistry: registryB,
		Logger:        zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	// Create object with 10 chunks
	chunks := make([]string, 10)
	chunkData := make(map[string][]byte)
	for i := 0; i < 10; i++ {
		hash := "chunkhash" + string(rune('0'+i))
		chunks[i] = hash
		chunkData[hash] = []byte("data" + string(rune('0'+i)))
	}
	s3a.addObjectWithChunks("bucket1", "bigfile.bin", chunks, chunkData)

	// Replicate
	err := rA.ReplicateObject(context.Background(), "bucket1", "bigfile.bin", "coord-b")
	require.NoError(t, err)

	// Wait for async processing
	time.Sleep(200 * time.Millisecond)

	// Verify all chunks were replicated
	for _, hash := range chunks {
		exists := s3b.ChunkExists(context.Background(), hash)
		assert.True(t, exists, "Chunk %s should be replicated", hash)
	}
}

func TestChunkPipelining_WindowOne(t *testing.T) {
	// Window of 1 should behave like sequential
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()
	registryB := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		ChunkPipelineWindow: 1, // Sequential
		Logger:              zerolog.Nop(),
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:        "coord-b",
		Transport:     broker.newTransportFor("coord-b"),
		S3Store:       s3b,
		ChunkRegistry: registryB,
		Logger:        zerolog.Nop(),
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rA.AddPeer("coord-b")

	chunks := []string{"hash-a", "hash-b", "hash-c"}
	chunkData := map[string][]byte{
		"hash-a": []byte("data-a"),
		"hash-b": []byte("data-b"),
		"hash-c": []byte("data-c"),
	}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	err := rA.ReplicateObject(context.Background(), "bucket1", "file.txt", "coord-b")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	for _, hash := range chunks {
		exists := s3b.ChunkExists(context.Background(), hash)
		assert.True(t, exists, "Chunk %s should be replicated with window=1", hash)
	}
}

func TestProcessQueuePut_ParallelPeerReplication(t *testing.T) {
	// Verify that processQueuePut replicates to multiple peers in parallel.
	// With 3 coordinators and RF=2, each chunk is assigned to 2 coordinators.
	// We use enough chunks that both peers get some assigned chunks.
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	s3c := newMockS3Store()
	registryA := newMockChunkRegistry()
	registryB := newMockChunkRegistry()
	registryC := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	require.NoError(t, rA.Start())
	defer func() { _ = rA.Stop() }()

	rB := NewReplicator(Config{
		NodeID:              "coord-b",
		Transport:           broker.newTransportFor("coord-b"),
		S3Store:             s3b,
		ChunkRegistry:       registryB,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	rC := NewReplicator(Config{
		NodeID:              "coord-c",
		Transport:           broker.newTransportFor("coord-c"),
		S3Store:             s3c,
		ChunkRegistry:       registryC,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
	})
	require.NoError(t, rC.Start())
	defer func() { _ = rC.Stop() }()

	rA.AddPeer("coord-b")
	rA.AddPeer("coord-c")

	// Create object with 6 chunks so striping distributes across all coordinators
	chunks := make([]string, 6)
	chunkData := make(map[string][]byte)
	for i := 0; i < 6; i++ {
		hash := fmt.Sprintf("hash%d", i)
		chunks[i] = hash
		chunkData[hash] = []byte(fmt.Sprintf("data%d", i))
	}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Enqueue and drain
	rA.EnqueueReplication("bucket1", "file.txt", "put")
	rA.drainReplicationQueue(context.Background())

	// Wait for async chunk processing
	time.Sleep(500 * time.Millisecond)

	// With RF=2 and 3 coordinators, each peer should get ~4 of 6 chunks.
	// Verify both peers received at least some chunks (proves parallel execution).
	bCount := 0
	cCount := 0
	for _, hash := range chunks {
		if s3b.ChunkExists(context.Background(), hash) {
			bCount++
		}
		if s3c.ChunkExists(context.Background(), hash) {
			cCount++
		}
	}
	assert.Greater(t, bCount, 0, "coord-b should have received some chunks")
	assert.Greater(t, cCount, 0, "coord-c should have received some chunks")
}

func TestProcessQueuePut_ReEnqueuesOnPartialFailure(t *testing.T) {
	// Verify that when one peer fails, the entry is re-enqueued for retry.
	// coord-c has no replicator registered, so sending to it fails immediately.
	// Note: rA is NOT started (no background worker) so re-enqueued entries stay in replPending.
	broker := newTestTransportBroker()
	s3a := newMockS3Store()
	s3b := newMockS3Store()
	registryA := newMockChunkRegistry()
	registryB := newMockChunkRegistry()

	rA := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           broker.newTransportFor("coord-a"),
		S3Store:             s3a,
		ChunkRegistry:       registryA,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	// Don't call rA.Start() — we drain manually to avoid the worker consuming re-enqueued entries
	t.Cleanup(func() { _ = rA.Stop() })

	rB := NewReplicator(Config{
		NodeID:              "coord-b",
		Transport:           broker.newTransportFor("coord-b"),
		S3Store:             s3b,
		ChunkRegistry:       registryB,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
	})
	require.NoError(t, rB.Start())
	defer func() { _ = rB.Stop() }()

	// Add coord-b (reachable) and coord-c (no replicator = unreachable)
	rA.AddPeer("coord-b")
	rA.AddPeer("coord-c")

	// Create object with 6 chunks for realistic striping
	chunks := make([]string, 6)
	chunkData := make(map[string][]byte)
	for i := 0; i < 6; i++ {
		hash := fmt.Sprintf("hash%d", i)
		chunks[i] = hash
		chunkData[hash] = []byte(fmt.Sprintf("data%d", i))
	}
	s3a.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Enqueue and drain manually
	rA.EnqueueReplication("bucket1", "file.txt", "put")
	rA.drainReplicationQueue(context.Background())

	// Wait for async chunk processing on receiver side
	time.Sleep(500 * time.Millisecond)

	// Entry should be re-enqueued because coord-c is unreachable
	count := 0
	rA.replPending.Range(func(_, _ any) bool {
		count++
		return true
	})
	assert.Equal(t, 1, count, "Entry should be re-enqueued due to partial failure (coord-c unreachable)")

	// coord-b should have received its assigned chunks
	bCount := 0
	for _, hash := range chunks {
		if s3b.ChunkExists(context.Background(), hash) {
			bCount++
		}
	}
	assert.Greater(t, bCount, 0, "coord-b should have received some chunks despite coord-c failure")
}

func TestAutoSyncCycle_SendsManifest(t *testing.T) {
	transport := newMockTransport()
	s3Store := newMockS3Store()
	registry := newMockChunkRegistry()

	r := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           transport,
		S3Store:             s3Store,
		ChunkRegistry:       registry,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	t.Cleanup(func() { _ = r.Stop() })

	r.AddPeer("coord-b")

	// Add some objects
	s3Store.addObjectWithChunks("bucket1", "file1.txt", []string{"h1"}, map[string][]byte{"h1": []byte("d1")})
	s3Store.addObjectWithChunks("bucket1", "file2.txt", []string{"h2"}, map[string][]byte{"h2": []byte("d2")})
	s3Store.addObjectWithChunks("bucket2", "doc.txt", []string{"h3"}, map[string][]byte{"h3": []byte("d3")})

	// Run auto-sync cycle — this enqueues puts AND sends the manifest
	r.runAutoSyncCycle()

	// Check that the transport received a manifest message
	transport.mu.Lock()
	messages := transport.sentMessages
	transport.mu.Unlock()

	var foundManifest bool
	for _, sent := range messages {
		msg, err := UnmarshalMessage(sent.data)
		if err != nil {
			continue
		}
		if msg.Type == MessageTypeObjectManifest {
			foundManifest = true
			var payload ObjectManifestPayload
			require.NoError(t, json.Unmarshal(msg.Payload, &payload))
			assert.Contains(t, payload.BucketKeys, "bucket1")
			assert.Contains(t, payload.BucketKeys, "bucket2")
			assert.ElementsMatch(t, []string{"file1.txt", "file2.txt"}, payload.BucketKeys["bucket1"])
			assert.ElementsMatch(t, []string{"doc.txt"}, payload.BucketKeys["bucket2"])
			break
		}
	}
	assert.True(t, foundManifest, "Auto-sync should send an object manifest to peers")
}

func TestAutoSyncCycle_EmptyStoreSkipsManifest(t *testing.T) {
	// An empty object store (e.g. during startup before S3 loads) must NOT
	// send a manifest — an empty manifest would cause replicas to purge everything.
	transport := newMockTransport()
	s3Store := newMockS3Store()
	registry := newMockChunkRegistry()

	r := NewReplicator(Config{
		NodeID:              "coord-a",
		Transport:           transport,
		S3Store:             s3Store,
		ChunkRegistry:       registry,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	t.Cleanup(func() { _ = r.Stop() })

	r.AddPeer("coord-b")

	// No objects in the store — run auto-sync
	r.runAutoSyncCycle()

	// No manifest should be sent
	transport.mu.Lock()
	messages := transport.sentMessages
	transport.mu.Unlock()

	for _, sent := range messages {
		msg, err := UnmarshalMessage(sent.data)
		if err != nil {
			continue
		}
		assert.NotEqual(t, MessageTypeObjectManifest, msg.Type,
			"Empty store must not send manifest (could cause mass purge on replicas)")
	}
}

func TestManifestReconciliation_SkipsBucketsNotOwnedBySender(t *testing.T) {
	// In a multi-coordinator setup, coordinator A should not be able to
	// purge objects from buckets owned by coordinator C on this node.
	transport := newMockTransport()
	s3Store := newMockS3Store()
	registry := newMockChunkRegistry()

	r := NewReplicator(Config{
		NodeID:              "coord-b",
		Transport:           transport,
		S3Store:             s3Store,
		ChunkRegistry:       registry,
		Logger:              zerolog.Nop(),
		ChunkPipelineWindow: 5,
		AutoSyncInterval:    0,
	})
	t.Cleanup(func() { _ = r.Stop() })

	// coord-b has objects from two different owners:
	// - bucket-a owned by coord-a (replicated from coord-a)
	// - bucket-c owned by coord-c (replicated from coord-c)
	s3Store.addObjectWithChunks("bucket-a", "file1.txt", []string{"h1"}, map[string][]byte{"h1": []byte("d1")})
	s3Store.addObjectWithChunks("bucket-c", "file1.txt", []string{"h2"}, map[string][]byte{"h2": []byte("d2")})
	s3Store.bucketOwners = map[string]string{
		"bucket-a": "coord-a",
		"bucket-c": "coord-c",
	}

	// coord-a sends a manifest listing bucket-a (empty) and bucket-c (empty).
	// Without ownership check, this would purge both. With the fix, only bucket-a
	// objects should be purged because coord-a doesn't own bucket-c.
	payload := ObjectManifestPayload{
		BucketKeys: map[string][]string{
			"bucket-a": {}, // coord-a removed all objects from its bucket
			"bucket-c": {}, // coord-a claims bucket-c is empty too (wrong!)
		},
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	msg := &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeObjectManifest,
		ID:      "test-manifest",
		From:    "coord-a", // Sender is coord-a
		Payload: json.RawMessage(payloadJSON),
	}

	err = r.handleObjectManifest(msg)
	require.NoError(t, err)

	// bucket-a/file1.txt should be purged (coord-a owns bucket-a, and file is not in manifest)
	_, _, err = s3Store.Get(context.Background(), "bucket-a", "file1.txt")
	assert.Error(t, err, "bucket-a/file1.txt should be purged (sender owns this bucket)")

	// bucket-c/file1.txt should NOT be purged (coord-a doesn't own bucket-c)
	_, _, err = s3Store.Get(context.Background(), "bucket-c", "file1.txt")
	assert.NoError(t, err, "bucket-c/file1.txt should survive (sender doesn't own this bucket)")
}
