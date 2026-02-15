package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStore(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir, nil)
	require.NoError(t, err)
	assert.Equal(t, tmpDir, store.DataDir())

	// Check buckets directory was created
	bucketsDir := filepath.Join(tmpDir, "buckets")
	info, err := os.Stat(bucketsDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestStoreCreateBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	// Verify bucket exists
	meta, err := store.HeadBucket(context.Background(), "test-bucket")
	require.NoError(t, err)
	assert.Equal(t, "test-bucket", meta.Name)
	assert.Equal(t, "alice", meta.Owner)
	assert.False(t, meta.CreatedAt.IsZero())
}

func TestStoreCreateBucketAlreadyExists(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	// Try to create again
	err = store.CreateBucket(context.Background(), "test-bucket", "bob", 2, nil)
	assert.ErrorIs(t, err, ErrBucketExists)
}

func TestStoreDeleteBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	err = store.DeleteBucket(context.Background(), "test-bucket")
	require.NoError(t, err)

	// Verify bucket is gone
	_, err = store.HeadBucket(context.Background(), "test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.DeleteBucket(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotEmpty(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	// Add an object
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("hello")), 5, "text/plain", nil)
	require.NoError(t, err)

	// Try to delete bucket
	err = store.DeleteBucket(context.Background(), "test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotEmpty)
}

