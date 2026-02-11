package replication

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the current replication protocol version.
// Version history:
//   - v1: Initial implementation with version vectors and conflict resolution
const ProtocolVersion = 1

// MessageType identifies the type of replication message.
type MessageType string

const (
	// MessageTypeReplicate is sent when a coordinator wants to replicate data to another coordinator
	MessageTypeReplicate MessageType = "replicate"

	// MessageTypeAck is sent in response to a successful replication
	MessageTypeAck MessageType = "ack"

	// MessageTypeSyncRequest is sent to request a full sync of all state
	MessageTypeSyncRequest MessageType = "sync_request"

	// MessageTypeSyncResponse contains the full state snapshot in response to a sync request
	MessageTypeSyncResponse MessageType = "sync_response"
)

// Message is the envelope for all replication protocol messages.
type Message struct {
	Version int             `json:"version"` // Protocol version for compatibility checking
	Type    MessageType     `json:"type"`
	ID      string          `json:"id"`      // Unique message ID for tracking ACKs
	From    string          `json:"from"`    // Sender's coordinator address
	Payload json.RawMessage `json:"payload"` // Type-specific payload
}

// ReplicatePayload contains the data for a replication operation.
type ReplicatePayload struct {
	Bucket        string            `json:"bucket"`
	Key           string            `json:"key"`
	Data          []byte            `json:"data"`           // S3 object data
	VersionVector VersionVector     `json:"version_vector"` // Version vector for conflict detection
	ContentType   string            `json:"content_type,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// AckPayload contains the response to a replication operation.
type AckPayload struct {
	ReplicateID   string        `json:"replicate_id"` // ID of the replicate message being acked
	Success       bool          `json:"success"`
	ErrorMessage  string        `json:"error_message,omitempty"`
	VersionVector VersionVector `json:"version_vector"` // Final version vector after merge
}

// SyncRequestPayload requests a full state sync.
type SyncRequestPayload struct {
	RequestedBuckets []string `json:"requested_buckets,omitempty"` // Empty means all buckets
}

// SyncResponsePayload contains the full state snapshot.
type SyncResponsePayload struct {
	StateSnapshot []byte            `json:"state_snapshot"` // Serialized replication state
	Objects       []SyncObjectEntry `json:"objects"`        // All S3 objects
}

// SyncObjectEntry represents a single S3 object in a sync response.
type SyncObjectEntry struct {
	Bucket        string            `json:"bucket"`
	Key           string            `json:"key"`
	Data          []byte            `json:"data"`
	VersionVector VersionVector     `json:"version_vector"`
	ContentType   string            `json:"content_type,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// NewReplicateMessage creates a new replication message.
func NewReplicateMessage(id, from string, payload ReplicatePayload) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal replicate payload: %w", err)
	}

	return &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeReplicate,
		ID:      id,
		From:    from,
		Payload: data,
	}, nil
}

// NewAckMessage creates a new acknowledgment message.
func NewAckMessage(id, from string, payload AckPayload) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal ack payload: %w", err)
	}

	return &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeAck,
		ID:      id,
		From:    from,
		Payload: data,
	}, nil
}

// NewSyncRequestMessage creates a new sync request message.
func NewSyncRequestMessage(id, from string, payload SyncRequestPayload) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal sync request payload: %w", err)
	}

	return &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeSyncRequest,
		ID:      id,
		From:    from,
		Payload: data,
	}, nil
}

// NewSyncResponseMessage creates a new sync response message.
func NewSyncResponseMessage(id, from string, payload SyncResponsePayload) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal sync response payload: %w", err)
	}

	return &Message{
		Version: ProtocolVersion,
		Type:    MessageTypeSyncResponse,
		ID:      id,
		From:    from,
		Payload: data,
	}, nil
}

// DecodeReplicatePayload decodes a replicate payload from a message.
func (m *Message) DecodeReplicatePayload() (*ReplicatePayload, error) {
	if m.Type != MessageTypeReplicate {
		return nil, fmt.Errorf("message type is %s, not replicate", m.Type)
	}

	var payload ReplicatePayload
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal replicate payload: %w", err)
	}

	return &payload, nil
}

// DecodeAckPayload decodes an ack payload from a message.
func (m *Message) DecodeAckPayload() (*AckPayload, error) {
	if m.Type != MessageTypeAck {
		return nil, fmt.Errorf("message type is %s, not ack", m.Type)
	}

	var payload AckPayload
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal ack payload: %w", err)
	}

	return &payload, nil
}

// DecodeSyncRequestPayload decodes a sync request payload from a message.
func (m *Message) DecodeSyncRequestPayload() (*SyncRequestPayload, error) {
	if m.Type != MessageTypeSyncRequest {
		return nil, fmt.Errorf("message type is %s, not sync_request", m.Type)
	}

	var payload SyncRequestPayload
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal sync request payload: %w", err)
	}

	return &payload, nil
}

// DecodeSyncResponsePayload decodes a sync response payload from a message.
func (m *Message) DecodeSyncResponsePayload() (*SyncResponsePayload, error) {
	if m.Type != MessageTypeSyncResponse {
		return nil, fmt.Errorf("message type is %s, not sync_response", m.Type)
	}

	var payload SyncResponsePayload
	if err := json.Unmarshal(m.Payload, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal sync response payload: %w", err)
	}

	return &payload, nil
}

// Marshal serializes the message to JSON.
func (m *Message) Marshal() ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return data, nil
}

// UnmarshalMessage deserializes a message from JSON.
func UnmarshalMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	// Version 0 is treated as version 1 for backward compatibility during rollout
	// Once all coordinators are upgraded, this fallback can be removed
	if msg.Version == 0 {
		msg.Version = 1
	}

	// Check protocol version compatibility
	if msg.Version != ProtocolVersion {
		return nil, fmt.Errorf("incompatible protocol version: got %d, expected %d", msg.Version, ProtocolVersion)
	}

	return &msg, nil
}
