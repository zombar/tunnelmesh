package replication

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

const (
	// defaultMaxBytesPerCycle limits how much data is transferred per rebalance cycle
	// to prevent network saturation.
	defaultMaxBytesPerCycle int64 = 50 * 1024 * 1024 // 50MB

	// defaultMaxConcurrentTransfers limits concurrent chunk transfers during rebalance.
	defaultMaxConcurrentTransfers = 5

	// rebalanceDebounce is the delay after a topology change notification before
	// starting a rebalance cycle. Multiple coordinators may join/leave in rapid
	// succession; debouncing coalesces these into a single cycle.
	rebalanceDebounce = 10 * time.Second
)

// Rebalancer redistributes S3 data when the coordinator topology changes.
//
// Tier 1 (immediate): Redistribute existing chunks to match new StripingPolicy.
// Tier 2 (background): Re-encode erasure-coded objects when fault tolerance is degraded.
type Rebalancer struct {
	replicator    *Replicator
	s3            S3Store
	chunkRegistry ChunkRegistryInterface
	logger        zerolog.Logger

	topologyChanged atomic.Bool
	lastPeerList    []string // protected by mu
	mu              sync.Mutex

	maxBytesPerCycle int64
	sendSem          chan struct{}

	// Metrics
	chunksRedistributed atomic.Uint64
	objectsReEncoded    atomic.Uint64
	bytesTransferred    atomic.Int64
	runsTotal           atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// For testing: allow overriding debounce duration
	debounceDuration time.Duration
}

// NewRebalancer creates a new rebalancer attached to the given replicator.
func NewRebalancer(r *Replicator, s3 S3Store, registry ChunkRegistryInterface, logger zerolog.Logger) *Rebalancer {
	ctx, cancel := context.WithCancel(r.ctx)
	return &Rebalancer{
		replicator:       r,
		s3:               s3,
		chunkRegistry:    registry,
		logger:           logger.With().Str("component", "rebalancer").Logger(),
		maxBytesPerCycle: defaultMaxBytesPerCycle,
		sendSem:          make(chan struct{}, defaultMaxConcurrentTransfers),
		ctx:              ctx,
		cancel:           cancel,
		debounceDuration: rebalanceDebounce,
	}
}

// Start starts the rebalancer background goroutine.
func (rb *Rebalancer) Start() {
	rb.wg.Add(1)
	go rb.run()
	rb.logger.Info().Msg("Rebalancer started")
}

// Stop stops the rebalancer and waits for the background goroutine to exit.
func (rb *Rebalancer) Stop() {
	rb.cancel()
	rb.wg.Wait()
	rb.logger.Info().Msg("Rebalancer stopped")
}

// NotifyTopologyChange signals that the coordinator topology has changed.
// The actual rebalance is debounced to avoid repeated work during rapid changes.
func (rb *Rebalancer) NotifyTopologyChange(newPeers []string) {
	rb.topologyChanged.Store(true)
	rb.logger.Debug().
		Int("new_peer_count", len(newPeers)).
		Msg("Topology change notification received")
}

// GetStats returns rebalancer statistics.
type RebalancerStats struct {
	RunsTotal           uint64 `json:"runs_total"`
	ChunksRedistributed uint64 `json:"chunks_redistributed"`
	ObjectsReEncoded    uint64 `json:"objects_reencoded"`
	BytesTransferred    int64  `json:"bytes_transferred"`
}

func (rb *Rebalancer) GetStats() RebalancerStats {
	return RebalancerStats{
		RunsTotal:           rb.runsTotal.Load(),
		ChunksRedistributed: rb.chunksRedistributed.Load(),
		ObjectsReEncoded:    rb.objectsReEncoded.Load(),
		BytesTransferred:    rb.bytesTransferred.Load(),
	}
}

// run is the main background loop that waits for topology changes and runs rebalance cycles.
func (rb *Rebalancer) run() {
	defer rb.wg.Done()

	// Check every second for topology changes (actual debounce is inside runRebalanceCycle)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Timer for debounce — nil until a topology change is detected
	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	for {
		select {
		case <-rb.ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case <-ticker.C:
			if rb.topologyChanged.Load() && debounceCh == nil {
				// Start debounce timer
				debounceTimer = time.NewTimer(rb.debounceDuration)
				debounceCh = debounceTimer.C
			}
		case <-debounceCh:
			// Debounce period elapsed, run rebalance
			debounceCh = nil
			debounceTimer = nil
			rb.runRebalanceCycle(rb.ctx)
		}
	}
}

