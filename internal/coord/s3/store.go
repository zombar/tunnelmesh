// Package s3 provides an S3-compatible object storage service for the coordinator.
package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// BucketMeta contains bucket metadata.
type BucketMeta struct {
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"created_at"`
	Owner        string     `json:"owner"`                   // User ID who created the bucket
	TombstonedAt *time.Time `json:"tombstoned_at,omitempty"` // When bucket was soft-deleted
	SizeBytes    int64      `json:"size_bytes"`              // Total size of non-tombstoned objects (updated incrementally)
}

// IsTombstoned returns true if the bucket has been soft-deleted.
func (bm *BucketMeta) IsTombstoned() bool {
	return bm.TombstonedAt != nil
}

// ObjectMeta contains object metadata.
type ObjectMeta struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ContentType  string            `json:"content_type"`
	ETag         string            `json:"etag"` // MD5 hash of content
	LastModified time.Time         `json:"last_modified"`
	Expires      *time.Time        `json:"expires,omitempty"`       // Optional expiration date
	TombstonedAt *time.Time        `json:"tombstoned_at,omitempty"` // When object was soft-deleted
	Metadata     map[string]string `json:"metadata,omitempty"`      // User-defined metadata
	VersionID    string            `json:"version_id,omitempty"`    // Version identifier
	Chunks       []string          `json:"chunks,omitempty"`        // Ordered list of chunk hashes (CAS)
}

// VersionInfo contains version information for listing.
type VersionInfo struct {
	VersionID    string    `json:"version_id"`
	Size         int64     `json:"size"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
	IsCurrent    bool      `json:"is_current"`
}

// IsTombstoned returns true if the object has been soft-deleted.
func (m *ObjectMeta) IsTombstoned() bool {
	return m.TombstonedAt != nil
}

// Store provides S3 storage with content-addressable chunks and versioning.
// Directory structure:
//
//	{dataDir}/
//	  chunks/
//	    {hash}                # content-addressed, compressed, encrypted blocks
//	  buckets/
//	    {bucket}/
//	      _meta.json          # bucket metadata
//	      meta/
//	        {key}.json        # object metadata (includes version ID and chunk list)
//	      versions/
//	        {key}/
//	          {versionID}.json # version metadata
//
// VersionRetentionPolicy configures smart tiered version retention.
type VersionRetentionPolicy struct {
	RecentDays    int // Keep all versions from last N days
	WeeklyWeeks   int // Then keep one version per week for N weeks
	MonthlyMonths int // Then keep one version per month for N months
}

// ChunkRegistryInterface defines operations for tracking chunk ownership across coordinators.
// This interface allows the Store to integrate with the distributed chunk registry
// without creating a circular dependency with the replication package.
type ChunkRegistryInterface interface {
	// RegisterChunk registers a chunk as owned by the local coordinator
	RegisterChunk(hash string, size int64) error

	// UnregisterChunk removes the local coordinator as an owner of the chunk
	UnregisterChunk(hash string) error

	// GetOwners returns the list of coordinator IDs that own the chunk
	GetOwners(hash string) ([]string, error)

	// GetChunksOwnedBy returns all chunk hashes owned by the specified coordinator
	GetChunksOwnedBy(coordID string) ([]string, error)
}

// Store provides S3 storage with content-addressable chunks and versioning.
type Store struct {
	dataDir                 string
	cas                     *CAS // Content-addressable storage for chunks
	quota                   *QuotaManager
	chunkRegistry           ChunkRegistryInterface // Optional distributed chunk ownership tracking
	defaultObjectExpiryDays int                    // Days until objects expire (0 = never)
	defaultShareExpiryDays  int                    // Days until file shares expire (0 = never)
	tombstoneRetentionDays  int                    // Days to retain tombstoned objects before purging (0 = never purge)
	versionRetentionDays    int                    // Days to retain object versions (0 = forever)
	maxVersionsPerObject    int                    // Max versions to keep per object (0 = unlimited)
	versionRetentionPolicy  VersionRetentionPolicy
	mu                      sync.RWMutex
}

// NewStore creates a new S3 store with the given data directory.
// If quota is nil, no quota enforcement is applied.
func NewStore(dataDir string, quota *QuotaManager) (*Store, error) {
	bucketsDir := filepath.Join(dataDir, "buckets")
	if err := os.MkdirAll(bucketsDir, 0755); err != nil {
		return nil, fmt.Errorf("create buckets dir: %w", err)
	}

	store := &Store{
		dataDir: dataDir,
		quota:   quota,
	}

	// Calculate initial quota usage from existing objects
	if quota != nil {
		if err := store.calculateQuotaUsage(); err != nil {
			return nil, fmt.Errorf("calculate quota usage: %w", err)
		}
	}

	return store, nil
}

// syncedWriteFile writes data to a file and calls fsync to ensure durability.
// This prevents data loss in case of sudden power loss or system crash.
// Use this instead of os.WriteFile for critical metadata.
//
// During tests, fsync is skipped (detected via TUNNELMESH_TEST=1 env var) since:
// - Tests use temp directories that are discarded anyway
// - fsync is very slow on Windows (100-500ms per call)
// - 179 S3 tests × fsync = 7+ minutes on Windows CI
func syncedWriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(data); err != nil {
		return err
	}

	// Skip fsync during tests to avoid 7+ minute test times on Windows
	// Production code always fsyncs for durability
	if os.Getenv("TUNNELMESH_TEST") == "" {
		if err := f.Sync(); err != nil {
			return err
		}
	}

	return nil
}

// NewStoreWithCAS creates a new S3 store with CAS (Content-Addressable Storage) enabled.
// This enables CDC chunking, encryption, compression, and version history.
func NewStoreWithCAS(dataDir string, quota *QuotaManager, masterKey [32]byte) (*Store, error) {
	store, err := NewStore(dataDir, quota)
	if err != nil {
		return nil, err
	}

	// Initialize CAS
	chunksDir := filepath.Join(dataDir, "chunks")
	cas, err := NewCAS(chunksDir, masterKey)
	if err != nil {
		return nil, fmt.Errorf("create CAS: %w", err)
	}
	store.cas = cas

	return store, nil
}

// DataDir returns the data directory path.
func (s *Store) DataDir() string {
	return s.dataDir
}

// SetChunkRegistry sets the distributed chunk registry for tracking ownership.
// This is optional and only used when replication is enabled.
func (s *Store) SetChunkRegistry(registry ChunkRegistryInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunkRegistry = registry
}

// SetDefaultObjectExpiryDays sets the default expiry for new objects in days.
// A value of 0 means objects don't expire.
func (s *Store) SetDefaultObjectExpiryDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultObjectExpiryDays = days
}

// SetDefaultShareExpiryDays sets the default expiry for new file shares in days.
// A value of 0 means shares don't expire.
func (s *Store) SetDefaultShareExpiryDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultShareExpiryDays = days
}

// DefaultShareExpiryDays returns the configured default share expiry in days.
func (s *Store) DefaultShareExpiryDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.defaultShareExpiryDays
}

// QuotaStats returns current quota statistics, or nil if no quota is configured.
func (s *Store) QuotaStats() *QuotaStats {
	if s.quota == nil {
		return nil
	}
	stats := s.quota.Stats()
	return &stats
}

