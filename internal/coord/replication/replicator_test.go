package replication

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTransport implements Transport for testing.
type mockTransport struct {
	mu      sync.Mutex
	sent    map[string][]byte // map[coordMeshIP]lastMessage
	handler func(from string, data []byte) error
	sendErr error // If set, SendToCoordinator will return this error
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		sent: make(map[string][]byte),
	}
}

func (m *mockTransport) SendToCoordinator(ctx context.Context, coordMeshIP string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sendErr != nil {
		return m.sendErr
	}

	m.sent[coordMeshIP] = append([]byte(nil), data...) // Copy data
	return nil
}

func (m *mockTransport) RegisterHandler(handler func(from string, data []byte) error) {
	m.handler = handler
}

func (m *mockTransport) simulateReceive(from string, data []byte) error {
	if m.handler == nil {
		return fmt.Errorf("no handler registered")
	}
	return m.handler(from, data)
}

func (m *mockTransport) getLastSent(coordMeshIP string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent[coordMeshIP]
}

// mockS3Store implements S3Store for testing.
type mockS3Store struct {
	mu            sync.Mutex
	objects       map[string]mockS3Object // map["bucket/key"]object
	chunks        map[string][]byte       // map[hash]data (for chunk-level operations)
	putErr        error                   // If set, Put will return this error
	metaErr       error                   // If set, GetObjectMeta will return this error
	chunkErr      error                   // If set, chunk operations will return this error
	chunkRequests int                     // Count of ReadChunk calls
}

type mockS3Object struct {
	data        []byte
	contentType string
	metadata    map[string]string
	chunks      []string                  // Chunk hashes (for chunk-level replication)
	chunkMeta   map[string]*ChunkMetadata // Per-chunk metadata
}

func newMockS3Store() *mockS3Store {
	return &mockS3Store{
		objects: make(map[string]mockS3Object),
		chunks:  make(map[string][]byte),
	}
}

func (m *mockS3Store) makeKey(bucket, key string) string {
	return bucket + "/" + key
}

func (m *mockS3Store) Get(ctx context.Context, bucket, key string) ([]byte, map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	obj, exists := m.objects[m.makeKey(bucket, key)]
	if !exists {
		return nil, nil, fmt.Errorf("object not found")
	}

	// Return empty map if metadata is nil
	metadata := obj.metadata
	if metadata == nil {
		metadata = make(map[string]string)
	}

	// Add content-type to metadata (mimics real S3 behavior where content-type is in metadata)
	if obj.contentType != "" {
		metadata["content-type"] = obj.contentType
	}

	return obj.data, metadata, nil
}

func (m *mockS3Store) Put(ctx context.Context, bucket, key string, data []byte, contentType string, metadata map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.putErr != nil {
		return m.putErr
	}

	m.objects[m.makeKey(bucket, key)] = mockS3Object{
		data:        append([]byte(nil), data...),
		contentType: contentType,
		metadata:    metadata,
	}

	return nil
}

func (m *mockS3Store) Delete(ctx context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.objects, m.makeKey(bucket, key))
	return nil
}

func (m *mockS3Store) List(ctx context.Context, bucket string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var keys []string
	prefix := bucket + "/"
	for k := range m.objects {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k[len(prefix):])
		}
	}
	return keys, nil
}

func (m *mockS3Store) ListBuckets(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	buckets := make(map[string]bool)
	for k := range m.objects {
		// Extract bucket name
		for i, c := range k {
			if c == '/' {
				buckets[k[:i]] = true
				break
			}
		}
	}

	result := make([]string, 0, len(buckets))
	for b := range buckets {
		result = append(result, b)
	}
	return result, nil
}

// GetObjectMeta returns object metadata (for chunk-level replication).
func (m *mockS3Store) GetObjectMeta(ctx context.Context, bucket, key string) (*ObjectMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.metaErr != nil {
		return nil, m.metaErr
	}

	obj, exists := m.objects[m.makeKey(bucket, key)]
	if !exists {
		return nil, fmt.Errorf("object not found")
	}

	return &ObjectMeta{
		Key:           key,
		Size:          int64(len(obj.data)),
		ContentType:   obj.contentType,
		Metadata:      obj.metadata,
		Chunks:        obj.chunks,
		ChunkMetadata: obj.chunkMeta,
	}, nil
}

// ReadChunk reads a chunk from CAS.
func (m *mockS3Store) ReadChunk(ctx context.Context, hash string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.chunkRequests++

	if m.chunkErr != nil {
		return nil, m.chunkErr
	}

	data, exists := m.chunks[hash]
	if !exists {
		return nil, fmt.Errorf("chunk not found: %s", hash)
	}

	return data, nil
}

// WriteChunkDirect writes a chunk to CAS.
func (m *mockS3Store) WriteChunkDirect(ctx context.Context, hash string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.chunkErr != nil {
		return m.chunkErr
	}

	m.chunks[hash] = append([]byte(nil), data...)
	return nil
}

// addObjectWithChunks adds an object with chunk-level metadata (helper for tests).
func (m *mockS3Store) addObjectWithChunks(bucket, key string, chunks []string, chunkData map[string][]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Add chunks to CAS
	chunkMetadata := make(map[string]*ChunkMetadata)
	var totalSize int64
	for _, hash := range chunks {
		if data, ok := chunkData[hash]; ok {
			m.chunks[hash] = append([]byte(nil), data...)
			chunkMetadata[hash] = &ChunkMetadata{
				Hash: hash,
				Size: int64(len(data)),
			}
			totalSize += int64(len(data))
		}
	}

	// Reconstruct full data (calculate size for preallocation)
	var size int
	for _, hash := range chunks {
		size += len(m.chunks[hash])
	}
	data := make([]byte, 0, size)
	for _, hash := range chunks {
		data = append(data, m.chunks[hash]...)
	}

	// Add object
	m.objects[m.makeKey(bucket, key)] = mockS3Object{
		data:        data,
		contentType: "application/octet-stream",
		metadata:    make(map[string]string),
		chunks:      chunks,
		chunkMeta:   chunkMetadata,
	}
}