// runRebalanceCycle performs a single rebalance cycle.
func (rb *Rebalancer) runRebalanceCycle(ctx context.Context) {
	if !rb.topologyChanged.Swap(false) {
		return // No change since last check
	}

	// Build current peer list
	peers := rb.replicator.GetPeers()
	allCoords := make([]string, 0, len(peers)+1)
	allCoords = append(allCoords, rb.replicator.nodeID)
	allCoords = append(allCoords, peers...)
	sort.Strings(allCoords)

	// Check if topology actually changed
	rb.mu.Lock()
	if equalStringSlices(allCoords, rb.lastPeerList) {
		rb.mu.Unlock()
		return // Same topology, no work needed
	}
	oldPeerList := rb.lastPeerList
	rb.lastPeerList = allCoords
	rb.mu.Unlock()

	rb.logger.Info().
		Strs("old_peers", oldPeerList).
		Strs("new_peers", allCoords).
		Msg("Topology change detected, starting rebalance cycle")

	rb.runsTotal.Add(1)

	// Get all objects for rebalancing
	allObjects, err := rb.s3.GetAllObjectKeys(ctx)
	if err != nil {
		rb.logger.Error().Err(err).Msg("Failed to get all object keys for rebalance")
		return
	}

	newPolicy := NewStripingPolicy(allCoords)
	var bytesThisCycle int64
	selfID := rb.replicator.nodeID

	for bucket, keys := range allObjects {
		select {
		case <-ctx.Done():
			rb.logger.Info().Msg("Rebalance cycle cancelled")
			return
		default:
		}

		replicationFactor := rb.s3.GetBucketReplicationFactor(ctx, bucket)
		if replicationFactor < 1 {
			replicationFactor = 2
		}

		for _, key := range keys {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Rate limit
			if bytesThisCycle >= rb.maxBytesPerCycle {
				rb.logger.Info().
					Int64("bytes_transferred", bytesThisCycle).
					Int64("max_bytes", rb.maxBytesPerCycle).
					Msg("Rebalance cycle rate limit reached, stopping")
				return
			}

			transferred, err := rb.rebalanceObject(ctx, bucket, key, newPolicy, replicationFactor, selfID)
			if err != nil {
				rb.logger.Warn().Err(err).
					Str("bucket", bucket).Str("key", key).
					Msg("Failed to rebalance object")
				continue
			}
			bytesThisCycle += transferred

			// Tier 2: Check if erasure-coded object needs re-encoding
			rb.checkAndReEncodeObject(ctx, bucket, key, allCoords)
		}
	}

	rb.bytesTransferred.Add(bytesThisCycle)
	rb.logger.Info().
		Int64("bytes_transferred", bytesThisCycle).
		Msg("Rebalance cycle completed")
}

