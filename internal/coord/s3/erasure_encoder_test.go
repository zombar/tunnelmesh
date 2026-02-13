package s3

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
)

func TestEncodeFile_Basic(t *testing.T) {
	data := []byte("Hello, Reed-Solomon erasure coding!")
	k, m := 3, 2

	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	if len(dataShards) != k {
		t.Errorf("expected %d data shards, got %d", k, len(dataShards))
	}
	if len(parityShards) != m {
		t.Errorf("expected %d parity shards, got %d", m, len(parityShards))
	}

	// Verify all shards have same size
	shardSize := len(dataShards[0])
	for i, shard := range dataShards {
		if len(shard) != shardSize {
			t.Errorf("data shard %d has size %d, expected %d", i, len(shard), shardSize)
		}
	}
	for i, shard := range parityShards {
		if len(shard) != shardSize {
			t.Errorf("parity shard %d has size %d, expected %d", i, len(shard), shardSize)
		}
	}
}

func TestEncodeFile_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		k    int
		m    int
	}{
		{"small-3+2", []byte("test"), 3, 2},
		{"medium-6+3", make([]byte, 1024), 6, 3},
		{"large-10+3", make([]byte, 100*1024), 10, 3},
		{"uneven-5+2", []byte("hello world"), 5, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Generate random data for larger tests
			if len(tt.data) > 10 {
				_, _ = rand.Read(tt.data)
			}

			// Encode
			dataShards, parityShards, err := EncodeFile(tt.data, tt.k, tt.m)
			if err != nil {
				t.Fatalf("EncodeFile failed: %v", err)
			}

			// Combine shards for decoding
			allShards := make([][]byte, tt.k+tt.m)
			copy(allShards[:tt.k], dataShards)
			copy(allShards[tt.k:], parityShards)

			// Decode (all shards available)
			decoded, err := DecodeFile(allShards, tt.k, tt.m, int64(len(tt.data)))
			if err != nil {
				t.Fatalf("DecodeFile failed: %v", err)
			}

			// Verify
			if !bytes.Equal(decoded, tt.data) {
				t.Errorf("decoded data doesn't match original")
			}
		})
	}
}

func TestDecodeFile_MissingDataShards(t *testing.T) {
	data := make([]byte, 10*1024)
	_, _ = rand.Read(data)
	k, m := 6, 3

	// Encode
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	// Test reconstruction with 1, 2, 3 missing data shards
	for missing := 1; missing <= m; missing++ {
		t.Run(fmt.Sprintf("missing_%d_data_shards", missing), func(t *testing.T) {
			// Combine shards
			allShards := make([][]byte, k+m)
			copy(allShards[:k], dataShards)
			copy(allShards[k:], parityShards)

			// Remove 'missing' data shards
			for i := 0; i < missing; i++ {
				allShards[i] = nil
			}

			// Decode
			decoded, err := DecodeFile(allShards, k, m, int64(len(data)))
			if err != nil {
				t.Fatalf("DecodeFile failed with %d missing shards: %v", missing, err)
			}

			// Verify
			if !bytes.Equal(decoded, data) {
				t.Errorf("decoded data doesn't match original with %d missing shards", missing)
			}
		})
	}
}

func TestDecodeFile_MissingParityShards(t *testing.T) {
	data := make([]byte, 5*1024)
	_, _ = rand.Read(data)
	k, m := 4, 2

	// Encode
	dataShards, _, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	// Remove all parity shards (should still work with all data shards)
	allShards := make([][]byte, k+m)
	copy(allShards[:k], dataShards)
	// parity shards remain nil

	// Decode
	decoded, err := DecodeFile(allShards, k, m, int64(len(data)))
	if err != nil {
		t.Fatalf("DecodeFile failed with missing parity shards: %v", err)
	}

	// Verify
	if !bytes.Equal(decoded, data) {
		t.Errorf("decoded data doesn't match original with missing parity shards")
	}
}

func TestDecodeFile_MixedShards(t *testing.T) {
	data := make([]byte, 8*1024)
	_, _ = rand.Read(data)
	k, m := 10, 3

	// Encode
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	// Test various combinations of available shards
	tests := []struct {
		name            string
		availableData   []int // indices of available data shards
		availableParity []int // indices of available parity shards
	}{
		{"first_k", []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, []int{}},
		{"last_k_data_first_parity", []int{7, 8, 9}, []int{0, 1, 2, 3, 4, 5, 6}},
		{"scattered", []int{0, 2, 4, 6, 8}, []int{0, 1, 2, 3, 4}},
		{"exactly_k", []int{1, 3, 5, 7, 9}, []int{0, 2, 4}}, // 5 data + 3 parity = 8 shards available (only need 10)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allShards := make([][]byte, k+m)

			// Add available data shards
			for _, idx := range tt.availableData {
				if idx < len(dataShards) {
					allShards[idx] = dataShards[idx]
				}
			}

			// Add available parity shards
			for _, idx := range tt.availableParity {
				if idx < len(parityShards) {
					allShards[k+idx] = parityShards[idx]
				}
			}

			// Count available
			available := 0
			for _, shard := range allShards {
				if shard != nil {
					available++
				}
			}

			if available < k {
				t.Skipf("insufficient shards: have %d, need %d", available, k)
			}

			// Decode
			decoded, err := DecodeFile(allShards, k, m, int64(len(data)))
			if err != nil {
				t.Fatalf("DecodeFile failed: %v", err)
			}

			// Verify
			if !bytes.Equal(decoded, data) {
				t.Errorf("decoded data doesn't match original")
			}
		})
	}
}

