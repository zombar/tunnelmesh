package replication

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ChunkOwnership tracks which coordinators have a specific chunk.
// This enables cross-coordinator deduplication and multi-source reads.
type ChunkOwnership struct {
	ChunkHash         string               `json:"chunk_hash"`         // SHA-256 of chunk plaintext
	Owners            map[string]time.Time `json:"owners"`             // coordinatorID -> last-seen timestamp
	VersionVector     VersionVector        `json:"version_vector"`     // Causality for ownership metadata
	ReplicationFactor int                  `json:"replication_factor"` // Desired number of replicas
	TotalSize         int64                `json:"total_size"`         // Uncompressed size
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`

	// Erasure coding shard metadata (optional, only for RS-encoded files)
	ShardType    string `json:"shard_type,omitempty"`     // "data" or "parity" (empty for non-RS chunks)
	ShardIndex   int    `json:"shard_index,omitempty"`    // Position in RS matrix (0-based)
	ParentFileID string `json:"parent_file_id,omitempty"` // Link to parent file VersionID
}

// ChunkRegistry is the distributed index of chunk ownership.
// Each coordinator maintains a local copy that's eventually consistent via gossip.
type ChunkRegistry struct {
	ownership    map[string]*ChunkOwnership // chunkHash -> ownership
	mu           sync.RWMutex
	localCoordID string // This coordinator's ID
	logger       Logger // Optional logger for debugging
}

// Logger interface for optional logging.
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// noopLogger is a silent logger when none is provided.
type noopLogger struct{}

func (noopLogger) Debug(string, ...interface{}) {}
func (noopLogger) Info(string, ...interface{})  {}
func (noopLogger) Warn(string, ...interface{})  {}
func (noopLogger) Error(string, ...interface{}) {}

// truncateHash truncates a hash to 8 characters for logging.
// Handles short hashes gracefully.
func truncateHash(hash string) string {
	if len(hash) <= 8 {
		return hash
	}
	return hash[:8]
}

// NewChunkRegistry creates a new distributed chunk registry.
func NewChunkRegistry(coordID string, logger Logger) *ChunkRegistry {
	if logger == nil {
		logger = noopLogger{}
	}

	return &ChunkRegistry{
		ownership:    make(map[string]*ChunkOwnership),
		localCoordID: coordID,
		logger:       logger,
	}
}

// RegisterChunk registers a chunk as owned by the local coordinator.
// Returns error if the chunk hash is empty.
func (cr *ChunkRegistry) RegisterChunk(hash string, size int64) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	now := time.Now()

	ownership, exists := cr.ownership[hash]
	if !exists {
		// New chunk - create ownership record
		ownership = &ChunkOwnership{
			ChunkHash:         hash,
			Owners:            make(map[string]time.Time),
			VersionVector:     NewVersionVector(),
			ReplicationFactor: 2, // Default: 2 replicas
			TotalSize:         size,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		cr.ownership[hash] = ownership
	}

	// Add local coordinator as owner
	ownership.Owners[cr.localCoordID] = now
	ownership.VersionVector.Increment(cr.localCoordID)
	ownership.UpdatedAt = now

	cr.logger.Debug("Registered chunk %s (size=%d, owners=%d)", truncateHash(hash), size, len(ownership.Owners))

	return nil
}

// RegisterChunkWithReplication registers a chunk with a custom replication factor.
// This is used when bucket-specific replication policies are configured.
func (cr *ChunkRegistry) RegisterChunkWithReplication(hash string, size int64, replicationFactor int) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}
	if size <= 0 {
		return fmt.Errorf("chunk size must be positive, got %d", size)
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	now := time.Now()

	ownership, exists := cr.ownership[hash]
	if !exists {
		// New chunk - create ownership record with custom replication factor
		ownership = &ChunkOwnership{
			ChunkHash:         hash,
			Owners:            make(map[string]time.Time),
			VersionVector:     NewVersionVector(),
			ReplicationFactor: replicationFactor, // Use custom replication factor
			TotalSize:         size,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		cr.ownership[hash] = ownership
	}

	// Add local coordinator as owner
	ownership.Owners[cr.localCoordID] = now
	ownership.VersionVector.Increment(cr.localCoordID)
	ownership.UpdatedAt = now

	cr.logger.Debug("Registered chunk %s (size=%d, replication=%d, owners=%d)",
		truncateHash(hash), size, replicationFactor, len(ownership.Owners))

	return nil
}

// UnregisterChunk removes the local coordinator as an owner of the chunk.
// The chunk ownership entry is removed entirely if no owners remain.
func (cr *ChunkRegistry) UnregisterChunk(hash string) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	ownership, exists := cr.ownership[hash]
	if !exists {
		return nil // Already gone
	}

	// Remove local coordinator from owners
	delete(ownership.Owners, cr.localCoordID)
	ownership.VersionVector.Increment(cr.localCoordID)
	ownership.UpdatedAt = time.Now()

	// If no owners remain, delete the entire entry
	if len(ownership.Owners) == 0 {
		delete(cr.ownership, hash)
		cr.logger.Debug("Unregistered chunk %s (no owners remain, deleted)", truncateHash(hash))
	} else {
		cr.logger.Debug("Unregistered chunk %s (owners remaining: %d)", truncateHash(hash), len(ownership.Owners))
	}

	return nil
}

// GetOwners returns the list of coordinator IDs that own the chunk.
// Returns empty slice if chunk is not tracked.
func (cr *ChunkRegistry) GetOwners(hash string) ([]string, error) {
	if hash == "" {
		return nil, fmt.Errorf("chunk hash cannot be empty")
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	ownership, exists := cr.ownership[hash]
	if !exists {
		return []string{}, nil
	}

	// Return list of owner IDs
	owners := make([]string, 0, len(ownership.Owners))
	for coordID := range ownership.Owners {
		owners = append(owners, coordID)
	}

	return owners, nil
}

// AddOwner adds a coordinator as an owner of a chunk.
// This is used when receiving ownership updates from other coordinators.
func (cr *ChunkRegistry) AddOwner(hash string, coordID string) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}
	if coordID == "" {
		return fmt.Errorf("coordinator ID cannot be empty")
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	now := time.Now()

	ownership, exists := cr.ownership[hash]
	if !exists {
		// Create ownership record if it doesn't exist
		ownership = &ChunkOwnership{
			ChunkHash:         hash,
			Owners:            make(map[string]time.Time),
			VersionVector:     NewVersionVector(),
			ReplicationFactor: 2,
			TotalSize:         0, // Unknown size
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		cr.ownership[hash] = ownership
	}

	// Add owner
	ownership.Owners[coordID] = now
	ownership.VersionVector.Increment(coordID)
	ownership.UpdatedAt = now

	cr.logger.Debug("Added owner %s for chunk %s (total owners: %d)", coordID, truncateHash(hash), len(ownership.Owners))

	return nil
}

// RemoveOwner removes a coordinator as an owner of a chunk.
// This is used when receiving ownership removal updates from other coordinators.
func (cr *ChunkRegistry) RemoveOwner(hash string, coordID string) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}
	if coordID == "" {
		return fmt.Errorf("coordinator ID cannot be empty")
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	ownership, exists := cr.ownership[hash]
	if !exists {
		return nil // Already gone
	}

	delete(ownership.Owners, coordID)
	ownership.VersionVector.Increment(coordID)
	ownership.UpdatedAt = time.Now()

	// If no owners remain, delete the entire entry
	if len(ownership.Owners) == 0 {
		delete(cr.ownership, hash)
		cr.logger.Debug("Removed owner %s from chunk %s (no owners remain, deleted)", coordID, truncateHash(hash))
	} else {
		cr.logger.Debug("Removed owner %s from chunk %s (owners remaining: %d)", coordID, truncateHash(hash), len(ownership.Owners))
	}

	return nil
}

// GetChunksOwnedBy returns all chunk hashes owned by the specified coordinator.
// This is used to determine which chunks need replication.
func (cr *ChunkRegistry) GetChunksOwnedBy(coordID string) ([]string, error) {
	if coordID == "" {
		return nil, fmt.Errorf("coordinator ID cannot be empty")
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	var chunks []string
	for hash, ownership := range cr.ownership {
		if _, owned := ownership.Owners[coordID]; owned {
			chunks = append(chunks, hash)
		}
	}

	return chunks, nil
}

// GetChunkOwnership returns the ownership record for a chunk.
// Returns nil if chunk is not tracked.
func (cr *ChunkRegistry) GetChunkOwnership(hash string) *ChunkOwnership {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	ownership, exists := cr.ownership[hash]
	if !exists {
		return nil
	}

	// Return a copy to prevent external modification
	return ownership.Copy()
}

// Copy creates a deep copy of chunk ownership.
func (co *ChunkOwnership) Copy() *ChunkOwnership {
	if co == nil {
		return nil
	}

	owners := make(map[string]time.Time, len(co.Owners))
	for k, v := range co.Owners {
		owners[k] = v
	}

	return &ChunkOwnership{
		ChunkHash:         co.ChunkHash,
		Owners:            owners,
		VersionVector:     co.VersionVector.Copy(),
		ReplicationFactor: co.ReplicationFactor,
		TotalSize:         co.TotalSize,
		CreatedAt:         co.CreatedAt,
		UpdatedAt:         co.UpdatedAt,
		ShardType:         co.ShardType,
		ShardIndex:        co.ShardIndex,
		ParentFileID:      co.ParentFileID,
	}
}

// MergeChunkOwnership merges a remote chunk ownership record with the local one.
// Uses version vectors to resolve conflicts.
// Returns true if the local state was modified.
func (cr *ChunkRegistry) MergeChunkOwnership(remote *ChunkOwnership) (bool, error) {
	if remote == nil {
		return false, fmt.Errorf("remote ownership cannot be nil")
	}
	if remote.ChunkHash == "" {
		return false, fmt.Errorf("chunk hash cannot be empty")
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	local, exists := cr.ownership[remote.ChunkHash]

	if !exists {
		// No local record - accept remote
		cr.ownership[remote.ChunkHash] = remote.Copy()
		cr.logger.Debug("Merged new chunk ownership for %s (owners: %d)", truncateHash(remote.ChunkHash), len(remote.Owners))
		return true, nil
	}

	// Compare version vectors to determine merge strategy
	relationship := local.VersionVector.Compare(remote.VersionVector)

	switch relationship {
	case VectorBefore:
		// Local is older - replace with remote
		cr.ownership[remote.ChunkHash] = remote.Copy()
		cr.logger.Debug("Merged chunk ownership for %s (remote newer)", truncateHash(remote.ChunkHash))
		return true, nil

	case VectorAfter:
		// Local is newer - keep local
		cr.logger.Debug("Skipped chunk ownership for %s (local newer)", truncateHash(remote.ChunkHash))
		return false, nil

	case VectorEqual:
		// Same version - no change needed
		return false, nil

	case VectorConcurrent:
		// Concurrent modification - merge owners and version vectors
		modified := false

		// Merge owners (union of both sets)
		for coordID, timestamp := range remote.Owners {
			if localTimestamp, exists := local.Owners[coordID]; !exists || timestamp.After(localTimestamp) {
				local.Owners[coordID] = timestamp
				modified = true
			}
		}

		// Merge shard metadata (prefer remote if set)
		if remote.ShardType != "" && local.ShardType == "" {
			local.ShardType = remote.ShardType
			modified = true
		}
		if remote.ParentFileID != "" && local.ParentFileID == "" {
			local.ParentFileID = remote.ParentFileID
			modified = true
		}
		// For ShardIndex, prefer remote if it has a parent file ID
		if remote.ParentFileID != "" {
			local.ShardIndex = remote.ShardIndex
		}

		// Merge version vectors
		local.VersionVector.Merge(remote.VersionVector)

		// Update timestamp
		if remote.UpdatedAt.After(local.UpdatedAt) {
			local.UpdatedAt = remote.UpdatedAt
		}

		if modified {
			cr.logger.Debug("Merged concurrent chunk ownership for %s (union of owners)", truncateHash(remote.ChunkHash))
		}

		return modified, nil
	}

	return false, nil
}

// GetAllChunks returns all chunk hashes tracked in the registry.
func (cr *ChunkRegistry) GetAllChunks() []string {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	chunks := make([]string, 0, len(cr.ownership))
	for hash := range cr.ownership {
		chunks = append(chunks, hash)
	}

	return chunks
}

// GetStats returns statistics about the chunk registry.
func (cr *ChunkRegistry) GetStats() map[string]interface{} {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	totalChunks := len(cr.ownership)
	totalSize := int64(0)
	underReplicated := 0
	overReplicated := 0
	localChunks := 0

	for _, ownership := range cr.ownership {
		totalSize += ownership.TotalSize

		ownerCount := len(ownership.Owners)
		if ownerCount < ownership.ReplicationFactor {
			underReplicated++
		} else if ownerCount > ownership.ReplicationFactor {
			overReplicated++
		}

		if _, owned := ownership.Owners[cr.localCoordID]; owned {
			localChunks++
		}
	}

	return map[string]interface{}{
		"total_chunks":     totalChunks,
		"total_size_bytes": totalSize,
		"local_chunks":     localChunks,
		"under_replicated": underReplicated,
		"over_replicated":  overReplicated,
		"coordinator_id":   cr.localCoordID,
	}
}

// ExportState exports the entire registry state for sync operations.
func (cr *ChunkRegistry) ExportState() ([]byte, error) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()

	// Create a snapshot of all ownership records
	snapshot := make([]*ChunkOwnership, 0, len(cr.ownership))
	for _, ownership := range cr.ownership {
		snapshot = append(snapshot, ownership.Copy())
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal registry state: %w", err)
	}

	return data, nil
}

// ImportState imports registry state from a sync operation.
// Merges with existing state using version vectors.
func (cr *ChunkRegistry) ImportState(data []byte) error {
	var snapshot []*ChunkOwnership
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("unmarshal registry state: %w", err)
	}

	// Merge each ownership record
	for _, remote := range snapshot {
		if _, err := cr.MergeChunkOwnership(remote); err != nil {
			cr.logger.Warn("Failed to merge chunk ownership for %s: %v", remote.ChunkHash, err)
			continue
		}
	}

	cr.logger.Info("Imported %d chunk ownership records", len(snapshot))

	return nil
}

// CleanupStaleOwners removes ownership entries for coordinators that haven't
// been seen for longer than the specified duration.
// This is used to clean up after coordinators that have left the mesh.
func (cr *ChunkRegistry) CleanupStaleOwners(maxAge time.Duration) int {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	cleaned := 0

	for hash, ownership := range cr.ownership {
		// Remove stale owners
		ownersCleaned := false
		for coordID, lastSeen := range ownership.Owners {
			if lastSeen.Before(cutoff) {
				delete(ownership.Owners, coordID)
				ownersCleaned = true
				cleaned++
				cr.logger.Debug("Cleaned stale owner %s for chunk %s (last seen: %v)", coordID, truncateHash(hash), lastSeen)
			}
		}

		// Update version vector once per chunk (not per owner removed)
		// This prevents causality tracking issues when multiple owners are cleaned
		if ownersCleaned {
			ownership.VersionVector.Increment(cr.localCoordID)
			ownership.UpdatedAt = time.Now()
		}

		// Remove entire entry if no owners remain
		if len(ownership.Owners) == 0 {
			delete(cr.ownership, hash)
			cr.logger.Debug("Deleted chunk %s (no owners remaining after cleanup)", truncateHash(hash))
		}
	}

	if cleaned > 0 {
		cr.logger.Info("Cleaned %d stale ownership entries", cleaned)
	}

	return cleaned
}

// RegisterShardChunk registers a chunk as an erasure coding shard.
// This is used when storing parity shards or data shards with tracking metadata.
func (cr *ChunkRegistry) RegisterShardChunk(hash string, size int64, parentFileID string, shardType string, shardIndex int, replicationFactor int) error {
	if hash == "" {
		return fmt.Errorf("chunk hash cannot be empty")
	}
	if shardType != "" && shardType != "data" && shardType != "parity" {
		return fmt.Errorf("invalid shard type: %s (must be 'data' or 'parity')", shardType)
	}

	cr.mu.Lock()
	defer cr.mu.Unlock()

	now := time.Now()

	ownership, exists := cr.ownership[hash]
	if !exists {
		// New chunk - create ownership record with shard metadata
		ownership = &ChunkOwnership{
			ChunkHash:         hash,
			Owners:            make(map[string]time.Time),
			VersionVector:     NewVersionVector(),
			ReplicationFactor: replicationFactor,
			TotalSize:         size,
			CreatedAt:         now,
			UpdatedAt:         now,
			ShardType:         shardType,
			ShardIndex:        shardIndex,
			ParentFileID:      parentFileID,
		}
		cr.ownership[hash] = ownership
	} else {
		// Update shard metadata if provided
		if shardType != "" {
			ownership.ShardType = shardType
		}
		if parentFileID != "" {
			ownership.ParentFileID = parentFileID
		}
		ownership.ShardIndex = shardIndex
	}

	// Add local coordinator as owner
	ownership.Owners[cr.localCoordID] = now
	ownership.VersionVector.Increment(cr.localCoordID)
	ownership.UpdatedAt = now

	cr.logger.Debug("Registered %s shard %d (chunk %s) for file %s on coordinator %s",
		shardType, shardIndex, truncateHash(hash), parentFileID, cr.localCoordID)

	return nil
}

// GetParityShardsForFile returns all parity shards for a specific file version.
// Returns a slice of ChunkOwnership records sorted by shard index.
func (cr *ChunkRegistry) GetParityShardsForFile(parentFileID string) ([]*ChunkOwnership, error) {
	if parentFileID == "" {
		return nil, fmt.Errorf("parent file ID cannot be empty")
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	var shards []*ChunkOwnership
	for _, ownership := range cr.ownership {
		if ownership.ParentFileID == parentFileID && ownership.ShardType == "parity" {
			// Create a copy to avoid race conditions
			shardCopy := *ownership
			shards = append(shards, &shardCopy)
		}
	}

	// Sort by shard index for consistent ordering
	// Using a simple bubble sort since the number of parity shards is typically small (< 10)
	for i := 0; i < len(shards); i++ {
		for j := i + 1; j < len(shards); j++ {
			if shards[i].ShardIndex > shards[j].ShardIndex {
				shards[i], shards[j] = shards[j], shards[i]
			}
		}
	}

	return shards, nil
}

// GetShardsForFile returns all shards (data + parity) for a specific file version.
// Returns a slice of ChunkOwnership records sorted by shard type then index.
func (cr *ChunkRegistry) GetShardsForFile(parentFileID string) ([]*ChunkOwnership, error) {
	if parentFileID == "" {
		return nil, fmt.Errorf("parent file ID cannot be empty")
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	var shards []*ChunkOwnership
	for _, ownership := range cr.ownership {
		if ownership.ParentFileID == parentFileID {
			// Create a copy to avoid race conditions
			shardCopy := *ownership
			shards = append(shards, &shardCopy)
		}
	}

	// Sort by shard type (data before parity) then by index
	for i := 0; i < len(shards); i++ {
		for j := i + 1; j < len(shards); j++ {
			// Sort data shards before parity shards
			if shards[i].ShardType == "parity" && shards[j].ShardType == "data" {
				shards[i], shards[j] = shards[j], shards[i]
			} else if shards[i].ShardType == shards[j].ShardType && shards[i].ShardIndex > shards[j].ShardIndex {
				shards[i], shards[j] = shards[j], shards[i]
			}
		}
	}

	return shards, nil
}

// GetShardOwnersByType returns a map of chunk hashes to their owners, grouped by shard type.
// This is useful for distributing shards across coordinators during replication.
func (cr *ChunkRegistry) GetShardOwnersByType(shardType string) (map[string][]string, error) {
	if shardType != "" && shardType != "data" && shardType != "parity" {
		return nil, fmt.Errorf("invalid shard type: %s (must be 'data', 'parity', or empty for all)", shardType)
	}

	cr.mu.RLock()
	defer cr.mu.RUnlock()

	result := make(map[string][]string)
	for hash, ownership := range cr.ownership {
		// Filter by shard type if specified
		if shardType != "" && ownership.ShardType != shardType {
			continue
		}

		// Convert map[string]time.Time to []string
		var owners []string
		for coordID := range ownership.Owners {
			owners = append(owners, coordID)
		}

		if len(owners) > 0 {
			result[hash] = owners
		}
	}

	return result, nil
}

// CleanupOrphanedShards removes shard chunks whose parent file no longer exists.
// This is called during garbage collection to clean up parity shards from deleted files.
// Returns the number of shards cleaned up.
func (cr *ChunkRegistry) CleanupOrphanedShards(activeFileIDs map[string]bool) int {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	cleaned := 0
	for hash, ownership := range cr.ownership {
		// Only clean up chunks with parent file IDs (i.e., shards)
		if ownership.ParentFileID == "" {
			continue
		}

		// Check if parent file is still active
		if !activeFileIDs[ownership.ParentFileID] {
			delete(cr.ownership, hash)
			cleaned++
			cr.logger.Debug("Cleaned up orphaned %s shard %d (chunk %s) for deleted file %s",
				ownership.ShardType, ownership.ShardIndex, truncateHash(hash), ownership.ParentFileID)
		}
	}

	if cleaned > 0 {
		cr.logger.Info("Cleaned up %d orphaned shard chunks", cleaned)
	}

	return cleaned
}
