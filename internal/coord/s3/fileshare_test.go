package s3

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

func TestFileShareManager_Create(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	// Create authorizer with groups
	authorizer := auth.NewAuthorizerWithGroups()

	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a file share
	share, err := mgr.Create("data", "Test data share", "user123", 0)
	require.NoError(t, err)

	assert.Equal(t, "data", share.Name)
	assert.Equal(t, "Test data share", share.Description)
	assert.Equal(t, "user123", share.Owner)
	assert.False(t, share.CreatedAt.IsZero())

	// Verify bucket was created
	_, err = store.HeadBucket(FileShareBucketPrefix + "data")
	assert.NoError(t, err)
}

func TestFileShareManager_Create_SetsPermissions(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("docs", "Documentation", "alice", 0)
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
	ownerBindings := authorizer.Bindings.GetForUser("alice")
	found = false
	for _, b := range ownerBindings {
		if b.BucketScope == bucketName && b.RoleName == auth.RoleBucketAdmin {
			found = true
			break
		}
	}
	assert.True(t, found, "owner should have bucket-admin on fs+docs")
}

func TestFileShareManager_Create_DuplicateName(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("data", "First share", "alice", 0)
	require.NoError(t, err)

	// Try to create share with same name
	_, err = mgr.Create("data", "Duplicate", "bob", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestFileShareManager_Delete(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("temp", "Temporary share", "alice", 0)
	require.NoError(t, err)

	// Delete the share
	err = mgr.Delete("temp")
	require.NoError(t, err)

	// Verify bucket was deleted
	_, err = store.HeadBucket(FileShareBucketPrefix + "temp")
	assert.Equal(t, ErrBucketNotFound, err)

	// Verify share is removed from list
	shares := mgr.List()
	for _, s := range shares {
		assert.NotEqual(t, "temp", s.Name)
	}
}

func TestFileShareManager_Delete_RemovesPermissions(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("temp", "Temporary", "alice", 0)
	require.NoError(t, err)

	bucketName := FileShareBucketPrefix + "temp"

	// Verify permissions exist
	assert.NotEmpty(t, authorizer.GroupBindings.GetForGroup(auth.GroupEveryone))
	assert.NotEmpty(t, authorizer.Bindings.GetForUser("alice"))

	// Delete the share
	err = mgr.Delete("temp")
	require.NoError(t, err)

	// Verify group bindings for this bucket are removed
	for _, b := range authorizer.GroupBindings.GetForGroup(auth.GroupEveryone) {
		assert.NotEqual(t, bucketName, b.BucketScope)
	}

	// Verify user bindings for this bucket are removed
	for _, b := range authorizer.Bindings.GetForUser("alice") {
		assert.NotEqual(t, bucketName, b.BucketScope)
	}
}

func TestFileShareManager_Get(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("myshare", "My share", "alice", 0)
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
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, _ = mgr.Create("share1", "First", "alice", 0)
	_, _ = mgr.Create("share2", "Second", "bob", 0)

	shares := mgr.List()
	assert.Len(t, shares, 2)
}

func TestFileShareManager_Persistence(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewStore(tempDir, nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	_, err = mgr.Create("persistent", "Should persist", "alice", 0)
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
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)

	// Set default expiry to 365 days
	store.SetDefaultShareExpiryDays(365)

	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)

	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	share, err := mgr.Create("data", "Test share", "alice", 0)
	require.NoError(t, err)

	// ExpiresAt should be set to approximately 365 days from now
	assert.False(t, share.ExpiresAt.IsZero(), "ExpiresAt should be set")
	expectedExpiry := time.Now().UTC().AddDate(0, 0, 365)
	// Allow 1 minute tolerance for test timing
	assert.WithinDuration(t, expectedExpiry, share.ExpiresAt, time.Minute)
}

func TestFileShareManager_IsProtectedBinding(t *testing.T) {
	store, err := NewStore(t.TempDir(), nil)
	require.NoError(t, err)
	systemStore, err := NewSystemStore(store, "svc:coordinator")
	require.NoError(t, err)
	authorizer := auth.NewAuthorizerWithGroups()
	mgr := NewFileShareManager(store, systemStore, authorizer)

	// Create a file share
	_, err = mgr.Create("docs", "Documents", "alice", 0)
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
				UserID:      "alice",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "fs+docs",
			},
			protected: true,
		},
		{
			name: "non-owner admin binding is not protected",
			binding: &auth.RoleBinding{
				Name:        "test2",
				UserID:      "bob",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "fs+docs",
			},
			protected: false,
		},
		{
			name: "owner read binding is not protected",
			binding: &auth.RoleBinding{
				Name:        "test3",
				UserID:      "alice",
				RoleName:    auth.RoleBucketRead,
				BucketScope: "fs+docs",
			},
			protected: false,
		},
		{
			name: "regular bucket admin is not protected",
			binding: &auth.RoleBinding{
				Name:        "test4",
				UserID:      "alice",
				RoleName:    auth.RoleBucketAdmin,
				BucketScope: "regular-bucket",
			},
			protected: false,
		},
		{
			name: "non-existent share is not protected",
			binding: &auth.RoleBinding{
				Name:        "test5",
				UserID:      "alice",
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
