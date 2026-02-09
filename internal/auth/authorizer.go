package auth

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/logging/audit"
)

// SystemBucket is the reserved bucket name for coordinator internal data.
const SystemBucket = "_tunnelmesh"

// Authorizer handles RBAC authorization decisions.
type Authorizer struct {
	Bindings      *BindingStore
	Groups        *GroupStore        // Optional: for group-based authorization
	GroupBindings *GroupBindingStore // Optional: for group-based authorization
	PanelRegistry *PanelRegistry     // Panel definitions and registry
	roles         map[string]*Role   // role name -> role
	mu            sync.RWMutex
	auditLogger   atomic.Pointer[audit.Logger] // Optional: for security audit logging (lock-free)
}

// NewAuthorizer creates a new authorizer with built-in roles.
func NewAuthorizer() *Authorizer {
	return NewAuthorizerWithRoles(BuiltinRoles())
}

// NewAuthorizerWithRoles creates an authorizer with custom roles.
func NewAuthorizerWithRoles(roles []Role) *Authorizer {
	auth := &Authorizer{
		Bindings:      NewBindingStore(),
		PanelRegistry: NewPanelRegistry(),
		roles:         make(map[string]*Role),
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

// Authorize checks if a peer can perform a verb on a resource in a bucket.
// The objectKey parameter is used for object-level prefix permission checks.
// Returns true if any of the peer's direct bindings or group bindings allow the action.
func (a *Authorizer) Authorize(peerID, verb, resource, bucketName, objectKey string) bool {
	// Check direct peer bindings first
	allowed := a.checkPeerBindings(peerID, verb, resource, bucketName, objectKey)

	// Check group bindings if groups are enabled and not already allowed
	if !allowed && a.Groups != nil && a.GroupBindings != nil {
		allowed = a.checkGroupBindings(peerID, verb, resource, bucketName, objectKey)
	}

	// Log authorization decision for security audit (lock-free read)
	if logger := a.auditLogger.Load(); logger != nil {
		result := "allowed"
		reason := ""
		if !allowed {
			result = "denied"
			reason = "no matching role binding"
		}
		logger.LogAuthz(peerID, verb, resource, bucketName, objectKey, result, reason)
	}

	return allowed
}

// checkPeerBindings checks direct peer role bindings.
func (a *Authorizer) checkPeerBindings(peerID, verb, resource, bucketName, objectKey string) bool {
	bindings := a.Bindings.GetForPeer(peerID)

	for _, binding := range bindings {
		// Check bucket and object prefix scope
		if !binding.AppliesToObject(bucketName, objectKey) {
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
func (a *Authorizer) checkGroupBindings(peerID, verb, resource, bucketName, objectKey string) bool {
	// Get all groups the peer belongs to
	peerGroups := a.Groups.GetGroupsForPeer(peerID)

	for _, groupName := range peerGroups {
		bindings := a.GroupBindings.GetForGroup(groupName)

		for _, binding := range bindings {
			// Check bucket and object prefix scope
			if !binding.AppliesToObject(bucketName, objectKey) {
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

// IsAdmin checks if a peer has the admin role.
// Checks both direct bindings and group membership in admins.
func (a *Authorizer) IsAdmin(peerID string) bool {
	// Check direct admin binding
	for _, binding := range a.Bindings.GetForPeer(peerID) {
		if binding.RoleName == RoleAdmin {
			return true
		}
	}

	// Check group membership if groups are enabled
	if a.Groups != nil {
		return a.Groups.IsMember(GroupAdmins, peerID)
	}

	return false
}

// HasHumanAdmin checks if there is at least one human admin (non-service peer).
// Checks both direct bindings and group membership.
func (a *Authorizer) HasHumanAdmin() bool {
	// Check direct bindings
	for _, binding := range a.Bindings.List() {
		if binding.RoleName == RoleAdmin && !strings.HasPrefix(binding.PeerID, ServicePeerPrefix) {
			return true
		}
	}

	// Check admins group membership if groups are enabled
	if a.Groups != nil {
		group := a.Groups.Get(GroupAdmins)
		if group != nil {
			for _, member := range group.Members {
				if !strings.HasPrefix(member, ServicePeerPrefix) {
					return true
				}
			}
		}
	}

	return false
}

// GetPeerRoles returns the names of all roles assigned to a peer.
func (a *Authorizer) GetPeerRoles(peerID string) []string {
	bindings := a.Bindings.GetForPeer(peerID)
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

// SetAuditLogger sets the audit logger for security event logging.
// This is optional - if not set, no audit logging will occur.
// Safe for concurrent use (lock-free atomic operation).
func (a *Authorizer) SetAuditLogger(logger *audit.Logger) {
	a.auditLogger.Store(logger)
}

// GetAllowedPrefixes returns all object prefixes a peer can access in a bucket.
// Returns nil if peer has unrestricted access (at least one binding with no prefix).
// Returns empty slice if peer has no access to the bucket.
func (a *Authorizer) GetAllowedPrefixes(peerID, bucketName string) []string {
	prefixes := make(map[string]bool)
	hasUnrestrictedAccess := false
	hasAnyAccess := false

	// Check direct bindings
	for _, binding := range a.Bindings.GetForPeer(peerID) {
		if !binding.AppliesToBucket(bucketName) {
			continue
		}
		hasAnyAccess = true
		if binding.ObjectPrefix == "" {
			hasUnrestrictedAccess = true
		} else {
			prefixes[binding.ObjectPrefix] = true
		}
	}

	// Check group bindings if groups are enabled
	if a.Groups != nil && a.GroupBindings != nil {
		for _, groupName := range a.Groups.GetGroupsForPeer(peerID) {
			for _, binding := range a.GroupBindings.GetForGroup(groupName) {
				if !binding.AppliesToBucket(bucketName) {
					continue
				}
				hasAnyAccess = true
				if binding.ObjectPrefix == "" {
					hasUnrestrictedAccess = true
				} else {
					prefixes[binding.ObjectPrefix] = true
				}
			}
		}
	}

	// Unrestricted access = nil (no filtering needed)
	if hasUnrestrictedAccess {
		return nil
	}

	// No access = empty slice
	if !hasAnyAccess {
		return []string{}
	}

	// Convert map to slice
	result := make([]string, 0, len(prefixes))
	for prefix := range prefixes {
		result = append(result, prefix)
	}
	return result
}

// CanAccessPanel checks if a peer can view a panel.
// Returns true if: panel is public, peer is admin, or peer has panel-viewer binding.
func (a *Authorizer) CanAccessPanel(peerID, panelID string) bool {
	// Check if panel is public (no auth required)
	if a.PanelRegistry != nil {
		if panel := a.PanelRegistry.Get(panelID); panel != nil && panel.Public {
			return true
		}
	}

	// Admin always has access
	if a.IsAdmin(peerID) {
		return true
	}

	// Check direct peer bindings for panel-viewer role
	for _, binding := range a.Bindings.GetForPeer(peerID) {
		if binding.RoleName == RolePanelViewer && binding.AppliesToPanel(panelID) {
			return true
		}
		// Admin role also grants all panel access
		if binding.RoleName == RoleAdmin {
			return true
		}
	}

	// Check group bindings if groups are enabled
	if a.Groups != nil && a.GroupBindings != nil {
		for _, groupName := range a.Groups.GetGroupsForPeer(peerID) {
			for _, binding := range a.GroupBindings.GetForGroup(groupName) {
				if binding.RoleName == RolePanelViewer && binding.AppliesToPanel(panelID) {
					return true
				}
				// Admin role also grants all panel access
				if binding.RoleName == RoleAdmin {
					return true
				}
			}
		}
	}

	log.Info().Str("peer_id", peerID).Str("panel_id", panelID).Msg("dashboard panel access denied")
	return false
}

// GetAccessiblePanels returns all panel IDs a peer can access.
// Always returns a non-nil slice (empty slice if no access) for consistent JSON encoding.
func (a *Authorizer) GetAccessiblePanels(peerID string) []string {
	if a.PanelRegistry == nil {
		log.Info().Str("peer_id", peerID).Msg("dashboard panel permissions: no registry configured")
		return []string{}
	}

	// Admin gets all panels
	if a.IsAdmin(peerID) {
		panels := a.PanelRegistry.ListIDs()
		log.Debug().Str("peer_id", peerID).Int("count", len(panels)).Msg("dashboard panel permissions: admin access")
		return panels
	}

	// Collect accessible panels
	accessible := make(map[string]bool)

	// Add public panels
	for _, panel := range a.PanelRegistry.ListPublic() {
		accessible[panel.ID] = true
	}

	// Check direct peer bindings
	for _, binding := range a.Bindings.GetForPeer(peerID) {
		if binding.RoleName == RolePanelViewer {
			if binding.PanelScope == "" {
				// Unrestricted panel access - return all panels
				panels := a.PanelRegistry.ListIDs()
				log.Debug().Str("peer_id", peerID).Int("count", len(panels)).Msg("dashboard panel permissions: unrestricted peer binding")
				return panels
			}
			accessible[binding.PanelScope] = true
		}
	}

	// Check group bindings if groups are enabled
	if a.Groups != nil && a.GroupBindings != nil {
		peerGroups := a.Groups.GetGroupsForPeer(peerID)
		for _, groupName := range peerGroups {
			for _, binding := range a.GroupBindings.GetForGroup(groupName) {
				if binding.RoleName == RolePanelViewer {
					if binding.PanelScope == "" {
						// Unrestricted panel access - return all panels
						panels := a.PanelRegistry.ListIDs()
						log.Debug().Str("peer_id", peerID).Str("group", groupName).Int("count", len(panels)).Msg("dashboard panel permissions: unrestricted group binding")
						return panels
					}
					accessible[binding.PanelScope] = true
				}
			}
		}
	}

	// Convert map to slice
	result := make([]string, 0, len(accessible))
	for panelID := range accessible {
		result = append(result, panelID)
	}

	// Log accessible panels (INFO for non-admin peers with limited access)
	if len(result) == 0 {
		var peerGroups []string
		if a.Groups != nil {
			peerGroups = a.Groups.GetGroupsForPeer(peerID)
		}
		log.Info().Str("peer_id", peerID).Strs("groups", peerGroups).Msg("dashboard panel permissions: no panels accessible")
	} else {
		log.Debug().Str("peer_id", peerID).Int("count", len(result)).Strs("panels", result).Msg("dashboard panel permissions: granted")
	}

	return result
}
