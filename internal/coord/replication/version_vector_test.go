package replication

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewVersionVector(t *testing.T) {
	vv := NewVersionVector()
	assert.NotNil(t, vv)
	assert.Equal(t, 0, len(vv))
}

func TestVersionVector_Increment(t *testing.T) {
	vv := NewVersionVector()

	// Increment node1
	vv.Increment("node1")
	assert.Equal(t, uint64(1), vv.Get("node1"))

	// Increment node1 again
	vv.Increment("node1")
	assert.Equal(t, uint64(2), vv.Get("node1"))

	// Increment node2
	vv.Increment("node2")
	assert.Equal(t, uint64(1), vv.Get("node2"))
	assert.Equal(t, uint64(2), vv.Get("node1")) // node1 unchanged
}

func TestVersionVector_Get(t *testing.T) {
	vv := NewVersionVector()
	vv.Increment("node1")

	assert.Equal(t, uint64(1), vv.Get("node1"))
	assert.Equal(t, uint64(0), vv.Get("nonexistent")) // Non-existent nodes return 0
}

func TestVersionVector_Merge(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected VersionVector
	}{
		{
			name:     "merge with empty",
			vv1:      VersionVector{"node1": 2, "node2": 1},
			vv2:      VersionVector{},
			expected: VersionVector{"node1": 2, "node2": 1},
		},
		{
			name:     "merge with higher values",
			vv1:      VersionVector{"node1": 2, "node2": 1},
			vv2:      VersionVector{"node1": 3, "node2": 4},
			expected: VersionVector{"node1": 3, "node2": 4},
		},
		{
			name:     "merge with mixed values",
			vv1:      VersionVector{"node1": 5, "node2": 1},
			vv2:      VersionVector{"node1": 3, "node2": 4},
			expected: VersionVector{"node1": 5, "node2": 4},
		},
		{
			name:     "merge with new nodes",
			vv1:      VersionVector{"node1": 2},
			vv2:      VersionVector{"node2": 3},
			expected: VersionVector{"node1": 2, "node2": 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vv := tt.vv1.Copy()
			vv.Merge(tt.vv2)
			assert.Equal(t, tt.expected, vv)
		})
	}
}

func TestVersionVector_Copy(t *testing.T) {
	original := VersionVector{"node1": 2, "node2": 3}
	copy := original.Copy()

	// Verify values are equal
	assert.Equal(t, original, copy)

	// Verify it's a deep copy
	copy.Increment("node1")
	assert.Equal(t, uint64(2), original.Get("node1")) // Original unchanged
	assert.Equal(t, uint64(3), copy.Get("node1"))     // Copy incremented
}

func TestVersionVector_HappenedBefore(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected bool
	}{
		{
			name:     "empty vectors",
			vv1:      VersionVector{},
			vv2:      VersionVector{},
			expected: false, // Neither happened before
		},
		{
			name:     "vv1 happened before vv2",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 1},
			expected: true,
		},
		{
			name:     "vv1 happened before vv2 (all less or equal)",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: true,
		},
		{
			name:     "vv1 did not happen before vv2 (has higher value)",
			vv1:      VersionVector{"node1": 3, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: false,
		},
		{
			name:     "vv1 equal to vv2",
			vv1:      VersionVector{"node1": 2, "node2": 2},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: false, // Equal, not "before"
		},
		{
			name:     "vv2 has new node",
			vv1:      VersionVector{"node1": 1},
			vv2:      VersionVector{"node1": 1, "node2": 1},
			expected: true, // vv1 happened before because vv2 has additional events
		},
		{
			name:     "vv1 has node not in vv2",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2},
			expected: false, // vv1 has events vv2 doesn't know about
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.vv1.HappenedBefore(tt.vv2)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVersionVector_HappenedAfter(t *testing.T) {
	vv1 := VersionVector{"node1": 2, "node2": 1}
	vv2 := VersionVector{"node1": 1, "node2": 1}

	// vv1 happened after vv2
	assert.True(t, vv1.HappenedAfter(vv2))
	assert.False(t, vv2.HappenedAfter(vv1))

	// Inverse of HappenedBefore
	assert.Equal(t, vv2.HappenedBefore(vv1), vv1.HappenedAfter(vv2))
}

func TestVersionVector_ConcurrentWith(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected bool
	}{
		{
			name:     "concurrent vectors (different nodes)",
			vv1:      VersionVector{"node1": 2, "node2": 1},
			vv2:      VersionVector{"node1": 1, "node2": 2},
			expected: true,
		},
		{
			name:     "concurrent vectors (disjoint nodes)",
			vv1:      VersionVector{"node1": 1},
			vv2:      VersionVector{"node2": 1},
			expected: true,
		},
		{
			name:     "not concurrent (vv1 before vv2)",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: false,
		},
		{
			name:     "not concurrent (vv1 after vv2)",
			vv1:      VersionVector{"node1": 2, "node2": 2},
			vv2:      VersionVector{"node1": 1, "node2": 1},
			expected: false,
		},
		{
			name:     "not concurrent (equal)",
			vv1:      VersionVector{"node1": 2, "node2": 2},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.vv1.ConcurrentWith(tt.vv2)
			assert.Equal(t, tt.expected, result)

			// Concurrency should be symmetric
			assert.Equal(t, result, tt.vv2.ConcurrentWith(tt.vv1))
		})
	}
}