// calculateQuotaUsage scans all chunks and updates quota tracking.
// This is called during initialization before the store is accessible to other
// goroutines, but we hold the mutex for defense in depth and future-proofing.
func (s *Store) calculateQuotaUsage() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cas == nil {
		return nil
	}

	// Calculate total chunk storage size (shared across all buckets)
	chunksDir := filepath.Join(s.dataDir, "chunks")
	var totalSize int64
	_ = filepath.Walk(chunksDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		totalSize += info.Size()
		return nil
	})
	if totalSize > 0 {
		s.quota.SetUsed("_chunks", totalSize)
	}
	return nil
}

// validateName validates a bucket or object key name to prevent path traversal.
// This is a defense-in-depth measure that runs at the storage layer.
// The admin.go also has validateS3Name which performs identical validation at
// the API layer. Both functions are intentionally duplicated to ensure path
// traversal protection even if one layer is bypassed or refactored.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	// Check for null bytes which could truncate paths on some filesystems
	if strings.ContainsRune(name, 0) {
		return fmt.Errorf("null bytes not allowed")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid name")
	}
	// Check for path traversal: ".." as a component (not "..." which is valid)
	// Split on both forward and back slashes to handle all platforms
	for _, sep := range []string{"/", "\\"} {
		parts := strings.Split(name, sep)
		for _, part := range parts {
			if part == ".." {
				return fmt.Errorf("path traversal not allowed")
			}
		}
	}
	// Block absolute paths
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("absolute paths not allowed")
	}
	// Block relative paths starting with "./" or ".\"
	if strings.HasPrefix(name, "./") || strings.HasPrefix(name, ".\\") {
		return fmt.Errorf("relative paths not allowed")
	}
	return nil
}

// bucketPath returns the path to a bucket directory.
func (s *Store) bucketPath(bucket string) string {
	return filepath.Join(s.dataDir, "buckets", bucket)
}

// bucketMetaPath returns the path to bucket metadata file.
func (s *Store) bucketMetaPath(bucket string) string {
	return filepath.Join(s.bucketPath(bucket), "_meta.json")
}

// objectMetaPath returns the path to an object metadata file.
func (s *Store) objectMetaPath(bucket, key string) string {
	return filepath.Join(s.bucketPath(bucket), "meta", key+".json")
}

// CreateBucket creates a new bucket.
func (s *Store) CreateBucket(ctx context.Context, bucket, owner string) error {
	// Validate bucket name (defense in depth)
	if err := validateName(bucket); err != nil {
		return fmt.Errorf("invalid bucket name: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	bucketDir := s.bucketPath(bucket)

	// Check if bucket already exists
	if _, err := os.Stat(bucketDir); err == nil {
		return ErrBucketExists
	}

	// Create bucket directory structure
	if err := os.MkdirAll(filepath.Join(bucketDir, "meta"), 0755); err != nil {
		return fmt.Errorf("create bucket meta dir: %w", err)
	}

	// Write bucket metadata
	meta := BucketMeta{
		Name:      bucket,
		CreatedAt: time.Now().UTC(),
		Owner:     owner,
	}

	metaPath := s.bucketMetaPath(bucket)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bucket meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write bucket meta: %w", err)
	}

	return nil
}

// DeleteBucket removes an empty bucket.
func (s *Store) DeleteBucket(ctx context.Context, bucket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucketDir := s.bucketPath(bucket)

	// Check if bucket exists
	if _, err := os.Stat(bucketDir); os.IsNotExist(err) {
		return ErrBucketNotFound
	}

	// Check if bucket is empty (only contains dirs and _meta.json)
	objects, _, _, err := s.listObjectsUnsafe(bucket, "", "", 1)
	if err != nil {
		return err
	}
	if len(objects) > 0 {
		return ErrBucketNotEmpty
	}

	// Remove bucket directory
	if err := os.RemoveAll(bucketDir); err != nil {
		return fmt.Errorf("remove bucket: %w", err)
	}

	return nil
}

// TombstoneBucket marks a bucket as soft-deleted.
// All objects in a tombstoned bucket are treated as tombstoned.
func (s *Store) TombstoneBucket(ctx context.Context, bucket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.getBucketMeta(bucket)
	if err != nil {
		return err
	}

	// Already tombstoned
	if meta.IsTombstoned() {
		return nil
	}

	now := time.Now().UTC()
	meta.TombstonedAt = &now

	return s.writeBucketMeta(bucket, meta)
}

// UntombstoneBucket restores a tombstoned bucket.
func (s *Store) UntombstoneBucket(ctx context.Context, bucket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.getBucketMeta(bucket)
	if err != nil {
		return err
	}

	// Not tombstoned
	if !meta.IsTombstoned() {
		return nil
	}

	meta.TombstonedAt = nil

	return s.writeBucketMeta(bucket, meta)
}

// writeBucketMeta writes bucket metadata (caller must hold lock).
func (s *Store) writeBucketMeta(bucket string, meta *BucketMeta) error {
	metaPath := s.bucketMetaPath(bucket)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bucket meta: %w", err)
	}
	if err := syncedWriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write bucket meta: %w", err)
	}
	return nil
}

// HeadBucket checks if a bucket exists.
func (s *Store) HeadBucket(ctx context.Context, bucket string) (*BucketMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getBucketMeta(bucket)
}

// getBucketMeta reads bucket metadata (caller must hold lock).
func (s *Store) getBucketMeta(bucket string) (*BucketMeta, error) {
	metaPath := s.bucketMetaPath(bucket)

	data, err := os.ReadFile(metaPath)
	if os.IsNotExist(err) {
		return nil, ErrBucketNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read bucket meta: %w", err)
	}

	var meta BucketMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal bucket meta: %w", err)
	}

	return &meta, nil
}

// ListBuckets returns all buckets.
func (s *Store) ListBuckets(ctx context.Context) ([]BucketMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bucketsDir := filepath.Join(s.dataDir, "buckets")
	entries, err := os.ReadDir(bucketsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read buckets dir: %w", err)
	}

	var buckets []BucketMeta
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		meta, err := s.getBucketMeta(entry.Name())
		if err != nil {
			continue // Skip buckets with invalid metadata
		}
		buckets = append(buckets, *meta)
	}

	return buckets, nil
}

// CalculateBucketSize calculates the total size of all objects in a bucket (including tombstoned).
// Tombstoned objects still consume disk space until purged, so they count toward size.
// Returns the total size in bytes, or 0 if the bucket doesn't exist or is empty.
func (s *Store) CalculateBucketSize(ctx context.Context, bucketName string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Verify bucket exists
	bucketMeta, err := s.getBucketMeta(bucketName)
	if err != nil {
		if errors.Is(err, ErrBucketNotFound) {
			return 0, nil // Bucket doesn't exist, size is 0
		}
		return 0, err
	}

	// Return cached size if available (updated incrementally)
	return bucketMeta.SizeBytes, nil
}

