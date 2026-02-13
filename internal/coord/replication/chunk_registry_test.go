package replication

import (
	"fmt"
	"testing"
	"time"
)

func TestChunkRegistryRegisterChunk(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register a chunk
	err := registry.RegisterChunk("abc123", 4096)
	if err != nil {
		t.Fatalf("RegisterChunk failed: %v", err)
	}

	// Verify ownership
	owners, err := registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 1 {
		t.Errorf("expected 1 owner, got %d", len(owners))
	}

	if owners[0] != "coord-1" {
		t.Errorf("expected owner=coord-1, got %s", owners[0])
	}

	// Verify chunk ownership metadata
	ownership := registry.GetChunkOwnership("abc123")
	if ownership == nil {
		t.Fatal("expected ownership record, got nil")
	}

	if ownership.TotalSize != 4096 {
		t.Errorf("expected size=4096, got %d", ownership.TotalSize)
	}

	if ownership.VersionVector.Get("coord-1") != 1 {
		t.Errorf("expected version vector coord-1=1, got %d", ownership.VersionVector.Get("coord-1"))
	}
}

func TestChunkRegistryRegisterChunkEmpty(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	err := registry.RegisterChunk("", 4096)
	if err == nil {
		t.Error("expected error for empty hash, got nil")
	}
}

func TestChunkRegistryUnregisterChunk(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register then unregister
	_ = registry.RegisterChunk("abc123", 4096)
	err := registry.UnregisterChunk("abc123")
	if err != nil {
		t.Fatalf("UnregisterChunk failed: %v", err)
	}

	// Verify chunk is gone
	owners, err := registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 0 {
		t.Errorf("expected 0 owners after unregister, got %d", len(owners))
	}

	// Verify ownership record deleted
	ownership := registry.GetChunkOwnership("abc123")
	if ownership != nil {
		t.Error("expected nil ownership after unregister, got record")
	}
}

func TestChunkRegistryAddOwner(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Add owner for a chunk not yet registered locally
	err := registry.AddOwner("abc123", "coord-2")
	if err != nil {
		t.Fatalf("AddOwner failed: %v", err)
	}

	// Verify ownership
	owners, err := registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 1 || owners[0] != "coord-2" {
		t.Errorf("expected owner=coord-2, got %v", owners)
	}

	// Add another owner
	err = registry.AddOwner("abc123", "coord-3")
	if err != nil {
		t.Fatalf("AddOwner failed: %v", err)
	}

	owners, err = registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 2 {
		t.Errorf("expected 2 owners, got %d", len(owners))
	}
}

func TestChunkRegistryRemoveOwner(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Add two owners
	_ = registry.AddOwner("abc123", "coord-2")
	_ = registry.AddOwner("abc123", "coord-3")

	// Remove one
	err := registry.RemoveOwner("abc123", "coord-2")
	if err != nil {
		t.Fatalf("RemoveOwner failed: %v", err)
	}

	owners, err := registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 1 || owners[0] != "coord-3" {
		t.Errorf("expected owner=coord-3, got %v", owners)
	}

	// Remove last owner - should delete record
	err = registry.RemoveOwner("abc123", "coord-3")
	if err != nil {
		t.Fatalf("RemoveOwner failed: %v", err)
	}

	owners, err = registry.GetOwners("abc123")
	if err != nil {
		t.Fatalf("GetOwners failed: %v", err)
	}

	if len(owners) != 0 {
		t.Errorf("expected 0 owners after removing all, got %d", len(owners))
	}
}

func TestChunkRegistryGetChunksOwnedBy(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register multiple chunks with different owners
	_ = registry.AddOwner("chunk1", "coord-1")
	_ = registry.AddOwner("chunk2", "coord-1")
	_ = registry.AddOwner("chunk2", "coord-2") // chunk2 owned by both
	_ = registry.AddOwner("chunk3", "coord-2")

	// Query chunks owned by coord-1
	chunks, err := registry.GetChunksOwnedBy("coord-1")
	if err != nil {
		t.Fatalf("GetChunksOwnedBy failed: %v", err)
	}

	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks for coord-1, got %d", len(chunks))
	}

	// Query chunks owned by coord-2
	chunks, err = registry.GetChunksOwnedBy("coord-2")
	if err != nil {
		t.Fatalf("GetChunksOwnedBy failed: %v", err)
	}

	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks for coord-2, got %d", len(chunks))
	}

	// Query non-existent coordinator
	chunks, err = registry.GetChunksOwnedBy("coord-999")
	if err != nil {
		t.Fatalf("GetChunksOwnedBy failed: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for coord-999, got %d", len(chunks))
	}
}

