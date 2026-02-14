package s3

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCAS(t *testing.T) *CAS {
	t.Helper()
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	cas, err := NewCAS(t.TempDir(), masterKey)
	require.NoError(t, err)
	return cas
}

func TestCAS_ConcurrentWriteSameHash(t *testing.T) {
	cas := newTestCAS(t)
	ctx := context.Background()
	data := []byte("identical content for all goroutines")

	const goroutines = 20
	var wg sync.WaitGroup
	hashes := make([]string, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			h, err := cas.WriteChunk(ctx, data)
			hashes[idx] = h
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All should succeed with the same hash
	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d failed", i)
		assert.Equal(t, hashes[0], hashes[i], "all hashes should be identical")
	}

	// Verify the chunk is readable and correct
	readBack, err := cas.ReadChunk(ctx, hashes[0])
	require.NoError(t, err)
	assert.Equal(t, data, readBack)
}

func TestCAS_ConcurrentWriteDifferentHashes(t *testing.T) {
	cas := newTestCAS(t)
	ctx := context.Background()

	const goroutines = 20
	var wg sync.WaitGroup
	hashes := make([]string, goroutines)
	errs := make([]error, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine writes unique data
			data := []byte("unique content " + string(rune('A'+idx)))
			h, err := cas.WriteChunk(ctx, data)
			hashes[idx] = h
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i := 0; i < goroutines; i++ {
		require.NoError(t, errs[i], "goroutine %d failed", i)
	}

	// All hashes should be unique (different content)
	seen := make(map[string]bool)
	for _, h := range hashes {
		assert.False(t, seen[h], "duplicate hash found: %s", h)
		seen[h] = true
	}

	// All should be readable
	for i := 0; i < goroutines; i++ {
		data := []byte("unique content " + string(rune('A'+i)))
		readBack, err := cas.ReadChunk(ctx, hashes[i])
		require.NoError(t, err, "failed to read chunk %d", i)
		assert.Equal(t, data, readBack, "chunk %d content mismatch", i)
	}
}

func TestCAS_WriteReadDeleteRoundTrip(t *testing.T) {
	cas := newTestCAS(t)
	ctx := context.Background()

	data := []byte("test chunk data")
	hash, err := cas.WriteChunk(ctx, data)
	require.NoError(t, err)

	// Read back
	readBack, err := cas.ReadChunk(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, data, readBack)

	// Exists
	assert.True(t, cas.ChunkExists(hash))

	// Size
	size, err := cas.ChunkSize(ctx, hash)
	require.NoError(t, err)
	assert.Greater(t, size, int64(0))

	// Delete
	err = cas.DeleteChunk(ctx, hash)
	require.NoError(t, err)

	// Should not exist anymore
	assert.False(t, cas.ChunkExists(hash))

	// Read should fail
	_, err = cas.ReadChunk(ctx, hash)
	assert.Error(t, err)
}
