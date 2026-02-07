package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGroupBinding(t *testing.T) {
	binding := NewGroupBinding("developers", RoleBucketRead, "")

	assert.NotEmpty(t, binding.Name)
	assert.Equal(t, "developers", binding.GroupName)
	assert.Equal(t, RoleBucketRead, binding.RoleName)
	assert.Empty(t, binding.BucketScope)
}

func TestNewGroupBindingWithScope(t *testing.T) {
	binding := NewGroupBinding("developers", RoleBucketWrite, "my-bucket")

	assert.Equal(t, "developers", binding.GroupName)
	assert.Equal(t, RoleBucketWrite, binding.RoleName)
	assert.Equal(t, "my-bucket", binding.BucketScope)
}

func TestGroupBinding_AppliesToBucket(t *testing.T) {
	tests := []struct {
		name        string
		binding     GroupBinding
		bucketName  string
		wantApplies bool
	}{
		{
			name:        "unscoped binding applies to any bucket",
			binding:     GroupBinding{BucketScope: ""},
			bucketName:  "any-bucket",
			wantApplies: true,
		},
		{
			name:        "scoped binding applies to matching bucket",
			binding:     GroupBinding{BucketScope: "my-bucket"},
			bucketName:  "my-bucket",
			wantApplies: true,
		},
		{
			name:        "scoped binding does not apply to different bucket",
			binding:     GroupBinding{BucketScope: "my-bucket"},
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

func TestGroupBindingStore_Add(t *testing.T) {
	store := NewGroupBindingStore()
	require.NotNil(t, store)

	binding := NewGroupBinding("developers", RoleAdmin, "")
	store.Add(binding)

	bindings := store.GetForGroup("developers")
	require.Len(t, bindings, 1)
	assert.Equal(t, RoleAdmin, bindings[0].RoleName)
}

func TestGroupBindingStore_Remove(t *testing.T) {
	store := NewGroupBindingStore()

	binding := NewGroupBinding("developers", RoleAdmin, "")
	store.Add(binding)

	store.Remove(binding.Name)

	bindings := store.GetForGroup("developers")
	assert.Empty(t, bindings)
}

func TestGroupBindingStore_GetForGroup(t *testing.T) {
	store := NewGroupBindingStore()

	store.Add(NewGroupBinding("developers", RoleAdmin, ""))
	store.Add(NewGroupBinding("developers", RoleBucketRead, "bucket1"))
	store.Add(NewGroupBinding("testers", RoleBucketRead, ""))

	devBindings := store.GetForGroup("developers")
	assert.Len(t, devBindings, 2)

	testBindings := store.GetForGroup("testers")
	assert.Len(t, testBindings, 1)

	emptyBindings := store.GetForGroup("nonexistent")
	assert.Empty(t, emptyBindings)
}

func TestGroupBindingStore_List(t *testing.T) {
	store := NewGroupBindingStore()

	store.Add(NewGroupBinding("developers", RoleAdmin, ""))
	store.Add(NewGroupBinding("testers", RoleBucketRead, "bucket1"))
	store.Add(NewGroupBinding("everyone", RoleBucketRead, "fs+data"))

	all := store.List()
	assert.Len(t, all, 3)
}

func TestGroupBindingStore_LoadBindings(t *testing.T) {
	store := NewGroupBindingStore()

	store.Add(NewGroupBinding("developers", RoleAdmin, ""))
	store.Add(NewGroupBinding("testers", RoleBucketRead, ""))

	saved := store.List()

	// Create fresh store and load
	store2 := NewGroupBindingStore()
	store2.LoadBindings(saved)

	devBindings := store2.GetForGroup("developers")
	assert.Len(t, devBindings, 1)
	assert.Equal(t, RoleAdmin, devBindings[0].RoleName)
}

func TestGroupBindingStore_RemoveForGroup(t *testing.T) {
	store := NewGroupBindingStore()

	store.Add(NewGroupBinding("developers", RoleAdmin, ""))
	store.Add(NewGroupBinding("developers", RoleBucketRead, "bucket1"))
	store.Add(NewGroupBinding("testers", RoleBucketRead, ""))

	store.RemoveForGroup("developers")

	devBindings := store.GetForGroup("developers")
	assert.Empty(t, devBindings)

	testBindings := store.GetForGroup("testers")
	assert.Len(t, testBindings, 1)
}
