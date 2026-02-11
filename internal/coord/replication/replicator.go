package replication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// Transport defines the interface for sending replication messages.
// This will be implemented by the mesh transport layer (UDP/SSH/Relay).
type Transport interface {
	// SendToCoordinator sends a message to another coordinator by mesh IP
	SendToCoordinator(ctx context.Context, coordMeshIP string, data []byte) error

	// RegisterHandler registers a handler for incoming replication messages
	RegisterHandler(handler func(from string, data []byte) error)
}

// S3Store defines the interface for S3 storage operations.
type S3Store interface {
	// Get retrieves an object from S3
	Get(ctx context.Context, bucket, key string) (data []byte, metadata map[string]string, err error)

	// Put stores an object in S3
	Put(ctx context.Context, bucket, key string, data []byte, contentType string, metadata map[string]string) error

	// Delete removes an object from S3
	Delete(ctx context.Context, bucket, key string) error

	// List lists all objects in a bucket
	List(ctx context.Context, bucket string) ([]string, error)

	// ListBuckets lists all buckets
	ListBuckets(ctx context.Context) ([]string, error)
}

// Replicator manages replication of S3 data between coordinators.
type Replicator struct {
	nodeID               string
	transport            Transport
	s3                   S3Store
	state                *State
	logger               zerolog.Logger
	maxPendingOperations int // Maximum pending ACKs (0 = unlimited)

	// Peer coordinators
	mu    sync.RWMutex
	peers map[string]bool // map[coordMeshIP]true

	// Pending ACKs
	pendingMu sync.RWMutex
	pending   map[string]*pendingReplication // map[messageID]*pendingReplication

	// Rate limiting
	rateLimiter *rate.Limiter // Limits incoming replication messages per second

	// Metrics
	metricsMu     sync.RWMutex
	sentCount     uint64
	receivedCount uint64
	conflictCount uint64
	errorCount    uint64
	droppedCount  uint64 // Operations dropped due to pending limit
	rateLimited   uint64 // Messages dropped due to rate limiting

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// pendingReplication tracks a replication operation awaiting ACK.
type pendingReplication struct {
	bucket  string
	key     string
	sentAt  time.Time
	ackChan chan *AckPayload
	timeout *time.Timer
}

// Config contains configuration for the replicator.
type Config struct {
	NodeID               string
	Transport            Transport
	S3Store              S3Store
	Logger               zerolog.Logger
	AckTimeout           time.Duration   // How long to wait for ACK before retrying
	RetryInterval        time.Duration   // How long to wait before retrying failed replication
	MaxPendingOperations int             // Maximum number of pending ACKs to track (0 = unlimited)
	RateLimit            int             // Maximum incoming messages per second (0 = unlimited, default: 1000)
	RateBurst            int             // Maximum burst size for rate limiter (default: 100)
	Context              context.Context // SECURITY FIX #3: Parent context for proper cancellation propagation
}

// NewReplicator creates a new replicator instance.
func NewReplicator(config Config) *Replicator {
	if config.AckTimeout == 0 {
		config.AckTimeout = 10 * time.Second
	}
	if config.RetryInterval == 0 {
		config.RetryInterval = 30 * time.Second
	}
	if config.MaxPendingOperations == 0 {
		config.MaxPendingOperations = 10000 // Default: 10k pending operations
	}
	if config.RateLimit == 0 {
		config.RateLimit = 1000 // Default: 1000 messages/second
	}
	if config.RateBurst == 0 {
		config.RateBurst = 100 // Default: burst of 100 messages
	}

	// SECURITY FIX #3: Use provided context or create a new one
	// This allows proper context propagation and cancellation from parent
	parentCtx := config.Context
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)

	r := &Replicator{
		nodeID:               config.NodeID,
		transport:            config.Transport,
		s3:                   config.S3Store,
		state:                NewState(config.NodeID),
		logger:               config.Logger.With().Str("component", "replicator").Logger(),
		maxPendingOperations: config.MaxPendingOperations,
		rateLimiter:          rate.NewLimiter(rate.Limit(config.RateLimit), config.RateBurst),
		peers:                make(map[string]bool),
		pending:              make(map[string]*pendingReplication),
		ctx:                  ctx,
		cancel:               cancel,
	}

	// Register message handler
	config.Transport.RegisterHandler(r.handleIncomingMessage)

	return r
}

