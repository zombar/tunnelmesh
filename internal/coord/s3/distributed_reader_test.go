package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockReplicator implements ReplicatorInterface for testing.
type mockReplicator struct {
	chunks map[string][]byte // chunkHash -> chunk data
}

func (m *mockReplicator) FetchChunk(ctx context.Context, peerID, chunkHash string) ([]byte, error) {
	data, exists := m.chunks[chunkHash]
	if !exists {
		return nil, fmt.Errorf("chunk not found: %s", chunkHash)
	}
	return data, nil
}

// mockChunkRegistry implements ChunkRegistryInterface for testing.
type mockChunkRegistry struct {
	owners map[string][]string // chunkHash -> []coordinatorID
}

func (m *mockChunkRegistry) RegisterChunk(hash string, size int64) error {
	return nil
}

func (m *mockChunkRegistry) UnregisterChunk(hash string) error {
	return nil
}

func (m *mockChunkRegistry) GetOwners(hash string) ([]string, error) {
	owners, exists := m.owners[hash]
	if !exists {
		return nil, fmt.Errorf("chunk not found in registry")
	}
	return owners, nil
}

func (m *mockChunkRegistry) GetChunksOwnedBy(coordinatorID string) ([]string, error) {
	var chunks []string
	for hash, owners := range m.owners {
		for _, owner := range owners {
			if owner == coordinatorID {
				chunks = append(chunks, hash)
				break
			}
		}
	}
	return chunks, nil
}

func TestDistributedChunkReader_AllChunksLocal(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Write test chunks locally
	chunk1 := []byte("Hello ")
	chunk2 := []byte("World!")

	hash1, err := cas.WriteChunk(ctx, chunk1)
	require.NoError(t, err)

	hash2, err := cas.WriteChunk(ctx, chunk2)
	require.NoError(t, err)

	// Create distributed reader (replicator is nil - not needed for local-only)
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:   []string{hash1, hash2},
		LocalCAS: cas,
		Logger:   zerolog.Nop(),
	})

	// Read all data
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "Hello World!", string(data))

	// Check progress
	assert.Equal(t, 0.0, reader.Progress()) // Total size not set
}

func TestDistributedChunkReader_FetchFromRemote(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Create test chunks (only write chunk1 locally)
	chunk1 := []byte("Local ")
	chunk2 := []byte("Remote ")
	chunk3 := []byte("Data!")

	hash1, err := cas.WriteChunk(ctx, chunk1)
	require.NoError(t, err)

	// Hash chunk2 and chunk3 without writing locally
	hash2 := ContentHash(chunk2)
	hash3 := ContentHash(chunk3)

	// Create mock replicator with remote chunks
	mockRepl := &mockReplicator{
		chunks: map[string][]byte{
			hash2: chunk2,
			hash3: chunk3,
		},
	}

	// Create mock registry
	mockReg := &mockChunkRegistry{
		owners: map[string][]string{
			hash1: {"local"},
			hash2: {"peer1"},
			hash3: {"peer2"},
		},
	}

	// Create distributed reader
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:     []string{hash1, hash2, hash3},
		LocalCAS:   cas,
		Registry:   mockReg,
		Replicator: mockRepl,
		Logger:     zerolog.Nop(),
		TotalSize:  int64(len(chunk1) + len(chunk2) + len(chunk3)),
	})

	// Read all data (should fetch chunk2 and chunk3 from remote)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "Local Remote Data!", string(data))

	// Verify chunks were cached locally
	cachedChunk2, err := cas.ReadChunk(ctx, hash2)
	require.NoError(t, err)
	assert.Equal(t, chunk2, cachedChunk2)

	cachedChunk3, err := cas.ReadChunk(ctx, hash3)
	require.NoError(t, err)
	assert.Equal(t, chunk3, cachedChunk3)

	// Check progress
	assert.Equal(t, 1.0, reader.Progress())
}

func TestDistributedChunkReader_ChunkNotFound(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Missing chunk hash (not in CAS or replicator)
	missingHash := "0000000000000000000000000000000000000000000000000000000000000000"

	// Create mock registry (no owners for missing chunk)
	mockReg := &mockChunkRegistry{
		owners: map[string][]string{},
	}

	// Create distributed reader
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:   []string{missingHash},
		LocalCAS: cas,
		Registry: mockReg,
		Logger:   zerolog.Nop(),
	})

	// Read should fail
	_, err = io.ReadAll(reader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDistributedChunkReader_EmptyFile(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Create distributed reader with no chunks
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:   []string{},
		LocalCAS: cas,
		Logger:   zerolog.Nop(),
	})

	// Read should return empty data
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestDistributedChunkReader_PartialReads(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Write test chunks
	chunk1 := []byte("AAAA")
	chunk2 := []byte("BBBB")
	chunk3 := []byte("CCCC")

	hash1, err := cas.WriteChunk(ctx, chunk1)
	require.NoError(t, err)

	hash2, err := cas.WriteChunk(ctx, chunk2)
	require.NoError(t, err)

	hash3, err := cas.WriteChunk(ctx, chunk3)
	require.NoError(t, err)

	// Create distributed reader
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:   []string{hash1, hash2, hash3},
		LocalCAS: cas,
		Logger:   zerolog.Nop(),
	})

	// Read in small chunks to test partial reads across chunk boundaries
	var result bytes.Buffer
	buf := make([]byte, 3) // Smaller than chunk size

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, "AAAABBBBCCCC", result.String())
}

func TestDistributedChunkReader_NoRegistry(t *testing.T) {
	dir := t.TempDir()

	// Create CAS (use zero key for testing)
	var encryptionKey [32]byte
	cas, err := NewCAS(filepath.Join(dir, "cas"), encryptionKey)
	require.NoError(t, err)

	ctx := context.Background()

	// Missing chunk (not in CAS)
	missingHash := "0000000000000000000000000000000000000000000000000000000000000000"

	// Create distributed reader without registry
	reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
		Chunks:   []string{missingHash},
		LocalCAS: cas,
		Registry: nil, // No registry
		Logger:   zerolog.Nop(),
	})

	// Read should fail with appropriate error
	_, err = io.ReadAll(reader)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no registry available")
}