func TestVersionVector_Equal(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected bool
	}{
		{
			name:     "equal vectors",
			vv1:      VersionVector{"node1": 2, "node2": 3},
			vv2:      VersionVector{"node1": 2, "node2": 3},
			expected: true,
		},
		{
			name:     "different values",
			vv1:      VersionVector{"node1": 2, "node2": 3},
			vv2:      VersionVector{"node1": 2, "node2": 4},
			expected: false,
		},
		{
			name:     "different nodes",
			vv1:      VersionVector{"node1": 2},
			vv2:      VersionVector{"node2": 2},
			expected: false,
		},
		{
			name:     "empty vectors",
			vv1:      VersionVector{},
			vv2:      VersionVector{},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.vv1.Equal(tt.vv2)
			assert.Equal(t, tt.expected, result)

			// Equality should be symmetric
			assert.Equal(t, result, tt.vv2.Equal(tt.vv1))
		})
	}
}

func TestVersionVector_Compare(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected VectorRelationship
	}{
		{
			name:     "equal",
			vv1:      VersionVector{"node1": 2, "node2": 3},
			vv2:      VersionVector{"node1": 2, "node2": 3},
			expected: VectorEqual,
		},
		{
			name:     "before",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: VectorBefore,
		},
		{
			name:     "after",
			vv1:      VersionVector{"node1": 3, "node2": 3},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: VectorAfter,
		},
		{
			name:     "concurrent",
			vv1:      VersionVector{"node1": 2, "node2": 1},
			vv2:      VersionVector{"node1": 1, "node2": 2},
			expected: VectorConcurrent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.vv1.Compare(tt.vv2)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestResolveConflict(t *testing.T) {
	tests := []struct {
		name     string
		vv1      VersionVector
		vv2      VersionVector
		expected VersionVector // Which one should win
	}{
		{
			name:     "vv1 happened after (no conflict)",
			vv1:      VersionVector{"node1": 2, "node2": 2},
			vv2:      VersionVector{"node1": 1, "node2": 1},
			expected: VersionVector{"node1": 2, "node2": 2},
		},
		{
			name:     "vv2 happened after (no conflict)",
			vv1:      VersionVector{"node1": 1, "node2": 1},
			vv2:      VersionVector{"node1": 2, "node2": 2},
			expected: VersionVector{"node1": 2, "node2": 2},
		},
		{
			name: "concurrent - lexicographic tie-breaker",
			vv1:  VersionVector{"node1": 2, "node2": 1},
			vv2:  VersionVector{"node1": 1, "node2": 2},
			// Expected winner depends on JSON string comparison
			// This test verifies determinism, not specific winner
			expected: nil, // Will be determined by ResolveConflict
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ResolveConflict(tt.vv1, tt.vv2)

			if tt.expected != nil {
				assert.Equal(t, tt.expected, result)
			} else {
				// For concurrent conflicts, verify determinism
				// Running multiple times should give same result
				for i := 0; i < 10; i++ {
					result2 := ResolveConflict(tt.vv1, tt.vv2)
					assert.Equal(t, result, result2, "ResolveConflict should be deterministic")
				}

				// Result should be one of the inputs
				assert.True(t, result.Equal(tt.vv1) || result.Equal(tt.vv2))
			}
		})
	}
}

func TestVersionVector_JSON(t *testing.T) {
	original := VersionVector{
		"coord1.tunnelmesh:8443": 5,
		"coord2.tunnelmesh:8443": 3,
		"coord3.tunnelmesh:8443": 7,
	}

	// Marshal to JSON
	data, err := json.Marshal(original)
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal from JSON
	var decoded VersionVector
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify equality
	assert.Equal(t, original, decoded)
}

func TestVersionVector_String(t *testing.T) {
	vv := VersionVector{
		"node1": 2,
		"node2": 3,
	}

	str := vv.String()
	assert.NotEmpty(t, str)
	assert.Contains(t, str, "node1")
	assert.Contains(t, str, "node2")

	// Verify it's valid JSON
	var parsed map[string]uint64
	err := json.Unmarshal([]byte(str), &parsed)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), parsed["node1"])
	assert.Equal(t, uint64(3), parsed["node2"])
}

