package replication

import (
	"encoding/json"
	"fmt"
)

// VersionVector represents a vector clock for tracking causality between events.
// Each node maintains a counter that increments when it modifies data.
// This is separate from S3's object versioning and is used only for replication coordination.
type VersionVector map[string]uint64

// NewVersionVector creates a new empty version vector.
func NewVersionVector() VersionVector {
	return make(VersionVector)
}

// Increment increments the counter for the given nodeID.
// This should be called when a node modifies data.
func (vv VersionVector) Increment(nodeID string) {
	vv[nodeID]++
}

// Get returns the counter value for a given nodeID, or 0 if not present.
func (vv VersionVector) Get(nodeID string) uint64 {
	return vv[nodeID]
}

// Merge merges another version vector into this one by taking the maximum
// value for each nodeID. This is used when receiving updates from other nodes.
func (vv VersionVector) Merge(other VersionVector) {
	for nodeID, count := range other {
		if count > vv[nodeID] {
			vv[nodeID] = count
		}
	}
}

// Copy creates a deep copy of this version vector.
func (vv VersionVector) Copy() VersionVector {
	copy := make(VersionVector, len(vv))
	for nodeID, count := range vv {
		copy[nodeID] = count
	}
	return copy
}

// HappenedBefore returns true if this version vector happened before the other.
// This means all counters in this VV are less than or equal to the other,
// and at least one is strictly less.
func (vv VersionVector) HappenedBefore(other VersionVector) bool {
	atLeastOneStrictlyLess := false

	// Check all entries in this VV
	for nodeID, count := range vv {
		otherCount := other.Get(nodeID)
		if count > otherCount {
			// This has a higher counter than other, so it didn't happen before
			return false
		}
		if count < otherCount {
			atLeastOneStrictlyLess = true
		}
	}

	// Check for entries that exist in other but not in this
	for nodeID := range other {
		if _, exists := vv[nodeID]; !exists {
			atLeastOneStrictlyLess = true
		}
	}

	return atLeastOneStrictlyLess
}

// HappenedAfter returns true if this version vector happened after the other.
// This is the inverse of HappenedBefore.
func (vv VersionVector) HappenedAfter(other VersionVector) bool {
	return other.HappenedBefore(vv)
}

// ConcurrentWith returns true if this version vector is concurrent with the other.
// Two version vectors are concurrent if neither happened before the other,
// which indicates a conflict that needs resolution.
func (vv VersionVector) ConcurrentWith(other VersionVector) bool {
	return !vv.HappenedBefore(other) && !vv.HappenedAfter(other) && !vv.Equal(other)
}

// Equal returns true if this version vector is identical to the other.
func (vv VersionVector) Equal(other VersionVector) bool {
	if len(vv) != len(other) {
		return false
	}

	for nodeID, count := range vv {
		if other.Get(nodeID) != count {
			return false
		}
	}

	return true
}

// String returns a human-readable representation of the version vector.
func (vv VersionVector) String() string {
	data, _ := json.Marshal(vv)
	return string(data)
}

// Compare compares two version vectors and returns their relationship.
type VectorRelationship int

const (
	// VectorEqual indicates the version vectors are identical
	VectorEqual VectorRelationship = iota
	// VectorBefore indicates the first vector happened before the second
	VectorBefore
	// VectorAfter indicates the first vector happened after the second
	VectorAfter
	// VectorConcurrent indicates the vectors are concurrent (conflict)
	VectorConcurrent
)

// String returns a human-readable representation of the vector relationship.
func (r VectorRelationship) String() string {
	switch r {
	case VectorEqual:
		return "equal"
	case VectorBefore:
		return "before"
	case VectorAfter:
		return "after"
	case VectorConcurrent:
		return "concurrent"
	default:
		return "unknown"
	}
}

// Compare returns the relationship between this version vector and another.
func (vv VersionVector) Compare(other VersionVector) VectorRelationship {
	if vv.Equal(other) {
		return VectorEqual
	}
	if vv.HappenedBefore(other) {
		return VectorBefore
	}
	if vv.HappenedAfter(other) {
		return VectorAfter
	}
	return VectorConcurrent
}

// ResolveConflict resolves a conflict between two concurrent version vectors
// using a deterministic tie-breaker. This implementation uses lexicographic
// comparison of canonical JSON representation (with sorted keys).
//
// Returns the version vector that should win the conflict resolution.
func ResolveConflict(vv1, vv2 VersionVector) VersionVector {
	// If not concurrent, return the one that happened after
	if vv1.HappenedAfter(vv2) {
		return vv1
	}
	if vv2.HappenedAfter(vv1) {
		return vv2
	}

	// They're concurrent - use lexicographic tie-breaker with canonical JSON
	// Go's json.Marshal already sorts map keys, making this deterministic
	s1 := vv1.String()
	s2 := vv2.String()

	if s1 < s2 {
		return vv1
	}
	return vv2
}

// MarshalJSON implements json.Marshaler.
func (vv VersionVector) MarshalJSON() ([]byte, error) {
	// Convert to map for marshaling
	m := make(map[string]uint64, len(vv))
	for k, v := range vv {
		m[k] = v
	}
	return json.Marshal(m)
}

// UnmarshalJSON implements json.Unmarshaler.
func (vv *VersionVector) UnmarshalJSON(data []byte) error {
	var m map[string]uint64
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("unmarshal version vector: %w", err)
	}

	*vv = VersionVector(m)
	return nil
}

// CompareVersionVectorMaps compares two version vector maps (used by s3.ChunkMetadata).
// Returns the relationship between them.
func CompareVersionVectorMaps(vv1, vv2 map[string]uint64) VectorRelationship {
	return VersionVector(vv1).Compare(VersionVector(vv2))
}

// MergeVersionVectorMaps merges two version vector maps, taking the maximum value for each key.
// Returns the merged version vector.
func MergeVersionVectorMaps(vv1, vv2 map[string]uint64) map[string]uint64 {
	merged := make(map[string]uint64)

	// Copy vv1
	for k, v := range vv1 {
		merged[k] = v
	}

	// Merge vv2 (taking max)
	for k, v := range vv2 {
		if v > merged[k] {
			merged[k] = v
		}
	}

	return merged
}

// ResolveConflictMaps resolves a conflict between two version vector maps.
// Returns the winner version vector.
func ResolveConflictMaps(vv1, vv2 map[string]uint64) map[string]uint64 {
	winner := ResolveConflict(VersionVector(vv1), VersionVector(vv2))
	return map[string]uint64(winner)
}
