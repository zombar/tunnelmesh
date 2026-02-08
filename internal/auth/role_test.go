package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuiltinRoles(t *testing.T) {
	roles := BuiltinRoles()

	// Should have 6 built-in roles
	assert.Len(t, roles, 6)

	// Verify each role exists
	roleNames := make(map[string]bool)
	for _, r := range roles {
		roleNames[r.Name] = true
	}

	assert.True(t, roleNames[RoleSystem])
	assert.True(t, roleNames[RoleAdmin])
	assert.True(t, roleNames[RoleBucketAdmin])
	assert.True(t, roleNames[RoleBucketWrite])
	assert.True(t, roleNames[RoleBucketRead])
	assert.True(t, roleNames[RolePanelViewer])
}

func TestRoleMatches(t *testing.T) {
	tests := []struct {
		name     string
		role     Role
		verb     string
		resource string
		want     bool
	}{
		{
			name: "admin matches everything",
			role: Role{
				Name:  RoleAdmin,
				Rules: []Rule{{Verbs: []string{"*"}, Resources: []string{"*"}}},
			},
			verb:     "delete",
			resource: "buckets",
			want:     true,
		},
		{
			name: "exact match",
			role: Role{
				Name:  "test",
				Rules: []Rule{{Verbs: []string{"get", "list"}, Resources: []string{"buckets"}}},
			},
			verb:     "get",
			resource: "buckets",
			want:     true,
		},
		{
			name: "verb not allowed",
			role: Role{
				Name:  "test",
				Rules: []Rule{{Verbs: []string{"get", "list"}, Resources: []string{"buckets"}}},
			},
			verb:     "delete",
			resource: "buckets",
			want:     false,
		},
		{
			name: "resource not allowed",
			role: Role{
				Name:  "test",
				Rules: []Rule{{Verbs: []string{"get", "list"}, Resources: []string{"buckets"}}},
			},
			verb:     "get",
			resource: "objects",
			want:     false,
		},
		{
			name: "wildcard verb",
			role: Role{
				Name:  "test",
				Rules: []Rule{{Verbs: []string{"*"}, Resources: []string{"objects"}}},
			},
			verb:     "delete",
			resource: "objects",
			want:     true,
		},
		{
			name: "wildcard resource",
			role: Role{
				Name:  "test",
				Rules: []Rule{{Verbs: []string{"get"}, Resources: []string{"*"}}},
			},
			verb:     "get",
			resource: "anything",
			want:     true,
		},
		{
			name: "multiple rules - second matches",
			role: Role{
				Name: "test",
				Rules: []Rule{
					{Verbs: []string{"get"}, Resources: []string{"buckets"}},
					{Verbs: []string{"put"}, Resources: []string{"objects"}},
				},
			},
			verb:     "put",
			resource: "objects",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.role.Matches(tt.verb, tt.resource)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetBuiltinRole(t *testing.T) {
	role := GetBuiltinRole(RoleAdmin)
	assert.NotNil(t, role)
	assert.Equal(t, RoleAdmin, role.Name)

	role = GetBuiltinRole("nonexistent")
	assert.Nil(t, role)
}