// testTransportBroker routes messages between multiple coordinators in tests.
type testTransportBroker struct {
	mu       sync.Mutex
	handlers map[string]func(from string, data []byte) error // map[coordID]handler
}

func newTestTransportBroker() *testTransportBroker {
	return &testTransportBroker{
		handlers: make(map[string]func(from string, data []byte) error),
	}
}

// newTransportFor creates a transport for a specific coordinator.
func (b *testTransportBroker) newTransportFor(coordID string) *brokerTransport {
	return &brokerTransport{
		broker:  b,
		coordID: coordID,
	}
}

// brokerTransport is a per-coordinator transport that routes via the broker.
type brokerTransport struct {
	broker  *testTransportBroker
	coordID string // This coordinator's ID
}

func (t *brokerTransport) SendToCoordinator(ctx context.Context, destCoordID string, data []byte) error {
	t.broker.mu.Lock()
	handler := t.broker.handlers[destCoordID]
	t.broker.mu.Unlock()

	if handler == nil {
		return fmt.Errorf("no handler registered for %s", destCoordID)
	}

	// Deliver synchronously for deterministic tests
	return handler(t.coordID, data)
}

func (t *brokerTransport) RegisterHandler(handler func(from string, data []byte) error) {
	t.broker.mu.Lock()
	defer t.broker.mu.Unlock()
	t.broker.handlers[t.coordID] = handler
}

// mockChunkRegistry implements ChunkRegistryInterface for testing.
type mockChunkRegistry struct {
	mu        sync.Mutex
	ownership map[string]map[string]bool // map[chunkHash]map[coordID]bool
}

func newMockChunkRegistry() *mockChunkRegistry {
	return &mockChunkRegistry{
		ownership: make(map[string]map[string]bool),
	}
}

func (m *mockChunkRegistry) RegisterChunk(hash string, size int64) error {
	return nil // No-op for tests
}

func (m *mockChunkRegistry) UnregisterChunk(hash string) error {
	return nil // No-op for tests
}

func (m *mockChunkRegistry) GetOwners(hash string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var owners []string
	if coords, ok := m.ownership[hash]; ok {
		for coordID := range coords {
			owners = append(owners, coordID)
		}
	}
	return owners, nil
}

func (m *mockChunkRegistry) GetChunksOwnedBy(coordID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var chunks []string
	for hash, coords := range m.ownership {
		if coords[coordID] {
			chunks = append(chunks, hash)
		}
	}
	return chunks, nil
}

func (m *mockChunkRegistry) AddOwner(hash string, coordID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ownership[hash] == nil {
		m.ownership[hash] = make(map[string]bool)
	}
	m.ownership[hash][coordID] = true
	return nil
}

// setOwnership sets chunk ownership for testing.
func (m *mockChunkRegistry) setOwnership(hash string, coordIDs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ownership[hash] = make(map[string]bool)
	for _, coordID := range coordIDs {
		m.ownership[hash][coordID] = true
	}
}

func TestNewReplicator(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	logger := zerolog.Nop()

	config := Config{
		NodeID:    "coord1.tunnelmesh:8443",
		Transport: transport,
		S3Store:   s3,
		Logger:    logger,
	}

	r := NewReplicator(config)
	require.NotNil(t, r)
	assert.Equal(t, "coord1.tunnelmesh:8443", r.nodeID)
	assert.NotNil(t, transport.handler) // Handler should be registered
}

func TestReplicator_AddRemovePeer(t *testing.T) {
	r := createTestReplicator(t)

	// Initially no peers
	assert.Empty(t, r.GetPeers())

	// Add peers
	r.AddPeer("coord2.mesh")
	r.AddPeer("coord3.mesh")

	peers := r.GetPeers()
	assert.Len(t, peers, 2)
	assert.Contains(t, peers, "coord2.mesh")
	assert.Contains(t, peers, "coord3.mesh")

	// Add duplicate (should be idempotent)
	r.AddPeer("coord2.mesh")
	assert.Len(t, r.GetPeers(), 2)

	// Remove peer
	r.RemovePeer("coord2.mesh")
	peers = r.GetPeers()
	assert.Len(t, peers, 1)
	assert.Contains(t, peers, "coord3.mesh")

	// Remove non-existent peer (should not error)
	r.RemovePeer("nonexistent.mesh")
	assert.Len(t, r.GetPeers(), 1)
}

func TestReplicator_ReplicateOperation_NoPeers(t *testing.T) {
	r := createTestReplicator(t)

	// Replicate with no peers should succeed
	err := r.ReplicateOperation(context.Background(), "system", "test.json", []byte("data"), "text/plain", nil)
	assert.NoError(t, err)

	// Should still update local version vector
	vv := r.state.Get("system", "test.json")
	assert.Equal(t, uint64(1), vv.Get("coord1.tunnelmesh:8443"))
}

