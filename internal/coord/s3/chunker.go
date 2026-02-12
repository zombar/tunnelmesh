// Package s3 provides an S3-compatible object storage service for the coordinator.
package s3

import (
	"io"
)

// Chunker configuration constants.
const (
	MinChunkSize    = 1024       // 1KB minimum chunk size
	TargetChunkSize = 4096       // 4KB target (average) chunk size
	MaxChunkSize    = 65536      // 64KB maximum chunk size
	ChunkMask       = 0xFFF      // Mask for boundary detection (1 in 4096 on average)
	BuzhashSeed     = 0x47b6137b // Arbitrary seed for buzhash
)

// buzhashTable is a precomputed table for the Buzhash rolling hash algorithm.
// Each byte maps to a pseudo-random 32-bit value.
var buzhashTable [256]uint32

func init() {
	// Initialize buzhash table using a simple PRNG seeded with BuzhashSeed
	state := uint32(BuzhashSeed)
	for i := range buzhashTable {
		// xorshift32 PRNG
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		buzhashTable[i] = state
	}
}

// Chunker splits data into variable-size chunks using content-defined chunking (CDC).
// It uses the Buzhash rolling hash algorithm to find chunk boundaries based on content,
// which provides stable boundaries even when data is inserted or deleted.
type Chunker struct {
	reader     io.Reader
	buf        []byte
	bufLen     int
	bufPos     int
	windowSize int
	hash       uint32
	done       bool
}

// NewChunker creates a new CDC chunker for the given reader.
func NewChunker(r io.Reader) *Chunker {
	return &Chunker{
		reader:     r,
		buf:        make([]byte, MaxChunkSize*2), // Double buffer for efficiency
		windowSize: 64,                           // Rolling hash window size
	}
}

// Next returns the next chunk from the reader.
// Returns nil when EOF is reached.
func (c *Chunker) Next() ([]byte, error) {
	if c.done {
		return nil, nil
	}

	// Ensure we have data in the buffer
	if err := c.fillBuffer(); err != nil {
		return nil, err
	}

	if c.bufLen == 0 {
		c.done = true
		return nil, nil
	}

	// Find chunk boundary
	chunkEnd := c.findBoundary()

	// Extract chunk
	chunk := make([]byte, chunkEnd)
	copy(chunk, c.buf[:chunkEnd])

	// Shift buffer
	copy(c.buf, c.buf[chunkEnd:c.bufLen])
	c.bufLen -= chunkEnd
	c.bufPos = 0
	c.hash = 0

	return chunk, nil
}

// fillBuffer reads more data into the buffer if needed.
func (c *Chunker) fillBuffer() error {
	// If we have less than MaxChunkSize, try to read more
	if c.bufLen < MaxChunkSize {
		n, err := c.reader.Read(c.buf[c.bufLen:])
		c.bufLen += n
		if err == io.EOF {
			// EOF is not an error, we'll process remaining data
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// findBoundary finds the next chunk boundary using Buzhash rolling hash.
// Uses the standard Buzhash formula: H_new = rol(H_old, 1) ^ h(new) ^ rol(h(old), n)
func (c *Chunker) findBoundary() int {
	// Initialize rolling hash with first windowSize bytes (or less if not enough data)
	windowStart := 0
	if c.bufLen >= c.windowSize {
		windowStart = c.windowSize
		for i := 0; i < c.windowSize; i++ {
			c.hash = rol32(c.hash, 1) ^ buzhashTable[c.buf[i]]
		}
	}

	// Scan for boundary starting after minimum chunk size
	start := MinChunkSize
	if start < windowStart {
		start = windowStart
	}

	for i := start; i < c.bufLen; i++ {
		// Update rolling hash using correct Buzhash order:
		// 1. Rotate the hash left by 1
		// 2. XOR in the new byte
		// 3. XOR out the old byte (rotated by window size)
		c.hash = rol32(c.hash, 1)
		c.hash ^= buzhashTable[c.buf[i]]
		if i >= c.windowSize {
			outByte := c.buf[i-c.windowSize]
			c.hash ^= rol32(buzhashTable[outByte], uint32(c.windowSize))
		}

		// Check for boundary (after minimum size)
		if i >= MinChunkSize && (c.hash&ChunkMask) == 0 {
			return i + 1
		}

		// Force boundary at maximum size
		if i+1 >= MaxChunkSize {
			return MaxChunkSize
		}
	}

	// If we haven't found a boundary and have all the data (EOF), return what we have
	if c.bufLen < MaxChunkSize {
		return c.bufLen
	}

	// Shouldn't reach here, but force boundary at max size
	return MaxChunkSize
}

// rol32 performs a 32-bit left rotation.
func rol32(x uint32, n uint32) uint32 {
	return (x << n) | (x >> (32 - n))
}

// ChunkData splits data into chunks using CDC.
// This is a convenience function for chunking in-memory data.
func ChunkData(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}

	chunker := NewChunker(&byteReader{data: data})
	var chunks [][]byte

	for {
		chunk, err := chunker.Next()
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			break
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// byteReader wraps a byte slice as an io.Reader.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// StreamingChunker wraps the existing Chunker and adds hash computation.
// It processes data in a streaming fashion, never buffering entire files in memory.
// For a 1GB file, peak memory usage is ~64KB (single chunk).
type StreamingChunker struct {
	chunker *Chunker
}

// NewStreamingChunker creates a new streaming chunker that computes hashes.
func NewStreamingChunker(r io.Reader) *StreamingChunker {
	return &StreamingChunker{
		chunker: NewChunker(r),
	}
}

// NextChunk reads and returns the next chunk along with its SHA-256 hash.
// Returns (nil, "", io.EOF) when all data has been processed.
// Returns (nil, "", err) on read errors.
func (sc *StreamingChunker) NextChunk() ([]byte, string, error) {
	chunk, err := sc.chunker.Next()
	if err != nil {
		return nil, "", err
	}
	if chunk == nil {
		return nil, "", io.EOF
	}

	// Compute SHA-256 hash of chunk
	hash := ContentHash(chunk)

	return chunk, hash, nil
}
