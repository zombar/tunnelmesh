package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizerDenyByDefault(t *testing.T) {
	auth := NewAuthorizer()

	// No bindings = deny
	allowed := auth.Authorize("alice", "get", "buckets", "")
	assert.False(t, allowed)
}

func TestAuthorizerAdminAccess(t *testing.T) {
	auth := NewAuthorizer()

	// Grant admin role
	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))

	// Admin can do anything
	assert.True(t, auth.Authorize("alice", "get", "buckets", ""))
	assert.True(t, auth.Authorize("alice", "delete", "objects", "my-bucket"))
	assert.True(t, auth.Authorize("alice", "put", "objects", "any-bucket"))
}

func TestAuthorizerBucketReadAccess(t *testing.T) {
	auth := NewAuthorizer()

	auth.Bindings.Add(NewRoleBinding("bob", RoleBucketRead, ""))

	// Can read
	assert.True(t, auth.Authorize("bob", "get", "buckets", ""))
	assert.True(t, auth.Authorize("bob", "list", "objects", "my-bucket"))

	// Cannot write or delete
	assert.False(t, auth.Authorize("bob", "put", "objects", "my-bucket"))
	assert.False(t, auth.Authorize("bob", "delete", "buckets", "my-bucket"))
}

func TestAuthorizerScopedBinding(t *testing.T) {
	auth := NewAuthorizer()

	// Bob has bucket-write only on "my-bucket"
	auth.Bindings.Add(NewRoleBinding("bob", RoleBucketWrite, "my-bucket"))

	// Can write to my-bucket
	assert.True(t, auth.Authorize("bob", "put", "objects", "my-bucket"))

	// Cannot write to other-bucket
	assert.False(t, auth.Authorize("bob", "put", "objects", "other-bucket"))
}

func TestAuthorizerSystemBucketAccess(t *testing.T) {
	auth := NewAuthorizer()

	// System role should only access _tunnelmesh/
	auth.Bindings.Add(NewRoleBinding("svc:coordinator", RoleSystem, ""))

	// Can access system bucket
	assert.True(t, auth.Authorize("svc:coordinator", "put", "objects", "_tunnelmesh"))
	assert.True(t, auth.Authorize("svc:coordinator", "get", "objects", "_tunnelmesh"))

	// Cannot access other buckets
	assert.False(t, auth.Authorize("svc:coordinator", "get", "objects", "user-bucket"))
}

func TestAuthorizerMultipleBindings(t *testing.T) {
	auth := NewAuthorizer()

	// Alice has read on bucket1 and write on bucket2
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketRead, "bucket1"))
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketWrite, "bucket2"))

	// Can read bucket1
	assert.True(t, auth.Authorize("alice", "get", "objects", "bucket1"))
	assert.False(t, auth.Authorize("alice", "put", "objects", "bucket1"))

	// Can write bucket2
	assert.True(t, auth.Authorize("alice", "put", "objects", "bucket2"))
}

func TestAuthorizerIsAdmin(t *testing.T) {
	auth := NewAuthorizer()

	assert.False(t, auth.IsAdmin("alice"))

	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))
	assert.True(t, auth.IsAdmin("alice"))
}

func TestAuthorizerHasHumanAdmin(t *testing.T) {
	auth := NewAuthorizer()

	assert.False(t, auth.HasHumanAdmin())

	// Service user doesn't count
	auth.Bindings.Add(NewRoleBinding("svc:coordinator", RoleSystem, ""))
	assert.False(t, auth.HasHumanAdmin())

	// Human admin
	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))
	assert.True(t, auth.HasHumanAdmin())
}

func TestNewAuthorizerWithRoles(t *testing.T) {
	customRole := Role{
		Name: "custom",
		Rules: []Rule{
			{Verbs: []string{"get"}, Resources: []string{"buckets"}},
		},
	}

	auth := NewAuthorizerWithRoles([]Role{customRole})
	auth.Bindings.Add(NewRoleBinding("alice", "custom", ""))

	assert.True(t, auth.Authorize("alice", "get", "buckets", ""))
	assert.False(t, auth.Authorize("alice", "delete", "buckets", ""))
}

func TestAuthorizerGetUserRoles(t *testing.T) {
	auth := NewAuthorizer()

	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketRead, "bucket1"))

	roles := auth.GetUserRoles("alice")
	require.Len(t, roles, 2)

	roleNames := make(map[string]bool)
	for _, r := range roles {
		roleNames[r] = true
	}
	assert.True(t, roleNames[RoleAdmin])
	assert.True(t, roleNames[RoleBucketRead])
}

