package s3

import (
	"bytes"
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
	store := newTestStore(t)

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
	store := newTestStore(t)

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	// Try to create again
	err = store.CreateBucket("test-bucket", "bob")
	assert.ErrorIs(t, err, ErrBucketExists)
}

func TestStoreDeleteBucket(t *testing.T) {
	store := newTestStore(t)

	err := store.CreateBucket("test-bucket", "alice")
	require.NoError(t, err)

	err = store.DeleteBucket("test-bucket")
	require.NoError(t, err)

	// Verify bucket is gone
	_, err = store.HeadBucket("test-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotFound(t *testing.T) {
	store := newTestStore(t)

	err := store.DeleteBucket("nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreDeleteBucketNotEmpty(t *testing.T) {
	store := newTestStore(t)

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
	store := newTestStore(t)

	_, err := store.HeadBucket("nonexistent")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListBuckets(t *testing.T) {
	store := newTestStore(t)

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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	// Don't set expiry - objects should have no expiry
	content := []byte("hello world")
	meta, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	assert.Nil(t, meta.Expires, "Expires should not be set when not configured")
}

func TestStorePutObjectBucketNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.PutObject("nonexistent", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStorePutObjectNestedKey(t *testing.T) {
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, _, err := store.GetObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreGetObjectBucketNotFound(t *testing.T) {
	store := newTestStore(t)

	_, _, err := store.GetObject("nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreHeadObject(t *testing.T) {
	store := newTestStore(t)
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
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.HeadObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObject(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	_, err := store.PutObject("test-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	// Verify object is gone
	_, err = store.HeadObject("test-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectNotFound(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	err := store.DeleteObject("test-bucket", "nonexistent.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestStoreDeleteObjectBucketNotFound(t *testing.T) {
	store := newTestStore(t)

	err := store.DeleteObject("nonexistent", "file.txt")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjects(t *testing.T) {
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)
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
	store := newTestStore(t)

	_, _, _, err := store.ListObjects("nonexistent", "", "", 0)
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestStoreListObjectsEmpty(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateBucket("test-bucket", "alice"))

	list, isTruncated, _, err := store.ListObjects("test-bucket", "", "", 0)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.False(t, isTruncated)
}

func TestStoreOverwriteObject(t *testing.T) {
	store := newTestStore(t)
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

// newTestStore creates a store with a temporary directory for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	return store
}

func TestStoreWithQuota(t *testing.T) {
	tmpDir := t.TempDir()
	quota := NewQuotaManager(1 * 1024 * 1024 * 1024) // 1 Gi limit

	store, err := NewStore(tmpDir, quota)
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

	// Delete object should release quota
	err = store.DeleteObject("test-bucket", "file.txt")
	require.NoError(t, err)

	stats = store.QuotaStats()
	assert.Equal(t, int64(0), stats.UsedBytes)
}