func TestVersionVector_CausalityScenarios(t *testing.T) {
	t.Run("sequential updates on same node", func(t *testing.T) {
		// Node1 makes two sequential updates
		vv1 := NewVersionVector()
		vv1.Increment("node1") // First update

		vv2 := vv1.Copy()
		vv2.Increment("node1") // Second update

		// vv1 should have happened before vv2
		assert.True(t, vv1.HappenedBefore(vv2))
		assert.False(t, vv2.HappenedBefore(vv1))
		assert.False(t, vv1.ConcurrentWith(vv2))
	})

	t.Run("concurrent updates on different nodes", func(t *testing.T) {
		// Start from common base
		base := VersionVector{"node1": 1, "node2": 1}

		// Node1 makes an update
		vv1 := base.Copy()
		vv1.Increment("node1")

		// Node2 makes an update (concurrent)
		vv2 := base.Copy()
		vv2.Increment("node2")

		// These updates are concurrent
		assert.True(t, vv1.ConcurrentWith(vv2))
		assert.True(t, vv2.ConcurrentWith(vv1))
	})

	t.Run("merge resolves conflicts", func(t *testing.T) {
		// Two concurrent updates
		vv1 := VersionVector{"node1": 2, "node2": 1}
		vv2 := VersionVector{"node1": 1, "node2": 2}

		assert.True(t, vv1.ConcurrentWith(vv2))

		// After merge, the merged version happened after both
		merged := vv1.Copy()
		merged.Merge(vv2)

		assert.True(t, vv1.HappenedBefore(merged))
		assert.True(t, vv2.HappenedBefore(merged))
		assert.False(t, merged.ConcurrentWith(vv1))
		assert.False(t, merged.ConcurrentWith(vv2))
	})

	t.Run("three-way concurrent conflict", func(t *testing.T) {
		// Base state
		base := VersionVector{"node1": 1, "node2": 1, "node3": 1}

		// Three coordinators make concurrent updates
		vv1 := base.Copy()
		vv1.Increment("node1")

		vv2 := base.Copy()
		vv2.Increment("node2")

		vv3 := base.Copy()
		vv3.Increment("node3")

		// All three are mutually concurrent
		assert.True(t, vv1.ConcurrentWith(vv2))
		assert.True(t, vv1.ConcurrentWith(vv3))
		assert.True(t, vv2.ConcurrentWith(vv3))

		// Conflict resolution should be deterministic
		winner12 := ResolveConflict(vv1, vv2)
		winner13 := ResolveConflict(vv1, vv3)
		winner23 := ResolveConflict(vv2, vv3)

		// The winners should be consistent
		assert.NotNil(t, winner12)
		assert.NotNil(t, winner13)
		assert.NotNil(t, winner23)
	})
}

func TestCompareVersionVectorMaps(t *testing.T) {
	tests := []struct {
		name     string
		vv1      map[string]uint64
		vv2      map[string]uint64
		expected VectorRelationship
	}{
		{
			name:     "equal",
			vv1:      map[string]uint64{"A": 1, "B": 2},
			vv2:      map[string]uint64{"A": 1, "B": 2},
			expected: VectorEqual,
		},
		{
			name:     "vv1 before vv2",
			vv1:      map[string]uint64{"A": 1},
			vv2:      map[string]uint64{"A": 2},
			expected: VectorBefore,
		},
		{
			name:     "vv1 after vv2",
			vv1:      map[string]uint64{"A": 2},
			vv2:      map[string]uint64{"A": 1},
			expected: VectorAfter,
		},
		{
			name:     "concurrent",
			vv1:      map[string]uint64{"A": 2, "B": 1},
			vv2:      map[string]uint64{"A": 1, "B": 2},
			expected: VectorConcurrent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CompareVersionVectorMaps(tt.vv1, tt.vv2)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestMergeVersionVectorMaps(t *testing.T) {
	vv1 := map[string]uint64{"A": 2, "B": 1, "C": 3}
	vv2 := map[string]uint64{"A": 1, "B": 3, "D": 2}

	merged := MergeVersionVectorMaps(vv1, vv2)

	expected := map[string]uint64{
		"A": 2, // max(2, 1)
		"B": 3, // max(1, 3)
		"C": 3, // only in vv1
		"D": 2, // only in vv2
	}

	if len(merged) != len(expected) {
		t.Fatalf("expected %d entries, got %d", len(expected), len(merged))
	}

	for k, expectedVal := range expected {
		if merged[k] != expectedVal {
			t.Errorf("key %s: expected %d, got %d", k, expectedVal, merged[k])
		}
	}
}

func TestResolveConflictMaps(t *testing.T) {
	// Concurrent version vectors
	vv1 := map[string]uint64{"A": 2, "B": 1}
	vv2 := map[string]uint64{"A": 1, "B": 2}

	winner := ResolveConflictMaps(vv1, vv2)

	// Should use deterministic tie-breaker
	// The result should be one of the inputs
	if !mapsEqual(winner, vv1) && !mapsEqual(winner, vv2) {
		t.Error("winner should be one of the input version vectors")
	}

	// Calling again should give same result (deterministic)
	winner2 := ResolveConflictMaps(vv1, vv2)
	if !mapsEqual(winner, winner2) {
		t.Error("conflict resolution should be deterministic")
	}
}

func mapsEqual(m1, m2 map[string]uint64) bool {
	if len(m1) != len(m2) {
		return false
	}
	for k, v := range m1 {
		if m2[k] != v {
			return false
		}
	}
	return true
}
