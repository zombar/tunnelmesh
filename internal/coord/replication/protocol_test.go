package replication

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReplicateMessage(t *testing.T) {
	payload := ReplicatePayload{
		Bucket:        "system",
		Key:           "test.json",
		Data:          []byte(`{"key":"value"}`),
		VersionVector: VersionVector{"coord1": 1},
		ContentType:   "application/json",
		Metadata:      map[string]string{"author": "test"},
	}

	msg, err := NewReplicateMessage("msg-001", "coord1.tunnelmesh:8443", payload)
	require.NoError(t, err)

	assert.Equal(t, MessageTypeReplicate, msg.Type)
	assert.Equal(t, "msg-001", msg.ID)
	assert.Equal(t, "coord1.tunnelmesh:8443", msg.From)
	assert.NotEmpty(t, msg.Payload)

	// Verify we can decode it back
	decoded, err := msg.DecodeReplicatePayload()
	require.NoError(t, err)
	assert.Equal(t, payload.Bucket, decoded.Bucket)
	assert.Equal(t, payload.Key, decoded.Key)
	assert.Equal(t, payload.Data, decoded.Data)
	assert.Equal(t, payload.ContentType, decoded.ContentType)
	assert.Equal(t, payload.Metadata, decoded.Metadata)
	assert.True(t, payload.VersionVector.Equal(decoded.VersionVector))
}

func TestNewAckMessage(t *testing.T) {
	payload := AckPayload{
		ReplicateID:   "msg-001",
		Success:       true,
		VersionVector: VersionVector{"coord1": 1, "coord2": 1},
	}

	msg, err := NewAckMessage("ack-001", "coord2.tunnelmesh:8443", payload)
	require.NoError(t, err)

	assert.Equal(t, MessageTypeAck, msg.Type)
	assert.Equal(t, "ack-001", msg.ID)
	assert.Equal(t, "coord2.tunnelmesh:8443", msg.From)

	// Verify we can decode it back
	decoded, err := msg.DecodeAckPayload()
	require.NoError(t, err)
	assert.Equal(t, payload.ReplicateID, decoded.ReplicateID)
	assert.Equal(t, payload.Success, decoded.Success)
	assert.Equal(t, payload.ErrorMessage, decoded.ErrorMessage)
	assert.True(t, payload.VersionVector.Equal(decoded.VersionVector))
}

func TestNewAckMessage_WithError(t *testing.T) {
	payload := AckPayload{
		ReplicateID:   "msg-001",
		Success:       false,
		ErrorMessage:  "conflict detected",
		VersionVector: VersionVector{"coord1": 1, "coord2": 2},
	}

	msg, err := NewAckMessage("ack-002", "coord2.tunnelmesh:8443", payload)
	require.NoError(t, err)

	decoded, err := msg.DecodeAckPayload()
	require.NoError(t, err)
	assert.False(t, decoded.Success)
	assert.Equal(t, "conflict detected", decoded.ErrorMessage)
}

func TestNewSyncRequestMessage(t *testing.T) {
	payload := SyncRequestPayload{
		RequestedBuckets: []string{"system", "user"},
	}

	msg, err := NewSyncRequestMessage("sync-001", "coord3.tunnelmesh:8443", payload)
	require.NoError(t, err)

	assert.Equal(t, MessageTypeSyncRequest, msg.Type)
	assert.Equal(t, "sync-001", msg.ID)

	// Verify we can decode it back
	decoded, err := msg.DecodeSyncRequestPayload()
	require.NoError(t, err)
	assert.Equal(t, payload.RequestedBuckets, decoded.RequestedBuckets)
}

func TestNewSyncRequestMessage_AllBuckets(t *testing.T) {
	payload := SyncRequestPayload{
		RequestedBuckets: nil, // Empty means all buckets
	}

	msg, err := NewSyncRequestMessage("sync-002", "coord3.tunnelmesh:8443", payload)
	require.NoError(t, err)

	decoded, err := msg.DecodeSyncRequestPayload()
	require.NoError(t, err)
	assert.Nil(t, decoded.RequestedBuckets)
}

func TestNewSyncResponseMessage(t *testing.T) {
	payload := SyncResponsePayload{
		StateSnapshot: []byte(`{"node_id":"coord1","vectors":{}}`),
		Objects: []SyncObjectEntry{
			{
				Bucket:        "system",
				Key:           "config.json",
				Data:          []byte(`{"setting":"value"}`),
				VersionVector: VersionVector{"coord1": 2},
				ContentType:   "application/json",
			},
			{
				Bucket:        "user",
				Key:           "data.json",
				Data:          []byte(`{"user":"test"}`),
				VersionVector: VersionVector{"coord1": 1, "coord2": 1},
			},
		},
	}

	msg, err := NewSyncResponseMessage("sync-resp-001", "coord1.tunnelmesh:8443", payload)
	require.NoError(t, err)

	assert.Equal(t, MessageTypeSyncResponse, msg.Type)

	// Verify we can decode it back
	decoded, err := msg.DecodeSyncResponsePayload()
	require.NoError(t, err)
	assert.Equal(t, payload.StateSnapshot, decoded.StateSnapshot)
	assert.Len(t, decoded.Objects, 2)
	assert.Equal(t, "system", decoded.Objects[0].Bucket)
	assert.Equal(t, "config.json", decoded.Objects[0].Key)
	assert.Equal(t, "user", decoded.Objects[1].Bucket)
}