// CalculatePrefixSize calculates the total size of all objects under a prefix (including tombstoned).
// This is used for displaying folder sizes in the S3 explorer.
// Returns the total size in bytes.
func (s *Store) CalculatePrefixSize(ctx context.Context, bucketName, prefix string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Verify bucket exists
	if _, err := s.getBucketMeta(bucketName); err != nil {
		return 0, err
	}

	var totalSize int64

	// Iterate through all objects with the given prefix
	marker := ""
	for {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return totalSize, fmt.Errorf("calculate prefix size for %s/%s: %w", bucketName, prefix, ctx.Err())
		default:
		}

		objects, isTruncated, nextMarker, err := s.ListObjects(ctx, bucketName, prefix, marker, 1000)
		if err != nil {
			return 0, fmt.Errorf("list objects: %w", err)
		}

		// Sum sizes of ALL objects (including tombstoned - they still consume disk space)
		for _, obj := range objects {
			totalSize += obj.Size
		}

		if !isTruncated {
			break
		}
		marker = nextMarker
	}

	return totalSize, nil
}

// updateBucketSize updates the cached bucket size by delta.
// Must be called with write lock held.
func (s *Store) updateBucketSize(bucketName string, delta int64) error {
	bucketMeta, err := s.getBucketMeta(bucketName)
	if err != nil {
		return err
	}

	bucketMeta.SizeBytes += delta
	if bucketMeta.SizeBytes < 0 {
		bucketMeta.SizeBytes = 0 // Safety: prevent negative sizes
	}

	return s.writeBucketMeta(bucketName, bucketMeta)
}

// PutObject writes an object to a bucket using CDC chunks stored in CAS.
// Archives the current version before overwriting (for version history).
//
// Streaming behavior: Data is chunked and written to CAS incrementally as it's read.
// This provides memory efficiency (peak usage ~64KB regardless of file size) but means
// that partial uploads on failure will leave orphaned chunks in CAS until the next
// garbage collection cycle. Orphaned chunks are cleaned up automatically during GC.
//
// Context cancellation: If ctx is canceled mid-upload, the function returns immediately
// but chunks already written to CAS will remain until GC cleanup.
//
//nolint:gocyclo // Complexity inherited from streaming refactor - will be addressed in future refactoring
func (s *Store) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string, metadata map[string]string) (*ObjectMeta, error) {
	// Validate names (defense in depth)
	if err := validateName(bucket); err != nil {
		return nil, fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return nil, fmt.Errorf("invalid key: %w", err)
	}

	// CAS must be initialized
	if s.cas == nil {
		return nil, fmt.Errorf("CAS not initialized - call InitCAS first")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	metaPath := s.objectMetaPath(bucket, key)

	// Check if object already exists (for quota update calculation and versioning)
	var oldSize int64
	if oldMeta, err := s.getObjectMeta(bucket, key); err == nil {
		oldSize = oldMeta.Size
	}

	// Check quota if configured (only if object is growing)
	if s.quota != nil && size > oldSize {
		delta := size - oldSize
		if !s.quota.CanAllocate(delta) {
			return nil, ErrQuotaExceeded
		}
	}

	// Archive current version for version history
	if err := s.archiveCurrentVersion(bucket, key); err != nil {
		return nil, fmt.Errorf("archive current version: %w", err)
	}

	// Create parent directories for metadata
	if err := os.MkdirAll(filepath.Dir(metaPath), 0755); err != nil {
		return nil, fmt.Errorf("create meta dir: %w", err)
	}

	// Stream data through CDC chunker (memory-efficient)
	// For a 1GB file, this uses ~64KB peak memory instead of 1GB
	streamChunker := NewStreamingChunker(reader)

	var chunks []string
	var written int64
	md5Hasher := md5.New()

	for {
		// Check for context cancellation to allow interrupting long uploads
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("upload canceled: %w", ctx.Err())
		default:
		}

		chunk, chunkHash, err := streamChunker.NextChunk()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read chunk: %w", err)
		}

		// Write chunk to CAS immediately (don't accumulate in memory)
		if _, err := s.cas.WriteChunk(ctx, chunk); err != nil {
			return nil, fmt.Errorf("write chunk %s: %w", chunkHash, err)
		}

		// Register chunk ownership in distributed registry (if configured)
		if s.chunkRegistry != nil {
			if err := s.chunkRegistry.RegisterChunk(chunkHash, int64(len(chunk))); err != nil {
				// Don't fail upload if registry update fails (eventual consistency)
				// but log to stderr in debug mode for troubleshooting
				if os.Getenv("DEBUG") != "" || os.Getenv("TUNNELMESH_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "chunk registry warning: failed to register %s: %v\n", chunkHash[:8], err)
				}
			}
		}

		// Update MD5 for ETag
		md5Hasher.Write(chunk)

		chunks = append(chunks, chunkHash)
		written += int64(len(chunk))
	}

	// Generate ETag from MD5 hash of all data (S3-compatible format)
	hash := md5Hasher.Sum(nil)
	etag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash))

	// Update quota tracking
	var quotaUpdated bool
	if s.quota != nil {
		if oldSize > 0 {
			s.quota.Update(bucket, oldSize, written)
		} else {
			s.quota.Allocate(bucket, written)
		}
		quotaUpdated = true
	}

	// Write object metadata
	now := time.Now().UTC()
	objMeta := ObjectMeta{
		Key:          key,
		Size:         written,
		ContentType:  contentType,
		ETag:         etag,
		LastModified: now,
		Metadata:     metadata,
		VersionID:    generateVersionID(),
		Chunks:       chunks,
	}

	// Set expiry if configured (skip for system bucket - internal data doesn't expire)
	if s.defaultObjectExpiryDays > 0 && bucket != SystemBucket {
		expiry := now.AddDate(0, 0, s.defaultObjectExpiryDays)
		objMeta.Expires = &expiry
	}

	metaData, err := json.MarshalIndent(objMeta, "", "  ")
	if err != nil {
		// Rollback quota on failure
		if quotaUpdated {
			if oldSize > 0 {
				s.quota.Update(bucket, written, oldSize) // Reverse the update
			} else {
				s.quota.Release(bucket, written)
			}
		}
		return nil, fmt.Errorf("marshal object meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, metaData, 0644); err != nil {
		// Rollback quota on failure
		if quotaUpdated {
			if oldSize > 0 {
				s.quota.Update(bucket, written, oldSize) // Reverse the update
			} else {
				s.quota.Release(bucket, written)
			}
		}
		return nil, fmt.Errorf("write object meta: %w", err)
	}

	// Prune expired versions (lazy cleanup)
	s.pruneExpiredVersions(ctx, bucket, key)

	// Update bucket size (new object adds to size, replacement adjusts delta)
	sizeDelta := written - oldSize
	if sizeDelta != 0 {
		if err := s.updateBucketSize(bucket, sizeDelta); err != nil {
			// Log error but don't fail the put operation
			// Size will be recalculated on next server restart
			_ = err
		}
	}

	return &objMeta, nil
}

// GetObject retrieves an object from a bucket.
func (s *Store) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, *ObjectMeta, error) {
	// Validate names (defense in depth)
	if err := validateName(bucket); err != nil {
		return nil, nil, fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return nil, nil, fmt.Errorf("invalid key: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, nil, err
	}

	// Read object metadata
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return nil, nil, err
	}

	return s.getObjectContent(ctx, bucket, key, meta)
}