func TestStoreDeleteBucketWithOnlyTombstones(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	require.NoError(t, store.CreateBucket(ctx, "test-bucket", "alice", 2, nil))

	// Add an object
	_, err := store.PutObject(ctx, "test-bucket", "file.txt", bytes.NewReader([]byte("hello")), 5, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the object (first delete)
	require.NoError(t, store.DeleteObject(ctx, "test-bucket", "file.txt"))

	// Bucket with only tombstoned objects should be deletable
	err = store.DeleteBucket(ctx, "test-bucket")
	assert.NoError(t, err, "bucket with only tombstones should be deletable")
}

func TestStoreHeadBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, err := store.HeadBucket(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListBuckets(t *testing.T) {
	store := newTestStoreWithCAS(t)

	// Empty initially
	buckets, err := store.ListBuckets(context.Background())
	require.NoError(t, err)
	assert.Empty(t, buckets)

	// Create some buckets
	require.NoError(t, store.CreateBucket(context.Background(), "bucket-a", "alice", 2, nil))
	require.NoError(t, store.CreateBucket(context.Background(), "bucket-b", "bob", 2, nil))

	buckets, err = store.ListBuckets(context.Background())
	require.NoError(t, err)
	assert.Len(t, buckets, 2)

	names := make(map[string]bool)
	for _, b := range buckets {
		names[b.Name] = true
	}
	assert.True(t, names["bucket-a"])
	assert.True(t, names["bucket-b"])
}

func TestStorePutObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("hello world")
	meta, err := store.PutObject(context.Background(), "test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	assert.Equal(t, "greeting.txt", meta.Key)
	assert.Equal(t, int64(len(content)), meta.Size)
	assert.Equal(t, "text/plain", meta.ContentType)
	assert.NotEmpty(t, meta.ETag)
	assert.False(t, meta.LastModified.IsZero())
}

func TestStorePutObject_SetsExpiry(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Set default object expiry to 25 years (9125 days)
	store.SetDefaultObjectExpiryDays(9125)

	content := []byte("hello world")
	meta, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	require.NotNil(t, meta.Expires, "Expires should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 9125)
	// Allow 1 minute tolerance for test timing
	assert.WithinDuration(t, expectedExpiry, *meta.Expires, time.Minute)
}

func TestStorePutObject_NoExpiryWhenNotConfigured(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Don't set expiry - objects should have no expiry
	content := []byte("hello world")
	meta, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	assert.Nil(t, meta.Expires, "Expires should not be set when not configured")
}

func TestStorePutObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, err := store.PutObject(context.Background(), "nonexistent", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStorePutObjectNestedKey(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("nested content")
	meta, err := store.PutObject(context.Background(), "test-bucket", "path/to/file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)
	assert.Equal(t, "path/to/file.txt", meta.Key)

	// Verify we can retrieve it
	reader, objMeta, err := store.GetObject(context.Background(), "test-bucket", "path/to/file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, "path/to/file.txt", objMeta.Key)
	data, _ := io.ReadAll(reader)
	assert.Equal(t, content, data)
}

func TestStorePutObjectWithMetadata(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	userMeta := map[string]string{
		"x-amz-meta-author":  "alice",
		"x-amz-meta-version": "1.0",
	}
	meta, err := store.PutObject(context.Background(), "test-bucket", "doc.txt", bytes.NewReader([]byte("data")), 4, "text/plain", userMeta)
	require.NoError(t, err)

	assert.Equal(t, userMeta, meta.Metadata)

	// Verify persisted
	objMeta, err := store.HeadObject(context.Background(), "test-bucket", "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, userMeta, objMeta.Metadata)
}

func TestStoreGetObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("hello world")
	_, err := store.PutObject(context.Background(), "test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	reader, meta, err := store.GetObject(context.Background(), "test-bucket", "greeting.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, "greeting.txt", meta.Key)
	assert.Equal(t, int64(len(content)), meta.Size)

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestStoreGetObjectNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, _, err := store.GetObject(context.Background(), "test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreGetObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, _, err := store.GetObject(context.Background(), "nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreHeadObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("hello world")
	_, err := store.PutObject(context.Background(), "test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	meta, err := store.HeadObject(context.Background(), "test-bucket", "greeting.txt")
	require.NoError(t, err)

	assert.Equal(t, "greeting.txt", meta.Key)
	assert.Equal(t, int64(len(content)), meta.Size)
	assert.Equal(t, "text/plain", meta.ContentType)
}

func TestStoreHeadObjectNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, err := store.HeadObject(context.Background(), "test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	err = store.DeleteObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is tombstoned (soft-deleted), not removed
	meta, err := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "object should be tombstoned")
}

func TestStorePurgeObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Purge actually removes the object
	err = store.PurgeObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	err := store.DeleteObject(context.Background(), "test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.DeleteObject(context.Background(), "nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjects(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Add some objects
	objects := []string{"a.txt", "b.txt", "c.txt"}
	for _, key := range objects {
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, _, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestStoreListObjectsWithPrefix(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Add some objects with different prefixes
	objects := []string{"docs/a.txt", "docs/b.txt", "images/c.png"}
	for _, key := range objects {
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, _, _, err := store.ListObjects(context.Background(), "test-bucket", "docs/", "", 0)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	for _, obj := range list {
		assert.True(t, strings.HasPrefix(obj.Key, "docs/"))
	}
}

func TestStoreListObjectsWithMaxKeys(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Add 5 objects
	for i := 0; i < 5; i++ {
		key := string(rune('a'+i)) + ".txt"
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, isTruncated, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 2)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.True(t, isTruncated)
}

func TestStoreListObjectsBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, _, _, err := store.ListObjects(context.Background(), "nonexistent", "", "", 0)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjectsEmpty(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	list, isTruncated, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.False(t, isTruncated)
}

func TestStoreOverwriteObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Write initial version
	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("version 1")), 9, "text/plain", nil)
	require.NoError(t, err)

	// Overwrite
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("version 2 - longer")), 18, "text/plain", nil)
	require.NoError(t, err)

	// Read back
	reader, meta, err := store.GetObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, int64(18), meta.Size)
	data, _ := io.ReadAll(reader)
	assert.Equal(t, "version 2 - longer", string(data))
}

func TestStoreWithQuota(t *testing.T) {
	tmpDir := t.TempDir()
	quota := NewQuotaManager(1 * 1024 * 1024 * 1024) // 1 Gi limit
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}

	store, err := NewStoreWithCAS(tmpDir, quota, masterKey)
	require.NoError(t, err)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Put object should update quota
	content := []byte("hello world")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	stats := store.QuotaStats()
	require.NotNil(t, stats)
	assert.Equal(t, int64(len(content)), stats.UsedBytes)
	assert.Equal(t, int64(len(content)), stats.PerBucket["test-bucket"])

	// Tombstone (soft delete) should NOT release quota yet
	err = store.DeleteObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	stats = store.QuotaStats()
	assert.Equal(t, int64(len(content)), stats.UsedBytes, "tombstoned objects still use quota")

	// Purge should release quota
	err = store.PurgeObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	stats = store.QuotaStats()
	assert.Equal(t, int64(0), stats.UsedBytes, "purged objects release quota")
}

// =========================================================================
// Object Lifecycle Tests
// =========================================================================

func TestObjectLifecycle_ExpirySetting(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Set object expiry to 30 days
	store.SetDefaultObjectExpiryDays(30)

	content := []byte("test content")
	meta, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Verify expiry is set
	require.NotNil(t, meta.Expires, "Expires should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 30)
	assert.WithinDuration(t, expectedExpiry, *meta.Expires, time.Minute)
}

func TestObjectLifecycle_TombstonePreservesContent(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("important data")
	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the object
	err = store.TombstoneObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is tombstoned
	meta, err := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned())

	// Content should still be readable
	reader, readMeta, err := store.GetObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	assert.True(t, readMeta.IsTombstoned())

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestObjectLifecycle_TombstonedObjectInList(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create two objects
	_, err := store.PutObject(context.Background(), "test-bucket", "live.txt", bytes.NewReader([]byte("live")), 4, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "dead.txt", bytes.NewReader([]byte("dead")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone one
	err = store.TombstoneObject(context.Background(), "test-bucket", "dead.txt")
	require.NoError(t, err)

	// Both should appear in list
	objects, _, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Len(t, objects, 2)

	// Check tombstone flags
	var liveObj, deadObj *ObjectMeta
	for i := range objects {
		switch objects[i].Key {
		case "live.txt":
			liveObj = &objects[i]
		case "dead.txt":
			deadObj = &objects[i]
		}
	}
	require.NotNil(t, liveObj)
	require.NotNil(t, deadObj)
	assert.False(t, liveObj.IsTombstoned())
	assert.True(t, deadObj.IsTombstoned())
}

func TestObjectLifecycle_PurgeRemovesCompletely(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	content := []byte("to be purged")
	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone first
	err = store.TombstoneObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Then purge
	err = store.PurgeObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is completely gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// Should not appear in list
	objects, _, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, objects)
}

func TestObjectLifecycle_DoubleTombstoneIsNoop(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone twice - should not error
	err = store.TombstoneObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	meta1, _ := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	tombstonedAt1 := meta1.TombstonedAt

	err = store.TombstoneObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	meta2, _ := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	// Timestamp should not change on second tombstone
	assert.Equal(t, tombstonedAt1, meta2.TombstonedAt)
}

func TestObjectLifecycle_PurgeTombstonedObjects(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create and tombstone some objects
	_, err := store.PutObject(context.Background(), "test-bucket", "old.txt", bytes.NewReader([]byte("old")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "new.txt", bytes.NewReader([]byte("new")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "live.txt", bytes.NewReader([]byte("live")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone old.txt and new.txt
	err = store.TombstoneObject(context.Background(), "test-bucket", "old.txt")
	require.NoError(t, err)
	err = store.TombstoneObject(context.Background(), "test-bucket", "new.txt")
	require.NoError(t, err)

	// Manually backdate old.txt tombstone to 100 days ago
	oldMeta, _ := store.HeadObject(context.Background(), "test-bucket", "old.txt")
	oldTime := time.Now().UTC().AddDate(0, 0, -100)
	oldMeta.TombstonedAt = &oldTime
	// Write the backdated metadata
	metaPath := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", "old.txt.json")
	metaData, _ := os.ReadFile(metaPath)
	var meta ObjectMeta
	_ = json.Unmarshal(metaData, &meta)
	meta.TombstonedAt = &oldTime
	updatedData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaPath, updatedData, 0644)

	// Set retention to 90 days and purge
	store.SetTombstoneRetentionDays(90)
	purged := store.PurgeTombstonedObjects(context.Background())

	// Should have purged 1 object (old.txt is > 90 days old)
	assert.Equal(t, 1, purged)

	// old.txt should be gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "old.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// new.txt should still exist (tombstoned < 90 days ago)
	newMeta, err := store.HeadObject(context.Background(), "test-bucket", "new.txt")
	require.NoError(t, err)
	assert.True(t, newMeta.IsTombstoned())

	// live.txt should still exist (not tombstoned)
	liveMeta, err := store.HeadObject(context.Background(), "test-bucket", "live.txt")
	require.NoError(t, err)
	assert.False(t, liveMeta.IsTombstoned())
}

func TestObjectLifecycle_PurgeTombstonedObjectsDisabled(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)
	err = store.TombstoneObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Retention days = 0 means disabled
	store.SetTombstoneRetentionDays(0)
	purged := store.PurgeTombstonedObjects(context.Background())
	assert.Equal(t, 0, purged)

	// Object should still exist
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.NoError(t, err)
}

func TestObjectLifecycle_PurgeCancellation(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create and tombstone many objects to ensure cancellation occurs mid-operation
	totalObjects := 10000
	if testing.Short() {
		totalObjects = 2000
	}
	for i := 0; i < totalObjects; i++ {
		key := fmt.Sprintf("file-%04d.txt", i)
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)

		// Tombstone with old timestamp so they'll be purged
		err = store.TombstoneObject(context.Background(), "test-bucket", key)
		require.NoError(t, err)

		// Manually backdate tombstone to 100 days ago
		meta, _ := store.HeadObject(context.Background(), "test-bucket", key)
		oldTime := time.Now().UTC().AddDate(0, 0, -100)
		meta.TombstonedAt = &oldTime
		metaPath := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", key+".json")
		metaJSON, _ := json.Marshal(meta)
		_ = os.WriteFile(metaPath, metaJSON, 0600)
	}

	store.SetTombstoneRetentionDays(30) // Expire after 30 days

	// Create a context with a short timeout (should cancel mid-operation with 10k objects)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run purge - it should be cancelled mid-operation by timeout
	purged := store.PurgeTombstonedObjects(ctx)

	// Verify partial progress: some objects purged, but not all
	assert.Greater(t, purged, 0, "should have purged at least one object before cancellation")
	assert.Less(t, purged, totalObjects, "should not have purged all objects due to cancellation")

	t.Logf("Purged %d objects before cancellation (out of %d)", purged, totalObjects)

	// Verify some objects still exist (not all were purged)
	remaining := 0
	for i := 0; i < totalObjects; i++ {
		key := fmt.Sprintf("file-%04d.txt", i)
		_, err := store.HeadObject(context.Background(), "test-bucket", key)
		if err == nil {
			remaining++
		}
	}
	assert.Greater(t, remaining, 0, "some objects should still exist after early cancellation")
	assert.Equal(t, totalObjects-purged, remaining, "remaining objects should equal total minus purged")
}

func TestObjectLifecycle_PurgeAllTombstonedObjects(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create objects: two tombstoned (one old, one recent), one live
	_, err := store.PutObject(context.Background(), "test-bucket", "old.txt", bytes.NewReader([]byte("old")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "new.txt", bytes.NewReader([]byte("new")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "live.txt", bytes.NewReader([]byte("live")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone old.txt and new.txt
	err = store.TombstoneObject(context.Background(), "test-bucket", "old.txt")
	require.NoError(t, err)
	err = store.TombstoneObject(context.Background(), "test-bucket", "new.txt")
	require.NoError(t, err)

	// Backdate old.txt tombstone to 100 days ago
	metaPath := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", "old.txt.json")
	metaData, _ := os.ReadFile(metaPath)
	var meta ObjectMeta
	_ = json.Unmarshal(metaData, &meta)
	oldTime := time.Now().UTC().AddDate(0, 0, -100)
	meta.TombstonedAt = &oldTime
	updatedData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(metaPath, updatedData, 0644)

	// PurgeAll should purge BOTH tombstoned objects regardless of age
	purged := store.PurgeAllTombstonedObjects(context.Background())
	assert.Equal(t, 2, purged)

	// Both tombstoned objects should be gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "old.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
	_, err = store.HeadObject(context.Background(), "test-bucket", "new.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// Live object should still exist
	liveMeta, err := store.HeadObject(context.Background(), "test-bucket", "live.txt")
	require.NoError(t, err)
	assert.False(t, liveMeta.IsTombstoned())
}

func TestObjectLifecycle_PurgeAllTombstonedObjectsEmpty(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// No tombstoned objects — should return 0
	purged := store.PurgeAllTombstonedObjects(context.Background())
	assert.Equal(t, 0, purged)
}

func TestObjectLifecycle_PurgeAllCancellation(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create and tombstone enough objects to observe partial cancellation
	count := 500
	if testing.Short() {
		count = 200
	}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("file-%04d.txt", i)
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
		err = store.TombstoneObject(context.Background(), "test-bucket", key)
		require.NoError(t, err)
	}

	// Cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	purged := store.PurgeAllTombstonedObjects(ctx)
	assert.Less(t, purged, count, "should not purge all objects when cancelled")
}

func TestBucketTombstone(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	// Create bucket with objects
	err = store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	content := []byte("test content")
	_, err = store.PutObject(context.Background(), "test-bucket", "file1.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject(context.Background(), "test-bucket", "file2.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the bucket
	err = store.TombstoneBucket(context.Background(), "test-bucket")
	require.NoError(t, err)

	// Bucket should be tombstoned
	bucketMeta, err := store.HeadBucket(context.Background(), "test-bucket")
	require.NoError(t, err)
	assert.True(t, bucketMeta.IsTombstoned())

	// Objects should appear tombstoned via HeadObject
	obj1, err := store.HeadObject(context.Background(), "test-bucket", "file1.txt")
	require.NoError(t, err)
	assert.True(t, obj1.IsTombstoned(), "object should appear tombstoned when bucket is tombstoned")

	// Objects should appear tombstoned in ListObjects
	objects, _, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 100)
	require.NoError(t, err)
	assert.Len(t, objects, 2)
	for _, obj := range objects {
		assert.True(t, obj.IsTombstoned(), "all objects should appear tombstoned")
	}

	// Untombstone the bucket
	err = store.UntombstoneBucket(context.Background(), "test-bucket")
	require.NoError(t, err)

	// Bucket should no longer be tombstoned
	bucketMeta, err = store.HeadBucket(context.Background(), "test-bucket")
	require.NoError(t, err)
	assert.False(t, bucketMeta.IsTombstoned())

	// Objects should no longer appear tombstoned
	obj1, err = store.HeadObject(context.Background(), "test-bucket", "file1.txt")
	require.NoError(t, err)
	assert.False(t, obj1.IsTombstoned(), "object should not appear tombstoned after bucket untombstoned")
}

func TestDeleteTwiceToPurge(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	err = store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	content := []byte("delete me")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// First delete: should tombstone
	err = store.DeleteObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	meta, err := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "first delete should tombstone")

	// Second delete: should purge
	err = store.DeleteObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.Equal(t, ErrObjectNotFound, err, "second delete should purge")
}

func TestDeleteTwiceToPurge_BucketTombstoned(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	err = store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	content := []byte("delete me")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the bucket (simulates file share deletion)
	err = store.TombstoneBucket(context.Background(), "test-bucket")
	require.NoError(t, err)

	// Object appears tombstoned via bucket
	meta, err := store.HeadObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned())

	// Delete should purge (since already tombstoned via bucket)
	err = store.DeleteObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.Equal(t, ErrObjectNotFound, err, "delete of bucket-tombstoned object should purge")
}

// --- Path Traversal Regression Tests ---

func TestPathTraversal_BucketName(t *testing.T) {
	store := newTestStoreWithCAS(t)

	// Test various path traversal attempts in bucket names
	// Note: URL-encoded attacks (like %2f) are handled at the API layer where
	// URL decoding occurs before validation. These tests cover the storage layer.
	maliciousBuckets := []string{
		"..",
		"../etc",
		"bucket/../etc",
		"bucket/../../etc",
		"./bucket",
		".\\bucket",
		"bucket\\..\\etc",
		"/etc/passwd",
		"\\etc\\passwd",
		"bucket\x00evil",  // null byte injection
		"\x00/etc/passwd", // null byte at start
	}

	for _, name := range maliciousBuckets {
		t.Run(name, func(t *testing.T) {
			err := store.CreateBucket(context.Background(), name, "attacker", 2, nil)
			assert.Error(t, err, "bucket name %q should be rejected", name)
		})
	}
}

func TestPathTraversal_ObjectKey(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Test various path traversal attempts in object keys
	// Note: URL-encoded attacks are handled at the API layer
	maliciousKeys := []string{
		"..",
		"../etc/passwd",
		"foo/../../../etc/passwd",
		"foo/bar/../../..",
		"foo\\..\\..\\etc",
		"/etc/passwd",
		"\\etc\\passwd",
	}

	content := []byte("malicious content")
	for _, key := range maliciousKeys {
		t.Run(key, func(t *testing.T) {
			_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
			assert.Error(t, err, "object key %q should be rejected", key)
		})
	}
}

func TestPathTraversal_ValidPaths(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Test that valid paths with dots are still allowed
	validKeys := []string{
		"file.txt",
		"folder/file.txt",
		"folder.with.dots/file.txt",
		".hidden",
		"folder/.hidden",
		"...file",
		"file...",
		"folder/...file",
		"dotdot..file",
		"file..dotdot",
	}

	content := []byte("valid content")
	for _, key := range validKeys {
		t.Run(key, func(t *testing.T) {
			_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
			assert.NoError(t, err, "object key %q should be allowed", key)
		})
	}
}

func TestPathTraversal_ValidNestedPaths(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Test nested paths that should work
	content := []byte("valid content")

	// First create a folder, then a file in it
	_, err := store.PutObject(context.Background(), "test-bucket", "dotfolder/.hidden", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	assert.NoError(t, err, "nested .hidden file should be allowed")

	_, err = store.PutObject(context.Background(), "test-bucket", ".hiddendir/file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	assert.NoError(t, err, "file in .hidden directory should be allowed")
}

// =========================================================================
// CDC (Content-Defined Chunking) Tests
// =========================================================================

func TestCDCChunking_BasicSplit(t *testing.T) {
	// Create data larger than min chunk size
	data := make([]byte, 10*1024) // 10KB
	for i := range data {
		data[i] = byte(i % 256)
	}

	chunks, err := ChunkData(data)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)

	// Verify chunks are within bounds
	// Note: The last chunk can be smaller than MinChunkSize (it's the remainder)
	for i, chunk := range chunks {
		if i < len(chunks)-1 {
			// Non-final chunks should be at least MinChunkSize
			assert.GreaterOrEqual(t, len(chunk), MinChunkSize, "chunk %d too small", i)
		}
		assert.LessOrEqual(t, len(chunk), MaxChunkSize, "chunk %d too large", i)
		assert.Greater(t, len(chunk), 0, "chunk %d is empty", i)
	}

	// Verify reassembly
	var totalSize int
	for _, chunk := range chunks {
		totalSize += len(chunk)
	}
	reassembled := make([]byte, 0, totalSize)
	for _, chunk := range chunks {
		reassembled = append(reassembled, chunk...)
	}
	assert.Equal(t, data, reassembled)
}

func TestCDCChunking_EmptyData(t *testing.T) {
	chunks, err := ChunkData([]byte{})
	require.NoError(t, err)
	assert.Nil(t, chunks)
}

func TestCDCChunking_SmallData(t *testing.T) {
	// Data smaller than min chunk size
	data := []byte("hello world")
	chunks, err := ChunkData(data)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, data, chunks[0])
}

func TestCDCChunking_BoundaryStability(t *testing.T) {
	// Test that inserting bytes only affects nearby chunks
	base := make([]byte, 50*1024) // 50KB
	for i := range base {
		base[i] = byte(i % 256)
	}

	// Insert a few bytes in the middle
	modified := make([]byte, len(base)+10)
	copy(modified[:len(base)/2], base[:len(base)/2])
	copy(modified[len(base)/2:len(base)/2+10], []byte("INSERTED!"))
	copy(modified[len(base)/2+10:], base[len(base)/2:])

	baseChunks, err := ChunkData(base)
	require.NoError(t, err)

	modChunks, err := ChunkData(modified)
	require.NoError(t, err)

	// Most chunks should be identical (CDC property)
	// Count matching chunks
	baseHashes := make(map[string]bool)
	for _, chunk := range baseChunks {
		baseHashes[ContentHash(chunk)] = true
	}

	matchCount := 0
	for _, chunk := range modChunks {
		if baseHashes[ContentHash(chunk)] {
			matchCount++
		}
	}

	// At least some chunks should match (CDC preserves boundaries)
	// We expect chunks before and after the insertion point to match
	assert.Greater(t, matchCount, 0, "CDC should preserve some chunk boundaries")
}

// =========================================================================
// CAS (Content-Addressable Storage) Tests
// =========================================================================

func TestCAS_WriteAndReadChunk(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	data := []byte("hello content-addressable world")
	hash, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Read it back
	retrieved, err := cas.ReadChunk(context.Background(), hash)
	require.NoError(t, err)
	assert.Equal(t, data, retrieved)
}

func TestCAS_Deduplication(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	data := []byte("duplicate me")

	// Write same data twice
	hash1, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	hash2, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	// Should return same hash (dedup)
	assert.Equal(t, hash1, hash2)

	// Should only have one chunk file
	totalSize, err := cas.TotalSize(context.Background())
	require.NoError(t, err)

	// Write once more to verify it doesn't increase
	_, _, err = cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	totalSize2, err := cas.TotalSize(context.Background())
	require.NoError(t, err)
	assert.Equal(t, totalSize, totalSize2, "duplicate write should not increase storage")
}

func TestCAS_ConvergentEncryption(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{1, 2, 3}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	data := []byte("same plaintext should produce same ciphertext")

	// Write the data
	hash1, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	// Get the encrypted size
	size1, err := cas.ChunkSize(context.Background(), hash1)
	require.NoError(t, err)

	// Delete and rewrite
	_, err = cas.DeleteChunk(context.Background(), hash1)
	require.NoError(t, err)

	hash2, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	size2, err := cas.ChunkSize(context.Background(), hash2)
	require.NoError(t, err)

	// Same hash and same encrypted size (convergent encryption)
	assert.Equal(t, hash1, hash2)
	assert.Equal(t, size1, size2)
}

func TestCAS_ChunkNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	_, err = cas.ReadChunk(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestCAS_DeleteChunk(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	data := []byte("delete me")
	hash, _, err := cas.WriteChunk(context.Background(), data)
	require.NoError(t, err)

	assert.True(t, cas.ChunkExists(hash))

	_, err = cas.DeleteChunk(context.Background(), hash)
	require.NoError(t, err)

	assert.False(t, cas.ChunkExists(hash))
}

// =========================================================================
// Version History Tests
// =========================================================================

// newTestStoreWithCAS creates a store with CAS enabled for testing versioning.
func newTestStoreWithCAS(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	store, err := NewStoreWithCAS(tmpDir, nil, masterKey)
	require.NoError(t, err)
	return store
}

func TestVersioning_PutCreatesVersion(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// First write
	content1 := []byte("version 1")
	meta1, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, meta1.VersionID)

	// Second write (should create version)
	content2 := []byte("version 2")
	meta2, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, meta2.VersionID)
	assert.NotEqual(t, meta1.VersionID, meta2.VersionID)

	// List versions
	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 2)

	// Current version should be first
	assert.True(t, versions[0].IsCurrent)
	assert.Equal(t, meta2.VersionID, versions[0].VersionID)
	assert.False(t, versions[1].IsCurrent)
	assert.Equal(t, meta1.VersionID, versions[1].VersionID)
}

func TestVersioning_ListVersions(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create multiple versions
	for i := 1; i <= 5; i++ {
		content := []byte(strings.Repeat("x", i*100))
		_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 5)

	// Should be sorted newest first
	for i := 0; i < len(versions)-1; i++ {
		assert.True(t, versions[i].LastModified.After(versions[i+1].LastModified) ||
			versions[i].LastModified.Equal(versions[i+1].LastModified))
	}

	// Only one should be current
	currentCount := 0
	for _, v := range versions {
		if v.IsCurrent {
			currentCount++
		}
	}
	assert.Equal(t, 1, currentCount)
}

func TestVersioning_GetObjectVersion(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create versions
	content1 := []byte("original content")
	meta1, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)

	content2 := []byte("updated content")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Get old version
	reader, meta, err := store.GetObjectVersion(context.Background(), "test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content1, data)
	assert.Equal(t, meta1.VersionID, meta.VersionID)
}

func TestVersioning_RestoreVersion(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create versions
	content1 := []byte("original content")
	meta1, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)

	content2 := []byte("updated content - longer")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Restore old version
	restored, err := store.RestoreVersion(context.Background(), "test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)
	assert.NotEqual(t, meta1.VersionID, restored.VersionID, "restore should create new version ID")

	// Current content should be original
	reader, _, err := store.GetObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content1, data)

	// Should now have 3 versions
	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 3)
}

func TestVersioning_RestoreSharesChunks(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create original version with specific content
	content := []byte("content to restore")
	meta1, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Overwrite
	content2 := []byte("different content")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Get storage size before restore
	sizeBefore, err := store.cas.TotalSize(context.Background())
	require.NoError(t, err)

	// Restore - should reuse chunks, not duplicate
	_, err = store.RestoreVersion(context.Background(), "test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)

	// Storage should not significantly increase (chunks are shared)
	sizeAfter, err := store.cas.TotalSize(context.Background())
	require.NoError(t, err)

	// Size should be roughly the same (may vary slightly due to metadata)
	assert.InDelta(t, sizeBefore, sizeAfter, 100, "restore should share chunks, not duplicate")
}

func TestVersioning_DeleteCleansVersions(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create versions
	for i := 1; i <= 3; i++ {
		content := []byte(strings.Repeat("v", i*100))
		_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	// Verify versions exist
	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 3)

	// Purge the object
	err = store.PurgeObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject(context.Background(), "test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// Versions should be gone
	versions, err = store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Empty(t, versions)
}

func TestVersioning_RetentionPruning(t *testing.T) {
	store := newTestStoreWithCAS(t)
	store.SetVersionRetentionDays(30) // 30 day retention
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create an object
	content := []byte("original")
	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Create another version (triggers pruning check)
	content2 := []byte("updated")
	_, err = store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Both versions should exist (neither expired)
	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}

func TestVersioning_NoVersionsForNewObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create single object
	content := []byte("single version")
	_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Should only have 1 version (current)
	versions, err := store.ListVersions(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 1)
	assert.True(t, versions[0].IsCurrent)
}

// Concurrent access tests - run with -race flag

func TestConcurrent_PutObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	const numGoroutines = 10
	const numWrites = 5

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			for j := 0; j < numWrites; j++ {
				content := []byte(strings.Repeat("x", 1000+workerID*100+j*10))
				key := "shared-key.txt" // All workers write to same key
				_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
				if err != nil {
					t.Errorf("worker %d write %d failed: %v", workerID, j, err)
				}
			}
			done <- true
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify object still readable
	reader, meta, err := store.GetObject(context.Background(), "test-bucket", "shared-key.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	assert.True(t, meta.Size > 0)
}

func TestConcurrent_ReadWhileGC(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create some objects with versions
	for i := 0; i < 5; i++ {
		content := []byte(strings.Repeat("y", 1000+i*100))
		_, err := store.PutObject(context.Background(), "test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	done := make(chan bool, 2)

	// Concurrent GC
	go func() {
		for i := 0; i < 3; i++ {
			store.RunGarbageCollection(context.Background())
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 10; i++ {
			reader, _, err := store.GetObject(context.Background(), "test-bucket", "file.txt")
			if err == nil {
				_, _ = io.ReadAll(reader)
				_ = reader.Close()
			}
		}
		done <- true
	}()

	<-done
	<-done

	// Verify object still accessible after concurrent operations
	reader, _, err := store.GetObject(context.Background(), "test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
}

func TestConcurrent_MultipleKeys(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	const numGoroutines = 10
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(workerID int) {
			key := strings.Repeat("k", workerID+1) + ".txt"
			for j := 0; j < 5; j++ {
				content := []byte(strings.Repeat("z", 500+j*50))
				_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
				if err != nil {
					t.Errorf("worker %d failed: %v", workerID, err)
				}
			}
			done <- true
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify all keys exist
	objects, _, _, err := store.ListObjects(context.Background(), "test-bucket", "", "", 100)
	require.NoError(t, err)
	assert.Equal(t, numGoroutines, len(objects))
}

func TestStreamingRead_LargeFile(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create a file larger than a single chunk (target ~4KB, max 64KB)
	// Use 200KB to ensure multiple chunks
	content := make([]byte, 200*1024)
	for i := range content {
		content[i] = byte(i % 256)
	}

	_, err := store.PutObject(context.Background(), "test-bucket", "large.bin", bytes.NewReader(content), int64(len(content)), "application/octet-stream", nil)
	require.NoError(t, err)

	// Read back and verify streaming works correctly
	reader, meta, err := store.GetObject(context.Background(), "test-bucket", "large.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, int64(len(content)), meta.Size)

	// Read in small chunks to test streaming
	readContent := make([]byte, 0, len(content))
	buf := make([]byte, 1024) // 1KB buffer
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			readContent = append(readContent, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, content, readContent)
}

func TestCalculateBucketSize(t *testing.T) {
	store := newTestStoreWithCAS(t)
	bucket := "test-bucket"

	require.NoError(t, store.CreateBucket(context.Background(), bucket, "owner-id", 2, nil))

	// Initially, bucket size should be 0
	size, err := store.CalculateBucketSize(context.Background(), bucket)
	require.NoError(t, err)
	assert.Equal(t, int64(0), size)

	// Put some objects
	content1 := []byte("test content 1")
	_, err = store.PutObject(context.Background(), bucket, "file1.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)

	content2 := []byte("test content 2 is longer")
	_, err = store.PutObject(context.Background(), bucket, "file2.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Size should reflect both files
	size, err = store.CalculateBucketSize(context.Background(), bucket)
	require.NoError(t, err)
	expectedSize := int64(len(content1) + len(content2))
	assert.Equal(t, expectedSize, size)

	// Update an object (size should change)
	content3 := []byte("updated content")
	_, err = store.PutObject(context.Background(), bucket, "file1.txt", bytes.NewReader(content3), int64(len(content3)), "text/plain", nil)
	require.NoError(t, err)

	size, err = store.CalculateBucketSize(context.Background(), bucket)
	require.NoError(t, err)
	expectedSize = int64(len(content3) + len(content2))
	assert.Equal(t, expectedSize, size)
}

func TestCalculateBucketSize_NonExistentBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)

	// Non-existent bucket should return 0 size, not an error
	size, err := store.CalculateBucketSize(context.Background(), "non-existent")
	require.NoError(t, err)
	assert.Equal(t, int64(0), size)
}

func TestCalculatePrefixSize(t *testing.T) {
	store := newTestStoreWithCAS(t)
	bucket := "test-bucket"

	require.NoError(t, store.CreateBucket(context.Background(), bucket, "owner-id", 2, nil))

	// Create files in different folders
	files := map[string][]byte{
		"folder1/file1.txt": []byte("content1"),
		"folder1/file2.txt": []byte("content2 longer"),
		"folder2/file3.txt": []byte("content3"),
		"root.txt":          []byte("root content"),
	}

	for key, content := range files {
		_, err := store.PutObject(context.Background(), bucket, key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	// Calculate size for folder1/ prefix
	size, err := store.CalculatePrefixSize(context.Background(), bucket, "folder1/")
	require.NoError(t, err)
	expectedSize := int64(len(files["folder1/file1.txt"]) + len(files["folder1/file2.txt"]))
	assert.Equal(t, expectedSize, size)

	// Calculate size for folder2/ prefix
	size, err = store.CalculatePrefixSize(context.Background(), bucket, "folder2/")
	require.NoError(t, err)
	expectedSize = int64(len(files["folder2/file3.txt"]))
	assert.Equal(t, expectedSize, size)

	// Calculate size for root (empty prefix)
	size, err = store.CalculatePrefixSize(context.Background(), bucket, "")
	require.NoError(t, err)
	var totalSize int64
	for _, content := range files {
		totalSize += int64(len(content))
	}
	assert.Equal(t, totalSize, size)
}

func TestCalculatePrefixSize_NonExistentBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)

	// Non-existent bucket should return an error
	_, err := store.CalculatePrefixSize(context.Background(), "non-existent", "folder/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket not found")
}

func TestCalculatePrefixSize_IncludesTombstoned(t *testing.T) {
	store := newTestStoreWithCAS(t)
	bucket := "test-bucket"

	require.NoError(t, store.CreateBucket(context.Background(), bucket, "owner-id", 2, nil))

	// Create and delete an object (tombstone it)
	content := []byte("test content")
	_, err := store.PutObject(context.Background(), bucket, "folder/file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Calculate initial size
	sizeBeforeDelete, err := store.CalculatePrefixSize(context.Background(), bucket, "folder/")
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), sizeBeforeDelete)

	// Delete (tombstone) the object
	require.NoError(t, store.DeleteObject(context.Background(), bucket, "folder/file.txt"))

	// Size should still include the tombstoned object until it's purged
	sizeAfterDelete, err := store.CalculatePrefixSize(context.Background(), bucket, "folder/")
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), sizeAfterDelete, "tombstoned objects should still count toward size")
}

// TestConcurrent_PurgeObjectNoDeadlock verifies that PurgeObject doesn't deadlock
// when called concurrently with read operations. Before the fix, PurgeObject held
// s.mu.Lock() while calling isChunkReferencedGlobally() which did a full filesystem
// scan, blocking all other S3 operations and causing dashboard hangs.
func TestConcurrent_PurgeObjectNoDeadlock(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create multiple objects to purge
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("file-%d.txt", i)
		content := []byte(fmt.Sprintf("content-%d-%s", i, strings.Repeat("x", 500)))
		_, err := store.PutObject(context.Background(), "test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
		// Tombstone so it can be purged
		require.NoError(t, store.DeleteObject(context.Background(), "test-bucket", key))
	}

	// Use timeout context to detect deadlock
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Concurrent purges
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("file-%d.txt", id)
			_ = store.PurgeObject(ctx, "test-bucket", key)
		}(i)
	}

	// Concurrent reads (these should NOT be blocked by purge)
	for i := 5; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// ListObjects and HeadObject require s.mu.RLock()
			// They would deadlock if PurgeObject holds s.mu.Lock() too long
			for j := 0; j < 5; j++ {
				_, _, _, _ = store.ListObjects(ctx, "test-bucket", "", "", 100)
				_, _ = store.HeadObject(ctx, "test-bucket", fmt.Sprintf("file-%d.txt", id))
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no deadlock
	case <-ctx.Done():
		t.Fatal("deadlock detected: concurrent PurgeObject + reads timed out after 10s")
	}
}

// TestConcurrent_GCAndPurgeNoDeadlock verifies that GC and PurgeObject running
// concurrently with reads don't deadlock. This simulates the production scenario
// where the hourly GC ticker fires while HTTP handlers are serving requests.
func TestConcurrent_GCAndPurgeNoDeadlock(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create objects with multiple versions
	for i := 0; i < 5; i++ {
		for v := 0; v < 3; v++ {
			content := []byte(fmt.Sprintf("v%d-%s", v, strings.Repeat("x", 500+i*100)))
			_, err := store.PutObject(context.Background(), "test-bucket", fmt.Sprintf("obj-%d.txt", i), bytes.NewReader(content), int64(len(content)), "text/plain", nil)
			require.NoError(t, err)
		}
	}

	// Tombstone some for purging
	for i := 0; i < 3; i++ {
		require.NoError(t, store.DeleteObject(context.Background(), "test-bucket", fmt.Sprintf("obj-%d.txt", i)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Run GC concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			store.RunGarbageCollection(ctx)
		}
	}()

	// Run purges concurrently
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = store.PurgeObject(ctx, "test-bucket", fmt.Sprintf("obj-%d.txt", id))
		}(i)
	}

	// Concurrent reads (simulate dashboard API calls)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _, _, _ = store.ListObjects(ctx, "test-bucket", "", "", 100)
			_, _ = store.ListBuckets(ctx)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no deadlock
	case <-ctx.Done():
		t.Fatal("deadlock detected: concurrent GC + Purge + reads timed out after 15s")
	}
}

// TestConcurrent_GCDoesNotBlockReads verifies that GC's filesystem scan
// (buildChunkReferenceSet) does not hold s.mu, allowing concurrent ListObjects
// and PutObject to proceed without blocking. This is a regression test for the
// S3 explorer freeze after ~1 hour (GC holding RLock during full walk).
func TestConcurrent_GCDoesNotBlockReads(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create objects to make GC scan take some time
	for i := 0; i < 10; i++ {
		content := []byte(fmt.Sprintf("content-%d-%s", i, strings.Repeat("x", 1000)))
		_, err := store.PutObject(context.Background(), "test-bucket", fmt.Sprintf("obj-%d.txt", i), bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Run GC repeatedly (this does the expensive filesystem scan)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			store.RunGarbageCollection(ctx)
		}
	}()

	// Concurrent writes — these acquire s.mu.Lock() and would deadlock
	// if buildChunkReferenceSet held s.mu.RLock() (write-preferring RWMutex
	// blocks new RLock acquires once a Lock waiter exists)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			content := []byte(fmt.Sprintf("concurrent-write-%d", i))
			_, _ = store.PutObject(ctx, "test-bucket", "concurrent.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		}
	}()

	// Concurrent reads — simulate dashboard API calls that were freezing
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_, _, _, _ = store.ListObjects(ctx, "test-bucket", "", "", 100)
			_, _ = store.ListBuckets(ctx)
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success — no lock contention blocking reads during GC
	case <-ctx.Done():
		t.Fatal("lock contention detected: concurrent GC + writes + reads timed out after 10s")
	}
}

// TestBuildChunkReferenceSet_Cancellation verifies that buildChunkReferenceSet
// respects context cancellation and returns early.
func TestBuildChunkReferenceSet_Cancellation(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create some objects
	for i := 0; i < 5; i++ {
		content := []byte(fmt.Sprintf("content-%d", i))
		_, err := store.PutObject(context.Background(), "test-bucket", fmt.Sprintf("obj-%d.txt", i), bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	// Cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	refs := store.buildChunkReferenceSet(ctx)
	// Should return without error (may have partial or empty results)
	assert.NotNil(t, refs)
}

// TestBuildChunkReferenceSet_SubdirectoryMeta verifies that buildChunkReferenceSet
// finds chunks referenced by metadata files in subdirectories (e.g., system bucket
// stores metadata in meta/auth/, meta/dns/, meta/filter/).
func TestBuildChunkReferenceSet_SubdirectoryMeta(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create a regular object (top-level meta)
	topContent := []byte("top-level-object")
	_, err := store.PutObject(context.Background(), "test-bucket", "top.txt", bytes.NewReader(topContent), int64(len(topContent)), "text/plain", nil)
	require.NoError(t, err)

	// Manually create a metadata file in a subdirectory to simulate system bucket layout
	subDir := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", "auth")
	require.NoError(t, os.MkdirAll(subDir, 0o700))

	subMeta := ObjectMeta{
		Key:          "auth/users.json",
		Chunks:       []string{"subdir-chunk-hash-abc123"},
		Size:         100,
		ContentType:  "application/json",
		LastModified: time.Now().UTC(),
		VersionID:    "v1",
	}
	data, err := json.Marshal(subMeta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "users.json"), data, 0o600))

	// Build reference set
	refs := store.buildChunkReferenceSet(context.Background())

	// Should find the chunk from the subdirectory metadata
	_, found := refs["subdir-chunk-hash-abc123"]
	assert.True(t, found, "buildChunkReferenceSet should find chunks in meta subdirectories")

	// Should also find chunks from top-level metadata
	topMeta, err := store.HeadObject(context.Background(), "test-bucket", "top.txt")
	require.NoError(t, err)
	for _, chunk := range topMeta.Chunks {
		_, found := refs[chunk]
		assert.True(t, found, "buildChunkReferenceSet should find chunks from top-level metadata")
	}
}

// TestIsChunkReferencedGlobally_SubdirectoryMeta verifies that isChunkReferencedGlobally
// correctly finds chunk references in subdirectory metadata files.
func TestIsChunkReferencedGlobally_SubdirectoryMeta(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create subdirectory metadata with a known chunk hash
	subDir := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", "dns")
	require.NoError(t, os.MkdirAll(subDir, 0o700))

	subMeta := ObjectMeta{
		Key:          "dns/records.json",
		Chunks:       []string{"dns-chunk-hash-xyz789"},
		Size:         50,
		ContentType:  "application/json",
		LastModified: time.Now().UTC(),
		VersionID:    "v1",
	}
	data, err := json.Marshal(subMeta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "records.json"), data, 0o600))

	// Should find the chunk in subdirectory
	ctx := context.Background()
	assert.True(t, store.isChunkReferencedGlobally(ctx, "dns-chunk-hash-xyz789"),
		"isChunkReferencedGlobally should find chunks referenced in meta subdirectories")

	// Should NOT find a non-existent chunk
	assert.False(t, store.isChunkReferencedGlobally(ctx, "nonexistent-chunk-hash"),
		"isChunkReferencedGlobally should return false for unreferenced chunks")
}

// TestPruneAllExpiredVersionsSimple_SubdirectoryMeta verifies that
// pruneAllExpiredVersionsSimple scans objects in subdirectories too.
func TestPruneAllExpiredVersionsSimple_SubdirectoryMeta(t *testing.T) {
	store := newTestStoreWithCAS(t)
	store.versionRetentionDays = 1 // Expire after 1 day
	require.NoError(t, store.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil))

	// Create an object in a subdirectory (key="filter/rules", file="meta/filter/rules.json")
	subDir := filepath.Join(store.dataDir, "buckets", "test-bucket", "meta", "filter")
	require.NoError(t, os.MkdirAll(subDir, 0o700))

	subMeta := ObjectMeta{
		Key:          "filter/rules",
		Size:         100,
		ContentType:  "application/json",
		LastModified: time.Now().UTC(),
		VersionID:    "current",
	}
	data, err := json.Marshal(subMeta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "rules.json"), data, 0o600))

	// Create an expired version for this subdirectory object
	// Key "filter/rules" → version dir at "versions/filter/rules/"
	versionDir := filepath.Join(store.dataDir, "buckets", "test-bucket", "versions", "filter", "rules")
	require.NoError(t, os.MkdirAll(versionDir, 0o700))

	expiredVersion := ObjectMeta{
		Key:          "filter/rules",
		Size:         80,
		ContentType:  "application/json",
		LastModified: time.Now().UTC().AddDate(0, 0, -30), // 30 days old
		VersionID:    "old-version",
	}
	vData, err := json.Marshal(expiredVersion)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "old-version.json"), vData, 0o600))

	// Run GC pruning
	stats := GCStats{}
	store.pruneAllExpiredVersionsSimple(context.Background(), &stats)

	// Should have scanned the subdirectory object
	assert.Greater(t, stats.ObjectsScanned, 0, "pruneAllExpiredVersionsSimple should scan objects in subdirectories")
	assert.Greater(t, stats.VersionsPruned, 0, "pruneAllExpiredVersionsSimple should prune expired versions in subdirectories")

	// The expired version file should be deleted
	_, err = os.Stat(filepath.Join(versionDir, "old-version.json"))
	assert.True(t, os.IsNotExist(err), "expired version should be deleted")
}

