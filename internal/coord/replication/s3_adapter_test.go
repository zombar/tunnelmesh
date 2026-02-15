package replication

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

func TestNewS3StoreAdapter(t *testing.T) {
	adapter := NewS3StoreAdapter((*s3.Store)(nil))

	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}
}

func TestS3StoreAdapter_Get_Success(t *testing.T) {
	// Create a test store with data
	testStore := createTestS3Store(t)
	testBucket := "test-bucket"
	testKey := "test-key"
	testData := []byte("test object data")
	testMetadata := map[string]string{
		"x-amz-meta-author":  "alice",
		"x-amz-meta-version": "1.0",
	}

	// Put object into store
	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	_, err = testStore.PutObject(context.Background(), testBucket, testKey, bytes.NewReader(testData), int64(len(testData)), "text/plain", testMetadata)
	if err != nil {
		t.Fatalf("failed to put object: %v", err)
	}

	// Create adapter
	adapter := NewS3StoreAdapter(testStore)

	// Test Get
	ctx := context.Background()
	data, metadata, err := adapter.Get(ctx, testBucket, testKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(data) != string(testData) {
		t.Errorf("expected data %q, got %q", string(testData), string(data))
	}

	if metadata["x-amz-meta-author"] != "alice" {
		t.Errorf("expected author alice, got %s", metadata["x-amz-meta-author"])
	}

	if metadata["x-amz-meta-version"] != "1.0" {
		t.Errorf("expected version 1.0, got %s", metadata["x-amz-meta-version"])
	}
}

func TestS3StoreAdapter_Get_NotFound(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	err := testStore.CreateBucket(context.Background(), "test-bucket", "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	ctx := context.Background()
	_, _, err = adapter.Get(ctx, "test-bucket", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent object")
	}

	if !strings.Contains(err.Error(), "get object") {
		t.Errorf("expected 'get object' error, got: %v", err)
	}
}

func TestS3StoreAdapter_Get_BucketNotFound(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	ctx := context.Background()
	_, _, err := adapter.Get(ctx, "nonexistent-bucket", "test-key")
	if err == nil {
		t.Fatal("expected error for nonexistent bucket")
	}
}

func TestS3StoreAdapter_Get_NilMetadata(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"
	testKey := "test-key"
	testData := []byte("data")

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	// Put object without metadata
	_, err = testStore.PutObject(context.Background(), testBucket, testKey, bytes.NewReader(testData), int64(len(testData)), "text/plain", nil)
	if err != nil {
		t.Fatalf("failed to put object: %v", err)
	}

	ctx := context.Background()
	_, metadata, err := adapter.Get(ctx, testBucket, testKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return empty map, not nil
	if metadata == nil {
		t.Error("expected non-nil metadata map")
	}
}

func TestS3StoreAdapter_Put_Success(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"
	testKey := "test-key"
	testData := []byte("test content")
	testMetadata := map[string]string{
		"x-amz-meta-type": "text",
	}

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	ctx := context.Background()
	err = adapter.Put(ctx, testBucket, testKey, testData, "text/plain", testMetadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify object was stored
	reader, meta, err := testStore.GetObject(context.Background(), testBucket, testKey)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read data: %v", err)
	}

	if string(data) != string(testData) {
		t.Errorf("expected data %q, got %q", string(testData), string(data))
	}

	if meta.Metadata["x-amz-meta-type"] != "text" {
		t.Errorf("expected metadata type 'text', got %s", meta.Metadata["x-amz-meta-type"])
	}
}

func TestS3StoreAdapter_Put_BucketNotFound(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	ctx := context.Background()
	err := adapter.Put(ctx, "nonexistent", "test-key", []byte("data"), "text/plain", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent bucket")
	}

	if !strings.Contains(err.Error(), "put object") {
		t.Errorf("expected 'put object' error, got: %v", err)
	}
}

func TestS3StoreAdapter_Put_EmptyData(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"
	testKey := "empty-file"

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	ctx := context.Background()
	err = adapter.Put(ctx, testBucket, testKey, []byte{}, "text/plain", nil)
	if err != nil {
		t.Fatalf("unexpected error for empty data: %v", err)
	}

	// Verify empty object was stored
	reader, _, err := testStore.GetObject(context.Background(), testBucket, testKey)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read data: %v", err)
	}

	if len(data) != 0 {
		t.Errorf("expected empty data, got %d bytes", len(data))
	}
}

func TestS3StoreAdapter_List_Success(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	// Add some objects
	objects := []string{"file1.txt", "file2.txt", "file3.txt"}
	for _, key := range objects {
		_, err := testStore.PutObject(context.Background(), testBucket, key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		if err != nil {
			t.Fatalf("failed to put object %s: %v", key, err)
		}
	}

	ctx := context.Background()
	keys, err := adapter.List(ctx, testBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}

	// Verify all keys are present (order may vary)
	keyMap := make(map[string]bool)
	for _, key := range keys {
		keyMap[key] = true
	}

	for _, expected := range objects {
		if !keyMap[expected] {
			t.Errorf("expected key %q not found in list", expected)
		}
	}
}

func TestS3StoreAdapter_List_Empty(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	ctx := context.Background()
	keys, err := adapter.List(ctx, testBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(keys))
	}
}

func TestS3StoreAdapter_List_BucketNotFound(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	ctx := context.Background()
	_, err := adapter.List(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent bucket")
	}

	if !strings.Contains(err.Error(), "list objects") {
		t.Errorf("expected 'list objects' error, got: %v", err)
	}
}

func TestS3StoreAdapter_ListBuckets_Success(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	// Create some buckets
	buckets := []string{"bucket1", "bucket2", "bucket3"}
	for _, bucket := range buckets {
		err := testStore.CreateBucket(context.Background(), bucket, "alice", 2, nil)
		if err != nil {
			t.Fatalf("failed to create bucket %s: %v", bucket, err)
		}
	}

	ctx := context.Background()
	names, err := adapter.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(names) != 3 {
		t.Errorf("expected 3 buckets, got %d", len(names))
	}

	// Verify all bucket names are present (order may vary)
	bucketMap := make(map[string]bool)
	for _, name := range names {
		bucketMap[name] = true
	}

	for _, expected := range buckets {
		if !bucketMap[expected] {
			t.Errorf("expected bucket %q not found in list", expected)
		}
	}
}

func TestS3StoreAdapter_ListBuckets_Empty(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	ctx := context.Background()
	buckets, err := adapter.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(buckets) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(buckets))
	}
}