// HeadObject returns object metadata without the body.
func (s *Store) HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists and get tombstone state
	bucketMeta, err := s.getBucketMeta(bucket)
	if err != nil {
		return nil, err
	}

	objMeta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return nil, err
	}

	// If bucket is tombstoned, mark object as tombstoned (virtually)
	if bucketMeta.IsTombstoned() && objMeta.TombstonedAt == nil {
		objMeta.TombstonedAt = bucketMeta.TombstonedAt
	}

	return objMeta, nil
}

// getObjectMeta reads object metadata (caller must hold lock).
func (s *Store) getObjectMeta(bucket, key string) (*ObjectMeta, error) {
	metaPath := s.objectMetaPath(bucket, key)

	data, err := os.ReadFile(metaPath)
	if os.IsNotExist(err) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read object meta: %w", err)
	}

	var meta ObjectMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal object meta: %w", err)
	}

	return &meta, nil
}

// DeleteObject soft-deletes an object by tombstoning it.
// Tombstoned objects are read-only and will be purged after the retention period.
// DeleteObject soft-deletes (tombstones) an object on first call.
// If the object is already tombstoned, it permanently purges it.
// This allows "delete twice to permanently remove" UX pattern.
func (s *Store) DeleteObject(ctx context.Context, bucket, key string) error {
	// Validate names (defense in depth)
	if err := validateName(bucket); err != nil {
		return fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return fmt.Errorf("invalid key: %w", err)
	}

	// Check if object or bucket is already tombstoned
	bucketMeta, err := s.HeadBucket(ctx, bucket)
	if err != nil {
		return err
	}

	objMeta, err := s.HeadObject(ctx, bucket, key)
	if err != nil {
		return err
	}

	// If already tombstoned (object or bucket level), purge permanently
	if objMeta.IsTombstoned() || bucketMeta.IsTombstoned() {
		return s.PurgeObject(ctx, bucket, key)
	}

	// First delete: tombstone
	return s.TombstoneObject(ctx, bucket, key)
}

// TombstoneObject marks an object as tombstoned (soft-deleted).
// The object data is preserved but marked for cleanup after the retention period.
func (s *Store) TombstoneObject(ctx context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return err
	}

	// Get object metadata
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return err
	}

	// Already tombstoned
	if meta.IsTombstoned() {
		return nil
	}

	// Set tombstone timestamp
	now := time.Now().UTC()
	meta.TombstonedAt = &now

	// Write updated metadata
	metaPath := s.objectMetaPath(bucket, key)
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal object meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, metaData, 0644); err != nil {
		return fmt.Errorf("write object meta: %w", err)
	}

	return nil
}

// UntombstoneObject restores a tombstoned object, making it accessible again.
func (s *Store) UntombstoneObject(ctx context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return err
	}

	// Get object metadata
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return err
	}

	// Not tombstoned
	if !meta.IsTombstoned() {
		return nil
	}

	// Clear tombstone timestamp
	meta.TombstonedAt = nil

	// Write updated metadata
	metaPath := s.objectMetaPath(bucket, key)
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal object meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, metaData, 0644); err != nil {
		return fmt.Errorf("write object meta: %w", err)
	}

	return nil
}

// PurgeObject permanently removes an object and all its versions from a bucket.
// This is used by the cleanup process for tombstoned objects past retention.
func (s *Store) PurgeObject(ctx context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return err
	}

	// Get current object metadata for quota release and chunk cleanup
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return err
	}

	metaPath := s.objectMetaPath(bucket, key)

	// Delete all versions first (this also collects chunks for GC)
	s.deleteAllVersions(ctx, bucket, key)

	// Delete current version's chunks
	if s.cas != nil && len(meta.Chunks) > 0 {
		for _, hash := range meta.Chunks {
			// Check if chunk is still referenced by other objects
			if !s.isChunkReferenced(hash, bucket, key, meta.VersionID) {
				_ = s.cas.DeleteChunk(ctx, hash)
			}
		}
	}

	// Remove current metadata
	_ = os.Remove(metaPath)

	// Release quota
	if s.quota != nil && meta.Size > 0 {
		s.quota.Release(bucket, meta.Size)
	}

	// Decrement bucket size (object is actually being removed now)
	if meta.Size > 0 {
		if err := s.updateBucketSize(bucket, -meta.Size); err != nil {
			// Log error but don't fail the purge operation
			_ = err
		}
	}

	return nil
}

// SetTombstoneRetentionDays sets the number of days to retain tombstoned objects before purging.
func (s *Store) SetTombstoneRetentionDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstoneRetentionDays = days
}

// TombstoneRetentionDays returns the configured tombstone retention period in days.
func (s *Store) TombstoneRetentionDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tombstoneRetentionDays
}

// PurgeTombstonedObjects removes all tombstoned objects older than the retention period.
// Returns the number of objects purged.
func (s *Store) PurgeTombstonedObjects(ctx context.Context) int {
	s.mu.RLock()
	retentionDays := s.tombstoneRetentionDays
	s.mu.RUnlock()

	if retentionDays <= 0 {
		return 0 // Disabled
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	purgedCount := 0

	// List all buckets
	buckets, err := s.ListBuckets(ctx)
	if err != nil {
		return 0
	}

	for _, bucket := range buckets {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return purgedCount // Early exit on cancellation
		default:
		}

		// Paginate through all objects in the bucket
		marker := ""
		for {
			// Check for cancellation in pagination loop
			select {
			case <-ctx.Done():
				return purgedCount // Early exit on cancellation
			default:
			}

			objects, isTruncated, nextMarker, err := s.ListObjects(ctx, bucket.Name, "", marker, 1000)
			if err != nil {
				break
			}

			for _, obj := range objects {
				if obj.IsTombstoned() && obj.TombstonedAt.Before(cutoff) {
					if err := s.PurgeObject(ctx, bucket.Name, obj.Key); err == nil {
						purgedCount++
					}
				}
			}

			if !isTruncated {
				break
			}
			marker = nextMarker
		}
	}

	return purgedCount
}

// ListObjects lists objects in a bucket with optional prefix filter and pagination.
// marker is the key to start after (exclusive) for pagination.
// Returns (objects, isTruncated, nextMarker, error).
func (s *Store) ListObjects(ctx context.Context, bucket, prefix, marker string, maxKeys int) ([]ObjectMeta, bool, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists and get tombstone state
	bucketMeta, err := s.getBucketMeta(bucket)
	if err != nil {
		return nil, false, "", err
	}

	objects, isTruncated, nextMarker, err := s.listObjectsUnsafe(bucket, prefix, marker, maxKeys)
	if err != nil {
		return nil, false, "", err
	}

	// If bucket is tombstoned, mark all objects as tombstoned (virtually)
	if bucketMeta.IsTombstoned() {
		for i := range objects {
			if objects[i].TombstonedAt == nil {
				objects[i].TombstonedAt = bucketMeta.TombstonedAt
			}
		}
	}

	return objects, isTruncated, nextMarker, nil
}