func TestMessage_Marshal_Unmarshal(t *testing.T) {
	original := &Message{
		Type: MessageTypeReplicate,
		ID:   "test-123",
		From: "coord1.tunnelmesh:8443",
		Payload: []byte(`{
			"bucket": "system",
			"key": "test.json",
			"data": "dGVzdA==",
			"version_vector": {"coord1": 5}
		}`),
	}

	// Marshal to JSON
	data, err := original.Marshal()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal back
	unmarshaled, err := UnmarshalMessage(data)
	require.NoError(t, err)

	assert.Equal(t, original.Type, unmarshaled.Type)
	assert.Equal(t, original.ID, unmarshaled.ID)
	assert.Equal(t, original.From, unmarshaled.From)
}

func TestMessage_DecodeWrongType(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeReplicate,
		ID:      "test",
		From:    "coord1",
		Payload: []byte(`{}`),
	}

	// Try to decode as wrong type
	_, err := msg.DecodeAckPayload()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not ack")

	_, err = msg.DecodeSyncRequestPayload()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not sync_request")

	_, err = msg.DecodeSyncResponsePayload()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not sync_response")
}

func TestMessage_DecodeInvalidJSON(t *testing.T) {
	msg := &Message{
		Type:    MessageTypeReplicate,
		ID:      "test",
		From:    "coord1",
		Payload: []byte(`invalid json`),
	}

	_, err := msg.DecodeReplicatePayload()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestReplicatePayload_WithMetadata(t *testing.T) {
	payload := ReplicatePayload{
		Bucket: "system",
		Key:    "config.json",
		Data:   []byte(`{"config": "value"}`),
		VersionVector: VersionVector{
			"coord1.tunnelmesh:8443": 1,
			"coord2.tunnelmesh:8443": 2,
		},
		ContentType: "application/json",
		Metadata: map[string]string{
			"author":    "admin",
			"timestamp": "2024-01-01T00:00:00Z",
			"checksum":  "abc123",
		},
	}

	msg, err := NewReplicateMessage("msg-123", "coord1.tunnelmesh:8443", payload)
	require.NoError(t, err)

	decoded, err := msg.DecodeReplicatePayload()
	require.NoError(t, err)

	assert.Equal(t, "application/json", decoded.ContentType)
	assert.Len(t, decoded.Metadata, 3)
	assert.Equal(t, "admin", decoded.Metadata["author"])
	assert.Equal(t, "2024-01-01T00:00:00Z", decoded.Metadata["timestamp"])
	assert.Equal(t, "abc123", decoded.Metadata["checksum"])
}

func TestSyncObjectEntry_CompleteRoundTrip(t *testing.T) {
	entries := []SyncObjectEntry{
		{
			Bucket:        "system",
			Key:           "peer_registry.json",
			Data:          []byte(`{"peers": []}`),
			VersionVector: VersionVector{"coord1": 5, "coord2": 3},
			ContentType:   "application/json",
			Metadata: map[string]string{
				"last_modified": "2024-01-01",
			},
		},
		{
			Bucket:        "user",
			Key:           "uploads/file.bin",
			Data:          []byte{0x00, 0x01, 0x02, 0xFF}, // Binary data
			VersionVector: VersionVector{"coord2": 1},
			ContentType:   "application/octet-stream",
		},
	}

	payload := SyncResponsePayload{
		StateSnapshot: []byte(`{"test": "snapshot"}`),
		Objects:       entries,
	}

	msg, err := NewSyncResponseMessage("sync-123", "coord1", payload)
	require.NoError(t, err)

	decoded, err := msg.DecodeSyncResponsePayload()
	require.NoError(t, err)

	require.Len(t, decoded.Objects, 2)

	// Verify first entry
	assert.Equal(t, entries[0].Bucket, decoded.Objects[0].Bucket)
	assert.Equal(t, entries[0].Key, decoded.Objects[0].Key)
	assert.Equal(t, entries[0].Data, decoded.Objects[0].Data)
	assert.Equal(t, entries[0].ContentType, decoded.Objects[0].ContentType)
	assert.True(t, entries[0].VersionVector.Equal(decoded.Objects[0].VersionVector))
	assert.Equal(t, entries[0].Metadata, decoded.Objects[0].Metadata)

	// Verify second entry (binary data)
	assert.Equal(t, entries[1].Data, decoded.Objects[1].Data)
}

func TestMessageType_Constants(t *testing.T) {
	// Verify message type constants are distinct
	types := map[MessageType]bool{
		MessageTypeReplicate:    true,
		MessageTypeAck:          true,
		MessageTypeSyncRequest:  true,
		MessageTypeSyncResponse: true,
	}

	assert.Len(t, types, 4, "All message types should be distinct")

	// Verify string values
	assert.Equal(t, MessageType("replicate"), MessageTypeReplicate)
	assert.Equal(t, MessageType("ack"), MessageTypeAck)
	assert.Equal(t, MessageType("sync_request"), MessageTypeSyncRequest)
	assert.Equal(t, MessageType("sync_response"), MessageTypeSyncResponse)
}