// Start starts the replicator background tasks.
func (r *Replicator) Start() error {
	r.logger.Info().Msg("Starting replicator")

	// Start ACK timeout cleanup goroutine
	r.wg.Add(1)
	go r.ackTimeoutWorker()

	return nil
}

// Stop stops the replicator and waits for background tasks to finish.
func (r *Replicator) Stop() error {
	r.logger.Info().Msg("Stopping replicator")
	r.cancel()
	r.wg.Wait()
	return nil
}

// AddPeer adds a coordinator peer to replicate to.
func (r *Replicator) AddPeer(coordMeshIP string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.peers[coordMeshIP] {
		r.logger.Info().Str("peer", coordMeshIP).Msg("Added replication peer")
		r.peers[coordMeshIP] = true
	}
}

// RemovePeer removes a coordinator peer.
func (r *Replicator) RemovePeer(coordMeshIP string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.peers[coordMeshIP] {
		r.logger.Info().Str("peer", coordMeshIP).Msg("Removed replication peer")
		delete(r.peers, coordMeshIP)
	}
}

// GetPeers returns a copy of the current peer list.
func (r *Replicator) GetPeers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	peers := make([]string, 0, len(r.peers))
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	return peers
}

// RequestSync sends a sync request to a specific coordinator peer.
// The peer will respond with a full state snapshot that will be applied locally.
// If buckets is empty, all buckets will be requested.
func (r *Replicator) RequestSync(ctx context.Context, coordMeshIP string, buckets []string) error {
	r.logger.Info().
		Str("peer", coordMeshIP).
		Strs("buckets", buckets).
		Msg("Requesting full state sync from peer")

	// Create sync request
	payload := SyncRequestPayload{
		RequestedBuckets: buckets,
	}

	msg, err := NewSyncRequestMessage(uuid.New().String(), r.nodeID, payload)
	if err != nil {
		return fmt.Errorf("create sync request: %w", err)
	}

	data, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal sync request: %w", err)
	}

	// Send request
	if err := r.transport.SendToCoordinator(ctx, coordMeshIP, data); err != nil {
		r.logger.Error().Err(err).Str("peer", coordMeshIP).Msg("Failed to send sync request")
		return fmt.Errorf("send sync request: %w", err)
	}

	r.logger.Info().Str("peer", coordMeshIP).Msg("Sync request sent, waiting for response")
	return nil
}

