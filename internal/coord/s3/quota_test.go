package s3

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewQuotaManager(t *testing.T) {
	qm := NewQuotaManager(10) // 10 GB

	assert.Equal(t, int64(10*1024*1024*1024), qm.MaxBytes())
	assert.Equal(t, int64(0), qm.UsedBytes())
}

func TestNewQuotaManagerUnlimited(t *testing.T) {
	qm := NewQuotaManager(0) // unlimited

	assert.Equal(t, int64(0), qm.MaxBytes())
	assert.Equal(t, int64(-1), qm.AvailableBytes())
}

func TestQuotaManagerAllocate(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	// Allocate 500 MB
	ok := qm.Allocate("bucket1", 500*1024*1024)
	assert.True(t, ok)
	assert.Equal(t, int64(500*1024*1024), qm.UsedBytes())
	assert.Equal(t, int64(500*1024*1024), qm.BucketUsedBytes("bucket1"))

	// Allocate another 400 MB
	ok = qm.Allocate("bucket2", 400*1024*1024)
	assert.True(t, ok)
	assert.Equal(t, int64(900*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerAllocateExceedsQuota(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	// Allocate 900 MB
	ok := qm.Allocate("bucket1", 900*1024*1024)
	assert.True(t, ok)

	// Try to allocate 200 MB more (exceeds 1 GB)
	ok = qm.Allocate("bucket1", 200*1024*1024)
	assert.False(t, ok)

	// Used bytes should not have changed
	assert.Equal(t, int64(900*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerAllocateUnlimited(t *testing.T) {
	qm := NewQuotaManager(0) // unlimited

	// Should always succeed
	ok := qm.Allocate("bucket1", 1000*1024*1024*1024) // 1 TB
	assert.True(t, ok)
	assert.Equal(t, int64(1000*1024*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerRelease(t *testing.T) {
	qm := NewQuotaManager(1)

	qm.Allocate("bucket1", 500*1024*1024)
	qm.Allocate("bucket2", 300*1024*1024)

	qm.Release("bucket1", 200*1024*1024)
	assert.Equal(t, int64(600*1024*1024), qm.UsedBytes())
	assert.Equal(t, int64(300*1024*1024), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerReleaseCleansBucket(t *testing.T) {
	qm := NewQuotaManager(1)

	qm.Allocate("bucket1", 100*1024*1024)
	qm.Release("bucket1", 100*1024*1024)

	assert.Equal(t, int64(0), qm.UsedBytes())
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerUpdate(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	// Initial allocation
	qm.Allocate("bucket1", 100*1024*1024)

	// Update: object grew from 100 MB to 200 MB
	ok := qm.Update("bucket1", 100*1024*1024, 200*1024*1024)
	assert.True(t, ok)
	assert.Equal(t, int64(200*1024*1024), qm.UsedBytes())
	assert.Equal(t, int64(200*1024*1024), qm.BucketUsedBytes("bucket1"))
}

func TestQuotaManagerUpdateExceedsQuota(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	// Use 900 MB
	qm.Allocate("bucket1", 900*1024*1024)

	// Try to update: object grew from 50 MB to 200 MB (would exceed quota)
	ok := qm.Update("bucket1", 50*1024*1024, 200*1024*1024)
	assert.False(t, ok)

	// Usage should not have changed
	assert.Equal(t, int64(900*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerUpdateShrink(t *testing.T) {
	qm := NewQuotaManager(1)

	qm.Allocate("bucket1", 500*1024*1024)

	// Update: object shrunk from 500 MB to 100 MB
	ok := qm.Update("bucket1", 500*1024*1024, 100*1024*1024)
	assert.True(t, ok)
	assert.Equal(t, int64(100*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerCanAllocate(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	assert.True(t, qm.CanAllocate(500*1024*1024))
	assert.True(t, qm.CanAllocate(1024*1024*1024)) // exactly 1 GB

	qm.Allocate("bucket1", 500*1024*1024)

	assert.True(t, qm.CanAllocate(500*1024*1024))
	assert.False(t, qm.CanAllocate(600*1024*1024))
}

func TestQuotaManagerCanAllocateUnlimited(t *testing.T) {
	qm := NewQuotaManager(0)

	assert.True(t, qm.CanAllocate(1000*1024*1024*1024)) // 1 TB
}

func TestQuotaManagerAvailableBytes(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	assert.Equal(t, int64(1024*1024*1024), qm.AvailableBytes())

	qm.Allocate("bucket1", 300*1024*1024)
	assert.Equal(t, int64(724*1024*1024), qm.AvailableBytes())
}

func TestQuotaManagerReset(t *testing.T) {
	qm := NewQuotaManager(1)

	qm.Allocate("bucket1", 100*1024*1024)
	qm.Allocate("bucket2", 200*1024*1024)

	qm.Reset()

	assert.Equal(t, int64(0), qm.UsedBytes())
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket1"))
	assert.Equal(t, int64(0), qm.BucketUsedBytes("bucket2"))
}

func TestQuotaManagerSetUsed(t *testing.T) {
	qm := NewQuotaManager(1)

	qm.SetUsed("bucket1", 100*1024*1024)
	assert.Equal(t, int64(100*1024*1024), qm.UsedBytes())
	assert.Equal(t, int64(100*1024*1024), qm.BucketUsedBytes("bucket1"))

	// Update bucket1
	qm.SetUsed("bucket1", 50*1024*1024)
	assert.Equal(t, int64(50*1024*1024), qm.UsedBytes())
	assert.Equal(t, int64(50*1024*1024), qm.BucketUsedBytes("bucket1"))

	// Add bucket2
	qm.SetUsed("bucket2", 25*1024*1024)
	assert.Equal(t, int64(75*1024*1024), qm.UsedBytes())
}

func TestQuotaManagerStats(t *testing.T) {
	qm := NewQuotaManager(1) // 1 GB

	qm.Allocate("bucket1", 100*1024*1024)
	qm.Allocate("bucket2", 200*1024*1024)

	stats := qm.Stats()

	assert.Equal(t, int64(1024*1024*1024), stats.MaxBytes)
	assert.Equal(t, int64(300*1024*1024), stats.UsedBytes)
	assert.Equal(t, int64(724*1024*1024), stats.AvailableBytes)
	assert.Equal(t, int64(100*1024*1024), stats.PerBucket["bucket1"])
	assert.Equal(t, int64(200*1024*1024), stats.PerBucket["bucket2"])
}

func TestQuotaManagerStatsUnlimited(t *testing.T) {
	qm := NewQuotaManager(0)

	qm.Allocate("bucket1", 100*1024*1024)

	stats := qm.Stats()

	assert.Equal(t, int64(0), stats.MaxBytes)
	assert.Equal(t, int64(100*1024*1024), stats.UsedBytes)
	assert.Equal(t, int64(-1), stats.AvailableBytes)
}