func TestChunkRegistryMergeChunkOwnership(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register a chunk locally
	_ = registry.RegisterChunk("abc123", 4096)

	// Create a remote ownership record with different owner
	remote := &ChunkOwnership{
		ChunkHash:         "abc123",
		Owners:            map[string]time.Time{"coord-2": time.Now()},
		VersionVector:     NewVersionVector(),
		ReplicationFactor: 2,
		TotalSize:         4096,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	remote.VersionVector.Increment("coord-2")

	// Merge remote ownership
	modified, err := registry.MergeChunkOwnership(remote)
	if err != nil {
		t.Fatalf("MergeChunkOwnership failed: %v", err)
	}

	if !modified {
		t.Error("expected merge to modify local state")
	}

	// Verify both owners present (concurrent merge)
	owners, _ := registry.GetOwners("abc123")
	if len(owners) != 2 {
		t.Errorf("expected 2 owners after merge, got %d", len(owners))
	}
}

func TestChunkRegistryMergeChunkOwnershipVersionVectors(t *testing.T) {
	tests := []struct {
		name           string
		localVV        map[string]uint64
		remoteVV       map[string]uint64
		expectModified bool
		description    string
	}{
		{
			name:           "remote newer",
			localVV:        map[string]uint64{"coord-1": 1},
			remoteVV:       map[string]uint64{"coord-1": 2},
			expectModified: true,
			description:    "remote version is strictly newer",
		},
		{
			name:           "local newer",
			localVV:        map[string]uint64{"coord-1": 2},
			remoteVV:       map[string]uint64{"coord-1": 1},
			expectModified: false,
			description:    "local version is strictly newer",
		},
		{
			name:           "equal",
			localVV:        map[string]uint64{"coord-1": 1},
			remoteVV:       map[string]uint64{"coord-1": 1},
			expectModified: false,
			description:    "versions are equal",
		},
		{
			name:           "concurrent",
			localVV:        map[string]uint64{"coord-1": 1, "coord-2": 0},
			remoteVV:       map[string]uint64{"coord-1": 0, "coord-2": 1},
			expectModified: true,
			description:    "versions are concurrent, merge owners",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := NewChunkRegistry("coord-1", nil)

			// Create local ownership
			local := &ChunkOwnership{
				ChunkHash:         "test-chunk",
				Owners:            map[string]time.Time{"coord-1": time.Now()},
				VersionVector:     VersionVector(tt.localVV),
				ReplicationFactor: 2,
				TotalSize:         4096,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}

			registry.mu.Lock()
			registry.ownership["test-chunk"] = local
			registry.mu.Unlock()

			// Create remote ownership
			remote := &ChunkOwnership{
				ChunkHash:         "test-chunk",
				Owners:            map[string]time.Time{"coord-2": time.Now()},
				VersionVector:     VersionVector(tt.remoteVV),
				ReplicationFactor: 2,
				TotalSize:         4096,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			}

			// Merge
			modified, err := registry.MergeChunkOwnership(remote)
			if err != nil {
				t.Fatalf("MergeChunkOwnership failed: %v", err)
			}

			if modified != tt.expectModified {
				t.Errorf("expected modified=%v, got %v (%s)", tt.expectModified, modified, tt.description)
			}
		})
	}
}

func TestChunkRegistryExportImportState(t *testing.T) {
	// Create source registry with some data
	source := NewChunkRegistry("coord-1", nil)
	_ = source.RegisterChunk("chunk1", 1024)
	_ = source.RegisterChunk("chunk2", 2048)
	_ = source.AddOwner("chunk1", "coord-2")

	// Export state
	data, err := source.ExportState()
	if err != nil {
		t.Fatalf("ExportState failed: %v", err)
	}

	// Create target registry and import
	target := NewChunkRegistry("coord-2", nil)
	err = target.ImportState(data)
	if err != nil {
		t.Fatalf("ImportState failed: %v", err)
	}

	// Verify chunks imported
	allChunks := target.GetAllChunks()
	if len(allChunks) != 2 {
		t.Errorf("expected 2 chunks after import, got %d", len(allChunks))
	}

	// Verify ownership
	owners, _ := target.GetOwners("chunk1")
	if len(owners) != 2 {
		t.Errorf("expected 2 owners for chunk1, got %d", len(owners))
	}
}

