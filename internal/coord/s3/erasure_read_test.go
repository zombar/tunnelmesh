package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStoreWithErasureCoding creates a test store and a bucket with erasure coding enabled.
func newTestStoreWithErasureCoding(t *testing.T, k, m int) *Store {
	t.Helper()
	store := newTestStoreWithCAS(t)
	err := store.CreateBucket(context.Background(), "ec-bucket", "test-user", 2, &ErasureCodingPolicy{
		Enabled:      true,
		DataShards:   k,
		ParityShards: m,
	})
	require.NoError(t, err)
	return store
}

// putAndGet is a helper that puts an object and reads it back.
func putAndGet(t *testing.T, store *Store, bucket, key string, data []byte) []byte {
	t.Helper()
	ctx := context.Background()

	_, err := store.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	reader, _, err := store.GetObject(ctx, bucket, key)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	return got
}

// TestErasureCodingReadPath_FastPath verifies the fast path (all data shards available).
func TestErasureCodingReadPath_FastPath(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		k    int
		m    int
	}{
		{"small_3+2", []byte("Hello, erasure coding!"), 3, 2},
		{"medium_6+3", bytes.Repeat([]byte("ABCDEFGH"), 512), 6, 3}, // 4KB
		{"uneven_size_5+2", []byte("This data doesn't divide evenly into shards"), 5, 2},
		{"minimal_2+1", []byte("Minimum viable erasure coding test data"), 2, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStoreWithErasureCoding(t, tt.k, tt.m)
			got := putAndGet(t, store, "ec-bucket", "test.bin", tt.data)
			assert.Equal(t, tt.data, got, "read data should match written data")
		})
	}
}

// TestErasureCodingReadPath_LargeFile tests the fast path with a larger file
// that produces multiple CDC chunks per shard.
func TestErasureCodingReadPath_LargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file erasure coding test in short mode")
	}

	// 256KB file with 6+3 encoding → each shard ~43KB → multiple CDC chunks per shard
	data := make([]byte, 256*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, 6, 3)
	got := putAndGet(t, store, "ec-bucket", "large.bin", data)
	assert.Equal(t, data, got, "large file should round-trip through erasure coding")
}

// TestErasureCodingReadPath_Reconstruction tests reading when some data shards are missing.
func TestErasureCodingReadPath_Reconstruction(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024) // 24KB → 4KB per shard
	_, err := rand.Read(data)
	require.NoError(t, err)

	for missing := 1; missing <= m; missing++ {
		t.Run(fmt.Sprintf("missing_%d_data_shards", missing), func(t *testing.T) {
			store := newTestStoreWithErasureCoding(t, k, m)
			ctx := context.Background()

			// Write the object
			meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
			require.NoError(t, err)
			require.NotNil(t, meta.ErasureCoding)

			// Delete some data chunks from CAS to simulate missing shards
			// Group data hashes by shard index to know which chunks to delete
			shardsDeleted := make(map[int]bool)
			for _, chunkHash := range meta.ErasureCoding.DataHashes {
				chunkMeta := meta.ChunkMetadata[chunkHash]
				if chunkMeta == nil {
					continue
				}
				if shardsDeleted[chunkMeta.ShardIndex] {
					// Already deleting this shard's chunks
					_, _ = store.cas.DeleteChunk(ctx, chunkHash)
					continue
				}
				if len(shardsDeleted) < missing {
					shardsDeleted[chunkMeta.ShardIndex] = true
					_, _ = store.cas.DeleteChunk(ctx, chunkHash)
				}
			}
			require.Equal(t, missing, len(shardsDeleted), "should have deleted chunks for %d shards", missing)

			// Read should succeed via reconstruction
			reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
			require.NoError(t, err)
			defer func() { _ = reader.Close() }()

			got, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, data, got, "reconstructed data should match original with %d missing shards", missing)

			// Wait for background caching goroutine to complete
			store.WaitBackground()
		})
	}
}

// TestErasureCodingReadPath_InsufficientShards tests that reading fails when too many shards are missing.
func TestErasureCodingReadPath_InsufficientShards(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Delete ALL data chunks AND one parity shard (m+1 shards missing → insufficient)
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		_, _ = store.cas.DeleteChunk(ctx, chunkHash)
	}
	// Also delete one parity shard so we have < k available
	_, _ = store.cas.DeleteChunk(ctx, meta.ErasureCoding.ParityHashes[0])

	// Read should fail
	_, _, err = store.GetObject(ctx, "ec-bucket", "test.bin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient shards")
}

