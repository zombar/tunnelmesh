package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthorizerDenyByDefault(t *testing.T) {
	auth := NewAuthorizer()

	// No bindings = deny
	allowed := auth.Authorize("alice", "get", "buckets", "", "")
	assert.False(t, allowed)
}

func TestAuthorizerAdminAccess(t *testing.T) {
	auth := NewAuthorizer()

	// Grant admin role
	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))

	// Admin can do anything
	assert.True(t, auth.Authorize("alice", "get", "buckets", "", ""))
	assert.True(t, auth.Authorize("alice", "delete", "objects", "my-bucket", ""))
	assert.True(t, auth.Authorize("alice", "put", "objects", "any-bucket", ""))
}

func TestAuthorizerBucketReadAccess(t *testing.T) {
	auth := NewAuthorizer()

	auth.Bindings.Add(NewRoleBinding("bob", RoleBucketRead, ""))

	// Can read
	assert.True(t, auth.Authorize("bob", "get", "buckets", "", ""))
	assert.True(t, auth.Authorize("bob", "list", "objects", "my-bucket", ""))

	// Cannot write or delete
	assert.False(t, auth.Authorize("bob", "put", "objects", "my-bucket", ""))
	assert.False(t, auth.Authorize("bob", "delete", "buckets", "my-bucket", ""))
}

func TestAuthorizerScopedBinding(t *testing.T) {
	auth := NewAuthorizer()

	// Bob has bucket-write only on "my-bucket"
	auth.Bindings.Add(NewRoleBinding("bob", RoleBucketWrite, "my-bucket"))

	// Can write to my-bucket
	assert.True(t, auth.Authorize("bob", "put", "objects", "my-bucket", ""))

	// Cannot write to other-bucket
	assert.False(t, auth.Authorize("bob", "put", "objects", "other-bucket", ""))
}

func TestAuthorizerSystemBucketAccess(t *testing.T) {
	auth := NewAuthorizer()

	// System role should only access _tunnelmesh/
	auth.Bindings.Add(NewRoleBinding("svc:coordinator", RoleSystem, ""))

	// Can access system bucket
	assert.True(t, auth.Authorize("svc:coordinator", "put", "objects", "_tunnelmesh", ""))
	assert.True(t, auth.Authorize("svc:coordinator", "get", "objects", "_tunnelmesh", ""))

	// Cannot access other buckets
	assert.False(t, auth.Authorize("svc:coordinator", "get", "objects", "user-bucket", ""))
}

func TestAuthorizerMultipleBindings(t *testing.T) {
	auth := NewAuthorizer()

	// Alice has read on bucket1 and write on bucket2
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketRead, "bucket1"))
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketWrite, "bucket2"))

	// Can read bucket1
	assert.True(t, auth.Authorize("alice", "get", "objects", "bucket1", ""))
	assert.False(t, auth.Authorize("alice", "put", "objects", "bucket1", ""))

	// Can write bucket2
	assert.True(t, auth.Authorize("alice", "put", "objects", "bucket2", ""))
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

	assert.True(t, auth.Authorize("alice", "get", "buckets", "", ""))
	assert.False(t, auth.Authorize("alice", "delete", "buckets", "", ""))
}

func TestAuthorizerGetPeerRoles(t *testing.T) {
	auth := NewAuthorizer()

	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))
	auth.Bindings.Add(NewRoleBinding("alice", RoleBucketRead, "bucket1"))

	roles := auth.GetPeerRoles("alice")
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
	assert.True(t, auth.Authorize("alice", "get", "objects", "any-bucket", ""))
	assert.True(t, auth.Authorize("alice", "list", "objects", "any-bucket", ""))

	// Alice should not have write access
	assert.False(t, auth.Authorize("alice", "put", "objects", "any-bucket", ""))

	// Bob is not in the group, should be denied
	assert.False(t, auth.Authorize("bob", "get", "objects", "any-bucket", ""))
}

func TestAuthorizer_AuthorizeWithScopedGroupBinding(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Add alice to everyone group
	_ = auth.Groups.AddMember(GroupEveryone, "alice")

	// Everyone gets bucket-read only on fs+data bucket
	auth.GroupBindings.Add(NewGroupBinding(GroupEveryone, RoleBucketRead, "fs+data"))

	// Alice can read fs+data
	assert.True(t, auth.Authorize("alice", "get", "objects", "fs+data", ""))

	// Alice cannot read other buckets via this binding
	assert.False(t, auth.Authorize("alice", "get", "objects", "other-bucket", ""))
}

func TestAuthorizer_UserBindingTakesPrecedence(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Alice has direct admin binding
	auth.Bindings.Add(NewRoleBinding("alice", RoleAdmin, ""))

	// Alice is also in a group with read-only access
	_ = auth.Groups.AddMember("readers", "alice")
	auth.GroupBindings.Add(NewGroupBinding("readers", RoleBucketRead, ""))

	// Alice should have admin access (from direct binding)
	assert.True(t, auth.Authorize("alice", "delete", "buckets", "any-bucket", ""))
}

func TestAuthorizer_IsAdmin_ViaGroup(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Alice is not admin initially
	assert.False(t, auth.IsAdmin("alice"))

	// Add alice to admins group
	_ = auth.Groups.AddMember(GroupAdmins, "alice")

	// Now alice should be considered admin
	assert.True(t, auth.IsAdmin("alice"))
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
	assert.True(t, auth.Authorize("alice", "get", "objects", "any-bucket", ""))
	assert.True(t, auth.Authorize("alice", "put", "objects", "any-bucket", ""))
}