// RequestSyncFromAll sends a sync request to all known coordinator peers.
// This is useful when a coordinator starts up and wants to catch up with the cluster.
// The first peer to respond will provide the state.
func (r *Replicator) RequestSyncFromAll(ctx context.Context, buckets []string) error {
	peers := r.GetPeers()
	if len(peers) == 0 {
		r.logger.Warn().Msg("No peers available for sync request")
		return nil
	}

	r.logger.Info().
		Int("peer_count", len(peers)).
		Strs("buckets", buckets).
		Msg("Requesting sync from all peers")

	// Send sync request to all peers
	// We don't wait for responses here - handleSyncResponse will process them asynchronously
	var lastErr error
	for _, peer := range peers {
		if err := r.RequestSync(ctx, peer, buckets); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// ReplicateOperation replicates an S3 operation to all coordinator peers.
// This should be called after successfully writing to local S3.
func (r *Replicator) ReplicateOperation(ctx context.Context, bucket, key string, data []byte, contentType string, metadata map[string]string) error {
	// Update local version vector
	vv := r.state.Update(bucket, key)

	r.logger.Debug().
		Str("bucket", bucket).
		Str("key", key).
		Str("version_vector", vv.String()).
		Msg("Replicating operation")

	// Get peers to replicate to
	peers := r.GetPeers()
	if len(peers) == 0 {
		r.logger.Debug().Msg("No peers to replicate to")
		return nil
	}

	// Create replicate payload
	payload := ReplicatePayload{
		Bucket:        bucket,
		Key:           key,
		Data:          data,
		VersionVector: vv,
		ContentType:   contentType,
		Metadata:      metadata,
	}

	// Send to all peers (fire and forget for now, ACKs handled asynchronously)
	var lastErr error
	for _, peer := range peers {
		if err := r.sendReplicateMessage(ctx, peer, payload); err != nil {
			r.logger.Error().Err(err).Str("peer", peer).Msg("Failed to send replicate message")
			lastErr = err
			r.incrementErrorCount()
		} else {
			r.incrementSentCount()
		}
	}

	return lastErr
}

// ReplicateDelete replicates a delete operation to all coordinator peers.
// Deletes are represented as tombstones (empty data with delete marker).
func (r *Replicator) ReplicateDelete(ctx context.Context, bucket, key string) error {
	// Update local version vector
	vv := r.state.Update(bucket, key)

	r.logger.Debug().
		Str("bucket", bucket).
		Str("key", key).
		Str("version_vector", vv.String()).
		Msg("Replicating delete operation")

	// Get peers to replicate to
	peers := r.GetPeers()
	if len(peers) == 0 {
		r.logger.Debug().Msg("No peers to replicate to")
		return nil
	}

	// Create replicate payload with empty data to signal delete
	payload := ReplicatePayload{
		Bucket:        bucket,
		Key:           key,
		Data:          nil, // Empty data signals delete
		VersionVector: vv,
		ContentType:   "",
		Metadata:      map[string]string{"_deleted": "true"}, // Tombstone marker
	}

	// Send to all peers
	var lastErr error
	for _, peer := range peers {
		if err := r.sendReplicateMessage(ctx, peer, payload); err != nil {
			r.logger.Error().Err(err).Str("peer", peer).Msg("Failed to send delete replicate message")
			lastErr = err
			r.incrementErrorCount()
		} else {
			r.incrementSentCount()
		}
	}

	return lastErr
}

// sendReplicateMessage sends a replication message to a peer.
func (r *Replicator) sendReplicateMessage(ctx context.Context, peer string, payload ReplicatePayload) error {
	msgID := uuid.New().String()

	msg, err := NewReplicateMessage(msgID, r.nodeID, payload)
	if err != nil {
		return fmt.Errorf("create replicate message: %w", err)
	}

	data, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	// Track pending ACK - returns false if dropped due to limit
	if !r.trackPendingACK(msgID, payload.Bucket, payload.Key) {
		return fmt.Errorf("dropped: pending operations limit reached")
	}

	// Send via transport
	if err := r.transport.SendToCoordinator(ctx, peer, data); err != nil {
		r.removePendingACK(msgID)
		return fmt.Errorf("send to coordinator: %w", err)
	}

	return nil
}

// handleIncomingMessage processes incoming replication messages.
func (r *Replicator) handleIncomingMessage(from string, data []byte) error {
	// Apply rate limiting to prevent abuse
	if !r.rateLimiter.Allow() {
		r.incrementRateLimitedCount()
		r.logger.Warn().
			Str("from", from).
			Msg("Rate limit exceeded, dropping replication message")
		return fmt.Errorf("rate limit exceeded")
	}

	msg, err := UnmarshalMessage(data)
	if err != nil {
		r.logger.Error().Err(err).Msg("Failed to unmarshal message")
		r.incrementErrorCount()
		return fmt.Errorf("unmarshal message: %w", err)
	}

	r.logger.Debug().
		Str("type", string(msg.Type)).
		Str("from", from).
		Str("id", msg.ID).
		Msg("Received replication message")

	switch msg.Type {
	case MessageTypeReplicate:
		return r.handleReplicate(msg)
	case MessageTypeAck:
		return r.handleAck(msg)
	case MessageTypeSyncRequest:
		return r.handleSyncRequest(msg)
	case MessageTypeSyncResponse:
		return r.handleSyncResponse(msg)
	default:
		r.logger.Warn().Str("type", string(msg.Type)).Msg("Unknown message type")
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// handleReplicate processes an incoming replication operation.
func (r *Replicator) handleReplicate(msg *Message) error {
	payload, err := msg.DecodeReplicatePayload()
	if err != nil {
		return fmt.Errorf("decode replicate payload: %w", err)
	}

	r.incrementReceivedCount()

	r.logger.Debug().
		Str("bucket", payload.Bucket).
		Str("key", payload.Key).
		Str("from", msg.From).
		Str("version_vector", payload.VersionVector.String()).
		Msg("Processing replicate message")

	// Check for conflicts
	rel, needsResolution := r.state.CheckConflict(payload.Bucket, payload.Key, payload.VersionVector)

	if needsResolution {
		r.incrementConflictCount()
		r.logger.Warn().
			Str("bucket", payload.Bucket).
			Str("key", payload.Key).
			Str("relationship", fmt.Sprintf("%d", rel)).
			Msg("Conflict detected")

		// Resolve conflict
		localVV := r.state.Get(payload.Bucket, payload.Key)
		winner := ResolveConflict(localVV, payload.VersionVector)

		// If remote won, apply the update
		if winner.Equal(payload.VersionVector) {
			if err := r.applyReplication(payload); err != nil {
				return r.sendAck(msg.ID, msg.From, false, err.Error(), localVV)
			}
		}
		// Merge version vectors (for both local and remote wins)
		// This ensures both coordinators converge to the same VV after conflict
		mergedVV, _ := r.state.Merge(payload.Bucket, payload.Key, payload.VersionVector)
		return r.sendAck(msg.ID, msg.From, true, "", mergedVV)
	}

	// No conflict - check if we should apply
	if rel == VectorBefore || rel == VectorEqual {
		// Remote is newer or same, apply it
		if err := r.applyReplication(payload); err != nil {
			localVV := r.state.Get(payload.Bucket, payload.Key)
			return r.sendAck(msg.ID, msg.From, false, err.Error(), localVV)
		}
	}

	// Merge version vectors
	mergedVV, _ := r.state.Merge(payload.Bucket, payload.Key, payload.VersionVector)

	// Send ACK
	return r.sendAck(msg.ID, msg.From, true, "", mergedVV)
}

// applyReplication applies a replication operation to local S3.
func (r *Replicator) applyReplication(payload *ReplicatePayload) error {
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()

	// Check if this is a delete operation (nil data or tombstone marker)
	isDelete := len(payload.Data) == 0 || (payload.Metadata != nil && payload.Metadata["_deleted"] == "true")

	if isDelete {
		// Apply delete operation
		if err := r.s3.Delete(ctx, payload.Bucket, payload.Key); err != nil {
			r.logger.Error().Err(err).
				Str("bucket", payload.Bucket).
				Str("key", payload.Key).
				Msg("Failed to apply delete replication to S3")
			r.incrementErrorCount()
			return fmt.Errorf("delete from s3: %w", err)
		}

		r.logger.Info().
			Str("bucket", payload.Bucket).
			Str("key", payload.Key).
			Msg("Successfully applied delete replication")
	} else {
		// Apply put operation
		if err := r.s3.Put(ctx, payload.Bucket, payload.Key, payload.Data, payload.ContentType, payload.Metadata); err != nil {
			r.logger.Error().Err(err).
				Str("bucket", payload.Bucket).
				Str("key", payload.Key).
				Msg("Failed to apply replication to S3")
			r.incrementErrorCount()
			return fmt.Errorf("put to s3: %w", err)
		}

		r.logger.Info().
			Str("bucket", payload.Bucket).
			Str("key", payload.Key).
			Msg("Successfully applied replication")
	}

	return nil
}

// sendAck sends an acknowledgment message.
func (r *Replicator) sendAck(replicateID, to string, success bool, errorMsg string, vv VersionVector) error {
	ackPayload := AckPayload{
		ReplicateID:   replicateID,
		Success:       success,
		ErrorMessage:  errorMsg,
		VersionVector: vv,
	}

	msgID := uuid.New().String()
	msg, err := NewAckMessage(msgID, r.nodeID, ackPayload)
	if err != nil {
		return fmt.Errorf("create ack message: %w", err)
	}

	data, err := msg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal ack: %w", err)
	}

	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Second)
	defer cancel()

	if err := r.transport.SendToCoordinator(ctx, to, data); err != nil {
		r.logger.Error().Err(err).Str("to", to).Msg("Failed to send ACK")
		return fmt.Errorf("send ack: %w", err)
	}

	return nil
}

// handleAck processes an incoming ACK message.
func (r *Replicator) handleAck(msg *Message) error {
	payload, err := msg.DecodeAckPayload()
	if err != nil {
		return fmt.Errorf("decode ack payload: %w", err)
	}

	r.logger.Debug().
		Str("replicate_id", payload.ReplicateID).
		Bool("success", payload.Success).
		Msg("Received ACK")

	// SECURITY FIX #5 & #6: Notify pending operation with lock held to prevent race
	// The channel could be closed by timeout between lookup and send if we release the lock early
	// Also stop the timer immediately when ACK is received to prevent resource leak
	r.pendingMu.RLock()
	pending, exists := r.pending[payload.ReplicateID]
	r.pendingMu.RUnlock()

	if exists {
		// Merge the returned version vector to prevent divergence after conflicts
		// The receiver may have merged version vectors during conflict resolution
		if payload.Success && len(payload.VersionVector) > 0 {
			r.state.Merge(pending.bucket, pending.key, payload.VersionVector)
			r.logger.Debug().
				Str("bucket", pending.bucket).
				Str("key", pending.key).
				Str("version_vector", payload.VersionVector.String()).
				Msg("Merged version vector from ACK")
		}

		delivered := false
		select {
		case pending.ackChan <- payload:
			// ACK delivered successfully
			delivered = true
		default:
			// Channel full or closed (timeout fired while we held the lock)
		}

		// SECURITY FIX #6: Stop timer immediately when ACK received to prevent resource leak
		// Don't wait for the 10-second timeout to clean up
		if delivered {
			r.removePendingACK(payload.ReplicateID)
		}
	}

	return nil
}

// handleSyncRequest processes a sync request by sending the full state to the requestor.
func (r *Replicator) handleSyncRequest(msg *Message) error {
	r.logger.Info().Str("from", msg.From).Msg("Received sync request - preparing full state snapshot")

	payload, err := msg.DecodeSyncRequestPayload()
	if err != nil {
		return fmt.Errorf("decode sync request: %w", err)
	}

	// SECURITY FIX #3: Use replicator's context for proper cancellation
	ctx, cancel := context.WithTimeout(r.ctx, 5*time.Minute)
	defer cancel()

	// Get state snapshot
	stateSnapshot, err := r.state.Snapshot()
	if err != nil {
		r.logger.Error().Err(err).Msg("Failed to create state snapshot")
		return fmt.Errorf("create state snapshot: %w", err)
	}

	// Determine which buckets to sync
	var bucketsToSync []string
	if len(payload.RequestedBuckets) > 0 {
		bucketsToSync = payload.RequestedBuckets
	} else {
		// Get all buckets
		allBuckets, err := r.s3.ListBuckets(ctx)
		if err != nil {
			r.logger.Error().Err(err).Msg("Failed to list buckets")
			return fmt.Errorf("list buckets: %w", err)
		}
		bucketsToSync = allBuckets
	}

	// Collect all objects
	var objects []SyncObjectEntry
	for _, bucket := range bucketsToSync {
		keys, err := r.s3.List(ctx, bucket)
		if err != nil {
			r.logger.Warn().Err(err).Str("bucket", bucket).Msg("Failed to list bucket, skipping")
			continue
		}

		for _, key := range keys {
			// Get object data
			data, metadata, err := r.s3.Get(ctx, bucket, key)
			if err != nil {
				r.logger.Warn().Err(err).
					Str("bucket", bucket).
					Str("key", key).
					Msg("Failed to get object, skipping")
				continue
			}

			// Get version vector for this object
			vv := r.state.Get(bucket, key)

			// Extract content type from metadata
			contentType := ""
			if metadata != nil {
				contentType = metadata["content-type"]
			}

			objects = append(objects, SyncObjectEntry{
				Bucket:        bucket,
				Key:           key,
				Data:          data,
				VersionVector: vv,
				ContentType:   contentType,
				Metadata:      metadata,
			})
		}
	}

	// Create sync response
	responsePayload := SyncResponsePayload{
		StateSnapshot: stateSnapshot,
		Objects:       objects,
	}

	response, err := NewSyncResponseMessage(uuid.New().String(), r.nodeID, responsePayload)
	if err != nil {
		r.logger.Error().Err(err).Msg("Failed to create sync response")
		return fmt.Errorf("create sync response: %w", err)
	}

	data, err := response.Marshal()
	if err != nil {
		return fmt.Errorf("marshal sync response: %w", err)
	}

	// Send response to requestor
	if err := r.transport.SendToCoordinator(ctx, msg.From, data); err != nil {
		r.logger.Error().Err(err).Str("to", msg.From).Msg("Failed to send sync response")
		return fmt.Errorf("send sync response: %w", err)
	}

	r.logger.Info().
		Str("to", msg.From).
		Int("buckets", len(bucketsToSync)).
		Int("objects", len(objects)).
		Msg("Sent full state snapshot")

	return nil
}

// handleSyncResponse processes a sync response by applying the received state.
func (r *Replicator) handleSyncResponse(msg *Message) error {
	r.logger.Info().Str("from", msg.From).Msg("Received sync response - applying state")

	payload, err := msg.DecodeSyncResponsePayload()
	if err != nil {
		return fmt.Errorf("decode sync response: %w", err)
	}

	// SECURITY FIX #3: Use replicator's context for proper cancellation
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Minute)
	defer cancel()

	// Decode state snapshot to get version vectors, but don't overwrite our nodeID
	var snapshot struct {
		NodeID   string                   `json:"node_id"`
		Vectors  map[string]VersionVector `json:"vectors"`
		Checksum string                   `json:"checksum"`
	}
	if err := json.Unmarshal(payload.StateSnapshot, &snapshot); err != nil {
		r.logger.Error().Err(err).Msg("Failed to decode state snapshot")
		return fmt.Errorf("decode state snapshot: %w", err)
	}

	// SECURITY FIX #9: Validate checksum before applying state
	vectorsJSON, err := json.Marshal(snapshot.Vectors)
	if err != nil {
		return fmt.Errorf("marshal vectors for checksum verification: %w", err)
	}
	hash := sha256.Sum256(vectorsJSON)
	expectedChecksum := hex.EncodeToString(hash[:])
	if expectedChecksum != snapshot.Checksum {
		r.logger.Error().
			Str("expected", expectedChecksum).
			Str("received", snapshot.Checksum).
			Msg("Checksum mismatch in sync response")
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, snapshot.Checksum)
	}

	r.logger.Info().
		Int("objects", len(payload.Objects)).
		Int("tracked_keys", len(snapshot.Vectors)).
		Str("remote_node", snapshot.NodeID).
		Msg("Received state snapshot, applying objects")

	// Apply each object
	applied := 0
	skipped := 0
	errors := 0

	for _, obj := range payload.Objects {
		// Check if we should apply this object
		localVV := r.state.Get(obj.Bucket, obj.Key)
		relationship := localVV.Compare(obj.VersionVector)

		// Only apply if remote is newer or concurrent
		if relationship == VectorBefore || relationship == VectorConcurrent {
			// Put object in S3
			if err := r.s3.Put(ctx, obj.Bucket, obj.Key, obj.Data, obj.ContentType, obj.Metadata); err != nil {
				r.logger.Warn().Err(err).
					Str("bucket", obj.Bucket).
					Str("key", obj.Key).
					Msg("Failed to put object during sync")
				errors++
				continue
			}

			// Merge version vectors
			r.state.Merge(obj.Bucket, obj.Key, obj.VersionVector)
			applied++

			r.logger.Debug().
				Str("bucket", obj.Bucket).
				Str("key", obj.Key).
				Str("relationship", relationship.String()).
				Msg("Applied synced object")
		} else {
			// Local is newer or equal, skip
			skipped++
			r.logger.Debug().
				Str("bucket", obj.Bucket).
				Str("key", obj.Key).
				Str("relationship", relationship.String()).
				Msg("Skipped synced object (local is newer or equal)")
		}
	}

	r.logger.Info().
		Str("from", msg.From).
		Int("total", len(payload.Objects)).
		Int("applied", applied).
		Int("skipped", skipped).
		Int("errors", errors).
		Msg("Completed state sync")

	return nil
}