// TestErasureCodingReadPath_ContextCancellation tests that reads respect context cancellation.
func TestErasureCodingReadPath_ContextCancellation(t *testing.T) {
	k, m := 3, 2
	data := []byte("Test data for context cancellation")

	store := newTestStoreWithErasureCoding(t, k, m)

	// Write the object
	_, err := store.PutObject(context.Background(), "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "text/plain", nil)
	require.NoError(t, err)

	// Read with already-cancelled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, _, err = store.GetObject(canceledCtx, "ec-bucket", "test.bin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

// TestErasureCodingReadPath_ChunkOrdering verifies that chunks are reassembled
// in the correct order even with multiple chunks per shard.
func TestErasureCodingReadPath_ChunkOrdering(t *testing.T) {
	// Use large enough random data so each shard has multiple CDC chunks.
	// Random data avoids cross-shard CDC hash collisions that could occur
	// with sequential/repeating patterns.
	// With k=3, each shard will be ~85KB → multiple CDC chunks (target 4KB)
	data := make([]byte, 256*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, 3, 2)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "ordered.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)
	require.NotNil(t, meta.ErasureCoding)

	// Verify ChunkSequence is set in metadata
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		require.NotNil(t, chunkMeta, "chunk metadata should exist for %s", chunkHash[:8])
		assert.Equal(t, "data", chunkMeta.ShardType)
		// ChunkSequence should be >= 0 (0-based)
		assert.GreaterOrEqual(t, chunkMeta.ChunkSequence, 0)
	}

	// Read back and verify byte-for-byte correctness
	reader, _, err := store.GetObject(ctx, "ec-bucket", "ordered.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "data should match byte-for-byte (ordering preserved)")
}

// TestErasureCodingReadPath_ParityOnlyReconstruction tests reconstruction using
// only parity shards when all data shards are missing.
func TestErasureCodingReadPath_ParityOnlyReconstruction(t *testing.T) {
	// Use k=3, m=3 so we have enough parity to reconstruct with zero data shards
	k, m := 3, 3
	data := []byte("Test parity-only reconstruction with equal data and parity shards")

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "text/plain", nil)
	require.NoError(t, err)

	// Delete ALL data chunks
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		_, _ = store.cas.DeleteChunk(ctx, chunkHash)
	}

	// Read should succeed (k=3 parity shards available = k needed)
	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "should reconstruct from parity shards alone")

	// Wait for background caching goroutine
	store.WaitBackground()
}

// TestErasureCodingReadPath_MixedShardReconstruction tests reconstruction with
// a mix of data and parity shards.
func TestErasureCodingReadPath_MixedShardReconstruction(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Delete 2 data shards and 1 parity shard (still have k+m-3 = 6 available)
	shardsDeleted := 0
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		if chunkMeta != nil && chunkMeta.ShardIndex < 2 {
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
			shardsDeleted++
		}
	}
	_, _ = store.cas.DeleteChunk(ctx, meta.ErasureCoding.ParityHashes[0])

	// Should still be able to read (6 data + 3 parity - 2 data - 1 parity = 6 ≥ k=6)
	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "should reconstruct with mixed available shards")

	// Wait for background caching goroutine
	store.WaitBackground()
}

// TestErasureCodingReadPath_MetadataIntegrity verifies that erasure coding metadata
// is correctly stored and retrieved.
func TestErasureCodingReadPath_MetadataIntegrity(t *testing.T) {
	k, m := 6, 3
	data := bytes.Repeat([]byte("metadata test"), 1000)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "meta-test.bin", bytes.NewReader(data), int64(len(data)), "text/plain", nil)
	require.NoError(t, err)

	// Verify ErasureCodingInfo
	ec := meta.ErasureCoding
	require.NotNil(t, ec)
	assert.True(t, ec.Enabled)
	assert.Equal(t, k, ec.DataShards)
	assert.Equal(t, m, ec.ParityShards)
	assert.NotEmpty(t, ec.DataHashes, "should have data chunk hashes")
	assert.Equal(t, m, len(ec.ParityHashes), "should have exactly m parity hashes")
	assert.Greater(t, ec.ShardSize, int64(0), "shard size should be positive")

	// Verify ChunkMetadata for data chunks
	for _, hash := range ec.DataHashes {
		cm := meta.ChunkMetadata[hash]
		require.NotNil(t, cm, "data chunk %s should have metadata", hash[:8])
		assert.Equal(t, "data", cm.ShardType)
		assert.GreaterOrEqual(t, cm.ShardIndex, 0)
		assert.Less(t, cm.ShardIndex, k)
	}

	// Verify ChunkMetadata for parity shards
	for i, hash := range ec.ParityHashes {
		cm := meta.ChunkMetadata[hash]
		require.NotNil(t, cm, "parity shard %d should have metadata", i)
		assert.Equal(t, "parity", cm.ShardType)
		assert.Equal(t, i, cm.ShardIndex)
	}
}

