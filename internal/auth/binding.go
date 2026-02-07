package auth

import (
	"sync"

	"github.com/google/uuid"
)

// RoleBinding binds a user to a role with optional bucket scope.
type RoleBinding struct {
	Name        string `json:"name"`                   // Unique binding name
	UserID      string `json:"user_id"`                // User being granted access
	RoleName    string `json:"role_name"`              // Role being granted
	BucketScope string `json:"bucket_scope,omitempty"` // Optional: scope to specific bucket
}

// NewRoleBinding creates a new role binding.
func NewRoleBinding(userID, roleName, bucketScope string) *RoleBinding {
	return &RoleBinding{
		Name:        uuid.New().String()[:8],
		UserID:      userID,
		RoleName:    roleName,
		BucketScope: bucketScope,
	}
}

// AppliesToBucket checks if this binding applies to the given bucket.
// Unscoped bindings (empty BucketScope) apply to all buckets.
func (rb *RoleBinding) AppliesToBucket(bucketName string) bool {
	if rb.BucketScope == "" {
		return true
	}
	return rb.BucketScope == bucketName
}

// BindingStore manages role bindings in memory.
type BindingStore struct {
	bindings map[string]*RoleBinding // keyed by binding name
	mu       sync.RWMutex
}

// NewBindingStore creates a new binding store.
func NewBindingStore() *BindingStore {
	return &BindingStore{
		bindings: make(map[string]*RoleBinding),
	}
}

// Add adds a role binding to the store.
func (bs *BindingStore) Add(binding *RoleBinding) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.bindings[binding.Name] = binding
}

// Remove removes a role binding by name.
func (bs *BindingStore) Remove(name string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	delete(bs.bindings, name)
}

// GetForUser returns all bindings for a user.
func (bs *BindingStore) GetForUser(userID string) []*RoleBinding {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	var result []*RoleBinding
	for _, b := range bs.bindings {
		if b.UserID == userID {
			result = append(result, b)
		}
	}
	return result
}

// List returns all bindings.
func (bs *BindingStore) List() []*RoleBinding {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	result := make([]*RoleBinding, 0, len(bs.bindings))
	for _, b := range bs.bindings {
		result = append(result, b)
	}
	return result
}

// LoadBindings loads bindings from a slice (e.g., from JSON).
func (bs *BindingStore) LoadBindings(bindings []*RoleBinding) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.bindings = make(map[string]*RoleBinding)
	for _, b := range bindings {
		bs.bindings[b.Name] = b
	}
}
