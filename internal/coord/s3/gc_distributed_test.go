package s3

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGC_DistributedAware tests that GC doesn't delete chunks owned by other coordinators.
func TestGC_DistributedAware(t *testing.T) {
	dir := t.TempDir()

	// Create store with chunk registry
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create mock registry
	mockReg := &mockChunkRegistry{
		owners: make(map[string][]string),
	}
	store.SetChunkRegistry(mockReg)
	store.SetCoordinatorID("coord1")

	// Create an orphaned chunk directly (not referenced by any object)
	chunkData := []byte("Orphaned chunk data for testing")
	chunkHash, err := store.cas.WriteChunk(ctx, chunkData)
	require.NoError(t, err)

	// Simulate: another coordinator (coord2) also owns this chunk
	mockReg.owners[chunkHash] = []string{"coord1", "coord2"}

	// Age the chunk (set mod time to 2 hours ago to pass grace period)
	// Find the chunk file path
	chunkPath := filepath.Join(store.dataDir, "chunks", chunkHash[:2], chunkHash)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	err = os.Chtimes(chunkPath, twoHoursAgo, twoHoursAgo)
	require.NoError(t, err)

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Chunk should NOT be deleted (still owned by coord2)
	assert.Equal(t, 0, stats.ChunksDeleted, "Should not delete shared chunks")
	assert.Equal(t, 1, stats.ChunksSkippedShared, "Should skip shared chunks")

	// Verify chunk still exists
	_, err = store.cas.ReadChunk(ctx, chunkHash)
	assert.NoError(t, err, "Chunk should still exist")
}

