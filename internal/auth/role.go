package auth

// Built-in role names
const (
	RoleSystem      = "system"       // Coordinator internal access to _tunnelmesh/
	RoleAdmin       = "admin"        // Full access to all buckets
	RoleBucketAdmin = "bucket-admin" // Create/delete buckets, full object access
	RoleBucketWrite = "bucket-write" // Read buckets, read/write objects
	RoleBucketRead  = "bucket-read"  // Read-only access
	RolePanelViewer = "panel-viewer" // View UI panels (scoped by PanelScope)
)

// Resource types for authorization
const (
	ResourceBuckets = "buckets"
	ResourceObjects = "objects"
	ResourcePanels  = "panels"
)

// Role defines a set of permissions.
type Role struct {
	Name    string `json:"name"`
	Rules   []Rule `json:"rules"`
	Builtin bool   `json:"builtin,omitempty"` // True for built-in roles
}

// Rule defines permissions for a set of resources.
type Rule struct {
	Verbs     []string `json:"verbs"`     // e.g., ["get", "list", "put", "delete"]
	Resources []string `json:"resources"` // e.g., ["buckets", "objects"]
}

// Matches checks if the role allows the given verb on the given resource.
func (r *Role) Matches(verb, resource string) bool {
	for _, rule := range r.Rules {
		if rule.Matches(verb, resource) {
			return true
		}
	}
	return false
}

// Matches checks if the rule allows the given verb on the given resource.
func (r *Rule) Matches(verb, resource string) bool {
	verbMatch := false
	for _, v := range r.Verbs {
		if v == "*" || v == verb {
			verbMatch = true
			break
		}
	}
	if !verbMatch {
		return false
	}

	for _, res := range r.Resources {
		if res == "*" || res == resource {
			return true
		}
	}
	return false
}

// BuiltinRoles returns all built-in roles.
func BuiltinRoles() []Role {
	return []Role{
		{
			Name:    RoleSystem,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"*"}, Resources: []string{"*"}}, // Scoped to _tunnelmesh/ by authorizer
			},
		},
		{
			Name:    RoleAdmin,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"*"}, Resources: []string{"*"}},
			},
		},
		{
			Name:    RoleBucketAdmin,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"create", "delete", "get", "list"}, Resources: []string{"buckets"}},
				{Verbs: []string{"*"}, Resources: []string{"objects"}},
			},
		},
		{
			Name:    RoleBucketWrite,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"get", "list"}, Resources: []string{"buckets"}},
				{Verbs: []string{"get", "put", "delete", "list"}, Resources: []string{"objects"}},
			},
		},
		{
			Name:    RoleBucketRead,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"get", "list"}, Resources: []string{"buckets"}},
				{Verbs: []string{"get", "list"}, Resources: []string{"objects"}},
			},
		},
		{
			Name:    RolePanelViewer,
			Builtin: true,
			Rules: []Rule{
				{Verbs: []string{"view"}, Resources: []string{"panels"}},
			},
		},
	}
}

// GetBuiltinRole returns a built-in role by name, or nil if not found.
func GetBuiltinRole(name string) *Role {
	for _, role := range BuiltinRoles() {
		if role.Name == name {
			return &role
		}
	}
	return nil
}
