package s3

import (
	"sync"
)

// QuotaManager tracks storage usage and enforces quotas.
type QuotaManager struct {
	maxBytes  int64 // 0 = unlimited
	usedBytes int64
	perBucket map[string]int64
	mu        sync.RWMutex
}

// NewQuotaManager creates a new quota manager.
// maxSizeGB of 0 means unlimited.
func NewQuotaManager(maxSizeGB int) *QuotaManager {
	return &QuotaManager{
		maxBytes:  int64(maxSizeGB) * 1024 * 1024 * 1024,
		perBucket: make(map[string]int64),
	}
}

// MaxBytes returns the maximum allowed storage in bytes.
func (qm *QuotaManager) MaxBytes() int64 {
	return qm.maxBytes
}

// UsedBytes returns the current total storage usage.
func (qm *QuotaManager) UsedBytes() int64 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.usedBytes
}

// AvailableBytes returns the remaining available storage.
// Returns -1 if unlimited.
func (qm *QuotaManager) AvailableBytes() int64 {
	if qm.maxBytes == 0 {
		return -1 // unlimited
	}
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	avail := qm.maxBytes - qm.usedBytes
	if avail < 0 {
		return 0
	}
	return avail
}

// BucketUsedBytes returns the storage usage for a specific bucket.
func (qm *QuotaManager) BucketUsedBytes(bucket string) int64 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.perBucket[bucket]
}

// CanAllocate checks if the given number of bytes can be allocated.
func (qm *QuotaManager) CanAllocate(bytes int64) bool {
	if qm.maxBytes == 0 {
		return true // unlimited
	}
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.usedBytes+bytes <= qm.maxBytes
}

// Allocate records storage allocation for a bucket.
// Returns false if quota would be exceeded.
func (qm *QuotaManager) Allocate(bucket string, bytes int64) bool {
	if qm.maxBytes > 0 {
		qm.mu.Lock()
		defer qm.mu.Unlock()

		if qm.usedBytes+bytes > qm.maxBytes {
			return false
		}
		qm.usedBytes += bytes
		qm.perBucket[bucket] += bytes
	} else {
		qm.mu.Lock()
		defer qm.mu.Unlock()
		qm.usedBytes += bytes
		qm.perBucket[bucket] += bytes
	}
	return true
}

// Release records storage deallocation for a bucket.
func (qm *QuotaManager) Release(bucket string, bytes int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.usedBytes -= bytes
	if qm.usedBytes < 0 {
		qm.usedBytes = 0
	}

	qm.perBucket[bucket] -= bytes
	if qm.perBucket[bucket] <= 0 {
		delete(qm.perBucket, bucket)
	}
}

// Update updates allocation (for object replacement).
// Releases oldBytes and allocates newBytes atomically.
func (qm *QuotaManager) Update(bucket string, oldBytes, newBytes int64) bool {
	delta := newBytes - oldBytes

	if qm.maxBytes > 0 && delta > 0 {
		qm.mu.Lock()
		defer qm.mu.Unlock()

		if qm.usedBytes+delta > qm.maxBytes {
			return false
		}
		qm.usedBytes += delta
		qm.perBucket[bucket] += delta
	} else {
		qm.mu.Lock()
		defer qm.mu.Unlock()
		qm.usedBytes += delta
		qm.perBucket[bucket] += delta
		if qm.perBucket[bucket] <= 0 {
			delete(qm.perBucket, bucket)
		}
	}
	return true
}

// Reset clears all quota tracking.
func (qm *QuotaManager) Reset() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.usedBytes = 0
	qm.perBucket = make(map[string]int64)
}

// SetUsed sets the current usage (used during initialization/recovery).
func (qm *QuotaManager) SetUsed(bucket string, bytes int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	// Adjust total
	oldBucket := qm.perBucket[bucket]
	qm.usedBytes = qm.usedBytes - oldBucket + bytes

	// Set bucket usage
	if bytes > 0 {
		qm.perBucket[bucket] = bytes
	} else {
		delete(qm.perBucket, bucket)
	}
}

// Stats returns quota statistics.
type QuotaStats struct {
	MaxBytes       int64            `json:"max_bytes"`
	UsedBytes      int64            `json:"used_bytes"`
	AvailableBytes int64            `json:"available_bytes"` // -1 if unlimited
	PerBucket      map[string]int64 `json:"per_bucket"`
}

// Stats returns current quota statistics.
func (qm *QuotaManager) Stats() QuotaStats {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	perBucket := make(map[string]int64)
	for k, v := range qm.perBucket {
		perBucket[k] = v
	}

	avail := int64(-1)
	if qm.maxBytes > 0 {
		avail = qm.maxBytes - qm.usedBytes
		if avail < 0 {
			avail = 0
		}
	}

	return QuotaStats{
		MaxBytes:       qm.maxBytes,
		UsedBytes:      qm.usedBytes,
		AvailableBytes: avail,
		PerBucket:      perBucket,
	}
}
