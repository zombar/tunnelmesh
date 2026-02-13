package s3

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// newTestStoreWithCASForFileshare creates a store with CAS for fileshare tests.
func newTestStoreWithCASForFileshare(t *testing.T) *Store {
	t.Helper()
	masterKey := [32]byte{1, 2, 3, 4, 5, 6, 7, 8}
	store, err := NewStoreWithCAS(t.TempDir(), nil, masterKey)
	require.NoError(t, err)
	return store
}

func TestFileShareManager_Create(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Create authorizer with groups
	authorizer := auth.NewAuthorizerWithGroups()

	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a file share
	share, err := mgr.Create(context.Background(), "data", "Test data share", "user123", 0, nil)
	require.NoError(t, err)

	assert.Equal(t, "data", share.Name)
	assert.Equal(t, "Test data share", share.Description)
	assert.Equal(t, "user123", share.Owner)
	assert.False(t, share.CreatedAt.IsZero())

	// Verify bucket was created
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"data")
	assert.NoError(t, err)
}

func TestFileShareManager_Create_SetsPermissions(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "docs", "Documentation", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "docs"

	// Verify everyone group has bucket-read
	bindings := authorizer.GroupBindings.GetForGroup(auth.GroupEveryone)
	found := false
	for _, b := range bindings {
		if b.BucketScope == bucketName && b.RoleName == auth.RoleBucketRead {
			found = true
			break
		}
	}
	assert.True(t, found, "everyone group should have bucket-read on fs+docs")

	// Verify owner has bucket-admin
	ownerBindings := authorizer.Bindings.GetForPeer("alice")
	found = false
	for _, b := range ownerBindings {
		if b.BucketScope == bucketName && b.RoleName == auth.RoleBucketAdmin {
			found = true
			break
		}
	}
	assert.True(t, found, "owner should have bucket-admin on fs+docs")
}

func TestFileShareManager_Create_GuestReadDisabled(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create share with guest read disabled
	opts := &FileShareOptions{GuestRead: false, GuestReadSet: true}
	share, err := mgr.Create(context.Background(), "private", "Private share", "alice", 0, opts)
	require.NoError(t, err)

	assert.False(t, share.GuestRead, "share should have guest read disabled")

	bucketName := FileShareBucketPrefix + "private"

	// Verify everyone group does NOT have bucket-read
	bindings := authorizer.GroupBindings.GetForGroup(auth.GroupEveryone)
	found := false
	for _, b := range bindings {
		if b.BucketScope == bucketName && b.RoleName == auth.RoleBucketRead {
			found = true
			break
		}
	}
	assert.False(t, found, "everyone group should NOT have bucket-read when guest read is disabled")

	// Verify owner still has bucket-admin
	ownerBindings := authorizer.Bindings.GetForPeer("alice")
	found = false
	for _, b := range ownerBindings {
		if b.BucketScope == bucketName && b.RoleName == auth.RoleBucketAdmin {
			found = true
			break
		}
	}
	assert.True(t, found, "owner should have bucket-admin")
}

func TestFileShareManager_Create_ExpiryFromOptions(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create share with explicit expiry
	expiry := time.Now().Add(7 * 24 * time.Hour).UTC()
	opts := &FileShareOptions{ExpiresAt: expiry}
	share, err := mgr.Create(context.Background(), "temp", "Temporary share", "alice", 0, opts)
	require.NoError(t, err)

	// Expiry should match what we set (within a second)
	assert.WithinDuration(t, expiry, share.ExpiresAt, time.Second)
}

