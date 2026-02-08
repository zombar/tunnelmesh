package s3

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	// Verify bucket exists
	meta, err := store.HeadBucket("test-bucket")
	require.NoError(t, err)
	assert.Equal(t, "test-bucket", meta.Name)
	assert.Equal(t, "alice", meta.Owner)
	assert.False(t, meta.CreatedAt.IsZero())
}

func TestStoreCreateBucketAlreadyExists(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	// Try to create again
	err = store.CreateBucket("test-bucket", "bob")
	assert.ErrorIs(t, err, ErrBucketExists)
}

func TestStoreDeleteBucket(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	err = store.DeleteBucket("test-bucket")
	require.NoError(t, err)

	// Verify bucket is gone
	_, err = store.HeadBucket("test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.DeleteBucket("nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotEmpty(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	// Add an object
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("hello")), 5, "text/plain", nil)
	require.NoError(t, err)

	// Try to delete bucket
	err = store.DeleteBucket("test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotEmpty)
}

func TestStoreHeadBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, err := store.HeadBucket("nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListBuckets(t *testing.T) {
	store := newTestStoreWithCAS(t)

	// Empty initially
	buckets, err := store.ListBuckets()
	require.NoError(t, err)
	assert.Empty(t, buckets)

	// Create some buckets
	require.NoError(t, store.CreateBucket("bucket-a", "alice"))
	require.NoError(t, store.CreateBucket("bucket-b", "bob"))

	buckets, err = store.ListBuckets()
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("hello world")
	meta, err := store.PutObject("test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	assert.Equal(t, "greeting.txt", meta.Key)
	assert.Equal(t, int64(len(content)), meta.Size)
	assert.Equal(t, "text/plain", meta.ContentType)
	assert.NotEmpty(t, meta.ETag)
	assert.False(t, meta.LastModified.IsZero())
}

func TestStorePutObject_SetsExpiry(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Set default object expiry to 25 years (9125 days)
	store.SetDefaultObjectExpiryDays(9125)

	content := []byte("hello world")
	meta, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	require.NotNil(t, meta.Expires, "Expires should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 9125)
	// Allow 1 minute tolerance for test timing
	assert.WithinDuration(t, expectedExpiry, *meta.Expires, time.Minute)
}

func TestStorePutObject_NoExpiryWhenNotConfigured(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Don't set expiry - objects should have no expiry
	content := []byte("hello world")
	meta, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	assert.Nil(t, meta.Expires, "Expires should not be set when not configured")
}

func TestStorePutObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, err := store.PutObject("nonexistent", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStorePutObjectNestedKey(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("nested content")
	meta, err := store.PutObject("test-bucket", "path/to/file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)
	assert.Equal(t, "path/to/file.txt", meta.Key)

	// Verify we can retrieve it
	reader, objMeta, err := store.GetObject("test-bucket", "path/to/file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, "path/to/file.txt", objMeta.Key)
	data, _ := io.ReadAll(reader)
	assert.Equal(t, content, data)
}

func TestStorePutObjectWithMetadata(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	userMeta := map[string]string{
		"x-amz-meta-author":  "alice",
		"x-amz-meta-version": "1.0",
	}
	meta, err := store.PutObject("test-bucket", "doc.txt", bytes.NewReader([]byte("data")), 4, "text/plain", userMeta)
	require.NoError(t, err)

	assert.Equal(t, userMeta, meta.Metadata)

	// Verify persisted
	objMeta, err := store.HeadObject("test-bucket", "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, userMeta, objMeta.Metadata)
}

func TestStoreGetObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("hello world")
	_, err := store.PutObject("test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	reader, meta, err := store.GetObject("test-bucket", "greeting.txt")
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, _, err := store.GetObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreGetObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, _, err := store.GetObject("nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreHeadObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("hello world")
	_, err := store.PutObject("test-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	meta, err := store.HeadObject("test-bucket", "greeting.txt")
	require.NoError(t, err)

	assert.Equal(t, "greeting.txt", meta.Key)
	assert.Equal(t, int64(len(content)), meta.Size)
	assert.Equal(t, "text/plain", meta.ContentType)
}

func TestStoreHeadObjectNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.HeadObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is tombstoned (soft-deleted), not removed
	meta, err := store.HeadObject("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "object should be tombstoned")
}

func TestStorePurgeObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Purge actually removes the object
	err = store.PurgeObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is gone
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	err := store.DeleteObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	err := store.DeleteObject("nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjects(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Add some objects
	objects := []string{"a.txt", "b.txt", "c.txt"}
	for _, key := range objects {
		_, err := store.PutObject("test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, _, _, err := store.ListObjects("test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestStoreListObjectsWithPrefix(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Add some objects with different prefixes
	objects := []string{"docs/a.txt", "docs/b.txt", "images/c.png"}
	for _, key := range objects {
		_, err := store.PutObject("test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, _, _, err := store.ListObjects("test-bucket", "docs/", "", 0)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	for _, obj := range list {
		assert.True(t, strings.HasPrefix(obj.Key, "docs/"))
	}
}

func TestStoreListObjectsWithMaxKeys(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Add 5 objects
	for i := 0; i < 5; i++ {
		key := string(rune('a'+i)) + ".txt"
		_, err := store.PutObject("test-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	list, isTruncated, _, err := store.ListObjects("test-bucket", "", "", 2)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.True(t, isTruncated)
}

func TestStoreListObjectsBucketNotFound(t *testing.T) {
	store := newTestStoreWithCAS(t)

	_, _, _, err := store.ListObjects("nonexistent", "", "", 0)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjectsEmpty(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	list, isTruncated, _, err := store.ListObjects("test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.False(t, isTruncated)
}

func TestStoreOverwriteObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Write initial version
	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("version 1")), 9, "text/plain", nil)
	require.NoError(t, err)

	// Overwrite
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("version 2 - longer")), 18, "text/plain", nil)
	require.NoError(t, err)

	// Read back
	reader, meta, err := store.GetObject("test-bucket", "file.txt")
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Put object should update quota
	content := []byte("hello world")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	stats := store.QuotaStats()
	require.NotNil(t, stats)
	assert.Equal(t, int64(len(content)), stats.UsedBytes)
	assert.Equal(t, int64(len(content)), stats.PerBucket["test-bucket"])

	// Tombstone (soft delete) should NOT release quota yet
	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	stats = store.QuotaStats()
	assert.Equal(t, int64(len(content)), stats.UsedBytes, "tombstoned objects still use quota")

	// Purge should release quota
	err = store.PurgeObject("test-bucket", "file.txt")
	require.NoError(t, err)

	stats = store.QuotaStats()
	assert.Equal(t, int64(0), stats.UsedBytes, "purged objects release quota")
}

// =========================================================================
// Object Lifecycle Tests
// =========================================================================

func TestObjectLifecycle_ExpirySetting(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Set object expiry to 30 days
	store.SetDefaultObjectExpiryDays(30)

	content := []byte("test content")
	meta, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Verify expiry is set
	require.NotNil(t, meta.Expires, "Expires should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 30)
	assert.WithinDuration(t, expectedExpiry, *meta.Expires, time.Minute)
}

func TestObjectLifecycle_TombstonePreservesContent(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("important data")
	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the object
	err = store.TombstoneObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is tombstoned
	meta, err := store.HeadObject("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned())

	// Content should still be readable
	reader, readMeta, err := store.GetObject("test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	assert.True(t, readMeta.IsTombstoned())

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestObjectLifecycle_TombstonedObjectInList(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create two objects
	_, err := store.PutObject("test-bucket", "live.txt", bytes.NewReader([]byte("live")), 4, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject("test-bucket", "dead.txt", bytes.NewReader([]byte("dead")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone one
	err = store.TombstoneObject("test-bucket", "dead.txt")
	require.NoError(t, err)

	// Both should appear in list
	objects, _, _, err := store.ListObjects("test-bucket", "", "", 0)
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	content := []byte("to be purged")
	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone first
	err = store.TombstoneObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Then purge
	err = store.PurgeObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is completely gone
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// Should not appear in list
	objects, _, _, err := store.ListObjects("test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, objects)
}

func TestObjectLifecycle_DoubleTombstoneIsNoop(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone twice - should not error
	err = store.TombstoneObject("test-bucket", "file.txt")
	require.NoError(t, err)

	meta1, _ := store.HeadObject("test-bucket", "file.txt")
	tombstonedAt1 := meta1.TombstonedAt

	err = store.TombstoneObject("test-bucket", "file.txt")
	require.NoError(t, err)

	meta2, _ := store.HeadObject("test-bucket", "file.txt")
	// Timestamp should not change on second tombstone
	assert.Equal(t, tombstonedAt1, meta2.TombstonedAt)
}

func TestObjectLifecycle_PurgeTombstonedObjects(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create and tombstone some objects
	_, err := store.PutObject("test-bucket", "old.txt", bytes.NewReader([]byte("old")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject("test-bucket", "new.txt", bytes.NewReader([]byte("new")), 3, "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject("test-bucket", "live.txt", bytes.NewReader([]byte("live")), 4, "text/plain", nil)
	require.NoError(t, err)

	// Tombstone old.txt and new.txt
	err = store.TombstoneObject("test-bucket", "old.txt")
	require.NoError(t, err)
	err = store.TombstoneObject("test-bucket", "new.txt")
	require.NoError(t, err)

	// Manually backdate old.txt tombstone to 100 days ago
	oldMeta, _ := store.HeadObject("test-bucket", "old.txt")
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
	purged := store.PurgeTombstonedObjects()

	// Should have purged 1 object (old.txt is > 90 days old)
	assert.Equal(t, 1, purged)

	// old.txt should be gone
	_, err = store.HeadObject("test-bucket", "old.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// new.txt should still exist (tombstoned < 90 days ago)
	newMeta, err := store.HeadObject("test-bucket", "new.txt")
	require.NoError(t, err)
	assert.True(t, newMeta.IsTombstoned())

	// live.txt should still exist (not tombstoned)
	liveMeta, err := store.HeadObject("test-bucket", "live.txt")
	require.NoError(t, err)
	assert.False(t, liveMeta.IsTombstoned())
}

func TestObjectLifecycle_PurgeTombstonedObjectsDisabled(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)
	err = store.TombstoneObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Retention days = 0 means disabled
	store.SetTombstoneRetentionDays(0)
	purged := store.PurgeTombstonedObjects()
	assert.Equal(t, 0, purged)

	// Object should still exist
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.NoError(t, err)
}

func TestBucketTombstone(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	// Create bucket with objects
	err = store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	content := []byte("test content")
	_, err = store.PutObject("test-bucket", "file1.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)
	_, err = store.PutObject("test-bucket", "file2.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the bucket
	err = store.TombstoneBucket("test-bucket")
	require.NoError(t, err)

	// Bucket should be tombstoned
	bucketMeta, err := store.HeadBucket("test-bucket")
	require.NoError(t, err)
	assert.True(t, bucketMeta.IsTombstoned())

	// Objects should appear tombstoned via HeadObject
	obj1, err := store.HeadObject("test-bucket", "file1.txt")
	require.NoError(t, err)
	assert.True(t, obj1.IsTombstoned(), "object should appear tombstoned when bucket is tombstoned")

	// Objects should appear tombstoned in ListObjects
	objects, _, _, err := store.ListObjects("test-bucket", "", "", 100)
	require.NoError(t, err)
	assert.Len(t, objects, 2)
	for _, obj := range objects {
		assert.True(t, obj.IsTombstoned(), "all objects should appear tombstoned")
	}

	// Untombstone the bucket
	err = store.UntombstoneBucket("test-bucket")
	require.NoError(t, err)

	// Bucket should no longer be tombstoned
	bucketMeta, err = store.HeadBucket("test-bucket")
	require.NoError(t, err)
	assert.False(t, bucketMeta.IsTombstoned())

	// Objects should no longer appear tombstoned
	obj1, err = store.HeadObject("test-bucket", "file1.txt")
	require.NoError(t, err)
	assert.False(t, obj1.IsTombstoned(), "object should not appear tombstoned after bucket untombstoned")
}

func TestDeleteTwiceToPurge(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	err = store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	content := []byte("delete me")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// First delete: should tombstone
	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	meta, err := store.HeadObject("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "first delete should tombstone")

	// Second delete: should purge
	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.Equal(t, ErrObjectNotFound, err, "second delete should purge")
}

func TestDeleteTwiceToPurge_BucketTombstoned(t *testing.T) {
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)

	err = store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	content := []byte("delete me")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Tombstone the bucket (simulates file share deletion)
	err = store.TombstoneBucket("test-bucket")
	require.NoError(t, err)

	// Object appears tombstoned via bucket
	meta, err := store.HeadObject("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned())

	// Delete should purge (since already tombstoned via bucket)
	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject("test-bucket", "file.txt")
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
			err := store.CreateBucket(name, "attacker")
			assert.Error(t, err, "bucket name %q should be rejected", name)
		})
	}
}

func TestPathTraversal_ObjectKey(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

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
			_, err := store.PutObject("test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
			assert.Error(t, err, "object key %q should be rejected", key)
		})
	}
}

func TestPathTraversal_ValidPaths(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

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
			_, err := store.PutObject("test-bucket", key, bytes.NewReader(content), int64(len(content)), "text/plain", nil)
			assert.NoError(t, err, "object key %q should be allowed", key)
		})
	}
}

func TestPathTraversal_ValidNestedPaths(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Test nested paths that should work
	content := []byte("valid content")

	// First create a folder, then a file in it
	_, err := store.PutObject("test-bucket", "dotfolder/.hidden", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	assert.NoError(t, err, "nested .hidden file should be allowed")

	_, err = store.PutObject("test-bucket", ".hiddendir/file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
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
	var reassembled []byte
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
	hash, err := cas.WriteChunk(data)
	require.NoError(t, err)
	assert.NotEmpty(t, hash)

	// Read it back
	retrieved, err := cas.ReadChunk(hash)
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
	hash1, err := cas.WriteChunk(data)
	require.NoError(t, err)

	hash2, err := cas.WriteChunk(data)
	require.NoError(t, err)

	// Should return same hash (dedup)
	assert.Equal(t, hash1, hash2)

	// Should only have one chunk file
	totalSize, err := cas.TotalSize()
	require.NoError(t, err)

	// Write once more to verify it doesn't increase
	_, err = cas.WriteChunk(data)
	require.NoError(t, err)

	totalSize2, err := cas.TotalSize()
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
	hash1, err := cas.WriteChunk(data)
	require.NoError(t, err)

	// Get the encrypted size
	size1, err := cas.ChunkSize(hash1)
	require.NoError(t, err)

	// Delete and rewrite
	err = cas.DeleteChunk(hash1)
	require.NoError(t, err)

	hash2, err := cas.WriteChunk(data)
	require.NoError(t, err)

	size2, err := cas.ChunkSize(hash2)
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

	_, err = cas.ReadChunk("nonexistent")
	assert.Error(t, err)
}

func TestCAS_DeleteChunk(t *testing.T) {
	tmpDir := t.TempDir()
	masterKey := [32]byte{}

	cas, err := NewCAS(tmpDir, masterKey)
	require.NoError(t, err)

	data := []byte("delete me")
	hash, err := cas.WriteChunk(data)
	require.NoError(t, err)

	assert.True(t, cas.ChunkExists(hash))

	err = cas.DeleteChunk(hash)
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// First write
	content1 := []byte("version 1")
	meta1, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, meta1.VersionID)

	// Second write (should create version)
	content2 := []byte("version 2")
	meta2, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, meta2.VersionID)
	assert.NotEqual(t, meta1.VersionID, meta2.VersionID)

	// List versions
	versions, err := store.ListVersions("test-bucket", "file.txt")
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create multiple versions
	for i := 1; i <= 5; i++ {
		content := []byte(strings.Repeat("x", i*100))
		_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	versions, err := store.ListVersions("test-bucket", "file.txt")
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
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create versions
	content1 := []byte("original content")
	meta1, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)

	content2 := []byte("updated content")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Get old version
	reader, meta, err := store.GetObjectVersion("test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content1, data)
	assert.Equal(t, meta1.VersionID, meta.VersionID)
}

func TestVersioning_RestoreVersion(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create versions
	content1 := []byte("original content")
	meta1, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content1), int64(len(content1)), "text/plain", nil)
	require.NoError(t, err)

	content2 := []byte("updated content - longer")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Restore old version
	restored, err := store.RestoreVersion("test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)
	assert.NotEqual(t, meta1.VersionID, restored.VersionID, "restore should create new version ID")

	// Current content should be original
	reader, _, err := store.GetObject("test-bucket", "file.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content1, data)

	// Should now have 3 versions
	versions, err := store.ListVersions("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 3)
}

func TestVersioning_RestoreSharesChunks(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create original version with specific content
	content := []byte("content to restore")
	meta1, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Overwrite
	content2 := []byte("different content")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Get storage size before restore
	sizeBefore, err := store.cas.TotalSize()
	require.NoError(t, err)

	// Restore - should reuse chunks, not duplicate
	_, err = store.RestoreVersion("test-bucket", "file.txt", meta1.VersionID)
	require.NoError(t, err)

	// Storage should not significantly increase (chunks are shared)
	sizeAfter, err := store.cas.TotalSize()
	require.NoError(t, err)

	// Size should be roughly the same (may vary slightly due to metadata)
	assert.InDelta(t, sizeBefore, sizeAfter, 100, "restore should share chunks, not duplicate")
}

func TestVersioning_DeleteCleansVersions(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create versions
	for i := 1; i <= 3; i++ {
		content := []byte(strings.Repeat("v", i*100))
		_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
		require.NoError(t, err)
	}

	// Verify versions exist
	versions, err := store.ListVersions("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 3)

	// Purge the object
	err = store.PurgeObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Object should be gone
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)

	// Versions should be gone
	versions, err = store.ListVersions("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Empty(t, versions)
}

func TestVersioning_RetentionPruning(t *testing.T) {
	store := newTestStoreWithCAS(t)
	store.SetVersionRetentionDays(30) // 30 day retention
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create an object
	content := []byte("original")
	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Create another version (triggers pruning check)
	content2 := []byte("updated")
	_, err = store.PutObject("test-bucket", "file.txt", bytes.NewReader(content2), int64(len(content2)), "text/plain", nil)
	require.NoError(t, err)

	// Both versions should exist (neither expired)
	versions, err := store.ListVersions("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}

func TestVersioning_NoVersionsForNewObject(t *testing.T) {
	store := newTestStoreWithCAS(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Create single object
	content := []byte("single version")
	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Should only have 1 version (current)
	versions, err := store.ListVersions("test-bucket", "file.txt")
	require.NoError(t, err)
	assert.Len(t, versions, 1)
	assert.True(t, versions[0].IsCurrent)
}