// listObjectsUnsafe lists objects without lock (caller must hold lock).
// Returns (objects, isTruncated, nextMarker, error).
func (s *Store) listObjectsUnsafe(bucket, prefix, marker string, maxKeys int) ([]ObjectMeta, bool, string, error) {
	metaDir := filepath.Join(s.bucketPath(bucket), "meta")

	var objects []ObjectMeta
	passedMarker := marker == "" // If no marker, we've already passed it

	err := filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}

		// Only process .json files
		if filepath.Ext(path) != ".json" {
			return nil
		}

		// Get key from path (remove .json suffix and metaDir prefix)
		relPath, err := filepath.Rel(metaDir, path)
		if err != nil {
			return nil
		}
		key := relPath[:len(relPath)-5] // Remove .json

		// Normalize path separators to forward slashes (S3 uses forward slashes)
		key = filepath.ToSlash(key)

		// Apply prefix filter
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		// Skip until we pass the marker
		if !passedMarker {
			if key == marker {
				passedMarker = true
			}
			return nil
		}

		// Read metadata
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var meta ObjectMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil
		}

		objects = append(objects, meta)

		// Collect maxKeys + 1 to detect truncation
		if maxKeys > 0 && len(objects) > maxKeys {
			return filepath.SkipAll
		}

		return nil
	})

	if err != nil {
		return nil, false, "", fmt.Errorf("walk meta dir: %w", err)
	}

	// Check if results are truncated (only if maxKeys > 0)
	var isTruncated bool
	var nextMarker string
	if maxKeys > 0 && len(objects) > maxKeys {
		isTruncated = true
		objects = objects[:maxKeys] // Trim to maxKeys
		nextMarker = objects[maxKeys-1].Key
	}

	return objects, isTruncated, nextMarker, nil
}

// InitCAS initializes the content-addressable storage for the store.
// masterKey should be derived from the mesh PSK for consistent encryption across coordinators.
func (s *Store) InitCAS(ctx context.Context, masterKey [32]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chunksDir := filepath.Join(s.dataDir, "chunks")
	cas, err := NewCAS(chunksDir, masterKey)
	if err != nil {
		return fmt.Errorf("init CAS: %w", err)
	}
	s.cas = cas
	return nil
}

// SetVersionRetentionDays sets the number of days to retain object versions.
func (s *Store) SetVersionRetentionDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionRetentionDays = days
}

// VersionRetentionDays returns the configured version retention period in days.
func (s *Store) VersionRetentionDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versionRetentionDays
}

// SetMaxVersionsPerObject sets the maximum number of versions to keep per object.
func (s *Store) SetMaxVersionsPerObject(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxVersionsPerObject = max
}

// SetVersionRetentionPolicy sets the smart tiered version retention policy.
func (s *Store) SetVersionRetentionPolicy(policy VersionRetentionPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionRetentionPolicy = policy
}

// generateVersionID creates a unique, sortable version ID.
// Format: {unixNano}-{random6hex}
// Uses crypto/rand for unpredictable random component.
func generateVersionID() string {
	var randomBytes [4]byte
	_, _ = cryptorand.Read(randomBytes[:])
	randomInt := binary.BigEndian.Uint32(randomBytes[:]) & 0xFFFFFF
	return fmt.Sprintf("%d-%06x", time.Now().UnixNano(), randomInt)
}

// versionDir returns the path to an object's version directory.
func (s *Store) versionDir(bucket, key string) string {
	return filepath.Join(s.bucketPath(bucket), "versions", key)
}

// versionMetaPath returns the path to a version's metadata file.
func (s *Store) versionMetaPath(bucket, key, versionID string) string {
	return filepath.Join(s.versionDir(bucket, key), versionID+".json")
}

// archiveCurrentVersion copies the current object metadata to the versions directory.
// This is called before overwriting an object to preserve its history.
func (s *Store) archiveCurrentVersion(bucket, key string) error {
	meta, err := s.getObjectMeta(bucket, key)
	if errors.Is(err, ErrObjectNotFound) {
		return nil // No current version to archive
	}
	if err != nil {
		return err
	}

	// Generate version ID if not present (defensive - all new objects have IDs)
	versionID := meta.VersionID
	if versionID == "" {
		versionID = generateVersionID()
		meta.VersionID = versionID
	}

	// Create version directory
	versionDir := s.versionDir(bucket, key)
	if err := os.MkdirAll(versionDir, 0755); err != nil {
		return fmt.Errorf("create version dir: %w", err)
	}

	// Write version metadata
	versionPath := s.versionMetaPath(bucket, key, versionID)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal version meta: %w", err)
	}

	if err := syncedWriteFile(versionPath, data, 0644); err != nil {
		return fmt.Errorf("write version meta: %w", err)
	}

	return nil
}

// pruneExpiredVersions removes versions according to the retention policy.
// Smart tiered pruning (when configured):
//   - Keep all versions from last RecentDays
//   - Keep one version per week for WeeklyWeeks
//   - Keep one version per month for MonthlyMonths
//   - Delete everything older
//   - Enforce MaxVersionsPerObject limit
//
// If no smart pruning is configured (all values 0), falls back to simple
// versionRetentionDays cutoff.
//
// Returns the number of versions pruned.
// nolint:gocyclo // Version pruning has complex retention logic
func (s *Store) pruneExpiredVersions(ctx context.Context, bucket, key string) int {
	versionDir := s.versionDir(bucket, key)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return 0
	}

	// Load all versions with metadata
	type versionEntry struct {
		path     string
		meta     ObjectMeta
		modified time.Time
	}
	var versions []versionEntry

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		versionPath := filepath.Join(versionDir, entry.Name())
		data, err := os.ReadFile(versionPath)
		if err != nil {
			continue
		}

		var meta ObjectMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		versions = append(versions, versionEntry{
			path:     versionPath,
			meta:     meta,
			modified: meta.LastModified,
		})
	}

	if len(versions) == 0 {
		return 0
	}

	// Sort by modified time (newest first)
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].modified.After(versions[j].modified)
	})

	now := time.Now().UTC()
	policy := s.versionRetentionPolicy
	maxVersions := s.maxVersionsPerObject

	// Check if smart pruning is configured
	smartPruningEnabled := policy.RecentDays > 0 || policy.WeeklyWeeks > 0 || policy.MonthlyMonths > 0

	// Track which versions to keep
	keep := make(map[int]bool)

	if smartPruningEnabled {
		// Smart tiered pruning
		weekKept := make(map[int]bool)  // year*100 + week
		monthKept := make(map[int]bool) // year*100 + month

		// Calculate time boundaries (only if that tier is enabled)
		var recentCutoff, weeklyCutoff, monthlyCutoff time.Time
		if policy.RecentDays > 0 {
			recentCutoff = now.AddDate(0, 0, -policy.RecentDays)
		}
		if policy.WeeklyWeeks > 0 {
			weeklyCutoff = now.AddDate(0, 0, -policy.RecentDays-(policy.WeeklyWeeks*7))
		}
		if policy.MonthlyMonths > 0 {
			monthlyCutoff = now.AddDate(0, -policy.MonthlyMonths, -policy.RecentDays-(policy.WeeklyWeeks*7))
		}

		for i, v := range versions {
			// Recent period: keep all (if configured)
			if policy.RecentDays > 0 && v.modified.After(recentCutoff) {
				keep[i] = true
				continue
			}

			// Weekly period: keep one per week (if configured)
			if policy.WeeklyWeeks > 0 && v.modified.After(weeklyCutoff) {
				year, week := v.modified.ISOWeek()
				weekKey := year*100 + week
				if !weekKept[weekKey] {
					keep[i] = true
					weekKept[weekKey] = true
				}
				continue
			}

			// Monthly period: keep one per month (if configured)
			if policy.MonthlyMonths > 0 && v.modified.After(monthlyCutoff) {
				monthKey := v.modified.Year()*100 + int(v.modified.Month())
				if !monthKept[monthKey] {
					keep[i] = true
					monthKept[monthKey] = true
				}
				continue
			}

			// Older than all configured periods: check max versions
			if maxVersions > 0 && i < maxVersions {
				keep[i] = true
			}
		}
	} else {
		// No smart pruning - use simple retention days only
		if s.versionRetentionDays > 0 {
			simpleCutoff := now.AddDate(0, 0, -s.versionRetentionDays)
			for i, v := range versions {
				if v.modified.After(simpleCutoff) || v.modified.Equal(simpleCutoff) {
					keep[i] = true
				} else if maxVersions > 0 && i < maxVersions {
					keep[i] = true
				}
			}
		} else {
			// No retention configured - keep all (but respect max versions)
			for i := range versions {
				if maxVersions <= 0 || i < maxVersions {
					keep[i] = true
				}
			}
		}
	}

	// Delete versions not in keep set
	prunedCount := 0
	for i, v := range versions {
		if keep[i] {
			continue
		}

		// Delete version metadata
		_ = os.Remove(v.path)
		prunedCount++

		// Garbage collect chunks if using CAS
		if s.cas != nil && len(v.meta.Chunks) > 0 {
			for _, hash := range v.meta.Chunks {
				// Use global check to ensure chunk isn't used elsewhere
				if !s.isChunkReferencedGlobally(ctx, hash) {
					_ = s.cas.DeleteChunk(ctx, hash)
				}
			}
		}
	}

	return prunedCount
}

