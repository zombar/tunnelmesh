package s3

import (
	"bytes"
	"fmt"
	"io"

	"github.com/klauspost/reedsolomon"
)

// EncodeFile encodes file data into k data shards and m parity shards using Reed-Solomon erasure coding.
// Returns the data shards (which match the original data split into k pieces) and parity shards.
// The original data can be reconstructed from any k shards.
//
// For files larger than ~100MB, consider using EncodeStream (when implemented in Phase 6)
// to avoid loading the entire file into memory.
//
// Performance: Approximately 20-50 MB/s on modern CPUs with SIMD support (AVX2/NEON).
func EncodeFile(data []byte, k, m int) (dataShards, parityShards [][]byte, err error) {
	if k < 1 {
		return nil, nil, fmt.Errorf("data shards (k) must be >= 1, got %d", k)
	}
	if m < 1 {
		return nil, nil, fmt.Errorf("parity shards (m) must be >= 1, got %d", m)
	}
	if k+m > 256 {
		return nil, nil, fmt.Errorf("total shards (k+m) must be <= 256, got %d", k+m)
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("cannot encode empty data")
	}

	// Create Reed-Solomon encoder
	enc, err := reedsolomon.New(k, m)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create RS encoder: %w", err)
	}

	// Calculate shard size (must be equal for all shards)
	shardSize := (len(data) + k - 1) / k // Ceiling division

	// Split data into k shards, padding last shard if necessary
	shards := make([][]byte, k+m)
	for i := 0; i < k; i++ {
		shards[i] = make([]byte, shardSize)
		start := i * shardSize
		if start < len(data) {
			end := start + shardSize
			if end > len(data) {
				// Last shard: copy partial data, rest is already zero-padded
				copy(shards[i], data[start:])
			} else {
				copy(shards[i], data[start:end])
			}
		}
		// If start >= len(data), shard remains zero-padded
	}

	// Initialize parity shards (m empty slices)
	for i := k; i < k+m; i++ {
		shards[i] = make([]byte, shardSize)
	}

	// Generate parity shards
	if err := enc.Encode(shards); err != nil {
		return nil, nil, fmt.Errorf("failed to encode shards: %w", err)
	}

	// Verify encoding (sanity check)
	ok, err := enc.Verify(shards)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to verify encoded shards: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("shard verification failed after encoding")
	}

	// Separate data and parity shards
	dataShards = shards[:k]
	parityShards = shards[k:]

	return dataShards, parityShards, nil
}

// DecodeFile reconstructs the original file from k or more shards (mix of data and parity).
// shards is a slice of length k+m where missing shards are represented as nil.
// originalSize is the size of the original file before encoding (to strip padding).
//
// NOTE: This function modifies the shards slice in-place by reconstructing nil entries.
// Callers should not rely on the original nil values after calling this function.
func DecodeFile(shards [][]byte, k, m int, originalSize int64) ([]byte, error) {
	if k < 1 || m < 1 {
		return nil, fmt.Errorf("invalid k=%d or m=%d", k, m)
	}
	if len(shards) != k+m {
		return nil, fmt.Errorf("expected %d shards (k+m), got %d", k+m, len(shards))
	}
	if originalSize <= 0 {
		return nil, fmt.Errorf("original size must be > 0, got %d", originalSize)
	}

	// Validate originalSize against maximum reconstructible data
	// (prevent corruption/overflow if originalSize is invalid)
	var maxShardSize int64
	for _, shard := range shards {
		if shard != nil && int64(len(shard)) > maxShardSize {
			maxShardSize = int64(len(shard))
		}
	}
	maxReconstructibleSize := maxShardSize * int64(k)
	if originalSize > maxReconstructibleSize {
		return nil, fmt.Errorf("originalSize %d exceeds maximum reconstructible data %d (shard_size * k)", originalSize, maxReconstructibleSize)
	}

	// Count available shards
	available := 0
	for _, shard := range shards {
		if shard != nil {
			available++
		}
	}

	if available < k {
		return nil, fmt.Errorf("insufficient shards for reconstruction: need %d, have %d", k, available)
	}

	// Create Reed-Solomon decoder
	enc, err := reedsolomon.New(k, m)
	if err != nil {
		return nil, fmt.Errorf("failed to create RS decoder: %w", err)
	}

	// Reconstruct missing shards
	if err := enc.Reconstruct(shards); err != nil {
		return nil, fmt.Errorf("failed to reconstruct shards: %w", err)
	}

	// Verify reconstructed data
	ok, err := enc.Verify(shards)
	if err != nil {
		return nil, fmt.Errorf("failed to verify reconstructed shards: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("shard verification failed after reconstruction")
	}

	// Reassemble data shards into original file
	var buf bytes.Buffer
	for i := 0; i < k; i++ {
		if shards[i] == nil {
			return nil, fmt.Errorf("data shard %d is nil after reconstruction", i)
		}
		if _, err := buf.Write(shards[i]); err != nil {
			return nil, fmt.Errorf("failed to write shard %d: %w", i, err)
		}
	}

	// Trim padding to original size
	data := buf.Bytes()
	if int64(len(data)) < originalSize {
		return nil, fmt.Errorf("reconstructed data too small: got %d bytes, expected %d", len(data), originalSize)
	}
	data = data[:originalSize]

	return data, nil
}

// EncodeStream encodes data from a reader into shards, writing them to provided writers.
// This is a streaming version for large files to avoid loading everything into memory.
// NOT IMPLEMENTED YET - placeholder for Phase 6 (future work).
func EncodeStream(r io.Reader, k, m int, dataWriters, parityWriters []io.Writer) error {
	return fmt.Errorf("streaming encoder not yet implemented")
}

// DecodeStream reconstructs data from shard readers, writing to a writer.
// This is a streaming version for large files.
// NOT IMPLEMENTED YET - placeholder for Phase 6 (future work).
func DecodeStream(shardReaders []io.Reader, k, m int, w io.Writer, originalSize int64) error {
	return fmt.Errorf("streaming decoder not yet implemented")
}