// rebalanceObject performs Tier 1 redistribution for a single object.
// Returns bytes transferred.
func (rb *Rebalancer) rebalanceObject(ctx context.Context, bucket, key string, policy *StripingPolicy, replicationFactor int, selfID string) (int64, error) {
	meta, err := rb.s3.GetObjectMeta(ctx, bucket, key)
	if err != nil {
		return 0, err
	}

	if len(meta.Chunks) == 0 {
		return 0, nil
	}

	totalChunks := len(meta.Chunks)
	assignedToSelf := policy.ChunksForPeer(selfID, totalChunks, replicationFactor)
	assignedSet := make(map[int]bool, len(assignedToSelf))
	for _, idx := range assignedToSelf {
		assignedSet[idx] = true
	}

	var bytesTransferred int64

	for idx, chunkHash := range meta.Chunks {
		select {
		case <-ctx.Done():
			return bytesTransferred, ctx.Err()
		default:
		}

		if assignedSet[idx] {
			// We should have this chunk — ensure we do
			if _, readErr := rb.s3.ReadChunk(ctx, chunkHash); readErr != nil {
				// We don't have it but should — fetch from peers
				data, fetchErr := rb.fetchChunkFromAnyPeer(ctx, chunkHash)
				if fetchErr != nil {
					rb.logger.Warn().Err(fetchErr).
						Str("chunk", truncateHashForLog(chunkHash)).
						Msg("Failed to fetch missing chunk during rebalance")
					continue
				}
				if writeErr := rb.s3.WriteChunkDirect(ctx, chunkHash, data); writeErr != nil {
					rb.logger.Warn().Err(writeErr).
						Str("chunk", truncateHashForLog(chunkHash)).
						Msg("Failed to write fetched chunk during rebalance")
					continue
				}
				bytesTransferred += int64(len(data))
				rb.chunksRedistributed.Add(1)
			}
		} else {
			// We have this chunk but shouldn't — the correct owner should get it
			// via their own rebalance cycle or normal replication. We send it proactively
			// to the primary owner if we have it.
			owner := policy.PrimaryOwner(idx)
			if owner == selfID {
				continue // We are the primary, shouldn't happen if !assignedSet[idx]
			}

			data, readErr := rb.s3.ReadChunk(ctx, chunkHash)
			if readErr != nil {
				continue // We don't have it, nothing to send
			}

			// Acquire semaphore for concurrent transfer control
			select {
			case rb.sendSem <- struct{}{}:
			case <-ctx.Done():
				return bytesTransferred, ctx.Err()
			}

			sendErr := rb.replicator.sendReplicateChunk(ctx, owner, ReplicateChunkPayload{
				Bucket:      bucket,
				Key:         key,
				ChunkHash:   chunkHash,
				ChunkData:   data,
				ChunkIndex:  idx,
				TotalChunks: totalChunks,
				ChunkSize:   int64(len(data)),
			})
			<-rb.sendSem

			if sendErr != nil {
				rb.logger.Debug().Err(sendErr).
					Str("chunk", truncateHashForLog(chunkHash)).
					Str("owner", owner).
					Msg("Failed to send chunk to new owner during rebalance")
				continue
			}

			bytesTransferred += int64(len(data))
			rb.chunksRedistributed.Add(1)
		}
	}

	return bytesTransferred, nil
}

// checkAndReEncodeObject checks if an erasure-coded object has degraded fault tolerance
// and re-encodes if necessary (Tier 2).
func (rb *Rebalancer) checkAndReEncodeObject(ctx context.Context, bucket, key string, currentPeers []string) {
	enabled, _, _, err := rb.s3.GetBucketErasureCodingPolicy(ctx, bucket)
	if err != nil || !enabled {
		return // Not erasure-coded or error — skip
	}

	meta, err := rb.s3.GetObjectMeta(ctx, bucket, key)
	if err != nil {
		return
	}

	if len(meta.Chunks) == 0 {
		return
	}

	// Check shard distribution across coordinators
	policy := NewStripingPolicy(currentPeers)
	shardOwnerCount := make(map[string]int)
	for idx := range meta.Chunks {
		owner := policy.PrimaryOwner(idx)
		shardOwnerCount[owner]++
	}

	// If all coordinators are still present and distribution is reasonable, skip
	// We only need to re-encode if a coordinator left and we lost shards
	if len(currentPeers) > 0 && len(shardOwnerCount) == len(currentPeers) {
		return // All coordinators covered, distribution is fine
	}

	// TODO: Implement actual re-encoding when erasure coding module is available.
	// For now, log that re-encoding would be needed.
	rb.logger.Info().
		Str("bucket", bucket).
		Str("key", key).
		Int("coordinators", len(currentPeers)).
		Int("shard_owners", len(shardOwnerCount)).
		Msg("Erasure-coded object may need re-encoding (not yet implemented)")
}

// fetchChunkFromAnyPeer tries to fetch a chunk from any available peer.
func (rb *Rebalancer) fetchChunkFromAnyPeer(ctx context.Context, chunkHash string) ([]byte, error) {
	return rb.replicator.fetchChunkFromPeers(ctx, chunkHash)
}

// equalStringSlices compares two sorted string slices for equality.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
