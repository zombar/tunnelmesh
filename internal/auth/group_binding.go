package auth

import (
	"strings"
	"sync"

	"github.com/google/uuid"
)

// GroupBinding binds a group to a role with optional bucket and object prefix scope.
type GroupBinding struct {
	Name         string `json:"name"`                    // Unique binding name
	GroupName    string `json:"group_name"`              // Group being granted access
	RoleName     string `json:"role_name"`               // Role being granted
	BucketScope  string `json:"bucket_scope,omitempty"`  // Optional: scope to specific bucket
	ObjectPrefix string `json:"object_prefix,omitempty"` // Optional: scope to object key prefix
}

// NewGroupBinding creates a new group binding.
func NewGroupBinding(groupName, roleName, bucketScope string) *GroupBinding {
	return NewGroupBindingWithPrefix(groupName, roleName, bucketScope, "")
}

// NewGroupBindingWithPrefix creates a new group binding with object prefix.
func NewGroupBindingWithPrefix(groupName, roleName, bucketScope, objectPrefix string) *GroupBinding {
	return &GroupBinding{
		Name:         uuid.New().String()[:8],
		GroupName:    groupName,
		RoleName:     roleName,
		BucketScope:  bucketScope,
		ObjectPrefix: objectPrefix,
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

// AppliesToObject checks if this binding applies to a bucket and object key.
// Empty key (bucket-level operations like list) always passes the prefix check.
func (gb *GroupBinding) AppliesToObject(bucket, key string) bool {
	// Check bucket scope first
	if gb.BucketScope != "" && gb.BucketScope != bucket {
		return false
	}
	// Check object prefix (empty key = bucket-level op, always allowed)
	if gb.ObjectPrefix != "" && key != "" {
		return strings.HasPrefix(key, gb.ObjectPrefix)
	}
	return true
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
