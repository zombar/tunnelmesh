package s3

import (
	"fmt"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/auth"
)

// FileShareManager manages file shares backed by S3 buckets.
type FileShareManager struct {
	store       *Store
	systemStore *SystemStore
	authorizer  *auth.Authorizer
	shares      []*FileShare
	mu          sync.RWMutex
}

// NewFileShareManager creates a new file share manager.
// It loads existing shares from the system store.
func NewFileShareManager(store *Store, systemStore *SystemStore, authorizer *auth.Authorizer) *FileShareManager {
	mgr := &FileShareManager{
		store:       store,
		systemStore: systemStore,
		authorizer:  authorizer,
	}

	// Load existing shares
	if systemStore != nil {
		shares, _ := systemStore.LoadFileShares()
		mgr.shares = shares
	}

	return mgr
}

// Create creates a new file share.
// It creates the underlying bucket, sets up default permissions, and persists the share metadata.
func (m *FileShareManager) Create(name, description, ownerID string) (*FileShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if share already exists
	for _, s := range m.shares {
		if s.Name == name {
			return nil, fmt.Errorf("file share %q already exists", name)
		}
	}

	bucketName := FileShareBucketPrefix + name

	// Check if bucket already exists
	if _, err := m.store.HeadBucket(bucketName); err == nil {
		return nil, fmt.Errorf("bucket %q already exists", bucketName)
	}

	// Create the bucket
	if err := m.store.CreateBucket(bucketName, ownerID); err != nil {
		return nil, fmt.Errorf("create bucket: %w", err)
	}

	// Create group binding: everyone -> bucket-read
	everyoneBinding := &auth.GroupBinding{
		Name:        fmt.Sprintf("everyone-%s-read", name),
		GroupName:   auth.GroupEveryone,
		RoleName:    auth.RoleBucketRead,
		BucketScope: bucketName,
	}
	m.authorizer.GroupBindings.Add(everyoneBinding)

	// Create role binding: owner -> bucket-admin
	ownerBinding := auth.NewRoleBinding(ownerID, auth.RoleBucketAdmin, bucketName)
	m.authorizer.Bindings.Add(ownerBinding)

	// Create share record
	share := &FileShare{
		Name:        name,
		Description: description,
		Owner:       ownerID,
		CreatedAt:   time.Now().UTC(),
	}
	m.shares = append(m.shares, share)

	// Persist
	if m.systemStore != nil {
		if err := m.systemStore.SaveFileShares(m.shares); err != nil {
			// Note: Bindings and bucket are already created, just log the error
			// In a real system, we might want to handle this more gracefully
			return share, fmt.Errorf("persist share (share created but not persisted): %w", err)
		}
	}

	return share, nil
}

// Delete removes a file share.
// It deletes the underlying bucket, removes permissions, and updates metadata.
func (m *FileShareManager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find the share
	var shareIdx = -1
	for i, s := range m.shares {
		if s.Name == name {
			shareIdx = i
			break
		}
	}
	if shareIdx == -1 {
		return fmt.Errorf("file share %q not found", name)
	}

	bucketName := FileShareBucketPrefix + name

	// Delete the bucket (this will fail if not empty - that's intentional)
	// In a real implementation, we might want to delete all objects first
	if err := m.store.DeleteBucket(bucketName); err != nil && err != ErrBucketNotFound {
		// Try to delete all objects first, then retry
		objects, _, _, _ := m.store.ListObjects(bucketName, "", "", 1000)
		for _, obj := range objects {
			_ = m.store.DeleteObject(bucketName, obj.Key)
		}
		if err := m.store.DeleteBucket(bucketName); err != nil && err != ErrBucketNotFound {
			return fmt.Errorf("delete bucket: %w", err)
		}
	}

	// Remove group bindings for this bucket
	for _, b := range m.authorizer.GroupBindings.List() {
		if b.BucketScope == bucketName {
			m.authorizer.GroupBindings.Remove(b.Name)
		}
	}

	// Remove all role bindings scoped to this bucket
	for _, b := range m.authorizer.Bindings.List() {
		if b.BucketScope == bucketName {
			m.authorizer.Bindings.Remove(b.Name)
		}
	}

	// Remove from shares list
	m.shares = append(m.shares[:shareIdx], m.shares[shareIdx+1:]...)

	// Persist
	if m.systemStore != nil {
		if err := m.systemStore.SaveFileShares(m.shares); err != nil {
			return fmt.Errorf("persist shares: %w", err)
		}
	}

	return nil
}

// Get returns a file share by name, or nil if not found.
func (m *FileShareManager) Get(name string) *FileShare {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, s := range m.shares {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// List returns all file shares.
func (m *FileShareManager) List() []*FileShare {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*FileShare, len(m.shares))
	copy(result, m.shares)
	return result
}

// BucketName returns the bucket name for a file share.
func (m *FileShareManager) BucketName(shareName string) string {
	return FileShareBucketPrefix + shareName
}
