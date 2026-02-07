package auth

import (
	"strings"
	"sync"
)

// SystemBucket is the reserved bucket name for coordinator internal data.
const SystemBucket = "_tunnelmesh"

// Authorizer handles RBAC authorization decisions.
type Authorizer struct {
	Bindings *BindingStore
	roles    map[string]*Role // role name -> role
	mu       sync.RWMutex
}

// NewAuthorizer creates a new authorizer with built-in roles.
func NewAuthorizer() *Authorizer {
	return NewAuthorizerWithRoles(BuiltinRoles())
}

// NewAuthorizerWithRoles creates an authorizer with custom roles.
func NewAuthorizerWithRoles(roles []Role) *Authorizer {
	auth := &Authorizer{
		Bindings: NewBindingStore(),
		roles:    make(map[string]*Role),
	}

	// Add built-in roles first
	for _, r := range BuiltinRoles() {
		roleCopy := r
		auth.roles[r.Name] = &roleCopy
	}

	// Add/override with provided roles
	for _, r := range roles {
		roleCopy := r
		auth.roles[r.Name] = &roleCopy
	}

	return auth
}

// Authorize checks if a user can perform a verb on a resource in a bucket.
// Returns true if any of the user's bindings allow the action.
func (a *Authorizer) Authorize(userID, verb, resource, bucketName string) bool {
	bindings := a.Bindings.GetForUser(userID)
	if len(bindings) == 0 {
		return false
	}

	for _, binding := range bindings {
		// Check bucket scope
		if !binding.AppliesToBucket(bucketName) {
			continue
		}

		// Get the role
		a.mu.RLock()
		role, exists := a.roles[binding.RoleName]
		a.mu.RUnlock()

		if !exists {
			continue
		}

		// Special case: system role only applies to _tunnelmesh bucket
		if binding.RoleName == RoleSystem && bucketName != SystemBucket {
			continue
		}

		// Check if role allows this action
		if role.Matches(verb, resource) {
			return true
		}
	}

	return false
}

// IsAdmin checks if a user has the admin role.
func (a *Authorizer) IsAdmin(userID string) bool {
	for _, binding := range a.Bindings.GetForUser(userID) {
		if binding.RoleName == RoleAdmin {
			return true
		}
	}
	return false
}

// HasHumanAdmin checks if there is at least one human admin (non-service user).
func (a *Authorizer) HasHumanAdmin() bool {
	for _, binding := range a.Bindings.List() {
		if binding.RoleName == RoleAdmin && !strings.HasPrefix(binding.UserID, ServiceUserPrefix) {
			return true
		}
	}
	return false
}

// GetUserRoles returns the names of all roles assigned to a user.
func (a *Authorizer) GetUserRoles(userID string) []string {
	bindings := a.Bindings.GetForUser(userID)
	roles := make([]string, 0, len(bindings))
	for _, b := range bindings {
		roles = append(roles, b.RoleName)
	}
	return roles
}

// AddRole adds a custom role to the authorizer.
func (a *Authorizer) AddRole(role Role) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roles[role.Name] = &role
}

// GetRole returns a role by name.
func (a *Authorizer) GetRole(name string) *Role {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.roles[name]
}