// ==== ImportObjectMeta Tests ====

func TestImportObjectMeta_NewBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Import metadata for a bucket that doesn't exist yet
	meta := ObjectMeta{
		Key:         "report.pdf",
		Size:        5000,
		ContentType: "application/pdf",
		Chunks:      []string{"hash1", "hash2"},
	}
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)

	err = store.ImportObjectMeta(ctx, "newbucket", "report.pdf", metaJSON, "alice")
	require.NoError(t, err)

	// Verify the metadata is readable
	got, err := store.GetObjectMeta(ctx, "newbucket", "report.pdf")
	require.NoError(t, err)
	assert.Equal(t, "report.pdf", got.Key)
	assert.Equal(t, int64(5000), got.Size)
	assert.Equal(t, "application/pdf", got.ContentType)
	assert.Equal(t, []string{"hash1", "hash2"}, got.Chunks)

	// Verify bucket was created with correct owner
	buckets, err := store.ListBuckets(ctx)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	assert.Equal(t, "alice", buckets[0].Owner)
}

func TestImportObjectMeta_ExistingBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Create bucket first
	require.NoError(t, store.CreateBucket(ctx, "mybucket", "alice", 1, nil))

	meta := ObjectMeta{
		Key:         "doc.txt",
		Size:        100,
		ContentType: "text/plain",
		Chunks:      []string{"abc"},
	}
	metaJSON, err := json.Marshal(meta)
	require.NoError(t, err)

	err = store.ImportObjectMeta(ctx, "mybucket", "doc.txt", metaJSON, "")
	require.NoError(t, err)

	got, err := store.GetObjectMeta(ctx, "mybucket", "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, "doc.txt", got.Key)
}

