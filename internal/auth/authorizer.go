package auth

import (
	"strings"
	"sync"
)

// SystemBucket is the reserved bucket name for coordinator internal data.
const SystemBucket = "_tunnelmesh"

// Authorizer handles RBAC authorization decisions.
type Authorizer struct {
	Bindings      *BindingStore
	Groups        *GroupStore        // Optional: for group-based authorization
	GroupBindings *GroupBindingStore // Optional: for group-based authorization
	roles         map[string]*Role   // role name -> role
	mu            sync.RWMutex
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

// NewAuthorizerWithGroups creates an authorizer with group support enabled.
func NewAuthorizerWithGroups() *Authorizer {
	auth := NewAuthorizer()
	auth.Groups = NewGroupStore()
	auth.GroupBindings = NewGroupBindingStore()
	return auth
}

// Authorize checks if a user can perform a verb on a resource in a bucket.
// Returns true if any of the user's direct bindings or group bindings allow the action.
func (a *Authorizer) Authorize(userID, verb, resource, bucketName string) bool {
	// Check direct user bindings first
	if a.checkUserBindings(userID, verb, resource, bucketName) {
		return true
	}

	// Check group bindings if groups are enabled
	if a.Groups != nil && a.GroupBindings != nil {
		return a.checkGroupBindings(userID, verb, resource, bucketName)
	}

	return false
}

// checkUserBindings checks direct user role bindings.
func (a *Authorizer) checkUserBindings(userID, verb, resource, bucketName string) bool {
	bindings := a.Bindings.GetForUser(userID)

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

// checkGroupBindings checks group-based role bindings.
func (a *Authorizer) checkGroupBindings(userID, verb, resource, bucketName string) bool {
	// Get all groups the user belongs to
	userGroups := a.Groups.GetGroupsForUser(userID)

	for _, groupName := range userGroups {
		bindings := a.GroupBindings.GetForGroup(groupName)

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
	}

	return false
}

// IsAdmin checks if a user has the admin role.
// Checks both direct bindings and group membership in all_admin_users.
func (a *Authorizer) IsAdmin(userID string) bool {
	// Check direct admin binding
	for _, binding := range a.Bindings.GetForUser(userID) {
		if binding.RoleName == RoleAdmin {
			return true
		}
	}

	// Check group membership if groups are enabled
	if a.Groups != nil {
		return a.Groups.IsMember(GroupAllAdminUsers, userID)
	}

	return false
}

// HasHumanAdmin checks if there is at least one human admin (non-service user).
// Checks both direct bindings and group membership.
func (a *Authorizer) HasHumanAdmin() bool {
	// Check direct bindings
	for _, binding := range a.Bindings.List() {
		if binding.RoleName == RoleAdmin && !strings.HasPrefix(binding.UserID, ServiceUserPrefix) {
			return true
		}
	}

	// Check all_admin_users group membership if groups are enabled
	if a.Groups != nil {
		group := a.Groups.Get(GroupAllAdminUsers)
		if group != nil {
			for _, member := range group.Members {
				if !strings.HasPrefix(member, ServiceUserPrefix) {
					return true
				}
			}
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