// isChunkReferencedGlobally checks if a chunk is referenced by ANY object in ANY bucket.
// This is the safe way to check before deleting a chunk to prevent data loss.
func (s *Store) isChunkReferencedGlobally(ctx context.Context, hash string) bool {
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return true // Assume referenced if we can't check
	}

	for _, bucketEntry := range bucketEntries {
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()

		// Check all current objects in this bucket
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		metaEntries, _ := os.ReadDir(metaDir)
		for _, metaEntry := range metaEntries {
			if metaEntry.IsDir() || filepath.Ext(metaEntry.Name()) != ".json" {
				continue
			}
			metaPath := filepath.Join(metaDir, metaEntry.Name())
			data, err := os.ReadFile(metaPath)
			if err != nil {
				return true // Assume referenced on read error to prevent data loss
			}
			var meta ObjectMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				return true // Assume referenced on parse error to prevent data loss
			}
			for _, h := range meta.Chunks {
				if h == hash {
					return true
				}
			}
		}

		// Check all versions directory
		versionsDir := filepath.Join(bucketsDir, bucket, "versions")
		found := false
		parseError := false
		_ = filepath.Walk(versionsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				parseError = true
				return filepath.SkipAll
			}
			var meta ObjectMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				parseError = true
				return filepath.SkipAll
			}
			for _, h := range meta.Chunks {
				if h == hash {
					found = true
					return filepath.SkipAll
				}
			}
			return nil
		})
		if found || parseError {
			return true // Found or error - assume referenced
		}
	}

	return false
}

// isChunkReferenced checks if a chunk is referenced by any version other than the excluded one.
// For single-object operations. Use isChunkReferencedGlobally for deletion safety.
func (s *Store) isChunkReferenced(hash, bucket, key, excludeVersionID string) bool {
	// Check current version
	if meta, err := s.getObjectMeta(bucket, key); err == nil {
		if meta.VersionID != excludeVersionID {
			for _, h := range meta.Chunks {
				if h == hash {
					return true
				}
			}
		}
	}

	// Check all versions of this object
	versionDir := s.versionDir(bucket, key)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		versionID := strings.TrimSuffix(entry.Name(), ".json")
		if versionID == excludeVersionID {
			continue
		}

		versionPath := filepath.Join(versionDir, entry.Name())
		data, err := os.ReadFile(versionPath)
		if err != nil {
			continue
		}

		var meta ObjectMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		for _, h := range meta.Chunks {
			if h == hash {
				return true
			}
		}
	}

	return false
}

// CASStats holds statistics about content-addressed storage.
type CASStats struct {
	ChunkCount   int   // Total number of chunks
	ChunkBytes   int64 // Total bytes in chunks (after dedup)
	LogicalBytes int64 // Logical bytes (sum of object sizes, before dedup)
	VersionCount int   // Total number of versions
	ObjectCount  int   // Total number of current objects
}

// GetCASStats returns statistics about content-addressed storage.
// This scans the chunks directory and all object metadata.
func (s *Store) GetCASStats() CASStats {
	stats := CASStats{}

	if s.cas == nil {
		return stats
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count chunks and their sizes
	chunksDir := filepath.Join(s.dataDir, "chunks")
	_ = filepath.Walk(chunksDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		stats.ChunkCount++
		stats.ChunkBytes += info.Size()
		return nil
	})

	// Scan all buckets for objects and versions
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return stats
	}

	for _, bucketEntry := range bucketEntries {
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()

		// Count current objects and their logical sizes
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		_ = filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			stats.ObjectCount++
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var meta ObjectMeta
			if json.Unmarshal(data, &meta) == nil {
				stats.LogicalBytes += meta.Size
			}
			return nil
		})

		// Count versions
		versionsDir := filepath.Join(bucketsDir, bucket, "versions")
		_ = filepath.Walk(versionsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			stats.VersionCount++
			return nil
		})
	}

	return stats
}

// GCStats holds statistics from a garbage collection run.
type GCStats struct {
	VersionsPruned   int   // Number of version metadata files deleted
	ChunksDeleted    int   // Number of orphaned chunks deleted
	BytesReclaimed   int64 // Approximate bytes reclaimed from chunk deletion
	ObjectsScanned   int   // Number of objects scanned
	BucketsProcessed int   // Number of buckets processed
}

// RunGarbageCollection performs a full garbage collection pass.
// This should be called periodically (e.g., hourly) to clean up:
//   - Expired versions according to retention policy
//   - Orphaned chunks not referenced by any object
//
// Uses a phased approach to minimize lock contention:
//   - Phase 1: Prune expired versions (Lock per object, brief)
//   - Phase 2: Rebuild reference set after pruning (RLock only)
//   - Phase 3: Delete orphaned chunks (no Store lock, CAS has its own)
func (s *Store) RunGarbageCollection(ctx context.Context) GCStats {
	stats := GCStats{}

	if s.cas == nil {
		return stats // No CAS, no GC needed
	}

	// Record start time to avoid deleting chunks created during GC
	gcStartTime := time.Now()

	// Phase 1: Prune expired versions across all buckets
	// This uses the original per-object pruning which is safe for concurrent access
	s.pruneAllExpiredVersionsSimple(ctx, &stats)

	// Phase 2: Build reference set AFTER pruning (read-only)
	// This ensures we capture the current state after version deletion
	referencedChunks := s.buildChunkReferenceSet()

	// Phase 3: Delete orphaned chunks (not in reference set)
	// CAS has its own locking; we don't need Store lock here
	s.deleteOrphanedChunks(ctx, &stats, referencedChunks, gcStartTime)

	// Update quota if tracking
	if s.quota != nil {
		s.mu.Lock()
		_ = s.calculateQuotaUsage()
		s.mu.Unlock()
	}

	return stats
}