func TestImportObjectMeta_InvalidJSON(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	err := store.ImportObjectMeta(ctx, "bucket", "key", []byte("not json"), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid object meta JSON")
}

func TestImportObjectMeta_InvalidBucketName(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	meta := ObjectMeta{Key: "test"}
	metaJSON, _ := json.Marshal(meta)

	err := store.ImportObjectMeta(ctx, "../escape", "test", metaJSON, "")
	assert.Error(t, err)
}

func TestImportObjectMeta_ArchivesVersion(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	require.NoError(t, store.CreateBucket(ctx, "mybucket", "alice", 1, nil))

	// Import first version
	meta1 := ObjectMeta{
		Key:         "doc.txt",
		Size:        100,
		ContentType: "text/plain",
		Chunks:      []string{"hash1"},
	}
	metaJSON1, err := json.Marshal(meta1)
	require.NoError(t, err)
	require.NoError(t, store.ImportObjectMeta(ctx, "mybucket", "doc.txt", metaJSON1, ""))

	// Import second version (overwrites first)
	meta2 := ObjectMeta{
		Key:         "doc.txt",
		Size:        200,
		ContentType: "text/plain",
		Chunks:      []string{"hash2"},
	}
	metaJSON2, err := json.Marshal(meta2)
	require.NoError(t, err)
	require.NoError(t, store.ImportObjectMeta(ctx, "mybucket", "doc.txt", metaJSON2, ""))

	// Verify current version is meta2
	got, err := store.GetObjectMeta(ctx, "mybucket", "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(200), got.Size)

	// Verify a version was archived (versions directory has a file)
	versionsDir := filepath.Join(store.DataDir(), "buckets", "mybucket", "versions", "doc.txt")
	entries, err := os.ReadDir(versionsDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "should have 1 archived version")
}

// ==== DeleteChunk Tests ====

func TestDeleteChunk(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Write a chunk first
	data := []byte("test chunk data")
	hash := ContentHash(data)
	err := store.WriteChunkDirect(ctx, hash, data)
	require.NoError(t, err)

	// Verify chunk exists
	readData, err := store.ReadChunk(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, data, readData)

	// Delete the chunk
	err = store.DeleteChunk(ctx, hash)
	require.NoError(t, err)

	// Verify chunk is gone
	_, err = store.ReadChunk(ctx, hash)
	assert.Error(t, err)
}

func TestDeleteChunk_NonExistent(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Deleting a non-existent chunk should not error (idempotent behavior).
	// This is important because cleanup may run after a chunk was already
	// garbage collected or deleted by another process.
	err := store.DeleteChunk(ctx, "nonexistent-hash")
	assert.NoError(t, err)
}

func TestSyncedWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Write initial content
	original := []byte(`{"name":"original"}`)
	err := syncedWriteFile(path, original, 0644)
	require.NoError(t, err)

	// Verify initial content
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, data)

	// Overwrite with new content
	updated := []byte(`{"name":"updated","extra":"field"}`)
	err = syncedWriteFile(path, updated, 0644)
	require.NoError(t, err)

	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, updated, data)

	// Verify no temp files left behind
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temp files should remain after successful write")
}

