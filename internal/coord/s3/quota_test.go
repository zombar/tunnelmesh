package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	testGB = 1024 * 1024 * 1024 // 1 Gi in bytes
	testMB = 1024 * 1024        // 1 Mi in bytes
)

func TestNewQuotaManager(t *testing.T) {
	qm := NewQuotaManager(10 * testGB) // 10 Gi

	assert.Equal(t, int64(10*testGB), qm.MaxBytes())
	assert.Equal(t, int64(0), qm.UsedBytes())
}

func TestNewQuotaManagerUnlimited(t *testing.T) {
	qm := NewQuotaManager(0) // unlimited

	assert.Equal(t, int64(0), qm.MaxBytes())
	assert.Equal(t, int64(-1), qm.AvailableBytes())
}

func TestQuotaManagerAllocate(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	// Allocate 500 MB
	ok := qm.Allocate("bucket1", 500*testMB)
	assert.True(t, ok)
	assert.Equal(t, int64(500*testMB), qm.UsedBytes())
	assert.Equal(t, int64(500*testMB), qm.BucketUsedBytes("bucket1"))

	// Allocate another 400 MB
	ok = qm.Allocate("bucket2", 400*testMB)
	assert.True(t, ok)
	assert.Equal(t, int64(900*testMB), qm.UsedBytes())
}

func TestQuotaManagerAllocateExceedsQuota(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	// Allocate 900 MB
	ok := qm.Allocate("bucket1", 900*testMB)
	assert.True(t, ok)

	// Try to allocate 200 MB more (exceeds 1 GB)
	ok = qm.Allocate("bucket1", 200*testMB)
	assert.False(t, ok)

	// Used bytes should not have changed
	assert.Equal(t, int64(900*testMB), qm.UsedBytes())
}

func TestQuotaManagerAllocateUnlimited(t *testing.T) {
	qm := NewQuotaManager(0) // unlimited

	// Should always succeed
	ok := qm.Allocate("bucket1", 1000*testGB) // 1 TB
	assert.True(t, ok)
	assert.Equal(t, int64(1000*testGB), qm.UsedBytes())
}

func TestQuotaManagerRelease(t *testing.T) {
	qm := NewQuotaManager(1 * testGB)

	qm.Allocate("bucket1", 500*testMB)
	qm.Allocate("bucket2", 300*testMB)

	qm.Release("bucket1", 200*testMB)
	assert.Equal(t, int64(600*testMB), qm.UsedBytes())
	assert.Equal(t, int64(300*testMB), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerReleaseCleansBucket(t *testing.T) {
	qm := NewQuotaManager(1 * testGB)

	qm.Allocate("bucket1", 100*testMB)
	qm.Release("bucket1", 100*testMB)

	assert.Equal(t, int64(0), qm.UsedBytes())
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerUpdate(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	// Initial allocation
	qm.Allocate("bucket1", 100*testMB)

	// Update: object grew from 100 MB to 200 MB
	ok := qm.Update("bucket1", 100*1024*1024, 200*testMB)
	assert.True(t, ok)
	assert.Equal(t, int64(200*testMB), qm.UsedBytes())
	assert.Equal(t, int64(200*testMB), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerUpdateExceedsQuota(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	// Use 900 MB
	qm.Allocate("bucket1", 900*testMB)

	// Try to update: object grew from 50 MB to 200 MB (would exceed quota)
	ok := qm.Update("bucket1", 50*1024*1024, 200*testMB)
	assert.False(t, ok)

	// Usage should not have changed
	assert.Equal(t, int64(900*testMB), qm.UsedBytes())
}

func TestQuotaManagerUpdateShrink(t *testing.T) {
	qm := NewQuotaManager(1 * testGB)

	qm.Allocate("bucket1", 500*testMB)

	// Update: object shrunk from 500 MB to 100 MB
	ok := qm.Update("bucket1", 500*1024*1024, 100*testMB)
	assert.True(t, ok)
	assert.Equal(t, int64(100*testMB), qm.UsedBytes())
}

func TestQuotaManagerCanAllocate(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	assert.True(t, qm.CanAllocate(500*testMB))
	assert.True(t, qm.CanAllocate(testGB)) // exactly 1 GB

	qm.Allocate("bucket1", 500*testMB)

	assert.True(t, qm.CanAllocate(500*testMB))
	assert.False(t, qm.CanAllocate(600*testMB))
}

func TestQuotaManagerCanAllocateUnlimited(t *testing.T) {
	qm := NewQuotaManager(0)

	assert.True(t, qm.CanAllocate(1000*testGB)) // 1 TB
}

func TestQuotaManagerAvailableBytes(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	assert.Equal(t, int64(testGB), qm.AvailableBytes())

	qm.Allocate("bucket1", 300*testMB)
	assert.Equal(t, int64(724*testMB), qm.AvailableBytes())
}

func TestQuotaManagerReset(t *testing.T) {
	qm := NewQuotaManager(1 * testGB)

	qm.Allocate("bucket1", 100*testMB)
	qm.Allocate("bucket2", 200*testMB)

	qm.Reset()

	assert.Equal(t, int64(0), qm.UsedBytes())
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket1"))
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket2"))
}

func TestQuotaManagerSetUsed(t *testing.T) {
	qm := NewQuotaManager(1 * testGB)

	qm.SetUsed("bucket1", 100*testMB)
	assert.Equal(t, int64(100*testMB), qm.UsedBytes())
	assert.Equal(t, int64(100*testMB), qm.BucketUsedBytes("bucket1"))

	// Update bucket1
	qm.SetUsed("bucket1", 50*testMB)
	assert.Equal(t, int64(50*testMB), qm.UsedBytes())
	assert.Equal(t, int64(50*testMB), qm.BucketUsedBytes("bucket1"))

	// Add bucket2
	qm.SetUsed("bucket2", 25*testMB)
	assert.Equal(t, int64(75*testMB), qm.UsedBytes())
}

func TestQuotaManagerStats(t *testing.T) {
	qm := NewQuotaManager(1 * testGB) // 1 Gi

	qm.Allocate("bucket1", 100*testMB)
	qm.Allocate("bucket2", 200*testMB)

	stats := qm.Stats()

	assert.Equal(t, int64(testGB), stats.MaxBytes)
	assert.Equal(t, int64(300*testMB), stats.UsedBytes)
	assert.Equal(t, int64(724*testMB), stats.AvailableBytes)
	assert.Equal(t, int64(100*testMB), stats.PerBucket["bucket1"])
	assert.Equal(t, int64(200*testMB), stats.PerBucket["bucket2"])
}

func TestQuotaManagerStatsUnlimited(t *testing.T) {
	qm := NewQuotaManager(0)

	qm.Allocate("bucket1", 100*testMB)

	stats := qm.Stats()

	assert.Equal(t, int64(0), stats.MaxBytes)
	assert.Equal(t, int64(100*testMB), stats.UsedBytes)
	assert.Equal(t, int64(-1), stats.AvailableBytes)
}