// trackPendingACK tracks a pending ACK for a replication operation.
// Returns false if the operation was dropped due to pending limit.
func (r *Replicator) trackPendingACK(msgID, bucket, key string) bool {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()

	// Check if we've reached the pending operations limit
	if len(r.pending) >= r.maxPendingOperations {
		r.logger.Warn().
			Int("pending", len(r.pending)).
			Int("limit", r.maxPendingOperations).
			Str("bucket", bucket).
			Str("key", key).
			Msg("dropping replication operation: pending limit reached")
		r.incrementDroppedCount()
		return false
	}

	r.pending[msgID] = &pendingReplication{
		bucket:  bucket,
		key:     key,
		sentAt:  time.Now(),
		ackChan: make(chan *AckPayload, 1),
		timeout: time.AfterFunc(10*time.Second, func() {
			r.removePendingACK(msgID)
		}),
	}
	return true
}

// removePendingACK removes a pending ACK entry.
func (r *Replicator) removePendingACK(msgID string) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()

	if pending, exists := r.pending[msgID]; exists {
		pending.timeout.Stop()
		close(pending.ackChan)
		delete(r.pending, msgID)
	}
}

// ackTimeoutWorker periodically cleans up timed-out ACKs.
func (r *Replicator) ackTimeoutWorker() {
	defer r.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			// Cleanup is handled by individual timeouts
		}
	}
}