func TestSyncedWriteFileAtomic_ReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce read-only directory permissions via os.Chmod")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")

	// Write initial content
	original := []byte(`{"name":"original"}`)
	err := syncedWriteFile(path, original, 0644)
	require.NoError(t, err)

	// Make directory read-only to simulate write failure
	require.NoError(t, os.Chmod(dir, 0555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })

	// Attempt to overwrite — should fail, but original file should remain
	err = syncedWriteFile(path, []byte(`{"name":"corrupted"}`), 0644)
	assert.Error(t, err)

	// Restore permissions and verify original content is intact
	require.NoError(t, os.Chmod(dir, 0755))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, data, "original file should be untouched after failed write")
}

func TestGetBucketMeta_EmptyMetadata(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Create a valid bucket first
	err := store.CreateBucket(ctx, "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	// Verify it exists
	_, err = store.HeadBucket(ctx, "test-bucket")
	require.NoError(t, err)

	// Overwrite _meta.json with empty content (simulates disk-full truncation)
	metaPath := filepath.Join(store.DataDir(), "buckets", "test-bucket", "_meta.json")
	require.NoError(t, os.WriteFile(metaPath, []byte{}, 0644))

	// Should return ErrBucketNotFound, not a generic error
	_, err = store.HeadBucket(ctx, "test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestGetBucketMeta_CorruptedJSON(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	// Create a valid bucket first
	err := store.CreateBucket(ctx, "test-bucket", "alice", 2, nil)
	require.NoError(t, err)

	// Overwrite _meta.json with invalid JSON
	metaPath := filepath.Join(store.DataDir(), "buckets", "test-bucket", "_meta.json")
	require.NoError(t, os.WriteFile(metaPath, []byte(`{invalid json`), 0644))

	// Should return ErrBucketNotFound, not a generic unmarshal error
	_, err = store.HeadBucket(ctx, "test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStore_VolumeStats(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, nil)
	require.NoError(t, err)

	total, used, available, err := store.VolumeStats()
	require.NoError(t, err)

	assert.Greater(t, total, int64(0), "total should be positive")
	assert.GreaterOrEqual(t, used, int64(0), "used should be non-negative")
	assert.Greater(t, available, int64(0), "available should be positive")
}

func TestGetCASStats_LogicalBytesUsesChunkMetadata(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	require.NoError(t, store.CreateBucket(ctx, "test-bucket", "alice", 2, nil))

	// Put a single unique object — ratio should be 1.0
	content := []byte("unique content for stats test")
	_, err := store.PutObject(ctx, "test-bucket", "file1.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	stats := store.GetCASStats()

	assert.Equal(t, 1, stats.ObjectCount, "should have 1 object")
	assert.Greater(t, stats.ChunkBytes, int64(0), "should have chunk bytes on disk")
	assert.Greater(t, stats.LogicalBytes, int64(0), "should have logical bytes")
	// For unique content, LogicalBytes should equal ChunkBytes (both use on-disk sizes)
	assert.Equal(t, stats.ChunkBytes, stats.LogicalBytes,
		"unique content: logical bytes (%d) should equal chunk bytes (%d) — ratio 1.0",
		stats.LogicalBytes, stats.ChunkBytes)

	// Put a second identical object (dedup) — LogicalBytes should be 2x ChunkBytes
	_, err = store.PutObject(ctx, "test-bucket", "file2.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	stats = store.GetCASStats()

	assert.Equal(t, 2, stats.ObjectCount, "should have 2 objects")
	assert.Equal(t, stats.LogicalBytes, 2*stats.ChunkBytes,
		"deduplicated: logical bytes (%d) should be 2x chunk bytes (%d)",
		stats.LogicalBytes, stats.ChunkBytes)
}

func TestGetCASStats_LegacyObjectsUseChunkLookup(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	require.NoError(t, store.CreateBucket(ctx, "test-bucket", "alice", 2, nil))

	// Put an object normally
	content := []byte("legacy test content")
	_, err := store.PutObject(ctx, "test-bucket", "legacy.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Simulate a legacy object by stripping ChunkMetadata from the stored meta
	// The Chunks field is preserved, so on-disk lookup still works
	metaDir := filepath.Join(store.DataDir(), "buckets", "test-bucket", "meta")
	err = filepath.Walk(metaDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var meta ObjectMeta
		if jsonErr := json.Unmarshal(data, &meta); jsonErr != nil {
			return jsonErr
		}
		meta.ChunkMetadata = nil // Simulate legacy object with no chunk metadata
		updated, jsonErr := json.Marshal(meta)
		if jsonErr != nil {
			return jsonErr
		}
		return os.WriteFile(path, updated, 0o644)
	})
	require.NoError(t, err)

	stats := store.GetCASStats()

	assert.Equal(t, 1, stats.ObjectCount)
	// Legacy objects still have Chunks list — LogicalBytes uses on-disk chunk sizes
	assert.Equal(t, stats.ChunkBytes, stats.LogicalBytes,
		"legacy objects should use on-disk chunk sizes via Chunks list lookup")
}

func TestGetCASStats_VersionCountAfterOverwrite(t *testing.T) {
	store := newTestStoreWithCAS(t)
	ctx := context.Background()

	require.NoError(t, store.CreateBucket(ctx, "test-bucket", "alice", 2, nil))

	// Write v1 of a file
	v1 := []byte("version one content for logical bytes test")
	_, err := store.PutObject(ctx, "test-bucket", "file.txt", bytes.NewReader(v1), int64(len(v1)), "text/plain", nil)
	require.NoError(t, err)

	statsAfterV1 := store.GetCASStats()
	assert.Equal(t, statsAfterV1.ChunkBytes, statsAfterV1.LogicalBytes,
		"after v1: logical should equal physical (unique content)")

	// Overwrite with v2 — v1 is archived, its chunks remain on disk
	v2 := []byte("version two with different content for test")
	_, err = store.PutObject(ctx, "test-bucket", "file.txt", bytes.NewReader(v2), int64(len(v2)), "text/plain", nil)
	require.NoError(t, err)

	statsAfterV2 := store.GetCASStats()
	assert.Equal(t, 1, statsAfterV2.VersionCount, "should have 1 archived version")
	// LogicalBytes only counts current objects (not version files) to avoid
	// expensive I/O on every metrics refresh. ChunkBytes may exceed LogicalBytes
	// while old version chunks await GC cleanup — this is expected and
	// self-corrects after each GC cycle.
	assert.Greater(t, statsAfterV2.ChunkBytes, statsAfterV2.LogicalBytes,
		"after v2: physical > logical because version chunks await GC")
}