// TestErasureCodingReadPath_MultipleObjects tests reading multiple erasure-coded
// objects from the same bucket.
func TestErasureCodingReadPath_MultipleObjects(t *testing.T) {
	store := newTestStoreWithErasureCoding(t, 3, 2)
	ctx := context.Background()

	objects := map[string][]byte{
		"file1.txt": []byte("First file content"),
		"file2.txt": bytes.Repeat([]byte("Second file "), 100),
		"file3.bin": make([]byte, 8192),
	}
	_, _ = rand.Read(objects["file3.bin"])

	// Write all objects
	for key, data := range objects {
		_, err := store.PutObject(ctx, "ec-bucket", key, bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
		require.NoError(t, err)
	}

	// Read all objects and verify
	for key, expected := range objects {
		reader, _, err := store.GetObject(ctx, "ec-bucket", key)
		require.NoError(t, err)

		got, err := io.ReadAll(reader)
		_ = reader.Close()
		require.NoError(t, err)
		assert.Equal(t, expected, got, "object %s should match", key)
	}
}

// TestErasureCodingReadPath_NonErasureBucketUnaffected tests that non-EC buckets
// continue to work normally.
func TestErasureCodingReadPath_NonErasureBucketUnaffected(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Create a non-EC bucket
	err := store.CreateBucket(ctx, "normal-bucket", "test-user", 2, nil)
	require.NoError(t, err)

	data := []byte("Normal bucket data without erasure coding")
	_, err = store.PutObject(ctx, "normal-bucket", "test.txt", bytes.NewReader(data), int64(len(data)), "text/plain", nil)
	require.NoError(t, err)

	reader, meta, err := store.GetObject(ctx, "normal-bucket", "test.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Nil(t, meta.ErasureCoding, "non-EC bucket should not have erasure coding metadata")

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// TestErasureCodingReadPath_VersionedObject tests erasure coding with versioning.
func TestErasureCodingReadPath_VersionedObject(t *testing.T) {
	store := newTestStoreWithErasureCoding(t, 3, 2)
	ctx := context.Background()

	// Write two versions
	v1Data := []byte("Version 1 of the file")
	_, err := store.PutObject(ctx, "ec-bucket", "versioned.txt", bytes.NewReader(v1Data), int64(len(v1Data)), "text/plain", nil)
	require.NoError(t, err)

	v2Data := []byte("Version 2 of the file - with more content to be different")
	_, err = store.PutObject(ctx, "ec-bucket", "versioned.txt", bytes.NewReader(v2Data), int64(len(v2Data)), "text/plain", nil)
	require.NoError(t, err)

	// Read current version (should be v2)
	reader, _, err := store.GetObject(ctx, "ec-bucket", "versioned.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, v2Data, got, "should read latest version")
}

// TestErasureCodingReadPath_RepeatedContent tests erasure coding with data that
// may produce duplicate chunk hashes (deduplication scenario).
func TestErasureCodingReadPath_RepeatedContent(t *testing.T) {
	// Data with repeated patterns that CDC may chunk similarly
	data := []byte(strings.Repeat("ABCDEFGHIJKLMNOP", 256)) // 4KB of repeated pattern

	store := newTestStoreWithErasureCoding(t, 3, 2)
	got := putAndGet(t, store, "ec-bucket", "repeated.bin", data)
	assert.Equal(t, data, got, "repeated content should round-trip correctly")
}

// TestErasureCodingReadPath_DistributedFetch tests that missing local chunks are
// fetched from remote coordinators via the chunk registry and replicator.
func TestErasureCodingReadPath_DistributedFetch(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024) // 24KB
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	// Write the object
	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)
	require.NotNil(t, meta.ErasureCoding)

	// Read all data chunks from CAS before deleting them (to populate the mock replicator)
	remoteChunks := make(map[string][]byte)
	registryOwners := make(map[string][]string)
	shardsToDelete := 2 // Delete 2 data shards' worth of chunks
	shardsDeleted := make(map[int]bool)

	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		if chunkMeta == nil {
			continue
		}
		chunkData, readErr := store.cas.ReadChunk(ctx, chunkHash)
		if readErr != nil {
			continue
		}
		// Mark first N unseen shards for deletion
		if !shardsDeleted[chunkMeta.ShardIndex] && len(shardsDeleted) < shardsToDelete {
			shardsDeleted[chunkMeta.ShardIndex] = true
		}
		// Move all chunks belonging to marked shards to remote
		if shardsDeleted[chunkMeta.ShardIndex] {
			remoteChunks[chunkHash] = chunkData
			registryOwners[chunkHash] = []string{"remote-coord-1"}
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
		}
	}
	require.Equal(t, shardsToDelete, len(shardsDeleted), "should have deleted chunks for %d shards", shardsToDelete)

	// Set up mock registry and replicator
	mockReg := &mockChunkRegistry{owners: registryOwners}
	mockRepl := &mockReplicator{chunks: remoteChunks}
	store.SetChunkRegistry(mockReg)
	store.SetReplicator(mockRepl)

	// Read should succeed by fetching missing chunks from remote
	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "data should match after distributed fetch of missing chunks")
}