func TestChunkRegistryGetStats(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register some chunks
	_ = registry.RegisterChunk("chunk1", 1024)
	_ = registry.RegisterChunk("chunk2", 2048)
	_ = registry.AddOwner("chunk2", "coord-2") // chunk2 has 2 owners (replication factor=2, optimal)
	_ = registry.AddOwner("chunk2", "coord-3") // chunk2 has 3 owners (over-replicated)

	stats := registry.GetStats()

	if stats["total_chunks"].(int) != 2 {
		t.Errorf("expected total_chunks=2, got %v", stats["total_chunks"])
	}

	if stats["total_size_bytes"].(int64) != 3072 {
		t.Errorf("expected total_size_bytes=3072, got %v", stats["total_size_bytes"])
	}

	if stats["local_chunks"].(int) != 2 {
		t.Errorf("expected local_chunks=2, got %v", stats["local_chunks"])
	}

	if stats["under_replicated"].(int) != 1 {
		t.Errorf("expected under_replicated=1, got %v", stats["under_replicated"])
	}

	if stats["over_replicated"].(int) != 1 {
		t.Errorf("expected over_replicated=1, got %v", stats["over_replicated"])
	}
}

func TestChunkRegistryCleanupStaleOwners(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Add owners with different timestamps
	now := time.Now()
	old := now.Add(-2 * time.Hour)

	// Create ownership manually with stale timestamps
	ownership := &ChunkOwnership{
		ChunkHash: "chunk1",
		Owners: map[string]time.Time{
			"coord-1": now,                     // Recent
			"coord-2": old,                     // Stale
			"coord-3": old.Add(-1 * time.Hour), // Very stale
		},
		VersionVector:     NewVersionVector(),
		ReplicationFactor: 2,
		TotalSize:         4096,
		CreatedAt:         old,
		UpdatedAt:         now,
	}

	registry.mu.Lock()
	registry.ownership["chunk1"] = ownership
	registry.mu.Unlock()

	// Cleanup owners older than 1 hour
	cleaned := registry.CleanupStaleOwners(1 * time.Hour)

	if cleaned != 2 {
		t.Errorf("expected 2 stale owners cleaned, got %d", cleaned)
	}

	// Verify only recent owner remains
	owners, _ := registry.GetOwners("chunk1")
	if len(owners) != 1 {
		t.Errorf("expected 1 owner remaining, got %d", len(owners))
	}

	if owners[0] != "coord-1" {
		t.Errorf("expected coord-1 to remain, got %s", owners[0])
	}
}

func TestChunkRegistryCleanupDeletesChunkWithNoOwners(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Create chunk with only stale owners
	old := time.Now().Add(-2 * time.Hour)
	ownership := &ChunkOwnership{
		ChunkHash: "chunk1",
		Owners: map[string]time.Time{
			"coord-2": old,
		},
		VersionVector:     NewVersionVector(),
		ReplicationFactor: 2,
		TotalSize:         4096,
		CreatedAt:         old,
		UpdatedAt:         old,
	}

	registry.mu.Lock()
	registry.ownership["chunk1"] = ownership
	registry.mu.Unlock()

	// Cleanup
	registry.CleanupStaleOwners(1 * time.Hour)

	// Verify chunk deleted entirely
	allChunks := registry.GetAllChunks()
	if len(allChunks) != 0 {
		t.Errorf("expected 0 chunks after cleanup, got %d", len(allChunks))
	}
}