func TestReplicator_ReplicateOperation_WithPeers(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	r := createTestReplicatorWithMocks(t, transport, s3)

	r.AddPeer("coord2.mesh")
	r.AddPeer("coord3.mesh")

	// Replicate operation
	data := []byte(`{"key":"value"}`)
	err := r.ReplicateOperation(context.Background(), "system", "config.json", data, "application/json", nil)
	assert.NoError(t, err)

	// Verify messages were sent to both peers
	msg2 := transport.getLastSent("coord2.mesh")
	msg3 := transport.getLastSent("coord3.mesh")

	assert.NotNil(t, msg2)
	assert.NotNil(t, msg3)

	// Verify message content
	parsed2, err := UnmarshalMessage(msg2)
	require.NoError(t, err)
	assert.Equal(t, MessageTypeReplicate, parsed2.Type)
	assert.Equal(t, "coord1.tunnelmesh:8443", parsed2.From)

	payload2, err := parsed2.DecodeReplicatePayload()
	require.NoError(t, err)
	assert.Equal(t, "system", payload2.Bucket)
	assert.Equal(t, "config.json", payload2.Key)
	assert.Equal(t, data, payload2.Data)
	assert.Equal(t, uint64(1), payload2.VersionVector.Get("coord1.tunnelmesh:8443"))
}

func TestReplicator_HandleReplicate(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	r := createTestReplicatorWithMocks(t, transport, s3)

	// Create a replicate message
	payload := ReplicatePayload{
		Bucket:        "system",
		Key:           "test.json",
		Data:          []byte(`{"test":"data"}`),
		VersionVector: VersionVector{"coord2.mesh": 1},
		ContentType:   "application/json",
	}

	msg, err := NewReplicateMessage("msg-001", "coord2.mesh", payload)
	require.NoError(t, err)

	data, err := msg.Marshal()
	require.NoError(t, err)

	// Simulate receiving the message
	err = transport.simulateReceive("coord2.mesh", data)
	assert.NoError(t, err)

	// Verify data was written to S3
	storedData, storedMeta, err := s3.Get(context.Background(), "system", "test.json")
	require.NoError(t, err)
	assert.Equal(t, payload.Data, storedData)
	assert.NotNil(t, storedMeta)

	// Verify version vector was merged
	vv := r.state.Get("system", "test.json")
	assert.Equal(t, uint64(1), vv.Get("coord2.mesh"))
}

func TestReplicator_HandleReplicate_Conflict(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	r := createTestReplicatorWithMocks(t, transport, s3)

	// Create local version (simulating concurrent update)
	r.state.Update("system", "config.json")
	r.state.Update("system", "config.json")

	// Store local data
	localData := []byte(`{"local":"data"}`)
	err := s3.Put(context.Background(), "system", "config.json", localData, "application/json", nil)
	require.NoError(t, err)

	// Receive concurrent update from coord2
	payload := ReplicatePayload{
		Bucket:        "system",
		Key:           "config.json",
		Data:          []byte(`{"remote":"data"}`),
		VersionVector: VersionVector{"coord1.tunnelmesh:8443": 1, "coord2.mesh": 1},
		ContentType:   "application/json",
	}

	msg, err := NewReplicateMessage("msg-002", "coord2.mesh", payload)
	require.NoError(t, err)

	data, err := msg.Marshal()
	require.NoError(t, err)

	// Simulate receiving the message
	err = transport.simulateReceive("coord2.mesh", data)
	assert.NoError(t, err)

	// Conflict should have been detected
	stats := r.GetStats()
	assert.Greater(t, stats.ConflictCount, uint64(0))

	// Version vectors should be merged
	vv := r.state.Get("system", "config.json")
	assert.Equal(t, uint64(2), vv.Get("coord1.tunnelmesh:8443"))
	assert.Equal(t, uint64(1), vv.Get("coord2.mesh"))
}

func TestReplicator_HandleAck(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	r := createTestReplicatorWithMocks(t, transport, s3)

	// Track a pending ACK
	msgID := "test-replicate-001"
	r.trackPendingACK(msgID, "system", "test.json")

	// Create ACK message
	ackPayload := AckPayload{
		ReplicateID:   msgID,
		Success:       true,
		VersionVector: VersionVector{"coord1.tunnelmesh:8443": 1, "coord2.mesh": 1},
	}

	ackMsg, err := NewAckMessage("ack-001", "coord2.mesh", ackPayload)
	require.NoError(t, err)

	ackData, err := ackMsg.Marshal()
	require.NoError(t, err)

	// Get pending entry before sending ACK (it will be removed after ACK is delivered)
	r.pendingMu.RLock()
	pending, exists := r.pending[msgID]
	ackChan := pending.ackChan // Keep reference to channel before it's removed
	r.pendingMu.RUnlock()

	assert.True(t, exists)

	// Simulate receiving ACK
	err = transport.simulateReceive("coord2.mesh", ackData)
	assert.NoError(t, err)

	// Try to receive from ackChan (should be available)
	// Note: The pending entry is removed immediately after ACK delivery (security fix #6)
	select {
	case ack := <-ackChan:
		assert.True(t, ack.Success)
		assert.Equal(t, msgID, ack.ReplicateID)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ACK was not delivered to pending channel")
	}

	// Verify the pending entry was cleaned up (security fix #6)
	r.pendingMu.RLock()
	_, stillExists := r.pending[msgID]
	r.pendingMu.RUnlock()
	assert.False(t, stillExists, "Pending entry should be removed after ACK delivery")
}

