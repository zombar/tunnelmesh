package replication

import (
	"context"
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
	mu      sync.Mutex
	objects map[string]mockS3Object // map["bucket/key"]object
	putErr  error                   // If set, Put will return this error
}

type mockS3Object struct {
	data        []byte
	contentType string
	metadata    map[string]string
}

func newMockS3Store() *mockS3Store {
	return &mockS3Store{
		objects: make(map[string]mockS3Object),
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
