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
