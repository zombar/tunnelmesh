package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRoleBinding(t *testing.T) {
	binding := NewRoleBinding("alice", RoleAdmin, "")

	assert.NotEmpty(t, binding.Name)
	assert.Equal(t, "alice", binding.UserID)
	assert.Equal(t, RoleAdmin, binding.RoleName)
	assert.Empty(t, binding.BucketScope)
}

func TestNewRoleBindingWithScope(t *testing.T) {
	binding := NewRoleBinding("bob", RoleBucketRead, "my-bucket")

	assert.Equal(t, "bob", binding.UserID)
	assert.Equal(t, RoleBucketRead, binding.RoleName)
	assert.Equal(t, "my-bucket", binding.BucketScope)
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
