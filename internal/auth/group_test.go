package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGroup(t *testing.T) {
	group := NewGroup("developers", "Development team")

	assert.Equal(t, "developers", group.Name)
	assert.Equal(t, "Development team", group.Description)
	assert.Empty(t, group.Members)
	assert.False(t, group.Builtin)
	assert.False(t, group.CreatedAt.IsZero())
}

func TestGroupStore_Create(t *testing.T) {
	store := NewGroupStore()
	require.NotNil(t, store)

	group, err := store.Create("developers", "Development team")
	require.NoError(t, err)
	assert.Equal(t, "developers", group.Name)

	// Creating duplicate should fail
	_, err = store.Create("developers", "Another description")
	assert.Error(t, err)
	assert.Equal(t, ErrGroupExists, err)
}

func TestGroupStore_Get(t *testing.T) {
	store := NewGroupStore()

	_, err := store.Create("developers", "")
	require.NoError(t, err)

	group := store.Get("developers")
	require.NotNil(t, group)
	assert.Equal(t, "developers", group.Name)

	// Non-existent group
	group = store.Get("nonexistent")
	assert.Nil(t, group)
}

func TestGroupStore_Delete(t *testing.T) {
	store := NewGroupStore()

	_, err := store.Create("developers", "")
	require.NoError(t, err)

	err = store.Delete("developers")
	require.NoError(t, err)

	group := store.Get("developers")
	assert.Nil(t, group)

	// Deleting non-existent should fail
	err = store.Delete("developers")
	assert.Error(t, err)
	assert.Equal(t, ErrGroupNotFound, err)
}

func TestGroupStore_DeleteBuiltin(t *testing.T) {
	store := NewGroupStore()

	// Cannot delete built-in groups
	err := store.Delete(GroupEveryone)
	assert.Error(t, err)
	assert.Equal(t, ErrBuiltinGroup, err)
}

func TestGroupStore_AddMember(t *testing.T) {
	store := NewGroupStore()

	_, err := store.Create("developers", "")
	require.NoError(t, err)

	err = store.AddMember("developers", "alice")
	require.NoError(t, err)

	group := store.Get("developers")
	assert.Contains(t, group.Members, "alice")

	// Adding same member again should be idempotent
	err = store.AddMember("developers", "alice")
	require.NoError(t, err)
	assert.Len(t, group.Members, 1)

	// Adding to non-existent group should fail
	err = store.AddMember("nonexistent", "bob")
	assert.Error(t, err)
	assert.Equal(t, ErrGroupNotFound, err)
}

func TestGroupStore_RemoveMember(t *testing.T) {
	store := NewGroupStore()

	_, err := store.Create("developers", "")
	require.NoError(t, err)

	_ = store.AddMember("developers", "alice")
	_ = store.AddMember("developers", "bob")

	err = store.RemoveMember("developers", "alice")
	require.NoError(t, err)

	group := store.Get("developers")
	assert.NotContains(t, group.Members, "alice")
	assert.Contains(t, group.Members, "bob")

	// Removing non-member should be idempotent
	err = store.RemoveMember("developers", "alice")
	require.NoError(t, err)

	// Removing from non-existent group should fail
	err = store.RemoveMember("nonexistent", "bob")
	assert.Error(t, err)
}

func TestGroupStore_GetGroupsForPeer(t *testing.T) {
	store := NewGroupStore()

	_, _ = store.Create("developers", "")
	_, _ = store.Create("testers", "")

	_ = store.AddMember("developers", "alice")
	_ = store.AddMember("testers", "alice")
	_ = store.AddMember("developers", "bob")

	aliceGroups := store.GetGroupsForPeer("alice")
	assert.Len(t, aliceGroups, 2)
	assert.Contains(t, aliceGroups, "developers")
	assert.Contains(t, aliceGroups, "testers")

	bobGroups := store.GetGroupsForPeer("bob")
	assert.Len(t, bobGroups, 1)
	assert.Contains(t, bobGroups, "developers")

	charlieGroups := store.GetGroupsForPeer("charlie")
	assert.Empty(t, charlieGroups)
}

func TestGroupStore_IsMember(t *testing.T) {
	store := NewGroupStore()

	_, _ = store.Create("developers", "")
	_ = store.AddMember("developers", "alice")

	assert.True(t, store.IsMember("developers", "alice"))
	assert.False(t, store.IsMember("developers", "bob"))
	assert.False(t, store.IsMember("nonexistent", "alice"))
}

func TestGroupStore_BuiltinGroups(t *testing.T) {
	store := NewGroupStore()

	// Built-in groups should exist
	everyone := store.Get(GroupEveryone)
	require.NotNil(t, everyone)
	assert.True(t, everyone.Builtin)
	assert.Equal(t, "All registered peers", everyone.Description)

	allAdmin := store.Get(GroupAllAdminUsers)
	require.NotNil(t, allAdmin)
	assert.True(t, allAdmin.Builtin)
}

func TestGroupStore_List(t *testing.T) {
	store := NewGroupStore()

	// Should include built-in groups
	groups := store.List()
	assert.GreaterOrEqual(t, len(groups), 2) // everyone, all_admin_users

	_, _ = store.Create("developers", "")
	_, _ = store.Create("testers", "")

	groups = store.List()
	assert.GreaterOrEqual(t, len(groups), 4) // everyone, all_admin_users, developers, testers
}

func TestGroupStore_LoadGroups(t *testing.T) {
	store := NewGroupStore()

	// Create some groups and add members
	_, _ = store.Create("developers", "Dev team")
	_ = store.AddMember("developers", "alice")
	_ = store.AddMember(GroupEveryone, "alice")
	_ = store.AddMember(GroupEveryone, "bob")

	// Get the list to simulate saving
	saved := store.List()

	// Create a fresh store and load
	store2 := NewGroupStore()
	store2.LoadGroups(saved)

	// Verify loaded data
	devs := store2.Get("developers")
	require.NotNil(t, devs)
	assert.Equal(t, "Dev team", devs.Description)
	assert.Contains(t, devs.Members, "alice")

	everyone := store2.Get(GroupEveryone)
	require.NotNil(t, everyone)
	assert.Contains(t, everyone.Members, "alice")
	assert.Contains(t, everyone.Members, "bob")
}

func TestGroupStore_RemovePeerFromAllGroups(t *testing.T) {
	store := NewGroupStore()

	_, _ = store.Create("developers", "")
	_, _ = store.Create("testers", "")

	_ = store.AddMember("developers", "alice")
	_ = store.AddMember("testers", "alice")
	_ = store.AddMember(GroupEveryone, "alice")

	// Remove alice from all groups
	store.RemovePeerFromAllGroups("alice")

	assert.Empty(t, store.GetGroupsForPeer("alice"))
	assert.False(t, store.IsMember("developers", "alice"))
	assert.False(t, store.IsMember("testers", "alice"))
	assert.False(t, store.IsMember(GroupEveryone, "alice"))
}
