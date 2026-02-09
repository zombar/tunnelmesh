package s3

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
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

// FileShareOptions contains optional settings for creating a file share.
type FileShareOptions struct {
	ExpiresAt    time.Time // When the share expires (zero = use default or never)
	GuestRead    bool      // Allow all mesh users to read (default: true if not specified)
	GuestReadSet bool      // Whether GuestRead was explicitly set
}

// Create creates a new file share.
// It creates the underlying bucket, sets up default permissions, and persists the share metadata.
// If a bucket already exists (from a previously deleted share), it restores the tombstoned objects.
// quotaBytes of 0 means unlimited (within global quota).
func (m *FileShareManager) Create(name, description, ownerID string, quotaBytes int64, opts *FileShareOptions) (*FileShare, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if share already exists
	for _, s := range m.shares {
		if s.Name == name {
			return nil, fmt.Errorf("file share %q already exists", name)
		}
	}

	bucketName := FileShareBucketPrefix + name

	// Check if bucket already exists (from a previously deleted share)
	bucketExists := false
	if _, err := m.store.HeadBucket(bucketName); err == nil {
		bucketExists = true
		// Restore the bucket (O(1) - clears bucket tombstone flag)
		_ = m.store.UntombstoneBucket(bucketName)
	} else {
		// Create new bucket
		if err := m.store.CreateBucket(bucketName, ownerID); err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}

	// Determine if guest read should be enabled
	// Default is true unless explicitly disabled
	guestRead := true
	if opts != nil && opts.GuestReadSet {
		guestRead = opts.GuestRead
	}

	// Create group binding: everyone -> bucket-read (only if guest read is enabled)
	if guestRead {
		everyoneBinding := &auth.GroupBinding{
			Name:        fmt.Sprintf("everyone-%s-read", name),
			GroupName:   auth.GroupEveryone,
			RoleName:    auth.RoleBucketRead,
			BucketScope: bucketName,
		}
		m.authorizer.GroupBindings.Add(everyoneBinding)
	}

	// Create role binding: owner -> bucket-admin
	ownerBinding := auth.NewRoleBinding(ownerID, auth.RoleBucketAdmin, bucketName)
	m.authorizer.Bindings.Add(ownerBinding)

	// Create share record
	now := time.Now().UTC()
	share := &FileShare{
		Name:        name,
		Description: description,
		Owner:       ownerID,
		CreatedAt:   now,
		QuotaBytes:  quotaBytes,
		GuestRead:   guestRead,
	}

	// Set expiry from opts or use default
	if opts != nil && !opts.ExpiresAt.IsZero() {
		share.ExpiresAt = opts.ExpiresAt
	} else if expiryDays := m.store.DefaultShareExpiryDays(); expiryDays > 0 {
		share.ExpiresAt = now.AddDate(0, 0, expiryDays)
	}
	m.shares = append(m.shares, share)

	// Persist share metadata.
	// Note: If persistence fails, the share is still functional in memory. The bucket
	// and bindings have been created, but the share won't survive a restart.
	// This is acceptable because:
	// 1. The bucket data persists on disk via the Store
	// 2. On restart, if the bucket exists but share metadata doesn't, an admin can
	//    recreate the share with the same name - the existing bucket will be restored
	// 3. Bindings are persisted separately by the caller (handleFileShareCreate)
	// A full rollback would require deleting the bucket and bindings, which could
	// leave the system in an inconsistent state if those operations also fail.
	if m.systemStore != nil {
		if err := m.systemStore.SaveFileShares(m.shares); err != nil {
			log.Warn().Err(err).Str("share", name).Msg("failed to persist file share metadata")
			return share, fmt.Errorf("persist share (share created but not persisted): %w", err)
		}
	}

	// Log if we restored a previous share
	if bucketExists {
		log.Info().Str("share", name).Msg("restored file share with previous content")
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

	// Tombstone the bucket (soft delete - allows restore on recreate)
	// This is O(1) - all objects in a tombstoned bucket are treated as tombstoned
	_ = m.store.TombstoneBucket(bucketName)

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

// IsProtectedBinding returns true if the binding is a file share owner's admin binding.
// These bindings should not be deleted manually - they are managed by the file share lifecycle.
func (m *FileShareManager) IsProtectedBinding(binding *auth.RoleBinding) bool {
	// Only bucket-admin bindings on file share buckets can be protected
	if binding.RoleName != auth.RoleBucketAdmin {
		return false
	}
	if !strings.HasPrefix(binding.BucketScope, FileShareBucketPrefix) {
		return false
	}

	// Check if the user is the owner of this file share
	shareName := strings.TrimPrefix(binding.BucketScope, FileShareBucketPrefix)
	share := m.Get(shareName)
	if share == nil {
		return false
	}

	return share.Owner == binding.PeerID
}

// IsProtectedGroupBinding checks if a group binding is a file share's "everyone" binding.
func (m *FileShareManager) IsProtectedGroupBinding(binding *auth.GroupBinding) bool {
	// Only bindings on file share buckets can be protected
	if !strings.HasPrefix(binding.BucketScope, FileShareBucketPrefix) {
		return false
	}

	// Check if this file share exists
	shareName := strings.TrimPrefix(binding.BucketScope, FileShareBucketPrefix)
	share := m.Get(shareName)
	if share == nil {
		return false
	}

	// The "everyone" group binding is protected
	return binding.GroupName == auth.GroupEveryone
}

// TombstoneExpiredShareContents tombstones all objects in expired file shares.
// Returns the number of objects tombstoned.
func (m *FileShareManager) TombstoneExpiredShareContents() int {
	m.mu.RLock()
	expiredShares := make([]*FileShare, 0)
	for _, s := range m.shares {
		if s.IsExpired() {
			expiredShares = append(expiredShares, s)
		}
	}
	m.mu.RUnlock()

	tombstonedCount := 0
	for _, share := range expiredShares {
		bucketName := FileShareBucketPrefix + share.Name

		// Paginate through all objects in the bucket
		marker := ""
		for {
			objects, isTruncated, nextMarker, err := m.store.ListObjects(bucketName, "", marker, 1000)
			if err != nil {
				break
			}

			for _, obj := range objects {
				// Skip already tombstoned objects
				if obj.IsTombstoned() {
					continue
				}
				if err := m.store.TombstoneObject(bucketName, obj.Key); err == nil {
					tombstonedCount++
				}
			}

			if !isTruncated {
				break
			}
			marker = nextMarker
		}
	}

	return tombstonedCount
}
