package s3

import (
	"context"
	"fmt"
	"io"
	"time"
)

// DistributedChunkReader reads chunks from local storage or remote coordinators.
// It implements io.Reader and transparently fetches missing chunks from peers.
type DistributedChunkReader struct {
	ctx          context.Context
	chunks       []string               // Ordered list of chunk hashes
	chunkIndex   int                    // Current chunk being read
	currentChunk []byte                 // Buffer for current chunk
	chunkOffset  int                    // Offset within current chunk
	localCAS     *CAS                   // Local chunk storage
	registry     ChunkRegistryInterface // Global chunk ownership index
	replicator   ReplicatorInterface    // For fetching from remote peers
	timeout      time.Duration
	totalSize    int64 // Total file size (for progress tracking)
	bytesRead    int64 // Bytes read so far
}

// DistributedChunkReaderConfig contains configuration for the distributed chunk reader.
type DistributedChunkReaderConfig struct {
	Chunks     []string               // Ordered list of chunk hashes
	LocalCAS   *CAS                   // Local chunk storage
	Registry   ChunkRegistryInterface // Chunk ownership registry
	Replicator ReplicatorInterface    // For remote chunk fetching
	Timeout    time.Duration          // Timeout for remote chunk fetches
	TotalSize  int64                  // Total file size
}

// NewDistributedChunkReader creates a new distributed chunk reader.
func NewDistributedChunkReader(ctx context.Context, config DistributedChunkReaderConfig) *DistributedChunkReader {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
	}

	return &DistributedChunkReader{
		ctx:        ctx,
		chunks:     config.Chunks,
		localCAS:   config.LocalCAS,
		registry:   config.Registry,
		replicator: config.Replicator,
		timeout:    timeout,
		totalSize:  config.TotalSize,
	}
}

// Read implements io.Reader, reading data from chunks (local or remote).
func (r *DistributedChunkReader) Read(p []byte) (int, error) {
	// Load next chunk if needed
	if r.currentChunk == nil || r.chunkOffset >= len(r.currentChunk) {
		// Check if we've exhausted all chunks
		if r.chunkIndex >= len(r.chunks) {
			return 0, io.EOF
		}

		if err := r.loadNextChunk(); err != nil {
			return 0, err
		}
	}

	// Copy from current chunk to output buffer
	n := copy(p, r.currentChunk[r.chunkOffset:])
	r.chunkOffset += n
	r.bytesRead += int64(n)

	return n, nil
}

// loadNextChunk fetches the next chunk (local or remote).
func (r *DistributedChunkReader) loadNextChunk() error {
	if r.chunkIndex >= len(r.chunks) {
		return io.EOF
	}

	chunkHash := r.chunks[r.chunkIndex]

	// Try local first (fast path)
	chunkData, err := r.localCAS.ReadChunk(r.ctx, chunkHash)
	if err == nil {
		r.currentChunk = chunkData
		r.chunkOffset = 0
		r.chunkIndex++
		return nil
	}

	// Not local - check if registry is available
	if r.registry == nil {
		return fmt.Errorf("chunk %s not found locally and no registry available", chunkHash)
	}

	// Query registry for owners
	owners, err := r.registry.GetOwners(chunkHash)
	if err != nil || len(owners) == 0 {
		return fmt.Errorf("chunk %s not found: no owners in registry", chunkHash)
	}

	// Try each owner in order
	var lastErr error
	for _, ownerID := range owners {
		chunkData, err := r.fetchChunkFromPeer(ownerID, chunkHash)
		if err != nil {
			lastErr = err
			continue // Try next owner
		}

		// Success - cache locally for future reads
		// Ignore write errors - we have the data in memory
		_, _ = r.localCAS.WriteChunk(r.ctx, chunkData)

		// Register ownership locally (we now have the chunk)
		if r.registry != nil {
			_ = r.registry.RegisterChunk(chunkHash, int64(len(chunkData)))
		}

		r.currentChunk = chunkData
		r.chunkOffset = 0
		r.chunkIndex++
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("failed to fetch chunk %s from any owner: %w", chunkHash, lastErr)
	}

	return fmt.Errorf("failed to fetch chunk %s from %d owners", chunkHash, len(owners))
}

// fetchChunkFromPeer requests a chunk from a remote coordinator.
func (r *DistributedChunkReader) fetchChunkFromPeer(peerID, chunkHash string) ([]byte, error) {
	if r.replicator == nil {
		return nil, fmt.Errorf("no replicator configured for remote chunk fetching")
	}

	ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()

	// Use replicator to fetch chunk from peer
	// The replicator will send a chunk request and wait for the response
	chunkData, err := r.replicator.FetchChunk(ctx, peerID, chunkHash)
	if err != nil {
		return nil, fmt.Errorf("fetch chunk from %s: %w", peerID, err)
	}

	return chunkData, nil
}

// Close implements io.Closer (no-op for this reader).
func (r *DistributedChunkReader) Close() error {
	return nil
}

// Progress returns the current read progress (bytes read / total size).
func (r *DistributedChunkReader) Progress() float64 {
	if r.totalSize == 0 {
		return 0
	}
	return float64(r.bytesRead) / float64(r.totalSize)
}