// --- Group-based authorization tests ---

func TestAuthorizer_AuthorizeWithGroups(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Create developers group and add alice
	_, _ = auth.Groups.Create("developers", "Development team")
	_ = auth.Groups.AddMember("developers", "alice")

	// Grant developers bucket-read on all buckets
	auth.GroupBindings.Add(NewGroupBinding("developers", RoleBucketRead, ""))

	// Alice should have read access via group membership
	assert.True(t, auth.Authorize("alice", "get", "objects", "any-bucket"))
	assert.True(t, auth.Authorize("alice", "list", "objects", "any-bucket"))

	// Alice should not have write access
	assert.False(t, auth.Authorize("alice", "put", "objects", "any-bucket"))

	// Bob is not in the group, should be denied
	assert.False(t, auth.Authorize("bob", "get", "objects", "any-bucket"))
}

func TestAuthorizer_AuthorizeWithScopedGroupBinding(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Add alice to everyone group
	_ = auth.Groups.AddMember(GroupEveryone, "alice")

	// Everyone gets bucket-read only on fs+data bucket
	auth.GroupBindings.Add(NewGroupBinding(GroupEveryone, RoleBucketRead, "fs+data"))

	// Alice can read fs+data
	assert.True(t, auth.Authorize("alice", "get", "objects", "fs+data"))

	// Alice cannot read other buckets via this binding
	assert.False(t, auth.Authorize("alice", "get", "objects", "other-bucket"))
}

func TestAuthorizer_UserBindingTakesPrecedence(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Alice has direct admin binding
	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))

	// Alice is also in a group with read-only access
	_ = auth.Groups.AddMember("readers", "alice")
	auth.GroupBindings.Add(NewGroupBinding("readers", RoleBucketRead, ""))

	// Alice should have admin access (from direct binding)
	assert.True(t, auth.Authorize("alice", "delete", "buckets", "any-bucket"))
}

func TestAuthorizer_IsAdmin_ViaGroup(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Alice is not admin initially
	assert.False(t, auth.IsAdmin("alice"))

	// Add alice to all_admin_users group
	_ = auth.Groups.AddMember(GroupAllAdminUsers, "alice")

	// Now alice should be considered admin
	assert.True(t, auth.IsAdmin("alice"))
}

func TestAuthorizer_ServiceUsersViaGroup(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Add service user to all_service_users group
	_ = auth.Groups.AddMember(GroupAllServiceUsers, "svc:coordinator")

	// Grant system role to all_service_users group
	auth.GroupBindings.Add(NewGroupBinding(GroupAllServiceUsers, RoleSystem, ""))

	// Service user should access _tunnelmesh bucket
	assert.True(t, auth.Authorize("svc:coordinator", "put", "objects", "_tunnelmesh"))
	assert.True(t, auth.Authorize("svc:coordinator", "get", "objects", "_tunnelmesh"))

	// But not other buckets
	assert.False(t, auth.Authorize("svc:coordinator", "get", "objects", "user-bucket"))
}

func TestAuthorizer_GroupBindingPrecedence(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Create groups and add Alice to both
	_, _ = auth.Groups.Create("readers", "")
	_, _ = auth.Groups.Create("writers", "")
	_ = auth.Groups.AddMember("readers", "alice")
	_ = auth.Groups.AddMember("writers", "alice")

	// readers group has read access
	auth.GroupBindings.Add(NewGroupBinding("readers", RoleBucketRead, ""))
	// writers group has write access
	auth.GroupBindings.Add(NewGroupBinding("writers", RoleBucketWrite, ""))

	// Alice should have both read and write access
	assert.True(t, auth.Authorize("alice", "get", "objects", "any-bucket"))
	assert.True(t, auth.Authorize("alice", "put", "objects", "any-bucket"))
}

func TestAuthorizer_HasHumanAdmin_ViaGroup(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	assert.False(t, auth.HasHumanAdmin())

	// Add service user to admin group - should not count
	_ = auth.Groups.AddMember(GroupAllAdminUsers, "svc:coordinator")
	assert.False(t, auth.HasHumanAdmin())

	// Add human user to admin group
	_ = auth.Groups.AddMember(GroupAllAdminUsers, "alice")
	assert.True(t, auth.HasHumanAdmin())
}
