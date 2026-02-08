// Package s3 provides an S3-compatible object storage service for the coordinator.
package s3

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BucketMeta contains bucket metadata.
type BucketMeta struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Owner     string    `json:"owner"` // User ID who created the bucket
}

// ObjectMeta contains object metadata.
type ObjectMeta struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ContentType  string            `json:"content_type"`
	ETag         string            `json:"etag"` // MD5 hash of content
	LastModified time.Time         `json:"last_modified"`
	Expires      *time.Time        `json:"expires,omitempty"`  // Optional expiration date
	Metadata     map[string]string `json:"metadata,omitempty"` // User-defined metadata
}

// Store provides file-based S3 storage.
// Directory structure:
//
//	{dataDir}/
//	  buckets/
//	    {bucket}/
//	      _meta.json          # bucket metadata
//	      objects/
//	        {key}             # object data
//	      meta/
//	        {key}.json        # object metadata
type Store struct {
	dataDir string
	quota   *QuotaManager
	mu      sync.RWMutex
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

// DataDir returns the data directory path.
func (s *Store) DataDir() string {
	return s.dataDir
}

// QuotaStats returns current quota statistics, or nil if no quota is configured.
func (s *Store) QuotaStats() *QuotaStats {
	if s.quota == nil {
		return nil
	}
	stats := s.quota.Stats()
	return &stats
}

// calculateQuotaUsage scans all objects and updates quota tracking.
func (s *Store) calculateQuotaUsage() error {
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	entries, err := os.ReadDir(bucketsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bucket := entry.Name()
		var bucketSize int64

		// Sum all object sizes in this bucket
		objectsDir := filepath.Join(bucketsDir, bucket, "objects")
		_ = filepath.Walk(objectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			bucketSize += info.Size()
			return nil
		})

		if bucketSize > 0 {
			s.quota.SetUsed(bucket, bucketSize)
		}
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

// objectPath returns the path to an object data file.
func (s *Store) objectPath(bucket, key string) string {
	return filepath.Join(s.bucketPath(bucket), "objects", key)
}

// objectMetaPath returns the path to an object metadata file.
func (s *Store) objectMetaPath(bucket, key string) string {
	return filepath.Join(s.bucketPath(bucket), "meta", key+".json")
}

// CreateBucket creates a new bucket.
func (s *Store) CreateBucket(bucket, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucketDir := s.bucketPath(bucket)

	// Check if bucket already exists
	if _, err := os.Stat(bucketDir); err == nil {
		return ErrBucketExists
	}

	// Create bucket directory structure
	if err := os.MkdirAll(filepath.Join(bucketDir, "objects"), 0755); err != nil {
		return fmt.Errorf("create bucket objects dir: %w", err)
	}
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

	if err := os.WriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write bucket meta: %w", err)
	}

	return nil
}

// DeleteBucket removes an empty bucket.
func (s *Store) DeleteBucket(bucket string) error {
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

// HeadBucket checks if a bucket exists.
func (s *Store) HeadBucket(bucket string) (*BucketMeta, error) {
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
func (s *Store) ListBuckets() ([]BucketMeta, error) {
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

// PutObject writes an object to a bucket.
func (s *Store) PutObject(bucket, key string, reader io.Reader, size int64, contentType string, metadata map[string]string) (*ObjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	objectPath := s.objectPath(bucket, key)
	metaPath := s.objectMetaPath(bucket, key)

	// Check if object already exists (for quota update calculation)
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

	// Create parent directories for nested keys
	if err := os.MkdirAll(filepath.Dir(objectPath), 0755); err != nil {
		return nil, fmt.Errorf("create object dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0755); err != nil {
		return nil, fmt.Errorf("create meta dir: %w", err)
	}

	// Write object data while computing MD5 hash
	file, err := os.Create(objectPath)
	if err != nil {
		return nil, fmt.Errorf("create object file: %w", err)
	}
	defer func() { _ = file.Close() }()

	hash := md5.New()
	teeReader := io.TeeReader(reader, hash)

	written, err := io.Copy(file, teeReader)
	if err != nil {
		_ = os.Remove(objectPath)
		return nil, fmt.Errorf("write object: %w", err)
	}

	// Update quota tracking
	if s.quota != nil {
		if oldSize > 0 {
			s.quota.Update(bucket, oldSize, written)
		} else {
			s.quota.Allocate(bucket, written)
		}
	}

	// Generate ETag from MD5 hash (S3-compatible format)
	etag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash.Sum(nil)))

	// Write object metadata
	objMeta := ObjectMeta{
		Key:          key,
		Size:         written,
		ContentType:  contentType,
		ETag:         etag,
		LastModified: time.Now().UTC(),
		Metadata:     metadata,
	}

	metaData, err := json.MarshalIndent(objMeta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal object meta: %w", err)
	}

	if err := os.WriteFile(metaPath, metaData, 0644); err != nil {
		return nil, fmt.Errorf("write object meta: %w", err)
	}

	return &objMeta, nil
}

// GetObject retrieves an object from a bucket.
func (s *Store) GetObject(bucket, key string) (io.ReadCloser, *ObjectMeta, error) {
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

	// Open object file
	objectPath := s.objectPath(bucket, key)
	file, err := os.Open(objectPath)
	if os.IsNotExist(err) {
		return nil, nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open object: %w", err)
	}

	return file, meta, nil
}

// HeadObject returns object metadata without the body.
func (s *Store) HeadObject(bucket, key string) (*ObjectMeta, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	return s.getObjectMeta(bucket, key)
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

// DeleteObject removes an object from a bucket.
func (s *Store) DeleteObject(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return err
	}

	// Get object size for quota release
	var objectSize int64
	if meta, err := s.getObjectMeta(bucket, key); err == nil {
		objectSize = meta.Size
	}

	objectPath := s.objectPath(bucket, key)
	metaPath := s.objectMetaPath(bucket, key)

	// Check if object exists
	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		return ErrObjectNotFound
	}

	// Remove object and metadata
	if err := os.Remove(objectPath); err != nil {
		return fmt.Errorf("remove object: %w", err)
	}
	_ = os.Remove(metaPath) // Ignore error if meta doesn't exist

	// Release quota
	if s.quota != nil && objectSize > 0 {
		s.quota.Release(bucket, objectSize)
	}

	return nil
}

// ListObjects lists objects in a bucket with optional prefix filter and pagination.
// marker is the key to start after (exclusive) for pagination.
// Returns (objects, isTruncated, nextMarker, error).
func (s *Store) ListObjects(bucket, prefix, marker string, maxKeys int) ([]ObjectMeta, bool, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, false, "", err
	}

	return s.listObjectsUnsafe(bucket, prefix, marker, maxKeys)
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