// TestGC_DeletesWhenSoleOwner tests that GC deletes chunks when we're the only owner.
func TestGC_DeletesWhenSoleOwner(t *testing.T) {
	dir := t.TempDir()

	// Create store with chunk registry
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create mock registry
	mockReg := &mockChunkRegistry{
		owners: make(map[string][]string),
	}
	store.SetChunkRegistry(mockReg)
	store.SetCoordinatorID("coord1")

	// Create an orphaned chunk directly (not referenced by any object)
	chunkData := []byte("Test data for sole owner GC")
	chunkHash, err := store.cas.WriteChunk(ctx, chunkData)
	require.NoError(t, err)

	// We're the sole owner
	mockReg.owners[chunkHash] = []string{"coord1"}

	// Age the chunk (set mod time to 2 hours ago to pass grace period)
	chunkPath := filepath.Join(store.dataDir, "chunks", chunkHash[:2], chunkHash)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	err = os.Chtimes(chunkPath, twoHoursAgo, twoHoursAgo)
	require.NoError(t, err)

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Chunk should be deleted (we're the sole owner)
	assert.Equal(t, 1, stats.ChunksDeleted, "Should delete when sole owner")
	assert.Equal(t, 0, stats.ChunksSkippedShared, "No shared chunks to skip")

	// Verify chunk was deleted
	_, err = store.cas.ReadChunk(ctx, chunkHash)
	assert.Error(t, err, "Chunk should be deleted")
}

// TestGC_GracePeriod tests that GC doesn't delete recently created chunks.
func TestGC_GracePeriod(t *testing.T) {
	dir := t.TempDir()

	// Create store
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create bucket
	err = store.CreateBucket(ctx, "test-bucket", "test-user")
	require.NoError(t, err)

	// Upload file
	data := []byte("Test data for grace period")
	reader := bytes.NewReader(data)

	meta, err := store.PutObject(ctx, "test-bucket", "test-file", reader, int64(len(data)), "text/plain", nil)
	require.NoError(t, err)
	require.NotEmpty(t, meta.Chunks)

	chunkHash := meta.Chunks[0]

	// Delete the object (chunk becomes orphaned)
	err = store.DeleteObject(ctx, "test-bucket", "test-file")
	require.NoError(t, err)

	// Chunk is recent (within grace period) - don't age it

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Chunk should NOT be deleted (within grace period)
	assert.Equal(t, 0, stats.ChunksDeleted, "Should not delete recent chunks")
	assert.Equal(t, 1, stats.ChunksSkippedGracePeriod, "Should skip chunks in grace period")

	// Verify chunk still exists
	_, err = store.cas.ReadChunk(ctx, chunkHash)
	assert.NoError(t, err, "Chunk should still exist during grace period")
}

// TestGC_NoRegistry tests that GC works without a registry (local-only mode).
func TestGC_NoRegistry(t *testing.T) {
	dir := t.TempDir()

	// Create store WITHOUT chunk registry
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create an orphaned chunk directly (not referenced by any object)
	chunkData := []byte("Test data for no registry GC")
	chunkHash, err := store.cas.WriteChunk(ctx, chunkData)
	require.NoError(t, err)

	// Age the chunk (set mod time to 2 hours ago to pass grace period)
	chunkPath := filepath.Join(store.dataDir, "chunks", chunkHash[:2], chunkHash)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	err = os.Chtimes(chunkPath, twoHoursAgo, twoHoursAgo)
	require.NoError(t, err)

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Chunk should be deleted (no registry to check)
	assert.Equal(t, 1, stats.ChunksDeleted, "Should delete orphaned chunks without registry")
	assert.Equal(t, 0, stats.ChunksSkippedShared, "No registry means no shared check")

	// Verify chunk was deleted
	_, err = store.cas.ReadChunk(ctx, chunkHash)
	assert.Error(t, err, "Chunk should be deleted")
}

// TestGC_MultipleOwners tests GC with multiple owners in registry.
func TestGC_MultipleOwners(t *testing.T) {
	dir := t.TempDir()

	// Create store with chunk registry
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create mock registry
	mockReg := &mockChunkRegistry{
		owners: make(map[string][]string),
	}
	store.SetChunkRegistry(mockReg)
	store.SetCoordinatorID("coord1")

	// Create 3 orphaned chunks directly (not referenced by any object)
	chunk1Data := []byte("Chunk 1: sole owner")
	chunk1Hash, err := store.cas.WriteChunk(ctx, chunk1Data)
	require.NoError(t, err)

	chunk2Data := []byte("Chunk 2: shared ownership")
	chunk2Hash, err := store.cas.WriteChunk(ctx, chunk2Data)
	require.NoError(t, err)

	chunk3Data := []byte("Chunk 3: other coordinator only")
	chunk3Hash, err := store.cas.WriteChunk(ctx, chunk3Data)
	require.NoError(t, err)

	// Simulate different ownership scenarios:
	// Chunk 1: Only us (should be deleted)
	// Chunk 2: Us + coord2 (should NOT be deleted)
	// Chunk 3: Only coord2 (should NOT be deleted - we're out of sync)
	mockReg.owners[chunk1Hash] = []string{"coord1"}
	mockReg.owners[chunk2Hash] = []string{"coord1", "coord2"}
	mockReg.owners[chunk3Hash] = []string{"coord2"}

	// Age all chunks
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	for _, chunkHash := range []string{chunk1Hash, chunk2Hash, chunk3Hash} {
		chunkPath := filepath.Join(store.dataDir, "chunks", chunkHash[:2], chunkHash)
		err = os.Chtimes(chunkPath, twoHoursAgo, twoHoursAgo)
		require.NoError(t, err)
	}

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Only chunk 1 should be deleted (sole owner)
	assert.Equal(t, 1, stats.ChunksDeleted, "Should delete only sole-owned chunk")
	assert.Equal(t, 2, stats.ChunksSkippedShared, "Should skip 2 shared chunks")
}

// TestGC_RegistryUnavailable tests GC behavior when registry query fails.
func TestGC_RegistryUnavailable(t *testing.T) {
	dir := t.TempDir()

	// Create store with failing registry
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	ctx := context.Background()

	var encryptionKey [32]byte
	err = store.InitCAS(ctx, encryptionKey)
	require.NoError(t, err)

	// Create mock registry that returns errors
	mockReg := &failingChunkRegistry{}
	store.SetChunkRegistry(mockReg)
	store.SetCoordinatorID("coord1")

	// Create an orphaned chunk directly (not referenced by any object)
	chunkData := []byte("Test data for registry failure")
	chunkHash, err := store.cas.WriteChunk(ctx, chunkData)
	require.NoError(t, err)

	// Age the chunk (set mod time to 2 hours ago to pass grace period)
	chunkPath := filepath.Join(store.dataDir, "chunks", chunkHash[:2], chunkHash)
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	err = os.Chtimes(chunkPath, twoHoursAgo, twoHoursAgo)
	require.NoError(t, err)

	// Run GC
	stats := store.RunGarbageCollection(ctx)

	// Fail-safe: When registry is unavailable, proceed with deletion
	// (better to delete locally than leak storage)
	assert.Equal(t, 1, stats.ChunksDeleted, "Should delete when registry unavailable (fail-safe)")
}

// failingChunkRegistry is a mock that always returns errors.
type failingChunkRegistry struct{}

func (f *failingChunkRegistry) RegisterChunk(hash string, size int64) error {
	return assert.AnError
}

func (f *failingChunkRegistry) UnregisterChunk(hash string) error {
	return assert.AnError
}

func (f *failingChunkRegistry) GetOwners(hash string) ([]string, error) {
	return nil, assert.AnError
}

func (f *failingChunkRegistry) GetChunksOwnedBy(coordinatorID string) ([]string, error) {
	return nil, assert.AnError
}