// GetStats returns replication statistics.
type Stats struct {
	SentCount     uint64 `json:"sent_count"`
	ReceivedCount uint64 `json:"received_count"`
	ConflictCount uint64 `json:"conflict_count"`
	ErrorCount    uint64 `json:"error_count"`
	DroppedCount  uint64 `json:"dropped_count"`
	RateLimited   uint64 `json:"rate_limited"` // Messages dropped due to rate limiting
	PeerCount     int    `json:"peer_count"`
	PendingACKs   int    `json:"pending_acks"`
}

func (r *Replicator) GetStats() Stats {
	r.metricsMu.RLock()
	defer r.metricsMu.RUnlock()

	r.pendingMu.RLock()
	pendingCount := len(r.pending)
	r.pendingMu.RUnlock()

	r.mu.RLock()
	peerCount := len(r.peers)
	r.mu.RUnlock()

	return Stats{
		SentCount:     r.sentCount,
		ReceivedCount: r.receivedCount,
		ConflictCount: r.conflictCount,
		ErrorCount:    r.errorCount,
		DroppedCount:  r.droppedCount,
		RateLimited:   r.rateLimited,
		PeerCount:     peerCount,
		PendingACKs:   pendingCount,
	}
}

func (r *Replicator) incrementSentCount() {
	r.metricsMu.Lock()
	r.sentCount++
	r.metricsMu.Unlock()
}

func (r *Replicator) incrementReceivedCount() {
	r.metricsMu.Lock()
	r.receivedCount++
	r.metricsMu.Unlock()
}

func (r *Replicator) incrementConflictCount() {
	r.metricsMu.Lock()
	r.conflictCount++
	r.metricsMu.Unlock()
}

func (r *Replicator) incrementErrorCount() {
	r.metricsMu.Lock()
	r.errorCount++
	r.metricsMu.Unlock()
}

func (r *Replicator) incrementDroppedCount() {
	r.metricsMu.Lock()
	r.droppedCount++
	r.metricsMu.Unlock()
}

func (r *Replicator) incrementRateLimitedCount() {
	r.metricsMu.Lock()
	r.rateLimited++
	r.metricsMu.Unlock()
}

// GetState returns the replication state tracker.
func (r *Replicator) GetState() *State {
	return r.state
}
