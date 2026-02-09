package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRoleBinding(t *testing.T) {
	before := time.Now().UTC()
	binding := NewRoleBinding("alice", RoleAdmin, "")
	after := time.Now().UTC()

	assert.NotEmpty(t, binding.Name)
	assert.Equal(t, "alice", binding.UserID)
	assert.Equal(t, RoleAdmin, binding.RoleName)
	assert.Empty(t, binding.BucketScope)
	assert.False(t, binding.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.True(t, binding.CreatedAt.After(before) || binding.CreatedAt.Equal(before), "CreatedAt should be recent")
	assert.True(t, binding.CreatedAt.Before(after) || binding.CreatedAt.Equal(after), "CreatedAt should be recent")
}

func TestNewRoleBindingWithScope(t *testing.T) {
	before := time.Now().UTC()
	binding := NewRoleBinding("bob", RoleBucketRead, "my-bucket")
	after := time.Now().UTC()

	assert.Equal(t, "bob", binding.UserID)
	assert.Equal(t, RoleBucketRead, binding.RoleName)
	assert.Equal(t, "my-bucket", binding.BucketScope)
	assert.False(t, binding.CreatedAt.IsZero(), "CreatedAt should be set")
	assert.True(t, binding.CreatedAt.After(before) || binding.CreatedAt.Equal(before), "CreatedAt should be recent")
	assert.True(t, binding.CreatedAt.Before(after) || binding.CreatedAt.Equal(after), "CreatedAt should be recent")
}

func TestRoleBindingAppliesToBucket(t *testing.T) {
	tests := []struct {
		name        string
		binding     RoleBinding
		bucketName  string
		wantApplies bool
	}{
		{
			name:        "unscoped binding applies to any bucket",
			binding:     RoleBinding{BucketScope: ""},
			bucketName:  "any-bucket",
			wantApplies: true,
		},
		{
			name:        "scoped binding applies to matching bucket",
			binding:     RoleBinding{BucketScope: "my-bucket"},
			bucketName:  "my-bucket",
			wantApplies: true,
		},
		{
			name:        "scoped binding does not apply to different bucket",
			binding:     RoleBinding{BucketScope: "my-bucket"},
			bucketName:  "other-bucket",
			wantApplies: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.binding.AppliesToBucket(tt.bucketName)
			assert.Equal(t, tt.wantApplies, got)
		})
	}
}

// nolint:dupl // Test setup code follows similar pattern across binding tests
func TestRoleBinding_AppliesToObject(t *testing.T) {
	tests := []struct {
		name         string
		bucketScope  string
		objectPrefix string
		bucket       string
		key          string
		want         bool
	}{
		// No scope = applies to everything
		{"no scope, any bucket/key", "", "", "mybucket", "any/key.txt", true},
		{"no scope, empty key", "", "", "mybucket", "", true},

		// Bucket scope only (no prefix)
		{"bucket match, no prefix", "mybucket", "", "mybucket", "any/key.txt", true},
		{"bucket match, empty key", "mybucket", "", "mybucket", "", true},
		{"bucket mismatch", "mybucket", "", "other", "any/key.txt", false},

		// Prefix scope
		{"prefix match", "mybucket", "projects/teamA/", "mybucket", "projects/teamA/file.txt", true},
		{"prefix match nested", "mybucket", "projects/", "mybucket", "projects/teamA/sub/file.txt", true},
		{"prefix mismatch", "mybucket", "projects/teamA/", "mybucket", "projects/teamB/file.txt", false},
		{"prefix exact match", "mybucket", "config.json", "mybucket", "config.json", true},
		{"prefix partial mismatch", "mybucket", "projects/teamA/", "mybucket", "projects/teamABC/file.txt", false},

		// Empty key (bucket-level operations like list) - always passes prefix check
		{"empty key with prefix", "mybucket", "projects/", "mybucket", "", true},

		// Bucket mismatch with prefix
		{"bucket mismatch with prefix", "mybucket", "projects/", "other", "projects/file.txt", false},

		// No bucket scope but with prefix (applies to all buckets with that prefix)
		{"no bucket scope with prefix", "", "shared/", "anybucket", "shared/file.txt", true},
		{"no bucket scope prefix mismatch", "", "shared/", "anybucket", "private/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding := RoleBinding{
				BucketScope:  tt.bucketScope,
				ObjectPrefix: tt.objectPrefix,
			}
			got := binding.AppliesToObject(tt.bucket, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBindingStore(t *testing.T) {
	store := NewBindingStore()
	require.NotNil(t, store)

	// Add binding
	binding := NewRoleBinding("alice", RoleAdmin, "")
	store.Add(binding)

	// Get bindings for user
	bindings := store.GetForUser("alice")
	require.Len(t, bindings, 1)
	assert.Equal(t, RoleAdmin, bindings[0].RoleName)

	// Get bindings for different user
	bindings = store.GetForUser("bob")
	assert.Empty(t, bindings)
}

func TestBindingStoreRemove(t *testing.T) {
	store := NewBindingStore()

	binding := NewRoleBinding("alice", RoleAdmin, "")
	store.Add(binding)

	// Remove by name
	store.Remove(binding.Name)

	bindings := store.GetForUser("alice")
	assert.Empty(t, bindings)
}

func TestBindingStoreList(t *testing.T) {
	store := NewBindingStore()

	store.Add(NewRoleBinding("alice", RoleAdmin, ""))
	store.Add(NewRoleBinding("bob", RoleBucketRead, "bucket1"))
	store.Add(NewRoleBinding("alice", RoleBucketWrite, "bucket2"))

	all := store.List()
	assert.Len(t, all, 3)
}