func TestChunkOwnershipCopy(t *testing.T) {
	original := &ChunkOwnership{
		ChunkHash: "abc123",
		Owners: map[string]time.Time{
			"coord-1": time.Now(),
			"coord-2": time.Now(),
		},
		VersionVector:     NewVersionVector(),
		ReplicationFactor: 2,
		TotalSize:         4096,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	original.VersionVector.Increment("coord-1")

	// Create copy
	copy := original.Copy()

	// Verify copy is independent
	copy.Owners["coord-3"] = time.Now()
	copy.VersionVector.Increment("coord-2")

	if len(original.Owners) == len(copy.Owners) {
		t.Error("modifying copy affected original")
	}

	if original.VersionVector.Get("coord-2") == copy.VersionVector.Get("coord-2") {
		t.Error("modifying copy version vector affected original")
	}
}

func TestChunkRegistryConcurrentAccess(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Concurrent writers
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				chunk := fmt.Sprintf("chunk-%d-%d", id, j)
				_ = registry.RegisterChunk(chunk, 4096)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all chunks registered
	stats := registry.GetStats()
	expected := 1000 // 10 goroutines * 100 chunks each

	if stats["total_chunks"].(int) != expected {
		t.Errorf("expected %d chunks, got %v (possible race condition)", expected, stats["total_chunks"])
	}
}

// Shard-specific tests

func TestRegisterShardChunk(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register a parity shard
	err := registry.RegisterShardChunk("parity-hash-1", 1024, 2, "parity", 0, "file-version-123")
	if err != nil {
		t.Fatalf("RegisterShardChunk failed: %v", err)
	}

	// Verify ownership
	ownership := registry.GetChunkOwnership("parity-hash-1")
	if ownership == nil {
		t.Fatal("expected ownership record, got nil")
	}

	if ownership.ShardType != "parity" {
		t.Errorf("expected shard_type=parity, got %s", ownership.ShardType)
	}

	if ownership.ShardIndex != 0 {
		t.Errorf("expected shard_index=0, got %d", ownership.ShardIndex)
	}

	if ownership.ParentFileID != "file-version-123" {
		t.Errorf("expected parent_file_id=file-version-123, got %s", ownership.ParentFileID)
	}

	if ownership.ReplicationFactor != 2 {
		t.Errorf("expected replication_factor=2, got %d", ownership.ReplicationFactor)
	}
}

func TestRegisterShardChunkInvalidType(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register with invalid shard type
	err := registry.RegisterShardChunk("hash-1", 1024, 2, "invalid", 0, "file-version-123")
	if err == nil {
		t.Error("expected error for invalid shard type, got nil")
	}
}

func TestRegisterShardChunkDataShard(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register a data shard
	err := registry.RegisterShardChunk("data-hash-1", 2048, 2, "data", 5, "file-version-456")
	if err != nil {
		t.Fatalf("RegisterShardChunk failed: %v", err)
	}

	ownership := registry.GetChunkOwnership("data-hash-1")
	if ownership == nil {
		t.Fatal("expected ownership record, got nil")
	}

	if ownership.ShardType != "data" {
		t.Errorf("expected shard_type=data, got %s", ownership.ShardType)
	}

	if ownership.ShardIndex != 5 {
		t.Errorf("expected shard_index=5, got %d", ownership.ShardIndex)
	}
}

func TestGetParityShardsForFile(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register multiple shards for same file
	_ = registry.RegisterShardChunk("data-0", 1024, 2, "data", 0, "file-v1")
	_ = registry.RegisterShardChunk("data-1", 1024, 2, "data", 1, "file-v1")
	_ = registry.RegisterShardChunk("parity-0", 1024, 2, "parity", 0, "file-v1")
	_ = registry.RegisterShardChunk("parity-1", 1024, 2, "parity", 1, "file-v1")
	_ = registry.RegisterShardChunk("parity-2", 1024, 2, "parity", 2, "file-v1")

	// Register shards for different file
	_ = registry.RegisterShardChunk("other-parity", 1024, 2, "parity", 0, "file-v2")

	// Get parity shards for file-v1
	shards, err := registry.GetParityShardsForFile("file-v1")
	if err != nil {
		t.Fatalf("GetParityShardsForFile failed: %v", err)
	}

	if len(shards) != 3 {
		t.Fatalf("expected 3 parity shards, got %d", len(shards))
	}

	// Verify shards are sorted by index
	for i, shard := range shards {
		if shard.ShardIndex != i {
			t.Errorf("shard %d has index %d (expected sorted order)", i, shard.ShardIndex)
		}
		if shard.ShardType != "parity" {
			t.Errorf("shard %d has type %s (expected parity)", i, shard.ShardType)
		}
	}
}

func TestGetParityShardsForFileEmpty(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Query for file with no shards
	shards, err := registry.GetParityShardsForFile("nonexistent-file")
	if err != nil {
		t.Fatalf("GetParityShardsForFile failed: %v", err)
	}

	if len(shards) != 0 {
		t.Errorf("expected 0 shards, got %d", len(shards))
	}
}

func TestGetParityShardsForFileEmptyID(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	_, err := registry.GetParityShardsForFile("")
	if err == nil {
		t.Error("expected error for empty file ID, got nil")
	}
}

func TestGetShardsForFile(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register mixed shards
	_ = registry.RegisterShardChunk("data-0", 1024, 2, "data", 0, "file-v1")
	_ = registry.RegisterShardChunk("data-1", 1024, 2, "data", 1, "file-v1")
	_ = registry.RegisterShardChunk("parity-0", 1024, 2, "parity", 0, "file-v1")
	_ = registry.RegisterShardChunk("parity-1", 1024, 2, "parity", 1, "file-v1")

	// Get all shards for file
	shards, err := registry.GetShardsForFile("file-v1")
	if err != nil {
		t.Fatalf("GetShardsForFile failed: %v", err)
	}

	if len(shards) != 4 {
		t.Fatalf("expected 4 shards, got %d", len(shards))
	}

	// Verify data shards come before parity shards
	dataCount := 0
	parityCount := 0
	sawParity := false

	for _, shard := range shards {
		switch shard.ShardType {
		case "data":
			if sawParity {
				t.Error("data shard found after parity shard (expected data shards first)")
			}
			dataCount++
		case "parity":
			sawParity = true
			parityCount++
		}
	}

	if dataCount != 2 {
		t.Errorf("expected 2 data shards, got %d", dataCount)
	}
	if parityCount != 2 {
		t.Errorf("expected 2 parity shards, got %d", parityCount)
	}
}

func TestGetShardOwnersByType(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register various shards
	_ = registry.RegisterShardChunk("data-0", 1024, 2, "data", 0, "file-v1")
	_ = registry.RegisterShardChunk("data-1", 1024, 2, "data", 1, "file-v1")
	_ = registry.RegisterShardChunk("parity-0", 1024, 2, "parity", 0, "file-v1")
	_ = registry.RegisterShardChunk("parity-1", 1024, 2, "parity", 1, "file-v1")

	// Get parity shard owners
	owners, err := registry.GetShardOwnersByType("parity")
	if err != nil {
		t.Fatalf("GetShardOwnersByType failed: %v", err)
	}

	if len(owners) != 2 {
		t.Fatalf("expected 2 parity chunks, got %d", len(owners))
	}

	// Get data shard owners
	owners, err = registry.GetShardOwnersByType("data")
	if err != nil {
		t.Fatalf("GetShardOwnersByType failed: %v", err)
	}

	if len(owners) != 2 {
		t.Fatalf("expected 2 data chunks, got %d", len(owners))
	}

	// Get all shards (empty type)
	owners, err = registry.GetShardOwnersByType("")
	if err != nil {
		t.Fatalf("GetShardOwnersByType failed: %v", err)
	}

	if len(owners) != 4 {
		t.Fatalf("expected 4 total chunks, got %d", len(owners))
	}
}

func TestGetShardOwnersByTypeInvalid(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	_, err := registry.GetShardOwnersByType("invalid")
	if err == nil {
		t.Error("expected error for invalid shard type, got nil")
	}
}

func TestCleanupOrphanedShards(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register shards for multiple files
	_ = registry.RegisterShardChunk("data-0", 1024, 2, "data", 0, "file-v1")
	_ = registry.RegisterShardChunk("parity-0", 1024, 2, "parity", 0, "file-v1")
	_ = registry.RegisterShardChunk("data-1", 1024, 2, "data", 0, "file-v2")
	_ = registry.RegisterShardChunk("parity-1", 1024, 2, "parity", 0, "file-v2")

	// Register regular chunk (no parent file)
	_ = registry.RegisterChunk("regular-chunk", 1024)

	// Mark only file-v1 as active
	activeFiles := map[string]bool{
		"file-v1": true,
	}

	// Cleanup orphaned shards
	cleaned := registry.CleanupOrphanedShards(activeFiles)

	// Should clean up 2 shards from file-v2
	if cleaned != 2 {
		t.Errorf("expected 2 cleaned shards, got %d", cleaned)
	}

	// Verify file-v1 shards still exist
	shards, _ := registry.GetShardsForFile("file-v1")
	if len(shards) != 2 {
		t.Errorf("expected 2 shards remaining for file-v1, got %d", len(shards))
	}

	// Verify file-v2 shards are gone
	shards, _ = registry.GetShardsForFile("file-v2")
	if len(shards) != 0 {
		t.Errorf("expected 0 shards remaining for file-v2, got %d", len(shards))
	}

	// Verify regular chunk still exists
	ownership := registry.GetChunkOwnership("regular-chunk")
	if ownership == nil {
		t.Error("regular chunk was incorrectly removed")
	}
}

func TestCleanupOrphanedShardsEmptyActive(t *testing.T) {
	registry := NewChunkRegistry("coord-1", nil)

	// Register shards
	_ = registry.RegisterShardChunk("parity-0", 1024, 2, "parity", 0, "file-v1")
	_ = registry.RegisterShardChunk("parity-1", 1024, 2, "parity", 1, "file-v1")

	// Cleanup with no active files
	cleaned := registry.CleanupOrphanedShards(map[string]bool{})

	// Should clean up all shards
	if cleaned != 2 {
		t.Errorf("expected 2 cleaned shards, got %d", cleaned)
	}

	shards, _ := registry.GetShardsForFile("file-v1")
	if len(shards) != 0 {
		t.Errorf("expected 0 shards remaining, got %d", len(shards))
	}
}