// buildChunkReferenceSet scans all objects and versions to find referenced chunks.
// Uses RLock to allow concurrent reads during the scan.
func (s *Store) buildChunkReferenceSet() map[string]struct{} {
	referencedChunks := make(map[string]struct{})

	s.mu.RLock()
	defer s.mu.RUnlock()

	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return referencedChunks
	}

	for _, bucketEntry := range bucketEntries {
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()

		// Scan current objects
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		metaEntries, _ := os.ReadDir(metaDir)
		for _, metaEntry := range metaEntries {
			if metaEntry.IsDir() || filepath.Ext(metaEntry.Name()) != ".json" {
				continue
			}

			metaPath := filepath.Join(metaDir, metaEntry.Name())
			data, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var meta ObjectMeta
			if json.Unmarshal(data, &meta) == nil {
				for _, h := range meta.Chunks {
					referencedChunks[h] = struct{}{}
				}
			}
		}

		// Scan versions
		versionsDir := filepath.Join(bucketsDir, bucket, "versions")
		_ = filepath.Walk(versionsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, _ := os.ReadFile(path)
			var meta ObjectMeta
			if json.Unmarshal(data, &meta) == nil {
				for _, h := range meta.Chunks {
					referencedChunks[h] = struct{}{}
				}
			}
			return nil
		})
	}

	return referencedChunks
}

// pruneAllExpiredVersionsSimple prunes expired versions across all buckets.
// Takes brief write locks per object to minimize contention.
// Uses the standard pruneExpiredVersions which handles chunk cleanup safely.
func (s *Store) pruneAllExpiredVersionsSimple(ctx context.Context, stats *GCStats) {
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return
	}

	for _, bucketEntry := range bucketEntries {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()
		stats.BucketsProcessed++

		// Find all objects in this bucket
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		metaEntries, _ := os.ReadDir(metaDir)

		for _, metaEntry := range metaEntries {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if metaEntry.IsDir() || filepath.Ext(metaEntry.Name()) != ".json" {
				continue
			}
			stats.ObjectsScanned++

			// Extract key from filename
			key := strings.TrimSuffix(metaEntry.Name(), ".json")

			// Brief lock for this object's pruning
			s.mu.Lock()
			pruned := s.pruneExpiredVersions(ctx, bucket, key)
			s.mu.Unlock()

			stats.VersionsPruned += pruned
		}
	}
}

// deleteOrphanedChunks deletes chunks not in the reference set.
// Uses CAS's own locking; doesn't need Store lock.
// Skips chunks created after gcStartTime to avoid races.
func (s *Store) deleteOrphanedChunks(ctx context.Context, stats *GCStats, referencedChunks map[string]struct{}, gcStartTime time.Time) {
	chunksDir := filepath.Join(s.dataDir, "chunks")
	_ = filepath.Walk(chunksDir, func(path string, info os.FileInfo, err error) error {
		select {
		case <-ctx.Done():
			return fmt.Errorf("delete orphaned chunks cancelled: %w", ctx.Err())
		default:
		}
		if err != nil || info.IsDir() {
			return nil
		}

		// Skip chunks created after GC started (race protection)
		if info.ModTime().After(gcStartTime) {
			return nil
		}

		// Extract hash from path (chunks are stored as chunks/ab/abcdef...)
		hash := filepath.Base(path)
		if _, referenced := referencedChunks[hash]; !referenced {
			stats.BytesReclaimed += info.Size()
			// CAS.DeleteChunk has its own locking
			if s.cas.DeleteChunk(ctx, hash) == nil {
				stats.ChunksDeleted++

				// Unregister from chunk registry (if configured)
				// Note: In Phase 6, we'll make GC registry-aware to check if
				// chunks are owned by other coordinators before deleting
				if s.chunkRegistry != nil {
					_ = s.chunkRegistry.UnregisterChunk(hash)
				}
			}
		}
		return nil
	})
}

// ListVersions returns all versions of an object.
func (s *Store) ListVersions(ctx context.Context, bucket, key string) ([]VersionInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	var versions []VersionInfo

	// Get current version
	if meta, err := s.getObjectMeta(bucket, key); err == nil {
		versions = append(versions, VersionInfo{
			VersionID:    meta.VersionID,
			Size:         meta.Size,
			ETag:         meta.ETag,
			LastModified: meta.LastModified,
			IsCurrent:    true,
		})
	}

	// Get archived versions
	versionDir := s.versionDir(bucket, key)
	entries, err := os.ReadDir(versionDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read version dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		versionPath := filepath.Join(versionDir, entry.Name())
		data, err := os.ReadFile(versionPath)
		if err != nil {
			continue
		}

		var meta ObjectMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		versions = append(versions, VersionInfo{
			VersionID:    meta.VersionID,
			Size:         meta.Size,
			ETag:         meta.ETag,
			LastModified: meta.LastModified,
			IsCurrent:    false,
		})
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].LastModified.After(versions[j].LastModified)
	})

	return versions, nil
}

// GetObjectVersion retrieves a specific version of an object.
func (s *Store) GetObjectVersion(ctx context.Context, bucket, key, versionID string) (io.ReadCloser, *ObjectMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, nil, err
	}

	// Check if it's the current version
	currentMeta, err := s.getObjectMeta(bucket, key)
	if err != nil && !errors.Is(err, ErrObjectNotFound) {
		return nil, nil, err
	}

	if currentMeta != nil && currentMeta.VersionID == versionID {
		// Return current version
		return s.getObjectContent(ctx, bucket, key, currentMeta)
	}

	// Look for archived version
	versionPath := s.versionMetaPath(bucket, key, versionID)
	data, err := os.ReadFile(versionPath)
	if os.IsNotExist(err) {
		return nil, nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read version meta: %w", err)
	}

	var meta ObjectMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, nil, fmt.Errorf("unmarshal version meta: %w", err)
	}

	return s.getObjectContent(ctx, bucket, key, &meta)
}

// chunkReader implements io.ReadCloser for streaming chunk reads.
// It reads chunks on-demand from CAS, avoiding loading entire files into memory.
type chunkReader struct {
	// Note: Storing context here is necessary because io.Reader.Read() doesn't accept context.
	// This is a short-lived struct (per-request lifetime) used for streaming object content.
	// The context is checked on each Read() call to respect cancellation.
	ctx       context.Context
	cas       *CAS
	chunks    []string // Ordered list of chunk hashes
	chunkIdx  int      // Current chunk index
	chunkData []byte   // Current chunk data
	chunkPos  int      // Position within current chunk
}

