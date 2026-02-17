package replication

import (
	"context"
	"slices"
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

	// rebalanceDebounce is the delay after a topology change notification before
	// starting a rebalance cycle. Multiple coordinators may join/leave in rapid
	// succession; debouncing coalesces these into a single cycle.
	rebalanceDebounce = 10 * time.Second
)

// Rebalancer redistributes S3 data when the coordinator topology changes.
// It moves existing chunks to match the new StripingPolicy assignment.
type Rebalancer struct {
	replicator    *Replicator
	s3            S3Store
	chunkRegistry ChunkRegistryInterface
	logger        zerolog.Logger

	topologyChanged atomic.Bool
	lastPeerList    []string // protected by mu
	mu              sync.Mutex

	maxBytesPerCycle int64

	// Metrics
	chunksRedistributed atomic.Uint64
	bytesTransferred    atomic.Int64
	runsTotal           atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// For testing: allow overriding debounce duration
	debounceDuration time.Duration

	// OnCycleComplete is called after each rebalance cycle with current stats.
	// Used by server.go to push metrics to Prometheus without a circular import.
	OnCycleComplete func(stats RebalancerStats)
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
// The rebalancer reads current peers from the replicator when the cycle runs.
func (rb *Rebalancer) NotifyTopologyChange() {
	rb.topologyChanged.Store(true)
	rb.logger.Debug().Msg("Topology change notification received")
}

// RebalancerStats holds rebalancer statistics.
type RebalancerStats struct {
	RunsTotal           uint64 `json:"runs_total"`
	ChunksRedistributed uint64 `json:"chunks_redistributed"`
	ChunksCleaned       uint64 `json:"chunks_cleaned"`
	BytesTransferred    int64  `json:"bytes_transferred"`
}

// GetStats returns rebalancer statistics.
func (rb *Rebalancer) GetStats() RebalancerStats {
	return RebalancerStats{
		RunsTotal:           rb.runsTotal.Load(),
		ChunksRedistributed: rb.chunksRedistributed.Load(),
		BytesTransferred:    rb.bytesTransferred.Load(),
	}
}

// run is the main background loop that waits for topology changes and runs rebalance cycles.
func (rb *Rebalancer) run() {
	defer rb.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// debounceTimer is created when a topology change is first detected and fires
	// after debounceDuration to coalesce rapid changes into a single cycle.
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
				debounceTimer = time.NewTimer(rb.debounceDuration)
				debounceCh = debounceTimer.C
			}
		case <-debounceCh:
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
	if slices.Equal(allCoords, rb.lastPeerList) {
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
	chunksBeforeCycle := rb.chunksRedistributed.Load()

	// Get all objects for rebalancing
	allObjects, err := rb.s3.GetAllObjectKeys(ctx)
	if err != nil {
		rb.logger.Error().Err(err).Msg("Failed to get all object keys for rebalance")
		return
	}

	newPolicy := NewStripingPolicy(allCoords)
	var bytesThisCycle int64
	selfID := rb.replicator.nodeID

	// Track chunk hashes across all objects to safely clean up stale local copies.
	// neededChunks: hashes assigned to us by at least one object — must keep.
	// unassignedChunks: hashes we have locally but aren't assigned — candidates for deletion.
	// After the transfer pass, delete unassignedChunks that aren't in neededChunks.
	neededChunks := make(map[string]struct{})
	unassignedChunks := make(map[string]struct{})

	rateLimited := false
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

			// Rate limit transfers but still scan all objects to build
			// the needed/sent sets for correct cleanup.
			maxTransfer := rb.maxBytesPerCycle - bytesThisCycle
			if maxTransfer <= 0 {
				if !rateLimited {
					rateLimited = true
					rb.logger.Info().
						Int64("bytes_transferred", bytesThisCycle).
						Int64("max_bytes", rb.maxBytesPerCycle).
						Msg("Rebalance cycle rate limit reached, skipping further transfers")
				}
				maxTransfer = 0
			}

			transferred, err := rb.rebalanceObject(ctx, bucket, key, newPolicy, replicationFactor, selfID, maxTransfer, neededChunks, unassignedChunks)
			if err != nil {
				rb.logger.Warn().Err(err).
					Str("bucket", bucket).Str("key", key).
					Msg("Failed to rebalance object")
				continue
			}
			bytesThisCycle += transferred
		}
	}

	// Cleanup pass: delete local chunks that no object assigns to us.
	var chunksCleaned uint64
	for hash := range unassignedChunks {
		if _, needed := neededChunks[hash]; needed {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if err := rb.s3.DeleteChunk(ctx, hash); err != nil {
			rb.logger.Debug().Err(err).
				Str("chunk", truncateHashForLog(hash)).
				Msg("Failed to delete stale chunk during rebalance cleanup")
			continue
		}
		chunksCleaned++
	}
	if chunksCleaned > 0 {
		rb.logger.Info().
			Uint64("chunks_cleaned", chunksCleaned).
			Msg("Cleaned up stale local chunks after rebalance")
	}

	rb.bytesTransferred.Add(bytesThisCycle)
	chunksThisCycle := rb.chunksRedistributed.Load() - chunksBeforeCycle
	if rb.OnCycleComplete != nil {
		rb.OnCycleComplete(RebalancerStats{
			RunsTotal:           1,
			ChunksRedistributed: chunksThisCycle,
			ChunksCleaned:       chunksCleaned,
			BytesTransferred:    bytesThisCycle,
		})
	}
	rb.logger.Info().
		Int64("bytes_transferred", bytesThisCycle).
		Uint64("chunks_cleaned", chunksCleaned).
		Msg("Rebalance cycle completed")
}

// rebalanceObject performs chunk redistribution for a single object.
// It populates neededChunks with hashes assigned to us and sentChunks with hashes
// we successfully sent away. maxTransferBytes limits how much data to transfer;
// set to 0 to skip transfers but still populate the tracking sets.
// Returns bytes transferred.
func (rb *Rebalancer) rebalanceObject(ctx context.Context, bucket, key string, policy *StripingPolicy, replicationFactor int, selfID string, maxTransferBytes int64, neededChunks, unassignedChunks map[string]struct{}) (int64, error) {
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
			// We should have this chunk — record it and ensure we have it locally.
			neededChunks[chunkHash] = struct{}{}

			if maxTransferBytes > 0 && bytesTransferred < maxTransferBytes && !rb.s3.ChunkExists(ctx, chunkHash) {
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
			// We shouldn't own this chunk index.
			owner := policy.PrimaryOwner(idx)
			if owner == selfID {
				continue
			}

			if !rb.s3.ChunkExists(ctx, chunkHash) {
				continue // We don't have it, nothing to do
			}

			// We have this chunk but aren't assigned to own it.
			// Mark for cleanup — will be deleted only if no object assigns it to us.
			unassignedChunks[chunkHash] = struct{}{}

			// Best-effort: send to the correct owner if within rate limit.
			if maxTransferBytes > 0 && bytesTransferred < maxTransferBytes {
				data, readErr := rb.s3.ReadChunk(ctx, chunkHash)
				if readErr != nil {
					continue
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
	}

	return bytesTransferred, nil
}

// fetchChunkFromAnyPeer tries to fetch a chunk from any available peer.
func (rb *Rebalancer) fetchChunkFromAnyPeer(ctx context.Context, chunkHash string) ([]byte, error) {
	return rb.replicator.fetchChunkFromPeers(ctx, chunkHash)
}