func TestFileShareManager_Create_DuplicateName(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "data", "First share", "alice", 0, nil)
	require.NoError(t, err)

	// Try to create share with same name
	_, err = mgr.Create(context.Background(), "data", "Duplicate", "bob", 0, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestFileShareManager_Delete(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "temp", "Temporary share", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "temp"

	// Add some content
	content := []byte("hello")
	_, err = store.PutObject(context.Background(), bucketName, "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Delete the share
	err = mgr.Delete(context.Background(), "temp")
	require.NoError(t, err)

	// Bucket is kept (for potential restore), but objects are tombstoned
	_, err = store.HeadBucket(context.Background(), bucketName)
	assert.NoError(t, err, "bucket should still exist for restore capability")

	// Object should be tombstoned
	meta, err := store.HeadObject(context.Background(), bucketName, "file.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "object should be tombstoned")

	// Verify share is removed from list
	shares := mgr.List()
	for _, s := range shares {
		assert.NotEqual(t, "temp", s.Name)
	}
}

func TestFileShareManager_DeleteAndRecreate_RestoresContent(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create share with content
	_, err = mgr.Create(context.Background(), "docs", "Documents", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "docs"
	testData := []byte("important data")
	_, err = store.PutObject(context.Background(), bucketName, "readme.txt", bytes.NewReader(testData), int64(len(testData)), "text/plain", nil)
	require.NoError(t, err)

	// Delete the share (tombstones content)
	err = mgr.Delete(context.Background(), "docs")
	require.NoError(t, err)

	// Verify content is tombstoned
	meta, err := store.HeadObject(context.Background(), bucketName, "readme.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned())

	// Recreate with same name (should restore content)
	_, err = mgr.Create(context.Background(), "docs", "Restored docs", "bob", 0, nil)
	require.NoError(t, err)

	// Content should be restored (untombstoned)
	meta, err = store.HeadObject(context.Background(), bucketName, "readme.txt")
	require.NoError(t, err)
	assert.False(t, meta.IsTombstoned(), "object should be untombstoned after recreate")

	// Content should still be readable
	reader, _, err := store.GetObject(context.Background(), bucketName, "readme.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "important data", string(content))
}

func TestFileShareManager_Delete_RemovesPermissions(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "temp", "Temporary", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "temp"

	// Verify permissions exist
	assert.NotEmpty(t, authorizer.GroupBindings.GetForGroup(auth.GroupEveryone))
	assert.NotEmpty(t, authorizer.Bindings.GetForPeer("alice"))

	// Delete the share
	err = mgr.Delete(context.Background(), "temp")
	require.NoError(t, err)

	// Verify group bindings for this bucket are removed
	for _, b := range authorizer.GroupBindings.GetForGroup(auth.GroupEveryone) {
		assert.NotEqual(t, bucketName, b.BucketScope)
	}

	// Verify user bindings for this bucket are removed
	for _, b := range authorizer.Bindings.GetForPeer("alice") {
		assert.NotEqual(t, bucketName, b.BucketScope)
	}
}

func TestFileShareManager_Get(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "myshare", "My share", "alice", 0, nil)
	require.NoError(t, err)

	share := mgr.Get("myshare")
	require.NotNil(t, share)
	assert.Equal(t, "myshare", share.Name)
	assert.Equal(t, "alice", share.Owner)

	// Non-existent share
	share = mgr.Get("nonexistent")
	assert.Nil(t, share)
}

func TestFileShareManager_List(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, _ = mgr.Create(context.Background(), "share1", "First", "alice", 0, nil)
	_, _ = mgr.Create(context.Background(), "share2", "Second", "bob", 0, nil)

	shares := mgr.List()
	assert.Len(t, shares, 2)
}

func TestFileShareManager_Persistence(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "persistent", "Should persist", "alice", 0, nil)
	require.NoError(t, err)

	// Create new manager (simulating restart)
	mgr2 := NewFileShareManager(store, systemStore, authorizer)

	shares := mgr2.List()
	assert.Len(t, shares, 1)
	assert.Equal(t, "persistent", shares[0].Name)
}

func TestFileShareBucketName(t *testing.T) {
	// Verify the bucket naming convention
	shareName := "documents"
	bucketName := FileShareBucketPrefix + shareName
	assert.Equal(t, "fs+documents", bucketName)
}

func TestFileShare_CreatedAt(t *testing.T) {
	share := &FileShare{
		Name:      "test",
		Owner:     "alice",
		CreatedAt: time.Now().UTC(),
	}
	assert.False(t, share.CreatedAt.IsZero())
}

func TestFileShareManager_Create_SetsExpiry(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)

	// Set default expiry to 365 days
	store.SetDefaultShareExpiryDays(365)

	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	share, err := mgr.Create(context.Background(), "data", "Test share", "alice", 0, nil)
	require.NoError(t, err)

	// ExpiresAt should be set to approximately 365 days from now
	assert.False(t, share.ExpiresAt.IsZero(), "ExpiresAt should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 365)
	// Allow 1 minute tolerance for test timing
	assert.WithinDuration(t, expectedExpiry, share.ExpiresAt, time.Minute)
}

func TestFileShare_IsExpired(t *testing.T) {
	// Not expired - future date
	future := &FileShare{
		Name:      "future",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	assert.False(t, future.IsExpired())

	// Expired - past date
	past := &FileShare{
		Name:      "past",
		ExpiresAt: time.Now().Add(-24 * time.Hour),
	}
	assert.True(t, past.IsExpired())

	// Never expires - zero time
	never := &FileShare{
		Name: "never",
	}
	assert.False(t, never.IsExpired())
}

func TestFileShareManager_TombstoneExpiredShareContents(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)

	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a share and add some objects
	share, err := mgr.Create(context.Background(), "testshare", "Test share", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + share.Name

	// Add an object to the share's bucket
	content := []byte("test content")
	_, err = store.PutObject(context.Background(), bucketName, "file.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Manually expire the share
	mgr.mu.Lock()
	share.ExpiresAt = time.Now().Add(-24 * time.Hour)
	mgr.mu.Unlock()

	// Run tombstoning of expired share contents
	count := mgr.TombstoneExpiredShareContents(context.Background())
	assert.Equal(t, 1, count, "should tombstone 1 object")

	// Verify object is now tombstoned
	obj, err := store.HeadObject(context.Background(), bucketName, "file.txt")
	require.NoError(t, err)
	assert.True(t, obj.IsTombstoned())
}

func TestFileShareManager_RecreatesMissingBucketsOnLoad(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create shares that persist to system store
	_, err = mgr.Create(context.Background(), "photos", "Photos share", "alice", 0, nil)
	require.NoError(t, err)
	_, err = mgr.Create(context.Background(), "docs", "Documents share", "bob", 0, nil)
	require.NoError(t, err)

	// Verify buckets exist
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"photos")
	require.NoError(t, err)
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"docs")
	require.NoError(t, err)

	// Delete the bucket directories to simulate a coordinator that received
	// share metadata via replication but never had the buckets created locally
	err = store.DeleteBucket(context.Background(), FileShareBucketPrefix+"photos")
	require.NoError(t, err)
	err = store.DeleteBucket(context.Background(), FileShareBucketPrefix+"docs")
	require.NoError(t, err)

	// Verify buckets are gone
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"photos")
	assert.ErrorIs(t, err, ErrBucketNotFound)

	// Recreate the file share manager — this simulates coordinator restart
	// The constructor should detect missing buckets and recreate them
	mgr2 := NewFileShareManager(store, systemStore, authorizer)

	// Verify shares are still loaded
	assert.Len(t, mgr2.List(), 2)

	// Verify buckets were recreated
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"photos")
	assert.NoError(t, err, "photos bucket should be recreated on load")
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"docs")
	assert.NoError(t, err, "docs bucket should be recreated on load")

	// Verify we can write to the recreated buckets
	content := []byte("test data")
	_, err = store.PutObject(context.Background(), FileShareBucketPrefix+"photos", "test.txt",
		bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	assert.NoError(t, err, "should be able to write to recreated bucket")
}

func TestFileShareManager_RecreatesOnlyMissingBuckets(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create two shares
	_, err = mgr.Create(context.Background(), "photos", "Photos share", "alice", 0, nil)
	require.NoError(t, err)
	_, err = mgr.Create(context.Background(), "docs", "Documents share", "bob", 0, nil)
	require.NoError(t, err)

	// Write content to docs bucket so we can verify it's untouched
	docsBucket := FileShareBucketPrefix + "docs"
	content := []byte("important data")
	_, err = store.PutObject(context.Background(), docsBucket, "readme.txt",
		bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Delete only the photos bucket — docs stays intact
	err = store.DeleteBucket(context.Background(), FileShareBucketPrefix+"photos")
	require.NoError(t, err)

	// Recreate the file share manager
	mgr2 := NewFileShareManager(store, systemStore, authorizer)
	assert.Len(t, mgr2.List(), 2)

	// Photos bucket was recreated
	_, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"photos")
	assert.NoError(t, err, "photos bucket should be recreated")

	// Docs bucket still has its content
	reader, _, err := store.GetObject(context.Background(), docsBucket, "readme.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(reader)
	_ = reader.Close()
	assert.Equal(t, "important data", string(data), "existing bucket content should be untouched")
}

func TestFileShareManager_RecreatesWithCorrectReplicationFactor(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create share with custom replication factor 3
	opts := &FileShareOptions{ReplicationFactor: 3}
	_, err = mgr.Create(context.Background(), "critical", "Critical data", "alice", 0, opts)
	require.NoError(t, err)

	// Verify bucket has RF=3
	meta, err := store.HeadBucket(context.Background(), FileShareBucketPrefix+"critical")
	require.NoError(t, err)
	assert.Equal(t, 3, meta.ReplicationFactor)

	// Delete the bucket
	err = store.DeleteBucket(context.Background(), FileShareBucketPrefix+"critical")
	require.NoError(t, err)

	// Recreate the file share manager — should recreate with RF=3
	mgr2 := NewFileShareManager(store, systemStore, authorizer)
	assert.Len(t, mgr2.List(), 1)

	// Verify recreated bucket has RF=3 (not default 2)
	meta, err = store.HeadBucket(context.Background(), FileShareBucketPrefix+"critical")
	require.NoError(t, err)
	assert.Equal(t, 3, meta.ReplicationFactor, "recreated bucket should preserve original replication factor")
}

func TestFileShareManager_IsProtectedBinding(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)
	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a file share
	_, err = mgr.Create(context.Background(), "docs", "Documents", "alice", 0, nil)
	require.NoError(t, err)

	tests := []struct {
		name      string
		binding   *auth.RoleBinding
		protected bool
	}{
		{
			name: "owner admin binding is protected",
			binding: &auth.RoleBinding{
				Name:        "test1",
				PeerID:      "alice",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "fs+docs",
			},
			protected: true,
		},
		{
			name: "non-owner admin binding is not protected",
			binding: &auth.RoleBinding{
				Name:        "test2",
				PeerID:      "bob",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "fs+docs",
			},
			protected: false,
		},
		{
			name: "owner read binding is not protected",
			binding: &auth.RoleBinding{
				Name:        "test3",
				PeerID:      "alice",
				RoleName:    auth.RoleBucketRead,
				BucketScope: "fs+docs",
			},
			protected: false,
		},
		{
			name: "regular bucket admin is not protected",
			binding: &auth.RoleBinding{
				Name:        "test4",
				PeerID:      "alice",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "regular-bucket",
			},
			protected: false,
		},
		{
			name: "non-existent share is not protected",
			binding: &auth.RoleBinding{
				Name:        "test5",
				PeerID:      "alice",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "fs+nonexistent",
			},
			protected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mgr.IsProtectedBinding(tt.binding)
			assert.Equal(t, tt.protected, result)
		})
	}
}

func TestEnsureBucketForShare(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a share (which creates the bucket)
	_, err = mgr.Create(context.Background(), "docs", "Documentation", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "docs"

	// Verify bucket exists
	_, err = store.HeadBucket(context.Background(), bucketName)
	require.NoError(t, err)

	// Delete the bucket directory to simulate corruption/missing bucket
	bucketDir := filepath.Join(store.DataDir(), "buckets", bucketName)
	require.NoError(t, os.RemoveAll(bucketDir))

	// Verify bucket is gone
	_, err = store.HeadBucket(context.Background(), bucketName)
	require.ErrorIs(t, err, ErrBucketNotFound)

	// EnsureBucketForShare should recreate it
	err = mgr.EnsureBucketForShare(context.Background(), bucketName)
	require.NoError(t, err)

	// Bucket should exist again
	meta, err := store.HeadBucket(context.Background(), bucketName)
	require.NoError(t, err)
	assert.Equal(t, bucketName, meta.Name)
	assert.Equal(t, "alice", meta.Owner)
}

func TestEnsureBucketForShare_Idempotent(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create(context.Background(), "docs", "Documentation", "alice", 0, nil)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "docs"

	// Call twice — both should succeed (bucket already exists)
	err = mgr.EnsureBucketForShare(context.Background(), bucketName)
	assert.NoError(t, err)

	err = mgr.EnsureBucketForShare(context.Background(), bucketName)
	assert.NoError(t, err)
}

func TestEnsureBucketForShare_NonShareBucket(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Non-fs+ bucket should be a no-op
	err = mgr.EnsureBucketForShare(context.Background(), "regular-bucket")
	assert.NoError(t, err)
}

func TestEnsureBucketForShare_NoMatchingShare(t *testing.T) {
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// fs+ prefix but no share exists for it — should be a no-op
	err = mgr.EnsureBucketForShare(context.Background(), FileShareBucketPrefix+"nonexistent")
	assert.NoError(t, err)
}

func TestEnsureBucketForShare_ReplicatedShare(t *testing.T) {
	// Simulates the cross-coordinator scenario: a share is created on coordinator A
	// and replicated to coordinator B's system store. Coordinator B's FileShareManager
	// doesn't have the share in memory, but should discover it from the system store.
	store := newTestStoreWithCASForFileshare(t)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()

	// Create a share on "coordinator A"
	mgrA := NewFileShareManager(store, systemStore, authorizer)
	_, err = mgrA.Create(context.Background(), "photos", "Photos", "alice", 0, nil)
	require.NoError(t, err)

	// "Coordinator B": same system store (simulates replication), fresh FileShareManager
	// that hasn't loaded the share into memory yet. Delete the bucket to simulate
	// that B never had it locally.
	mgrB := NewFileShareManager(store, systemStore, authorizer)

	bucketName := FileShareBucketPrefix + "photos"

	// Delete bucket to simulate coordinator B having replicated share metadata
	// but not the bucket directories
	bucketDir := filepath.Join(store.DataDir(), "buckets", bucketName)
	require.NoError(t, os.RemoveAll(bucketDir))

	// Clear mgrB's in-memory shares to simulate it not having loaded this share
	mgrB.mu.Lock()
	mgrB.shares = nil
	mgrB.mu.Unlock()

	// Verify share is not in memory
	assert.Nil(t, mgrB.Get("photos"))

	// EnsureBucketForShare should discover the share from the system store and recreate the bucket
	err = mgrB.EnsureBucketForShare(context.Background(), bucketName)
	require.NoError(t, err)

	// Bucket should exist now
	meta, err := store.HeadBucket(context.Background(), bucketName)
	require.NoError(t, err)
	assert.Equal(t, bucketName, meta.Name)
	assert.Equal(t, "alice", meta.Owner)

	// Share should now be in mgrB's memory
	assert.NotNil(t, mgrB.Get("photos"))
}