// newChunkReader creates a streaming reader for the given chunks.
func newChunkReader(ctx context.Context, cas *CAS, chunks []string) *chunkReader {
	return &chunkReader{
		ctx:    ctx,
		cas:    cas,
		chunks: chunks,
	}
}

// Read implements io.Reader, fetching chunks on demand.
func (r *chunkReader) Read(p []byte) (n int, err error) {
	// Check for context cancellation before reading
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read cancelled: %w", err)
	}

	for n < len(p) {
		// If we've exhausted current chunk, load the next one
		if r.chunkPos >= len(r.chunkData) {
			if r.chunkIdx >= len(r.chunks) {
				// No more chunks
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}

			// Check for cancellation before expensive I/O
			if err := r.ctx.Err(); err != nil {
				return n, fmt.Errorf("read cancelled: %w", err)
			}

			// Load next chunk
			r.chunkData, err = r.cas.ReadChunk(r.ctx, r.chunks[r.chunkIdx])
			if err != nil {
				return n, fmt.Errorf("read chunk %s: %w", r.chunks[r.chunkIdx], err)
			}
			r.chunkIdx++
			r.chunkPos = 0
		}

		// Copy from current chunk to output buffer
		copied := copy(p[n:], r.chunkData[r.chunkPos:])
		r.chunkPos += copied
		n += copied
	}
	return n, nil
}

// Close implements io.Closer.
func (r *chunkReader) Close() error {
	// Release chunk data for GC
	r.chunkData = nil
	return nil
}

// getObjectContent returns the content of an object by reading its chunks from CAS.
// Uses streaming to avoid loading entire files into memory.
func (s *Store) getObjectContent(ctx context.Context, bucket, key string, meta *ObjectMeta) (io.ReadCloser, *ObjectMeta, error) {
	if s.cas == nil {
		return nil, nil, fmt.Errorf("CAS not initialized")
	}

	if len(meta.Chunks) == 0 {
		// Empty file
		return io.NopCloser(bytes.NewReader(nil)), meta, nil
	}

	return newChunkReader(ctx, s.cas, meta.Chunks), meta, nil
}

// RestoreVersion makes a previous version the current version.
// This creates a new version (with new version ID) containing the old version's content.
func (s *Store) RestoreVersion(ctx context.Context, bucket, key, versionID string) (*ObjectMeta, error) {
	// Validate names
	if err := validateName(bucket); err != nil {
		return nil, fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return nil, fmt.Errorf("invalid key: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	// Find the version to restore
	versionPath := s.versionMetaPath(bucket, key, versionID)
	data, err := os.ReadFile(versionPath)
	if os.IsNotExist(err) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read version meta: %w", err)
	}

	var oldMeta ObjectMeta
	if err := json.Unmarshal(data, &oldMeta); err != nil {
		return nil, fmt.Errorf("unmarshal version meta: %w", err)
	}

	// Archive current version
	if err := s.archiveCurrentVersion(bucket, key); err != nil {
		return nil, fmt.Errorf("archive current version: %w", err)
	}

	// Create new metadata pointing to old content
	now := time.Now().UTC()
	newMeta := ObjectMeta{
		Key:          key,
		Size:         oldMeta.Size,
		ContentType:  oldMeta.ContentType,
		ETag:         oldMeta.ETag,
		LastModified: now,
		Metadata:     oldMeta.Metadata,
		VersionID:    generateVersionID(),
		Chunks:       oldMeta.Chunks, // Reuse same chunks (no duplication)
	}

	// Set expiry if configured
	if s.defaultObjectExpiryDays > 0 && bucket != SystemBucket {
		expiry := now.AddDate(0, 0, s.defaultObjectExpiryDays)
		newMeta.Expires = &expiry
	}

	// Write new metadata
	metaPath := s.objectMetaPath(bucket, key)
	if err := os.MkdirAll(filepath.Dir(metaPath), 0755); err != nil {
		return nil, fmt.Errorf("create meta dir: %w", err)
	}

	metaData, err := json.MarshalIndent(newMeta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal object meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, metaData, 0644); err != nil {
		return nil, fmt.Errorf("write object meta: %w", err)
	}

	// Prune expired versions
	s.pruneExpiredVersions(ctx, bucket, key)

	return &newMeta, nil
}

// deleteAllVersions removes all versions of an object (used when deleting the object).
func (s *Store) deleteAllVersions(ctx context.Context, bucket, key string) {
	versionDir := s.versionDir(bucket, key)

	// Collect all chunk hashes from versions for GC
	chunksToCheck := make(map[string]struct{})
	entries, err := os.ReadDir(versionDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}

			versionPath := filepath.Join(versionDir, entry.Name())
			data, err := os.ReadFile(versionPath)
			if err != nil {
				continue
			}

			var meta ObjectMeta
			if err := json.Unmarshal(data, &meta); err != nil {
				continue
			}

			for _, h := range meta.Chunks {
				chunksToCheck[h] = struct{}{}
			}
		}
	}

	// Remove version directory
	_ = os.RemoveAll(versionDir)

	// GC chunks that are no longer referenced GLOBALLY
	// This checks ALL objects in ALL buckets to ensure chunk is truly orphaned
	if s.cas != nil {
		for hash := range chunksToCheck {
			if !s.isChunkReferencedGlobally(ctx, hash) {
				_ = s.cas.DeleteChunk(ctx, hash)
			}
		}
	}
}

// Close flushes any pending operations and syncs the data directory.
// Since all writes use syncedWriteFile (which calls fsync), this just ensures
// directory metadata is flushed for durability.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Sync all bucket directories and their subdirectories to ensure
	// all filesystem operations are complete before cleanup.
	// This is critical for tests with race detector where cleanup happens immediately.
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	if buckets, err := os.ReadDir(bucketsDir); err == nil {
		for _, bucket := range buckets {
			if !bucket.IsDir() {
				continue
			}
			bucketPath := filepath.Join(bucketsDir, bucket.Name())
			metaDir := filepath.Join(bucketPath, "meta")

			// Sync all metadata files first
			if metaFiles, err := os.ReadDir(metaDir); err == nil {
				for _, metaFile := range metaFiles {
					if metaFile.IsDir() {
						continue
					}
					metaPath := filepath.Join(metaDir, metaFile.Name())
					if f, err := os.Open(metaPath); err == nil {
						_ = f.Sync()
						_ = f.Close()
					}
				}
			}

			// Sync meta directory itself
			if f, err := os.Open(metaDir); err == nil {
				_ = f.Sync()
				_ = f.Close()
			}

			// Sync objects directory if it exists
			objectsDir := filepath.Join(bucketPath, "objects")
			if f, err := os.Open(objectsDir); err == nil {
				_ = f.Sync()
				_ = f.Close()
			}

			// Sync bucket directory
			if f, err := os.Open(bucketPath); err == nil {
				_ = f.Sync()
				_ = f.Close()
			}
		}

		// Sync buckets directory
		if f, err := os.Open(bucketsDir); err == nil {
			_ = f.Sync()
			_ = f.Close()
		}
	}

	// Sync the data directory to ensure directory entries are durable
	if f, err := os.Open(s.dataDir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}

	return nil
}
