package auth

import (
	"sync"

	"github.com/google/uuid"
)

// GroupBinding binds a group to a role with optional bucket scope.
type GroupBinding struct {
	Name        string `json:"name"`                   // Unique binding name
	GroupName   string `json:"group_name"`             // Group being granted access
	RoleName    string `json:"role_name"`              // Role being granted
	BucketScope string `json:"bucket_scope,omitempty"` // Optional: scope to specific bucket
}

// NewGroupBinding creates a new group binding.
func NewGroupBinding(groupName, roleName, bucketScope string) *GroupBinding {
	return &GroupBinding{
		Name:        uuid.New().String()[:8],
		GroupName:   groupName,
		RoleName:    roleName,
		BucketScope: bucketScope,
	}
}

// AppliesToBucket checks if this binding applies to the given bucket.
// Unscoped bindings (empty BucketScope) apply to all buckets.
func (gb *GroupBinding) AppliesToBucket(bucketName string) bool {
	if gb.BucketScope == "" {
		return true
	}
	return gb.BucketScope == bucketName
}

// GroupBindingStore manages group bindings in memory.
type GroupBindingStore struct {
	bindings map[string]*GroupBinding // keyed by binding name
	mu       sync.RWMutex
}

// NewGroupBindingStore creates a new group binding store.
func NewGroupBindingStore() *GroupBindingStore {
	return &GroupBindingStore{
		bindings: make(map[string]*GroupBinding),
	}
}

// Add adds a group binding to the store.
func (gbs *GroupBindingStore) Add(binding *GroupBinding) {
	gbs.mu.Lock()
	defer gbs.mu.Unlock()
	gbs.bindings[binding.Name] = binding
}

// Remove removes a group binding by name.
func (gbs *GroupBindingStore) Remove(name string) {
	gbs.mu.Lock()
	defer gbs.mu.Unlock()
	delete(gbs.bindings, name)
}

// GetForGroup returns all bindings for a group.
func (gbs *GroupBindingStore) GetForGroup(groupName string) []*GroupBinding {
	gbs.mu.RLock()
	defer gbs.mu.RUnlock()

	var result []*GroupBinding
	for _, b := range gbs.bindings {
		if b.GroupName == groupName {
			result = append(result, b)
		}
	}
	return result
}

// List returns all bindings.
func (gbs *GroupBindingStore) List() []*GroupBinding {
	gbs.mu.RLock()
	defer gbs.mu.RUnlock()

	result := make([]*GroupBinding, 0, len(gbs.bindings))
	for _, b := range gbs.bindings {
		result = append(result, b)
	}
	return result
}

// LoadBindings loads bindings from a slice (e.g., from JSON).
func (gbs *GroupBindingStore) LoadBindings(bindings []*GroupBinding) {
	gbs.mu.Lock()
	defer gbs.mu.Unlock()

	gbs.bindings = make(map[string]*GroupBinding)
	for _, b := range bindings {
		gbs.bindings[b.Name] = b
	}
}

// RemoveForGroup removes all bindings for a group.
func (gbs *GroupBindingStore) RemoveForGroup(groupName string) {
	gbs.mu.Lock()
	defer gbs.mu.Unlock()

	for name, b := range gbs.bindings {
		if b.GroupName == groupName {
			delete(gbs.bindings, name)
		}
	}
}