func TestReplicator_GetStats(t *testing.T) {
	r := createTestReplicator(t)

	// Initial stats
	stats := r.GetStats()
	assert.Equal(t, uint64(0), stats.SentCount)
	assert.Equal(t, uint64(0), stats.ReceivedCount)
	assert.Equal(t, uint64(0), stats.ConflictCount)
	assert.Equal(t, uint64(0), stats.ErrorCount)
	assert.Equal(t, 0, stats.PeerCount)
	assert.Equal(t, 0, stats.PendingACKs)

	// Add peers
	r.AddPeer("coord2.mesh")
	r.AddPeer("coord3.mesh")

	stats = r.GetStats()
	assert.Equal(t, 2, stats.PeerCount)

	// Simulate some operations
	r.incrementSentCount()
	r.incrementSentCount()
	r.incrementReceivedCount()
	r.incrementConflictCount()
	r.incrementErrorCount()

	stats = r.GetStats()
	assert.Equal(t, uint64(2), stats.SentCount)
	assert.Equal(t, uint64(1), stats.ReceivedCount)
	assert.Equal(t, uint64(1), stats.ConflictCount)
	assert.Equal(t, uint64(1), stats.ErrorCount)
}

func TestReplicator_StartStop(t *testing.T) {
	r := createTestReplicator(t)

	// Start
	err := r.Start()
	assert.NoError(t, err)

	// Give background workers a moment to start
	time.Sleep(50 * time.Millisecond)

	// Stop
	err = r.Stop()
	assert.NoError(t, err)
}

func TestReplicator_TransportError(t *testing.T) {
	transport := newMockTransport()
	transport.sendErr = fmt.Errorf("network error")

	s3 := newMockS3Store()
	r := createTestReplicatorWithMocks(t, transport, s3)

	r.AddPeer("coord2.mesh")

	// Replicate should return error
	err := r.ReplicateOperation(context.Background(), "system", "test.json", []byte("data"), "text/plain", nil)
	assert.Error(t, err)

	// Error count should be incremented
	stats := r.GetStats()
	assert.Greater(t, stats.ErrorCount, uint64(0))
}

func TestReplicator_S3Error(t *testing.T) {
	transport := newMockTransport()
	s3 := newMockS3Store()
	s3.putErr = fmt.Errorf("s3 error")

	r := createTestReplicatorWithMocks(t, transport, s3)

	// Receive a replicate message
	payload := ReplicatePayload{
		Bucket:        "system",
		Key:           "test.json",
		Data:          []byte("data"),
		VersionVector: VersionVector{"coord2.mesh": 1},
	}

	msg, err := NewReplicateMessage("msg-001", "coord2.mesh", payload)
	require.NoError(t, err)

	data, err := msg.Marshal()
	require.NoError(t, err)

	// Should handle error gracefully
	err = transport.simulateReceive("coord2.mesh", data)
	assert.NoError(t, err) // Handler doesn't propagate S3 errors

	// Error count should be incremented
	stats := r.GetStats()
	assert.Greater(t, stats.ErrorCount, uint64(0))
}

// Helper functions

func createTestReplicator(t *testing.T) *Replicator {
	transport := newMockTransport()
	s3 := newMockS3Store()
	return createTestReplicatorWithMocks(t, transport, s3)
}

func createTestReplicatorWithMocks(t *testing.T, transport *mockTransport, s3 *mockS3Store) *Replicator {
	logger := zerolog.Nop()

	config := Config{
		NodeID:    "coord1.tunnelmesh:8443",
		Transport: transport,
		S3Store:   s3,
		Logger:    logger,
	}

	return NewReplicator(config)
}

// ==== Phase 4: Chunk-Level Replication Tests ====