func TestS3StoreAdapter_Put_WithComplexMetadata(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"
	testKey := "test-key"
	testData := []byte("data")

	complexMetadata := map[string]string{
		"x-amz-meta-key1":  "value1",
		"x-amz-meta-key2":  "value2",
		"x-amz-meta-key3":  "value3",
		"x-amz-meta-empty": "",
	}

	err := testStore.CreateBucket(context.Background(), testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	ctx := context.Background()
	err = adapter.Put(ctx, testBucket, testKey, testData, "text/plain", complexMetadata)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify metadata was stored
	data, metadata, err := adapter.Get(ctx, testBucket, testKey)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	if string(data) != string(testData) {
		t.Errorf("expected data %q, got %q", string(testData), string(data))
	}

	// Check all metadata keys
	for k, expected := range complexMetadata {
		if metadata[k] != expected {
			t.Errorf("expected metadata[%s] = %q, got %q", k, expected, metadata[k])
		}
	}
}

func TestS3StoreAdapter_Get_ReadError(t *testing.T) {
	// This test verifies error handling when reading object data fails
	// We can't easily simulate this without mocking, so we skip it
	// The error path is covered by other tests
	t.Skip("Read error path difficult to test without mocking")
}

func TestS3StoreAdapter_List_SkipsTombstonedObjects(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	testBucket := "test-bucket"
	ctx := context.Background()

	err := testStore.CreateBucket(ctx, testBucket, "alice", 2, nil)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	// Add 3 objects
	for _, key := range []string{"file1.txt", "file2.txt", "file3.txt"} {
		_, err := testStore.PutObject(ctx, testBucket, key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		if err != nil {
			t.Fatalf("failed to put object %s: %v", key, err)
		}
	}

	// Tombstone file2.txt
	err = testStore.TombstoneObject(ctx, testBucket, "file2.txt")
	if err != nil {
		t.Fatalf("failed to tombstone object: %v", err)
	}

	// List should only return non-tombstoned objects
	keys, err := adapter.List(ctx, testBucket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(keys) != 2 {
		t.Errorf("expected 2 keys (tombstoned excluded), got %d: %v", len(keys), keys)
	}

	keyMap := make(map[string]bool)
	for _, key := range keys {
		keyMap[key] = true
	}

	if keyMap["file2.txt"] {
		t.Error("tombstoned object file2.txt should not appear in list")
	}
	if !keyMap["file1.txt"] || !keyMap["file3.txt"] {
		t.Error("non-tombstoned objects should appear in list")
	}
}

func TestS3StoreAdapter_ListBuckets_SkipsTombstonedBuckets(t *testing.T) {
	testStore := createTestS3Store(t)
	adapter := NewS3StoreAdapter(testStore)

	ctx := context.Background()

	// Create 3 buckets
	for _, bucket := range []string{"bucket1", "bucket2", "bucket3"} {
		err := testStore.CreateBucket(ctx, bucket, "alice", 2, nil)
		if err != nil {
			t.Fatalf("failed to create bucket %s: %v", bucket, err)
		}
	}

	// Tombstone bucket2
	err := testStore.TombstoneBucket(ctx, "bucket2")
	if err != nil {
		t.Fatalf("failed to tombstone bucket: %v", err)
	}

	// ListBuckets should only return non-tombstoned buckets
	names, err := adapter.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(names) != 2 {
		t.Errorf("expected 2 buckets (tombstoned excluded), got %d: %v", len(names), names)
	}

	bucketMap := make(map[string]bool)
	for _, name := range names {
		bucketMap[name] = true
	}

	if bucketMap["bucket2"] {
		t.Error("tombstoned bucket bucket2 should not appear in list")
	}
	if !bucketMap["bucket1"] || !bucketMap["bucket3"] {
		t.Error("non-tombstoned buckets should appear in list")
	}
}

// createTestS3Store creates a new S3 store for testing
func createTestS3Store(t *testing.T) *s3.Store {
	t.Helper()
	tmpDir := t.TempDir()

	// Create quota manager with 100MB limit
	quota := s3.NewQuotaManager(100 * 1024 * 1024) // 100MB

	// Use master key for CAS encryption (test key)
	var masterKey [32]byte
	copy(masterKey[:], []byte("test-master-key-for-testing!"))

	store, err := s3.NewStoreWithCAS(tmpDir, quota, masterKey)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}

	return store
}