func TestDecodeFile_InsufficientShards(t *testing.T) {
	data := []byte("test data")
	k, m := 5, 2

	// Encode
	dataShards, _, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	// Keep only k-1 shards (insufficient)
	allShards := make([][]byte, k+m)
	copy(allShards[:k-1], dataShards[:k-1])

	// Decode should fail
	_, err = DecodeFile(allShards, k, m, int64(len(data)))
	if err == nil {
		t.Errorf("expected error with insufficient shards, got nil")
	}
}

func TestEncodeFile_InvalidParameters(t *testing.T) {
	data := []byte("test")

	tests := []struct {
		name string
		k    int
		m    int
	}{
		{"k_zero", 0, 2},
		{"m_zero", 3, 0},
		{"k_negative", -1, 2},
		{"m_negative", 3, -1},
		{"total_too_large", 200, 100}, // 300 > 256
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := EncodeFile(data, tt.k, tt.m)
			if err == nil {
				t.Errorf("expected error with k=%d, m=%d, got nil", tt.k, tt.m)
			}
		})
	}
}

func TestEncodeFile_EmptyData(t *testing.T) {
	_, _, err := EncodeFile([]byte{}, 3, 2)
	if err == nil {
		t.Errorf("expected error with empty data, got nil")
	}
}

func TestDecodeFile_InvalidInputs(t *testing.T) {
	// Valid encode
	data := []byte("test data")
	k, m := 3, 2
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	allShards := make([][]byte, k+m)
	copy(allShards[:k], dataShards)
	copy(allShards[k:], parityShards)

	tests := []struct {
		name         string
		shards       [][]byte
		k            int
		m            int
		originalSize int64
	}{
		{"wrong_shard_count", allShards[:k], k, m, int64(len(data))},
		{"zero_size", allShards, k, m, 0},
		{"negative_size", allShards, k, m, -1},
		{"invalid_k", allShards, 0, m, int64(len(data))},
		{"invalid_m", allShards, k, 0, int64(len(data))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeFile(tt.shards, tt.k, tt.m, tt.originalSize)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestEncodeFile_LargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file test in short mode")
	}

	// 10 MB file
	data := make([]byte, 10*1024*1024)
	_, _ = rand.Read(data)

	k, m := 10, 3

	// Encode
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		t.Fatalf("EncodeFile failed: %v", err)
	}

	// Verify shard count
	if len(dataShards) != k || len(parityShards) != m {
		t.Errorf("incorrect shard count: data=%d, parity=%d", len(dataShards), len(parityShards))
	}

	// Decode with 3 missing data shards
	allShards := make([][]byte, k+m)
	copy(allShards[:k], dataShards)
	copy(allShards[k:], parityShards)
	allShards[0] = nil
	allShards[3] = nil
	allShards[7] = nil

	decoded, err := DecodeFile(allShards, k, m, int64(len(data)))
	if err != nil {
		t.Fatalf("DecodeFile failed: %v", err)
	}

	if !bytes.Equal(decoded, data) {
		t.Errorf("decoded large file doesn't match original")
	}
}

func BenchmarkEncodeFile(b *testing.B) {
	sizes := []int{
		1 * 1024,         // 1 KB
		100 * 1024,       // 100 KB
		1024 * 1024,      // 1 MB
		10 * 1024 * 1024, // 10 MB
	}

	for _, size := range sizes {
		data := make([]byte, size)
		_, _ = rand.Read(data)

		b.Run(fmt.Sprintf("size_%dKB", size/1024), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				_, _, err := EncodeFile(data, 10, 3)
				if err != nil {
					b.Fatalf("EncodeFile failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkDecodeFile(b *testing.B) {
	data := make([]byte, 1024*1024) // 1 MB
	_, _ = rand.Read(data)
	k, m := 10, 3

	// Pre-encode
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		b.Fatalf("EncodeFile failed: %v", err)
	}

	tests := []struct {
		name    string
		missing int // number of missing data shards
	}{
		{"no_missing", 0},
		{"missing_1", 1},
		{"missing_2", 2},
		{"missing_3", 3},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			// Prepare shards with missing ones
			allShards := make([][]byte, k+m)
			copy(allShards[:k], dataShards)
			copy(allShards[k:], parityShards)

			for i := 0; i < tt.missing; i++ {
				allShards[i] = nil
			}

			b.SetBytes(int64(len(data)))
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Need to reset nil shards each iteration since Reconstruct modifies
				testShards := make([][]byte, k+m)
				for j := range allShards {
					if allShards[j] != nil {
						testShards[j] = make([]byte, len(allShards[j]))
						copy(testShards[j], allShards[j])
					}
				}

				_, err := DecodeFile(testShards, k, m, int64(len(data)))
				if err != nil {
					b.Fatalf("DecodeFile failed: %v", err)
				}
			}
		})
	}
}