// TestErasureCodingReadPath_DistributedFetchFallbackToReconstruction tests that when
// remote fetch fails for some chunks, the EC path falls back to RS reconstruction.
func TestErasureCodingReadPath_DistributedFetchFallbackToReconstruction(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Delete 2 data shard chunks - but do NOT populate the replicator with them
	// This simulates remote fetch failing, forcing RS reconstruction
	shardsDeleted := make(map[int]bool)
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		if chunkMeta == nil {
			continue
		}
		if shardsDeleted[chunkMeta.ShardIndex] {
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
			continue
		}
		if len(shardsDeleted) < 2 {
			shardsDeleted[chunkMeta.ShardIndex] = true
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
		}
	}

	// Set up registry with empty owners (remote fetch will fail)
	mockReg := &mockChunkRegistry{owners: make(map[string][]string)}
	mockRepl := &mockReplicator{chunks: make(map[string][]byte)}
	store.SetChunkRegistry(mockReg)
	store.SetReplicator(mockRepl)

	// Read should still succeed via RS reconstruction (we have enough parity)
	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "should reconstruct via RS after failed distributed fetch")

	// Wait for background caching goroutine
	store.WaitBackground()
}

// TestErasureCodingReadPath_NoDistributedWithoutReplicator tests that without
// a replicator, the EC read path behaves as before (local-only + reconstruction).
func TestErasureCodingReadPath_NoDistributedWithoutReplicator(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Delete 2 data shards (within parity tolerance)
	shardsDeleted := make(map[int]bool)
	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		if chunkMeta == nil {
			continue
		}
		if shardsDeleted[chunkMeta.ShardIndex] {
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
			continue
		}
		if len(shardsDeleted) < 2 {
			shardsDeleted[chunkMeta.ShardIndex] = true
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
		}
	}

	// No registry or replicator set - should fall back to RS reconstruction
	assert.Nil(t, store.chunkRegistry)
	assert.Nil(t, store.replicator)

	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "should reconstruct via RS without distributed fetch capability")

	// Wait for background caching goroutine
	store.WaitBackground()
}

// TestErasureCodingReadPath_DistributedFetchHashMismatch tests that corrupt chunks
// from remote coordinators are rejected and the read falls back to RS reconstruction.
func TestErasureCodingReadPath_DistributedFetchHashMismatch(t *testing.T) {
	k, m := 6, 3
	data := make([]byte, 24*1024)
	_, err := rand.Read(data)
	require.NoError(t, err)

	store := newTestStoreWithErasureCoding(t, k, m)
	ctx := context.Background()

	meta, err := store.PutObject(ctx, "ec-bucket", "test.bin", bytes.NewReader(data), int64(len(data)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Delete 2 data shards and populate replicator with corrupted data
	corruptChunks := make(map[string][]byte)
	registryOwners := make(map[string][]string)
	shardsDeleted := make(map[int]bool)

	for _, chunkHash := range meta.ErasureCoding.DataHashes {
		chunkMeta := meta.ChunkMetadata[chunkHash]
		if chunkMeta == nil {
			continue
		}
		if !shardsDeleted[chunkMeta.ShardIndex] && len(shardsDeleted) < 2 {
			shardsDeleted[chunkMeta.ShardIndex] = true
		}
		if shardsDeleted[chunkMeta.ShardIndex] {
			// Store corrupted data (wrong bytes) so hash validation fails
			corruptChunks[chunkHash] = []byte("corrupted data that won't match hash")
			registryOwners[chunkHash] = []string{"bad-coord-1"}
			_, _ = store.cas.DeleteChunk(ctx, chunkHash)
		}
	}
	require.Equal(t, 2, len(shardsDeleted))

	mockReg := &mockChunkRegistry{owners: registryOwners}
	mockRepl := &mockReplicator{chunks: corruptChunks}
	store.SetChunkRegistry(mockReg)
	store.SetReplicator(mockRepl)

	// Remote chunks are corrupt → hash mismatch → fall back to RS reconstruction
	// With 2 missing data shards and 3 parity shards, RS should still succeed
	reader, _, err := store.GetObject(ctx, "ec-bucket", "test.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, data, got, "should reconstruct via RS after rejecting corrupt remote chunks")

	// Wait for background caching goroutine
	store.WaitBackground()
}