func TestReplicateObject_AllChunksAlreadyReplicated(t *testing.T) {
	ctx := context.Background()
	s3 := newMockS3Store()
	registry := newMockChunkRegistry()
	transport := newMockTransport()

	// Create object with 3 chunks
	chunks := []string{"chunk1", "chunk2", "chunk3"}
	chunkData := map[string][]byte{
		"chunk1": []byte("data1"),
		"chunk2": []byte("data2"),
		"chunk3": []byte("data3"),
	}
	s3.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Mark all chunks as already owned by peer
	for _, hash := range chunks {
		registry.setOwnership(hash, []string{"coord-local", "coord-peer"})
	}

	replicator := NewReplicator(Config{
		NodeID:        "coord-local",
		Transport:     transport,
		S3Store:       s3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Replicate to peer (should skip all chunks)
	err := replicator.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer")
	require.NoError(t, err)

	// Verify no chunk read requests were made
	assert.Equal(t, 0, s3.chunkRequests, "Expected 0 chunk requests when all chunks already replicated")
}

func TestReplicateObject_SomeMissingChunks(t *testing.T) {
	ctx := context.Background()
	senderS3 := newMockS3Store()
	receiverS3 := newMockS3Store()
	registry := newMockChunkRegistry()

	// Create transport broker for message routing
	broker := newTestTransportBroker()

	// Create replicator pair with separate transports
	sender := NewReplicator(Config{
		NodeID:          "coord-sender",
		Transport:       broker.newTransportFor("coord-sender"),
		S3Store:         senderS3,
		ChunkRegistry:   registry,
		ChunkAckTimeout: 1 * time.Second, // Reasonable timeout
		Logger:          zerolog.Nop(),
	})

	receiver := NewReplicator(Config{
		NodeID:        "coord-receiver",
		Transport:     broker.newTransportFor("coord-receiver"),
		S3Store:       receiverS3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Receiver is used indirectly via the broker routing messages to its handler
	_ = receiver

	// Create object with 5 chunks on sender
	chunks := []string{"chunk1", "chunk2", "chunk3", "chunk4", "chunk5"}
	chunkData := map[string][]byte{
		"chunk1": []byte("data1"),
		"chunk2": []byte("data2"),
		"chunk3": []byte("data3"),
		"chunk4": []byte("data4"),
		"chunk5": []byte("data5"),
	}
	senderS3.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Receiver already has chunks 1, 3, 5
	registry.setOwnership("chunk1", []string{"coord-sender", "coord-receiver"})
	registry.setOwnership("chunk2", []string{"coord-sender"})
	registry.setOwnership("chunk3", []string{"coord-sender", "coord-receiver"})
	registry.setOwnership("chunk4", []string{"coord-sender"})
	registry.setOwnership("chunk5", []string{"coord-sender", "coord-receiver"})

	// Replicate (should only send chunks 2 and 4)
	err := sender.ReplicateObject(ctx, "bucket1", "file.txt", "coord-receiver")
	require.NoError(t, err)

	// Wait for async handlers
	time.Sleep(100 * time.Millisecond)

	// Verify receiver got exactly 2 chunks (chunk2 and chunk4)
	assert.Equal(t, 2, len(receiverS3.chunks), "Expected 2 chunks to be replicated")

	// Verify correct chunks received
	assert.Contains(t, receiverS3.chunks, "chunk2", "chunk2 should be replicated")
	assert.Contains(t, receiverS3.chunks, "chunk4", "chunk4 should be replicated")

	// Verify chunk registry updated with receiver as owner
	owners2, _ := registry.GetOwners("chunk2")
	assert.Contains(t, owners2, "coord-receiver", "Receiver should own chunk2 after replication")

	owners4, _ := registry.GetOwners("chunk4")
	assert.Contains(t, owners4, "coord-receiver", "Receiver should own chunk4 after replication")
}

func TestReplicateObject_ChunkReadError(t *testing.T) {
	ctx := context.Background()
	s3 := newMockS3Store()
	registry := newMockChunkRegistry()
	transport := newMockTransport()

	// Create object
	chunks := []string{"chunk1"}
	chunkData := map[string][]byte{"chunk1": []byte("data1")}
	s3.addObjectWithChunks("bucket1", "file.txt", chunks, chunkData)

	// Mark chunk as needing replication
	registry.setOwnership("chunk1", []string{"coord-local"})

	// Inject read error
	s3.chunkErr = fmt.Errorf("disk error")

	replicator := NewReplicator(Config{
		NodeID:        "coord-local",
		Transport:     transport,
		S3Store:       s3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Replicate should fail
	err := replicator.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer")
	require.Error(t, err, "Expected error when chunk read fails")
	assert.Contains(t, err.Error(), "disk error", "Error should mention chunk error")
}

func TestReplicateObject_ObjectNotFound(t *testing.T) {
	ctx := context.Background()
	s3 := newMockS3Store()
	registry := newMockChunkRegistry()
	transport := newMockTransport()

	replicator := NewReplicator(Config{
		NodeID:        "coord-local",
		Transport:     transport,
		S3Store:       s3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Try to replicate non-existent object
	err := replicator.ReplicateObject(ctx, "bucket1", "nonexistent.txt", "coord-peer")
	require.Error(t, err, "Expected error for non-existent object")
	assert.Contains(t, err.Error(), "object not found", "Error should indicate object not found")
}

func TestReplicateObject_NoChunkRegistry(t *testing.T) {
	ctx := context.Background()
	s3 := newMockS3Store()
	transport := newMockTransport()

	// Create simple object (no chunk metadata)
	_ = s3.Put(ctx, "bucket1", "file.txt", []byte("data"), "text/plain", nil)

	// No chunk registry (should fall back to file-level replication)
	replicator := NewReplicator(Config{
		NodeID:    "coord-local",
		Transport: transport,
		S3Store:   s3,
		Logger:    zerolog.Nop(),
	})
	replicator.AddPeer("coord-peer")

	// Should not panic or error (falls back to file-level)
	err := replicator.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer")

	// The fallback calls ReplicateOperation which will send to all peers
	// We just verify it doesn't crash
	assert.NoError(t, err, "Fallback to file-level replication should work")
}

// ==== Striping Policy Tests ====

func TestStripingPolicy_PrimaryOwner(t *testing.T) {
	tests := []struct {
		name       string
		peers      []string
		chunkIndex int
		wantOwner  string
	}{
		{
			name:       "single peer",
			peers:      []string{"coord-a"},
			chunkIndex: 0,
			wantOwner:  "coord-a",
		},
		{
			name:       "single peer, any index",
			peers:      []string{"coord-a"},
			chunkIndex: 5,
			wantOwner:  "coord-a",
		},
		{
			name:       "two peers, index 0",
			peers:      []string{"coord-b", "coord-a"}, // will be sorted
			chunkIndex: 0,
			wantOwner:  "coord-a", // sorted: [coord-a, coord-b]
		},
		{
			name:       "two peers, index 1",
			peers:      []string{"coord-b", "coord-a"},
			chunkIndex: 1,
			wantOwner:  "coord-b",
		},
		{
			name:       "three peers, round-robin",
			peers:      []string{"coord-c", "coord-a", "coord-b"},
			chunkIndex: 2,
			wantOwner:  "coord-c", // sorted: [a, b, c], index 2 % 3 = 2 -> c
		},
		{
			name:       "three peers, wraps around",
			peers:      []string{"coord-a", "coord-b", "coord-c"},
			chunkIndex: 3,
			wantOwner:  "coord-a", // 3 % 3 = 0 -> a
		},
		{
			name:       "empty peers",
			peers:      []string{},
			chunkIndex: 0,
			wantOwner:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := NewStripingPolicy(tt.peers)
			got := sp.PrimaryOwner(tt.chunkIndex)
			assert.Equal(t, tt.wantOwner, got)
		})
	}
}

func TestStripingPolicy_AssignedChunks(t *testing.T) {
	// 10 chunks across 3 peers (sorted: [a, b, c])
	sp := NewStripingPolicy([]string{"coord-c", "coord-a", "coord-b"})

	chunksA := sp.AssignedChunks("coord-a", 10)
	chunksB := sp.AssignedChunks("coord-b", 10)
	chunksC := sp.AssignedChunks("coord-c", 10)

	// Round-robin: a gets 0,3,6,9; b gets 1,4,7; c gets 2,5,8
	assert.Equal(t, []int{0, 3, 6, 9}, chunksA)
	assert.Equal(t, []int{1, 4, 7}, chunksB)
	assert.Equal(t, []int{2, 5, 8}, chunksC)

	// All chunks are covered exactly once
	all := make(map[int]bool)
	for _, idx := range chunksA {
		all[idx] = true
	}
	for _, idx := range chunksB {
		all[idx] = true
	}
	for _, idx := range chunksC {
		all[idx] = true
	}
	assert.Len(t, all, 10)

	// Unknown peer gets nothing
	chunksX := sp.AssignedChunks("coord-x", 10)
	assert.Nil(t, chunksX)
}

func TestStripingPolicy_ChunksForPeer_WithReplicationFactor(t *testing.T) {
	// 6 chunks, 3 peers, RF=2
	sp := NewStripingPolicy([]string{"coord-a", "coord-b", "coord-c"})

	chunksA := sp.ChunksForPeer("coord-a", 6, 2)
	chunksB := sp.ChunksForPeer("coord-b", 6, 2)
	chunksC := sp.ChunksForPeer("coord-c", 6, 2)

	// Primary assignments: a=0,3; b=1,4; c=2,5
	// With RF=2, each chunk also goes to the next peer:
	// chunk 0: primary=a(0), replica=b(1) -> a,b
	// chunk 1: primary=b(1), replica=c(2) -> b,c
	// chunk 2: primary=c(2), replica=a(0) -> c,a
	// chunk 3: primary=a(0), replica=b(1) -> a,b
	// chunk 4: primary=b(1), replica=c(2) -> b,c
	// chunk 5: primary=c(2), replica=a(0) -> c,a
	assert.Equal(t, []int{0, 2, 3, 5}, chunksA) // primary 0,3 + replica from c: 2,5
	assert.Equal(t, []int{0, 1, 3, 4}, chunksB) // primary 1,4 + replica from a: 0,3
	assert.Equal(t, []int{1, 2, 4, 5}, chunksC) // primary 2,5 + replica from b: 1,4

	// Each chunk appears exactly RF times across all peers
	chunkCount := make(map[int]int)
	for _, idx := range chunksA {
		chunkCount[idx]++
	}
	for _, idx := range chunksB {
		chunkCount[idx]++
	}
	for _, idx := range chunksC {
		chunkCount[idx]++
	}
	for i := 0; i < 6; i++ {
		assert.Equal(t, 2, chunkCount[i], "chunk %d should appear exactly RF=2 times", i)
	}
}

func TestStripedReplicateObject_Distribution(t *testing.T) {
	// Test that ReplicateObject with striping only sends assigned chunks to each peer
	ctx := context.Background()
	broker := newTestTransportBroker()

	// Create sender coordinator
	senderTransport := broker.newTransportFor("coord-sender")
	senderS3 := newMockS3Store()

	// Create chunk data
	chunkHashes := []string{"hash-0", "hash-1", "hash-2", "hash-3", "hash-4", "hash-5"}
	chunkData := map[string][]byte{
		"hash-0": []byte("data-0"),
		"hash-1": []byte("data-1"),
		"hash-2": []byte("data-2"),
		"hash-3": []byte("data-3"),
		"hash-4": []byte("data-4"),
		"hash-5": []byte("data-5"),
	}

	senderS3.addObjectWithChunks("bucket1", "file.txt", chunkHashes, chunkData)

	// Create chunk registry - sender owns all chunks, peers own none
	registry := NewChunkRegistry("coord-sender", nil)
	for _, hash := range chunkHashes {
		_ = registry.RegisterChunk(hash, int64(len(chunkData[hash])))
	}

	sender := NewReplicator(Config{
		NodeID:        "coord-sender",
		Transport:     senderTransport,
		S3Store:       senderS3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Add two peers
	sender.AddPeer("coord-peer1")
	sender.AddPeer("coord-peer2")

	// Track chunks received by each peer via intercepting broker
	var peer1Chunks []string
	var peer2Chunks []string
	var mu sync.Mutex

	for _, peerID := range []string{"coord-peer1", "coord-peer2"} {
		peerTransport := broker.newTransportFor(peerID)
		peerS3 := newMockS3Store()
		peerRegistry := NewChunkRegistry(peerID, nil)

		capturedPeerID := peerID
		receiver := NewReplicator(Config{
			NodeID:        peerID,
			Transport:     peerTransport,
			S3Store:       peerS3,
			ChunkRegistry: peerRegistry,
			Logger:        zerolog.Nop(),
		})
		_ = receiver

		// Wrap the handler in the broker to intercept messages
		broker.mu.Lock()
		originalHandler := broker.handlers[peerID]
		broker.handlers[peerID] = func(from string, data []byte) error {
			msg, err := UnmarshalMessage(data)
			if err == nil && msg.Type == MessageTypeReplicateChunk {
				var payload ReplicateChunkPayload
				if jsonErr := json.Unmarshal(msg.Payload, &payload); jsonErr == nil {
					mu.Lock()
					if capturedPeerID == "coord-peer1" {
						peer1Chunks = append(peer1Chunks, payload.ChunkHash)
					} else {
						peer2Chunks = append(peer2Chunks, payload.ChunkHash)
					}
					mu.Unlock()
				}
			}
			return originalHandler(from, data)
		}
		broker.mu.Unlock()
	}

	// Replicate to peer1
	err := sender.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer1")
	require.NoError(t, err)

	// Replicate to peer2
	err = sender.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer2")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// With striping (3 coordinators, RF=2), each peer should get 4 of 6 chunks
	// Total sent should be 8, not 12 (without striping both would get all 6)
	totalSent := len(peer1Chunks) + len(peer2Chunks)
	assert.Less(t, totalSent, 12, "Striping should reduce total chunks sent (got %d)", totalSent)
	assert.Greater(t, len(peer1Chunks), 0, "peer1 should receive some chunks")
	assert.Greater(t, len(peer2Chunks), 0, "peer2 should receive some chunks")
}

func TestStripedReplicateObject_SinglePeer(t *testing.T) {
	// With only one peer (besides self), all chunks should be replicated
	ctx := context.Background()
	broker := newTestTransportBroker()

	senderTransport := broker.newTransportFor("coord-sender")
	senderS3 := newMockS3Store()

	chunkHashes := []string{"hash-0", "hash-1", "hash-2"}
	chunkData := map[string][]byte{
		"hash-0": []byte("data-0"),
		"hash-1": []byte("data-1"),
		"hash-2": []byte("data-2"),
	}

	senderS3.addObjectWithChunks("bucket1", "file.txt", chunkHashes, chunkData)

	registry := NewChunkRegistry("coord-sender", nil)
	for _, hash := range chunkHashes {
		_ = registry.RegisterChunk(hash, int64(len(chunkData[hash])))
	}

	sender := NewReplicator(Config{
		NodeID:        "coord-sender",
		Transport:     senderTransport,
		S3Store:       senderS3,
		ChunkRegistry: registry,
		Logger:        zerolog.Nop(),
	})

	// Only one peer
	sender.AddPeer("coord-peer1")

	// Set up receiver
	peerTransport := broker.newTransportFor("coord-peer1")
	peerS3 := newMockS3Store()
	_ = NewReplicator(Config{
		NodeID:        "coord-peer1",
		Transport:     peerTransport,
		S3Store:       peerS3,
		ChunkRegistry: NewChunkRegistry("coord-peer1", nil),
		Logger:        zerolog.Nop(),
	})

	var receivedChunks []string
	var mu sync.Mutex

	// Wrap the handler in the broker to intercept messages
	broker.mu.Lock()
	originalHandler := broker.handlers["coord-peer1"]
	broker.handlers["coord-peer1"] = func(from string, data []byte) error {
		msg, err := UnmarshalMessage(data)
		if err == nil && msg.Type == MessageTypeReplicateChunk {
			var payload ReplicateChunkPayload
			if jsonErr := json.Unmarshal(msg.Payload, &payload); jsonErr == nil {
				mu.Lock()
				receivedChunks = append(receivedChunks, payload.ChunkHash)
				mu.Unlock()
			}
		}
		return originalHandler(from, data)
	}
	broker.mu.Unlock()

	err := sender.ReplicateObject(ctx, "bucket1", "file.txt", "coord-peer1")
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// With RF=2 and only 2 coordinators total, all chunks go to the single peer
	assert.Len(t, receivedChunks, 3, "Single peer should receive all chunks with RF=2")
}

// Erasure coding shard distribution tests

func TestShardDistributionForPeer(t *testing.T) {
	// 3 coordinators, 10 data + 3 parity shards
	peers := []string{"coord-a", "coord-b", "coord-c"}
	sp := NewStripingPolicy(peers)

	// Get distribution for each coordinator
	dataA, parityA := sp.ShardDistributionForPeer("coord-a", 10, 3)
	dataB, parityB := sp.ShardDistributionForPeer("coord-b", 10, 3)
	dataC, parityC := sp.ShardDistributionForPeer("coord-c", 10, 3)

	// Verify all data shards are distributed
	totalDataShards := len(dataA) + len(dataB) + len(dataC)
	assert.Equal(t, 10, totalDataShards, "All data shards should be distributed")

	// Verify all parity shards are distributed
	totalParityShards := len(parityA) + len(parityB) + len(parityC)
	assert.Equal(t, 3, totalParityShards, "All parity shards should be distributed")

	// Verify round-robin distribution for data shards
	// coord-a should have indices 0, 3, 6, 9 (i % 3 == 0)
	assert.Equal(t, []int{0, 3, 6, 9}, dataA, "coord-a data shards")
	assert.Equal(t, []int{1, 4, 7}, dataB, "coord-b data shards")
	assert.Equal(t, []int{2, 5, 8}, dataC, "coord-c data shards")

	// Verify parity shards are distributed (with offset to spread load)
	// With 10 data shards, parity starts at index (10+i) % 3
	// parity[0] -> (10+0) % 3 = 1 -> coord-b
	// parity[1] -> (10+1) % 3 = 2 -> coord-c
	// parity[2] -> (10+2) % 3 = 0 -> coord-a
	assert.Equal(t, []int{2}, parityA, "coord-a parity shards")
	assert.Equal(t, []int{0}, parityB, "coord-b parity shards")
	assert.Equal(t, []int{1}, parityC, "coord-c parity shards")
}

func TestShardDistributionForPeerTwoCoordinators(t *testing.T) {
	// 2 coordinators, 6 data + 2 parity shards
	peers := []string{"coord-a", "coord-b"}
	sp := NewStripingPolicy(peers)

	dataA, parityA := sp.ShardDistributionForPeer("coord-a", 6, 2)
	dataB, parityB := sp.ShardDistributionForPeer("coord-b", 6, 2)

	// Verify data distribution
	assert.Equal(t, []int{0, 2, 4}, dataA, "coord-a data shards")
	assert.Equal(t, []int{1, 3, 5}, dataB, "coord-b data shards")

	// Verify parity distribution
	// parity[0] -> (6+0) % 2 = 0 -> coord-a
	// parity[1] -> (6+1) % 2 = 1 -> coord-b
	assert.Equal(t, []int{0}, parityA, "coord-a parity shards")
	assert.Equal(t, []int{1}, parityB, "coord-b parity shards")
}

func TestShardDistributionForPeerSingleCoordinator(t *testing.T) {
	// 1 coordinator, 10 data + 3 parity shards (all on same coordinator)
	peers := []string{"coord-a"}
	sp := NewStripingPolicy(peers)

	dataA, parityA := sp.ShardDistributionForPeer("coord-a", 10, 3)

	// Single coordinator gets all shards
	assert.Equal(t, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, dataA, "coord-a should get all data shards")
	assert.Equal(t, []int{0, 1, 2}, parityA, "coord-a should get all parity shards")
}

func TestShardDistributionForPeerUnknownPeer(t *testing.T) {
	peers := []string{"coord-a", "coord-b"}
	sp := NewStripingPolicy(peers)

	data, parity := sp.ShardDistributionForPeer("coord-unknown", 10, 3)

	assert.Nil(t, data, "Unknown peer should get no data shards")
	assert.Nil(t, parity, "Unknown peer should get no parity shards")
}

func TestShardDistributionForPeerEmptyPeers(t *testing.T) {
	sp := NewStripingPolicy([]string{})

	data, parity := sp.ShardDistributionForPeer("coord-a", 10, 3)

	assert.Nil(t, data, "Empty peer list should return nil data shards")
	assert.Nil(t, parity, "Empty peer list should return nil parity shards")
}

func TestGetShardOwner(t *testing.T) {
	peers := []string{"coord-a", "coord-b", "coord-c"}
	sp := NewStripingPolicy(peers)

	// Test data shard ownership (round-robin)
	assert.Equal(t, "coord-a", sp.GetShardOwner(0, false, 10))
	assert.Equal(t, "coord-b", sp.GetShardOwner(1, false, 10))
	assert.Equal(t, "coord-c", sp.GetShardOwner(2, false, 10))
	assert.Equal(t, "coord-a", sp.GetShardOwner(3, false, 10))

	// Test parity shard ownership (offset round-robin)
	// parity[0] with 10 data shards -> (10+0) % 3 = 1 -> coord-b
	// parity[1] with 10 data shards -> (10+1) % 3 = 2 -> coord-c
	// parity[2] with 10 data shards -> (10+2) % 3 = 0 -> coord-a
	assert.Equal(t, "coord-b", sp.GetShardOwner(0, true, 10))
	assert.Equal(t, "coord-c", sp.GetShardOwner(1, true, 10))
	assert.Equal(t, "coord-a", sp.GetShardOwner(2, true, 10))
}

func TestGetShardOwnerEmptyPeers(t *testing.T) {
	sp := NewStripingPolicy([]string{})

	owner := sp.GetShardOwner(0, false, 10)
	assert.Equal(t, "", owner, "Empty peer list should return empty owner")
}

func TestShardDistributionBalanced(t *testing.T) {
	// Test that shard distribution is reasonably balanced across coordinators
	peers := []string{"coord-a", "coord-b", "coord-c", "coord-d"}
	sp := NewStripingPolicy(peers)

	// 20 data + 5 parity shards across 4 coordinators
	distributions := make(map[string]int)

	for _, peer := range peers {
		data, parity := sp.ShardDistributionForPeer(peer, 20, 5)
		distributions[peer] = len(data) + len(parity)
	}

	// Each coordinator should get roughly 25/4 = 6-7 shards
	for peer, count := range distributions {
		assert.GreaterOrEqual(t, count, 5, "peer %s should get at least 5 shards", peer)
		assert.LessOrEqual(t, count, 7, "peer %s should get at most 7 shards", peer)
	}

	// Total should be 25 shards
	total := 0
	for _, count := range distributions {
		total += count
	}
	assert.Equal(t, 25, total, "Total shard count should be 25")
}

func TestShardDistributionSeparation(t *testing.T) {
	// Verify that no single coordinator has all data or all parity shards
	peers := []string{"coord-a", "coord-b", "coord-c"}
	sp := NewStripingPolicy(peers)

	for _, peer := range peers {
		data, parity := sp.ShardDistributionForPeer(peer, 10, 3)

		// No single coordinator should have all 10 data shards
		assert.Less(t, len(data), 10, "peer %s should not have all data shards", peer)

		// No single coordinator should have all 3 parity shards
		assert.Less(t, len(parity), 3, "peer %s should not have all parity shards", peer)

		// Most importantly, no coordinator should have both all data AND all parity
		if len(data) == 10 {
			assert.Less(t, len(parity), 3, "peer %s has all data shards, so should not have all parity shards", peer)
		}
	}
}
