package s3

import (
	"sort"
	"sync"
	"time"
)

// CapacitySnapshot represents a point-in-time view of a coordinator's storage capacity.
type CapacitySnapshot struct {
	Timestamp            time.Time `json:"timestamp"`
	CoordinatorName      string    `json:"coordinator_name"`
	VolumeTotalBytes     int64     `json:"volume_total_bytes"`
	VolumeUsedBytes      int64     `json:"volume_used_bytes"`
	VolumeAvailableBytes int64     `json:"volume_available_bytes"`
	QuotaMaxBytes        int64     `json:"quota_max_bytes"` // 0 = unlimited
	QuotaUsedBytes       int64     `json:"quota_used_bytes"`
	QuotaAvailBytes      int64     `json:"quota_avail_bytes"` // -1 = unlimited
}

// EffectiveAvailableBytes returns the effective available storage in bytes,
// taking both filesystem volume and logical quota into account.
// Returns min(volume available, quota available), with quota=-1 meaning volume-only.
func (s *CapacitySnapshot) EffectiveAvailableBytes() int64 {
	if s.QuotaAvailBytes < 0 {
		// Unlimited quota — constrained only by filesystem
		return s.VolumeAvailableBytes
	}
	if s.VolumeAvailableBytes < s.QuotaAvailBytes {
		return s.VolumeAvailableBytes
	}
	return s.QuotaAvailBytes
}

// CapacityRegistry maintains the latest capacity snapshots for all coordinators.
type CapacityRegistry struct {
	mu        sync.RWMutex
	snapshots map[string]*CapacitySnapshot // coordinatorName -> latest
}

// NewCapacityRegistry creates a new empty capacity registry.
func NewCapacityRegistry() *CapacityRegistry {
	return &CapacityRegistry{
		snapshots: make(map[string]*CapacitySnapshot),
	}
}

// Update stores or replaces the capacity snapshot for a coordinator.
func (r *CapacityRegistry) Update(snap *CapacitySnapshot) {
	if snap == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots[snap.CoordinatorName] = snap
}

// Get returns the latest snapshot for a coordinator, or nil if unknown.
func (r *CapacityRegistry) Get(coordinatorName string) *CapacitySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshots[coordinatorName]
}

// GetAll returns a copy of all snapshots.
func (r *CapacityRegistry) GetAll() map[string]*CapacitySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]*CapacitySnapshot, len(r.snapshots))
	for k, v := range r.snapshots {
		result[k] = v
	}
	return result
}

// HasCapacityFor checks whether the given coordinator has enough storage for the
// specified number of bytes. Fail-open: returns true if no snapshot is available.
func (r *CapacityRegistry) HasCapacityFor(coordinatorName string, bytes int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap, ok := r.snapshots[coordinatorName]
	if !ok {
		return true // fail-open
	}
	return snap.EffectiveAvailableBytes() >= bytes
}

// SortByAvailableCapacity sorts coordinator names by descending effective available
// capacity. Unknown coordinators (no snapshot) are placed last.
func (r *CapacityRegistry) SortByAvailableCapacity(ids []string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sorted := make([]string, len(ids))
	copy(sorted, ids)

	sort.SliceStable(sorted, func(i, j int) bool {
		si, oki := r.snapshots[sorted[i]]
		sj, okj := r.snapshots[sorted[j]]
		// Unknown goes last
		if !oki && !okj {
			return false
		}
		if !oki {
			return false
		}
		if !okj {
			return true
		}
		return si.EffectiveAvailableBytes() > sj.EffectiveAvailableBytes()
	})

	return sorted
}