func TestAuthorizer_HasHumanAdmin_ViaGroup(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	assert.False(t, auth.HasHumanAdmin())

	// Add service user to admin group - should not count
	_ = auth.Groups.AddMember(GroupAdmins, "svc:coordinator")
	assert.False(t, auth.HasHumanAdmin())

	// Add human user to admin group
	_ = auth.Groups.AddMember(GroupAdmins, "alice")
	assert.True(t, auth.HasHumanAdmin())
}

// --- Object-level prefix authorization tests ---

func TestAuthorizer_ObjectPrefix(t *testing.T) {
	auth := NewAuthorizer()

	// User with prefix-scoped write access
	binding := NewRoleBinding("alice", RoleBucketWrite, "projects")
	binding.ObjectPrefix = "teamA/"
	auth.Bindings.Add(binding)

	tests := []struct {
		name   string
		userID string
		verb   string
		bucket string
		key    string
		want   bool
	}{
		{"can write to teamA", "alice", "put", "projects", "teamA/doc.txt", true},
		{"can write nested", "alice", "put", "projects", "teamA/sub/doc.txt", true},
		{"cannot write to teamB", "alice", "put", "projects", "teamB/doc.txt", false},
		{"cannot write to root", "alice", "put", "projects", "doc.txt", false},
		{"can list bucket (empty key)", "alice", "list", "projects", "", true},
		{"can get from teamA", "alice", "get", "projects", "teamA/file.txt", true},
		{"cannot get from teamB", "alice", "get", "projects", "teamB/file.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := auth.Authorize(tt.userID, tt.verb, "objects", tt.bucket, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAuthorizer_ObjectPrefix_Group(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Create group and add user
	_, _ = auth.Groups.Create("teamA", "Team A developers")
	_ = auth.Groups.AddMember("teamA", "alice")

	// Grant teamA group access to projects/teamA/ prefix
	binding := NewGroupBinding("teamA", RoleBucketWrite, "projects")
	binding.ObjectPrefix = "teamA/"
	auth.GroupBindings.Add(binding)

	// Alice (via group) can write to teamA/
	assert.True(t, auth.Authorize("alice", "put", "objects", "projects", "teamA/doc.txt"))

	// Alice cannot write to teamB/
	assert.False(t, auth.Authorize("alice", "put", "objects", "projects", "teamB/doc.txt"))

	// Bob (not in group) cannot write to teamA/
	assert.False(t, auth.Authorize("bob", "put", "objects", "projects", "teamA/doc.txt"))
}

func TestAuthorizer_ObjectPrefix_MultipleBindings(t *testing.T) {
	auth := NewAuthorizer()

	// Alice has access to two different prefixes
	bindingA := NewRoleBinding("alice", RoleBucketWrite, "projects")
	bindingA.ObjectPrefix = "teamA/"
	auth.Bindings.Add(bindingA)

	bindingB := NewRoleBinding("alice", RoleBucketRead, "projects")
	bindingB.ObjectPrefix = "shared/"
	auth.Bindings.Add(bindingB)

	// Can write to teamA
	assert.True(t, auth.Authorize("alice", "put", "objects", "projects", "teamA/doc.txt"))

	// Can read from shared
	assert.True(t, auth.Authorize("alice", "get", "objects", "projects", "shared/doc.txt"))

	// Cannot write to shared (only read access)
	assert.False(t, auth.Authorize("alice", "put", "objects", "projects", "shared/doc.txt"))

	// Cannot access teamC
	assert.False(t, auth.Authorize("alice", "get", "objects", "projects", "teamC/doc.txt"))
}

func TestAuthorizer_GetAllowedPrefixes(t *testing.T) {
	auth := NewAuthorizer()

	// Alice has access to two prefixes in projects bucket
	bindingA := NewRoleBinding("alice", RoleBucketWrite, "projects")
	bindingA.ObjectPrefix = "teamA/"
	auth.Bindings.Add(bindingA)

	bindingB := NewRoleBinding("alice", RoleBucketRead, "projects")
	bindingB.ObjectPrefix = "shared/"
	auth.Bindings.Add(bindingB)

	// Bob has unrestricted access to projects
	auth.Bindings.Add(NewRoleBinding("bob", RoleBucketAdmin, "projects"))

	// Alice should get list of prefixes she can access
	prefixes := auth.GetAllowedPrefixes("alice", "projects")
	require.NotNil(t, prefixes)
	assert.Len(t, prefixes, 2)
	assert.Contains(t, prefixes, "teamA/")
	assert.Contains(t, prefixes, "shared/")

	// Bob has unrestricted access, should return nil
	prefixes = auth.GetAllowedPrefixes("bob", "projects")
	assert.Nil(t, prefixes)

	// Charlie has no access, should return empty slice
	prefixes = auth.GetAllowedPrefixes("charlie", "projects")
	require.NotNil(t, prefixes)
	assert.Empty(t, prefixes)
}

func TestAuthorizer_GetAllowedPrefixes_WithGroups(t *testing.T) {
	auth := NewAuthorizerWithGroups()

	// Alice is in teamA group
	_, _ = auth.Groups.Create("teamA", "")
	_ = auth.Groups.AddMember("teamA", "alice")

	// teamA group has access to teamA/ prefix
	binding := NewGroupBinding("teamA", RoleBucketWrite, "projects")
	binding.ObjectPrefix = "teamA/"
	auth.GroupBindings.Add(binding)

	// Alice also has direct access to shared/
	directBinding := NewRoleBinding("alice", RoleBucketRead, "projects")
	directBinding.ObjectPrefix = "shared/"
	auth.Bindings.Add(directBinding)

	prefixes := auth.GetAllowedPrefixes("alice", "projects")
	require.NotNil(t, prefixes)
	assert.Len(t, prefixes, 2)
	assert.Contains(t, prefixes, "teamA/")
	assert.Contains(t, prefixes, "shared/")
}
