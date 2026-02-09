package auth

import (
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RoleBinding binds a peer to a role with optional bucket, object prefix, or panel scope.
type RoleBinding struct {
	Name         string    `json:"name"`                    // Unique binding name
	PeerID       string    `json:"peer_id"`                 // Peer being granted access
	RoleName     string    `json:"role_name"`               // Role being granted
	BucketScope  string    `json:"bucket_scope,omitempty"`  // Optional: scope to specific bucket
	ObjectPrefix string    `json:"object_prefix,omitempty"` // Optional: scope to object key prefix
	PanelScope   string    `json:"panel_scope,omitempty"`   // Optional: scope to specific panel ID
	CreatedAt    time.Time `json:"created_at"`              // When this binding was created
}

// NewRoleBinding creates a new role binding.
func NewRoleBinding(peerID, roleName, bucketScope string) *RoleBinding {
	return NewRoleBindingWithPrefix(peerID, roleName, bucketScope, "")
}

// NewRoleBindingWithPrefix creates a new role binding with object prefix.
func NewRoleBindingWithPrefix(peerID, roleName, bucketScope, objectPrefix string) *RoleBinding {
	return &RoleBinding{
		Name:         uuid.New().String()[:8],
		PeerID:       peerID,
		RoleName:     roleName,
		BucketScope:  bucketScope,
		ObjectPrefix: objectPrefix,
		CreatedAt:    time.Now().UTC(),
	}
}

// NewRoleBindingForPanel creates a new role binding for panel access.
func NewRoleBindingForPanel(peerID, panelID string) *RoleBinding {
	return &RoleBinding{
		Name:       uuid.New().String()[:8],
		PeerID:     peerID,
		RoleName:   RolePanelViewer,
		PanelScope: panelID,
		CreatedAt:  time.Now().UTC(),
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

// AppliesToObject checks if this binding applies to a bucket and object key.
// Empty key (bucket-level operations like list) always passes the prefix check.
func (rb *RoleBinding) AppliesToObject(bucket, key string) bool {
	// Check bucket scope first
	if rb.BucketScope != "" && rb.BucketScope != bucket {
		return false
	}
	// Check object prefix (empty key = bucket-level op, always allowed)
	if rb.ObjectPrefix != "" && key != "" {
		return strings.HasPrefix(key, rb.ObjectPrefix)
	}
	return true
}

// AppliesToPanel checks if this binding grants access to a panel.
// Empty PanelScope means access to all panels (for this role).
func (rb *RoleBinding) AppliesToPanel(panelID string) bool {
	if rb.PanelScope == "" {
		return true // No scope = all panels
	}
	return rb.PanelScope == panelID
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

// Get returns a binding by name.
func (bs *BindingStore) Get(name string) *RoleBinding {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	return bs.bindings[name]
}

// GetForPeer returns all bindings for a peer.
func (bs *BindingStore) GetForPeer(peerID string) []*RoleBinding {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	var result []*RoleBinding
	for _, b := range bs.bindings {
		if b.PeerID == peerID {
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
