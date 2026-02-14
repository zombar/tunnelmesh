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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// GCGracePeriod is the minimum age of a chunk before it can be garbage collected.
// This prevents deleting chunks that are being replicated or uploaded.
//
// IMPORTANT: This assumes file uploads complete within this duration. For very large
// files or slow networks, increase this value to prevent premature chunk deletion.
//
// Default: 1 hour (suitable for typical replication lag and upload durations)
// For large file uploads: Consider 24 hours or more
const GCGracePeriod = 1 * time.Hour

// MaxErasureCodingFileSize is the maximum file size for erasure coding (Phase 1).
// Files larger than this will use standard replication.
// Streaming encoder (Phase 6) will remove this limit.
const MaxErasureCodingFileSize = 100 * 1024 * 1024 // 100 MB

// contextCheckInterval is how often to check for context cancellation during
// chunk fetching loops (~400KB at average chunk size of 4KB).
const contextCheckInterval = 100

// windowsFileRetries is the number of retry attempts for file operations on Windows,
// where antivirus or search indexer may transiently hold file handles.
const windowsFileRetries = 5

// windowsFileRetryDelay is the delay between file operation retries on Windows.
const windowsFileRetryDelay = 50 * time.Millisecond

// ErasureCodingPolicy defines the erasure coding configuration for a bucket.
type ErasureCodingPolicy struct {
	Enabled      bool `json:"enabled"`       // Whether erasure coding is enabled for new objects
	DataShards   int  `json:"data_shards"`   // k: number of data shards (must be >= 1)
	ParityShards int  `json:"parity_shards"` // m: number of parity shards (must be >= 1)
}

// BucketMeta contains bucket metadata.
type BucketMeta struct {
	Name              string               `json:"name"`
	CreatedAt         time.Time            `json:"created_at"`
	Owner             string               `json:"owner"`                    // User ID who created the bucket
	TombstonedAt      *time.Time           `json:"tombstoned_at,omitempty"`  // When bucket was soft-deleted
	SizeBytes         int64                `json:"size_bytes"`               // Total size of non-tombstoned objects (updated incrementally)
	ReplicationFactor int                  `json:"replication_factor"`       // Number of replicas (1-3)
	ErasureCoding     *ErasureCodingPolicy `json:"erasure_coding,omitempty"` // Erasure coding policy for new objects
}

// IsTombstoned returns true if the bucket has been soft-deleted.
func (bm *BucketMeta) IsTombstoned() bool {
	return bm.TombstonedAt != nil
}

// BucketMetadataUpdate contains mutable bucket metadata fields (admin-only).
type BucketMetadataUpdate struct {
	ReplicationFactor *int                 `json:"replication_factor,omitempty"` // Update replication factor (1-3)
	ErasureCoding     *ErasureCodingPolicy `json:"erasure_coding,omitempty"`     // Update erasure coding policy
}

// ChunkMetadata contains per-chunk metadata for distributed replication.
type ChunkMetadata struct {
	Hash           string            `json:"hash"`                      // SHA-256 of chunk plaintext
	Size           int64             `json:"size"`                      // Uncompressed size
	CompressedSize int64             `json:"compressed_size,omitempty"` // Compressed size (if known)
	VersionVector  map[string]uint64 `json:"version_vector"`            // Causality tracking (coordinatorID -> counter)
	Owners         []string          `json:"owners,omitempty"`          // Coordinators that have this chunk
	FirstSeen      time.Time         `json:"first_seen"`                // When chunk was first created
	LastModified   time.Time         `json:"last_modified"`             // When chunk metadata was last updated
	ShardType      string            `json:"shard_type,omitempty"`      // "data" or "parity" (for erasure-coded files)
	ShardIndex     int               `json:"shard_index,omitempty"`     // Position in RS matrix (0-based)
	ChunkSequence  int               `json:"chunk_sequence,omitempty"`  // Order within a shard (0-based, for CDC chunk reassembly)
	ParentFileID   string            `json:"parent_file_id,omitempty"`  // Link to parent file VersionID
}

// ErasureCodingInfo contains erasure coding metadata for a specific object version.
type ErasureCodingInfo struct {
	Enabled      bool     `json:"enabled"`                 // Whether this object uses erasure coding
	DataShards   int      `json:"data_shards"`             // k: number of data shards
	ParityShards int      `json:"parity_shards"`           // m: number of parity shards
	ShardSize    int64    `json:"shard_size"`              // Bytes per shard (before padding)
	DataHashes   []string `json:"data_hashes,omitempty"`   // Original CDC chunk hashes (data shards)
	ParityHashes []string `json:"parity_hashes,omitempty"` // Parity shard hashes (parity-*)
}

// ObjectMeta contains object metadata.
type ObjectMeta struct {
	Key           string                    `json:"key"`
	Size          int64                     `json:"size"`
	ContentType   string                    `json:"content_type"`
	ETag          string                    `json:"etag"` // MD5 hash of content
	LastModified  time.Time                 `json:"last_modified"`
	Expires       *time.Time                `json:"expires,omitempty"`        // Optional expiration date
	TombstonedAt  *time.Time                `json:"tombstoned_at,omitempty"`  // When object was soft-deleted
	Metadata      map[string]string         `json:"metadata,omitempty"`       // User-defined metadata
	VersionID     string                    `json:"version_id,omitempty"`     // Version identifier
	Chunks        []string                  `json:"chunks,omitempty"`         // Ordered list of chunk hashes (CAS)
	ChunkMetadata map[string]*ChunkMetadata `json:"chunk_metadata,omitempty"` // Per-chunk metadata with version vectors
	VersionVector map[string]uint64         `json:"version_vector,omitempty"` // File-level version vector
	ErasureCoding *ErasureCodingInfo        `json:"erasure_coding,omitempty"` // Erasure coding info (if enabled)
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

	// RegisterChunkWithReplication registers a chunk with a custom replication factor
	RegisterChunkWithReplication(hash string, size int64, replicationFactor int) error

	// RegisterShardChunk registers an erasure-coded shard chunk with shard metadata
	RegisterShardChunk(hash string, size int64, parentFileID, shardType string, shardIndex, replicationFactor int) error

	// UnregisterChunk removes the local coordinator as an owner of the chunk
	UnregisterChunk(hash string) error

	// GetOwners returns the list of coordinator IDs that own the chunk
	GetOwners(hash string) ([]string, error)

	// GetChunksOwnedBy returns all chunk hashes owned by the specified coordinator
	GetChunksOwnedBy(coordID string) ([]string, error)
}

// Store provides S3 storage with content-addressable chunks and versioning.
// ReplicatorInterface defines the interface for chunk replication (to avoid import cycle).
type ReplicatorInterface interface {
	FetchChunk(ctx context.Context, peerID, chunkHash string) ([]byte, error)
	GetPeers() []string
}

type Store struct {
	dataDir                 string
	cas                     *CAS // Content-addressable storage for chunks
	quota                   *QuotaManager
	chunkRegistry           ChunkRegistryInterface // Optional distributed chunk ownership tracking
	replicator              ReplicatorInterface    // Optional replicator for fetching remote chunks (Phase 5)
	coordinatorID           string                 // Local coordinator ID for version vectors (optional)
	logger                  zerolog.Logger         // Structured logger
	defaultObjectExpiryDays int                    // Days until objects expire (0 = never)
	defaultShareExpiryDays  int                    // Days until file shares expire (0 = never)
	tombstoneRetentionDays  int                    // Days to retain tombstoned objects before purging (0 = never purge)
	versionRetentionDays    int                    // Days to retain object versions (0 = forever)
	maxVersionsPerObject    int                    // Max versions to keep per object (0 = unlimited)
	versionRetentionPolicy  VersionRetentionPolicy
	erasureCodingSemaphore  chan struct{}  // Limits concurrent erasure coding operations (memory safety)
	bgWg                    sync.WaitGroup // Tracks background goroutines (e.g., shard caching)
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
		dataDir:                dataDir,
		quota:                  quota,
		logger:                 zerolog.Nop(),           // Default to no-op logger
		erasureCodingSemaphore: make(chan struct{}, 10), // Allow 10 concurrent EC operations
	}

	// Calculate initial quota usage from existing objects
	if quota != nil {
		if err := store.calculateQuotaUsage(); err != nil {
			return nil, fmt.Errorf("calculate quota usage: %w", err)
		}
	}

	return store, nil
}

// syncedWriteFile atomically writes data to a file using write-to-temp + rename.
// This prevents data loss from disk-full (ENOSPC) or crash during write:
// data is written to a temp file in the same directory, fsynced, then renamed
// over the target. If any step fails, the original file is untouched.
//
// During tests, fsync is skipped (detected via TUNNELMESH_TEST=1 env var) since:
// - Tests use temp directories that are discarded anyway
// - fsync is very slow on Windows (100-500ms per call)
// - 179 S3 tests × fsync = 7+ minutes on Windows CI
func syncedWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	// Write to a temp file in the same directory (same filesystem for atomic rename)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// Clean up temp file on any failure
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(perm); err != nil {
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		return err
	}

	// Skip fsync during tests to avoid 7+ minute test times on Windows
	// Production code always fsyncs for durability
	if os.Getenv("TUNNELMESH_TEST") == "" {
		if err := tmp.Sync(); err != nil {
			return err
		}
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	// Atomic rename — on POSIX, rename within same filesystem is atomic
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	success = true
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

// SetReplicator sets the replicator for fetching remote chunks (Phase 5).
func (s *Store) SetReplicator(replicator ReplicatorInterface) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replicator = replicator
}

// WaitBackground blocks until all background goroutines (e.g., shard caching) complete.
func (s *Store) WaitBackground() {
	s.bgWg.Wait()
}

// SetCoordinatorID sets the coordinator ID for version vector tracking.
// This is optional and only used when replication is enabled.
func (s *Store) SetCoordinatorID(coordID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coordinatorID = coordID
}

// SetLogger sets the logger for the store.
func (s *Store) SetLogger(logger zerolog.Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = logger
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
// Uses s.dataDir (immutable after construction) for the filesystem walk,
// and QuotaManager.SetUsed has its own internal mutex.
func (s *Store) calculateQuotaUsage() error {
	if s.cas == nil {
		return nil
	}

	// s.dataDir is immutable after init — no lock needed for the walk.
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

// CreateBucket creates a new bucket with specified replication factor.
func (s *Store) CreateBucket(ctx context.Context, bucket, owner string, replicationFactor int, erasureCoding *ErasureCodingPolicy) error {
	// Validate bucket name (defense in depth)
	if err := validateName(bucket); err != nil {
		return fmt.Errorf("invalid bucket name: %w", err)
	}

	// Validate replication factor
	if replicationFactor < 1 || replicationFactor > 3 {
		return fmt.Errorf("replication factor must be 1-3, got %d", replicationFactor)
	}

	// Validate erasure coding policy if provided
	if erasureCoding != nil {
		if err := validateErasureCodingPolicy(erasureCoding); err != nil {
			return fmt.Errorf("invalid erasure coding policy: %w", err)
		}
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
		Name:              bucket,
		CreatedAt:         time.Now().UTC(),
		Owner:             owner,
		ReplicationFactor: replicationFactor,
		ErasureCoding:     erasureCoding,
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

// UpdateBucketMetadata updates mutable bucket metadata (admin-only operation).
// Can update replication factor and erasure coding policy.
func (s *Store) UpdateBucketMetadata(ctx context.Context, bucket string, updates BucketMetadataUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta, err := s.getBucketMeta(bucket)
	if err != nil {
		return err
	}

	// Validate and apply replication factor update
	if updates.ReplicationFactor != nil {
		rf := *updates.ReplicationFactor
		if rf < 1 || rf > 3 {
			return fmt.Errorf("replication factor must be 1-3, got %d", rf)
		}
		meta.ReplicationFactor = rf
	}

	// Validate and apply erasure coding policy update
	if updates.ErasureCoding != nil {
		if err := validateErasureCodingPolicy(updates.ErasureCoding); err != nil {
			return fmt.Errorf("invalid erasure coding policy: %w", err)
		}
		meta.ErasureCoding = updates.ErasureCoding
	}

	// Save updated metadata
	return s.writeBucketMeta(bucket, meta)
}

// validateErasureCodingPolicy validates erasure coding policy parameters.
func validateErasureCodingPolicy(policy *ErasureCodingPolicy) error {
	// Always validate k and m values, even if disabled (prevents surprises when enabling later)
	if policy.DataShards < 1 {
		return fmt.Errorf("data shards (k) must be >= 1, got %d", policy.DataShards)
	}
	if policy.ParityShards < 1 {
		return fmt.Errorf("parity shards (m) must be >= 1, got %d", policy.ParityShards)
	}
	if policy.DataShards+policy.ParityShards > 256 {
		return fmt.Errorf("total shards (k+m) must be <= 256, got %d", policy.DataShards+policy.ParityShards)
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

	// Check if bucket has any live (non-tombstoned) objects.
	// Tombstoned objects don't block deletion — they'll be removed with the bucket directory.
	objects, _, _, err := s.listObjectsUnsafe(bucket, "", "", 0)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if !obj.IsTombstoned() {
			return ErrBucketNotEmpty
		}
	}

	// Remove bucket directory.
	// On Windows, file handles may be transiently held by OS processes
	// (antivirus, search indexer), causing RemoveAll to fail. Retry briefly.
	var removeErr error
	retries := 1
	if runtime.GOOS == "windows" {
		retries = windowsFileRetries
	}
	for i := 0; i < retries; i++ {
		removeErr = os.RemoveAll(bucketDir)
		if removeErr == nil {
			return nil
		}
		if i < retries-1 {
			time.Sleep(windowsFileRetryDelay)
		}
	}
	return fmt.Errorf("remove bucket: %w", removeErr)
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

	// Handle corrupted metadata (e.g. from past disk-full truncation)
	if len(data) == 0 {
		s.logger.Warn().Str("bucket", bucket).Str("path", metaPath).Msg("bucket metadata file is empty (possible disk-full corruption)")
		return nil, ErrBucketNotFound
	}

	var meta BucketMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		s.logger.Warn().Err(err).Str("bucket", bucket).Str("path", metaPath).Msg("bucket metadata file is corrupted")
		return nil, ErrBucketNotFound
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

// truncHash safely truncates a hash string for logging.
func truncHash(hash string) string {
	if len(hash) >= 8 {
		return hash[:8]
	}
	return hash
}

// fetchChunkDistributed tries local CAS first, then fetches from remote
// coordinators via the chunk registry and replicator.
func (s *Store) fetchChunkDistributed(ctx context.Context, chunkHash string) ([]byte, error) {
	// 1. Try local CAS (fast path)
	data, err := s.cas.ReadChunk(ctx, chunkHash)
	if err == nil {
		return data, nil
	}

	// 2. No distributed fetch possible without registry + replicator
	if s.chunkRegistry == nil || s.replicator == nil {
		return nil, fmt.Errorf("chunk %s not found locally and distributed fetch unavailable", truncHash(chunkHash))
	}

	// 3. Query registry for owners
	owners, regErr := s.chunkRegistry.GetOwners(chunkHash)
	if regErr != nil || len(owners) == 0 {
		return nil, fmt.Errorf("chunk %s: no remote owners found", truncHash(chunkHash))
	}

	// 4. Try each owner
	var lastErr error
	for _, ownerID := range owners {
		fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		data, lastErr = s.replicator.FetchChunk(fetchCtx, ownerID, chunkHash)
		cancel()
		if lastErr != nil {
			continue
		}

		// 5. Validate hash integrity
		if actualHash := ContentHash(data); actualHash != chunkHash {
			s.logger.Warn().
				Str("expected", truncHash(chunkHash)).
				Str("actual", truncHash(actualHash)).
				Str("owner", ownerID).
				Msg("remote chunk hash mismatch, skipping")
			lastErr = fmt.Errorf("hash mismatch from owner %s", ownerID)
			continue
		}

		// 6. Cache locally and register ownership (best-effort)
		if _, werr := s.cas.WriteChunk(ctx, data); werr != nil {
			s.logger.Warn().Err(werr).Str("hash", truncHash(chunkHash)).Msg("failed to cache remote chunk locally")
		} else if s.chunkRegistry != nil {
			if rerr := s.chunkRegistry.RegisterChunk(chunkHash, int64(len(data))); rerr != nil {
				s.logger.Warn().Err(rerr).Str("hash", truncHash(chunkHash)).Msg("failed to register chunk ownership")
			}
		}

		return data, nil
	}

	return nil, fmt.Errorf("chunk %s: failed to fetch from %d remote owners: %w", truncHash(chunkHash), len(owners), lastErr)
}

// getObjectContentWithErasureCoding reads an erasure-coded object and reconstructs
// missing data shards if necessary. Returns a reader for the reconstructed file.
//
// The write path CDC-chunks data shards, so ec.DataHashes contains chunk hashes (not shard hashes).
// We must fetch all chunks, reassemble them into data shards using ChunkMetadata.ShardIndex,
// then potentially reconstruct using Reed-Solomon if data shards are missing.
//
//nolint:gocyclo // Complexity from shard reconstruction logic
func (s *Store) getObjectContentWithErasureCoding(ctx context.Context, bucket, key string, meta *ObjectMeta) (io.ReadCloser, *ObjectMeta, error) {
	ec := meta.ErasureCoding
	k := ec.DataShards
	m := ec.ParityShards

	// Phase 1: Fetch all CDC chunks and reassemble into data shards
	// ec.DataHashes contains CDC chunk hashes from all data shards.
	// We group them by shard index using ChunkMetadata, then sort
	// by ChunkSequence to preserve correct ordering within each shard.
	type chunkEntry struct {
		data     []byte
		sequence int
	}
	dataShardChunks := make(map[int][]chunkEntry) // shardIndex -> []chunkEntry
	incompleteShard := make(map[int]bool)         // shards with any missing chunk
	for i, chunkHash := range ec.DataHashes {
		// Check for cancellation periodically
		if i%contextCheckInterval == 0 {
			select {
			case <-ctx.Done():
				return nil, nil, fmt.Errorf("context canceled during data chunk fetch: %w", ctx.Err())
			default:
			}
		}

		chunkMeta, exists := meta.ChunkMetadata[chunkHash]
		if !exists {
			return nil, nil, fmt.Errorf("missing chunk metadata for data chunk %s (versionID=%s)", truncHash(chunkHash), meta.VersionID)
		}
		if chunkMeta.ShardType != "data" {
			return nil, nil, fmt.Errorf("expected data shard chunk, got %s for chunk %s (versionID=%s)", chunkMeta.ShardType, truncHash(chunkHash), meta.VersionID)
		}

		// Fetch chunk from CAS (or remote coordinator)
		chunk, err := s.fetchChunkDistributed(ctx, chunkHash)
		if err != nil {
			// Chunk missing locally and remotely - entire shard will need reconstruction
			s.logger.Debug().Str("hash", truncHash(chunkHash)).Int("shard", chunkMeta.ShardIndex).Msg("data chunk unavailable")
			incompleteShard[chunkMeta.ShardIndex] = true
			continue
		}

		dataShardChunks[chunkMeta.ShardIndex] = append(dataShardChunks[chunkMeta.ShardIndex], chunkEntry{
			data:     chunk,
			sequence: chunkMeta.ChunkSequence,
		})
	}

	// Reassemble chunks into complete data shards
	dataShards := make([][]byte, k)
	for shardIdx := 0; shardIdx < k; shardIdx++ {
		// If any chunk was missing for this shard, mark entire shard as nil
		if incompleteShard[shardIdx] {
			dataShards[shardIdx] = nil
			continue
		}

		entries := dataShardChunks[shardIdx]
		if len(entries) == 0 {
			// No chunks found for this shard at all
			dataShards[shardIdx] = nil
			continue
		}

		// Sort chunks by sequence to ensure correct ordering within shard
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].sequence < entries[j].sequence
		})

		// Pre-allocate and concatenate chunks to rebuild shard
		totalSize := 0
		for _, entry := range entries {
			totalSize += len(entry.data)
		}
		shardData := make([]byte, 0, totalSize)
		for _, entry := range entries {
			shardData = append(shardData, entry.data...)
		}
		dataShards[shardIdx] = shardData
	}

	// Count available data shards
	availableDataShards := 0
	for i := 0; i < k; i++ {
		if dataShards[i] != nil {
			availableDataShards++
		}
	}

	// Phase 2: Fast path - all data shards available, skip parity fetch entirely
	if availableDataShards == k {
		// No reconstruction needed - reassemble file from data shards
		// Pre-allocate to avoid multiple reallocations
		totalShardSize := int64(0)
		for i := 0; i < k; i++ {
			totalShardSize += int64(len(dataShards[i]))
		}
		fileData := make([]byte, 0, totalShardSize)
		for i := 0; i < k; i++ {
			fileData = append(fileData, dataShards[i]...)
		}

		// Trim to original size (remove RS padding)
		if int64(len(fileData)) < meta.Size {
			return nil, nil, fmt.Errorf("reassembled data too small: got %d bytes, expected %d (versionID=%s)", len(fileData), meta.Size, meta.VersionID)
		}
		fileData = fileData[:meta.Size]

		return io.NopCloser(bytes.NewReader(fileData)), meta, nil
	}

	// Phase 3: Fetch parity shards (only needed when data shards are missing)
	// Check for cancellation before fetching parity shards
	select {
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("context canceled before parity shard fetch: %w", ctx.Err())
	default:
	}

	parityShards := make([][]byte, m)
	for i, parityHash := range ec.ParityHashes {
		chunk, err := s.fetchChunkDistributed(ctx, parityHash)
		if err != nil {
			// Parity shard unavailable locally and remotely
			s.logger.Debug().Str("hash", truncHash(parityHash)).Int("shard", i).Msg("parity shard unavailable")
			parityShards[i] = nil
			continue
		}
		parityShards[i] = chunk
	}

	// Count available parity shards
	availableParityShards := 0
	for i := 0; i < m; i++ {
		if parityShards[i] != nil {
			availableParityShards++
		}
	}

	// Phase 4: Reconstruction path - some data shards missing
	totalAvailable := availableDataShards + availableParityShards
	if totalAvailable < k {
		return nil, nil, fmt.Errorf("insufficient shards: need %d, have %d data + %d parity = %d total (versionID=%s)",
			k, availableDataShards, availableParityShards, totalAvailable, meta.VersionID)
	}

	s.logger.Info().
		Int("available_data", availableDataShards).
		Int("available_parity", availableParityShards).
		Int("needed", k).
		Str("version", meta.VersionID).
		Msg("reconstructing erasure-coded object")

	// Combine data + parity shards into single array for DecodeFile
	allShards := make([][]byte, k+m)
	for i := 0; i < k; i++ {
		allShards[i] = dataShards[i]
	}
	for i := 0; i < m; i++ {
		allShards[k+i] = parityShards[i]
	}

	// Check for cancellation before expensive reconstruction
	select {
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("context canceled before reconstruction: %w", ctx.Err())
	default:
	}

	// Reconstruct using Reed-Solomon
	// NOTE: DecodeFile modifies the shards slice in-place
	reconstructedData, err := DecodeFile(allShards, k, m, meta.Size)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to reconstruct file (versionID=%s): %w", meta.VersionID, err)
	}

	// Phase 5: Cache reconstructed data shards (best-effort, async)
	// This improves future read performance by storing reconstructed shards locally.
	// Deep copy reconstructed shards since DecodeFile modifies allShards in-place
	// and the caller may still be reading reconstructedData.
	cachedK := k
	cachedShards := make([][]byte, k)
	missingShards := make([]bool, k)
	for i := 0; i < k; i++ {
		missingShards[i] = dataShards[i] == nil
		if missingShards[i] && allShards[i] != nil {
			cachedShards[i] = make([]byte, len(allShards[i]))
			copy(cachedShards[i], allShards[i])
		}
	}
	s.bgWg.Add(1)
	go func() {
		defer s.bgWg.Done()
		// Use background context since original request may have completed
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Cache any data shards that were reconstructed
		for i := 0; i < cachedK; i++ {
			if !missingShards[i] {
				// Already had this shard
				continue
			}

			// This shard was reconstructed - cache it by chunking and storing
			reconstructedShard := cachedShards[i]
			if reconstructedShard == nil {
				s.logger.Warn().Int("shard", i).Msg("reconstructed shard is nil, skipping cache")
				continue
			}

			shardChunker := NewStreamingChunker(bytes.NewReader(reconstructedShard))
			for {
				chunk, chunkHash, err := shardChunker.NextChunk()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					s.logger.Warn().Err(err).Int("shard", i).Msg("failed to chunk reconstructed shard")
					break
				}

				// Write chunk to local CAS (best-effort)
				if _, err := s.cas.WriteChunk(bgCtx, chunk); err != nil {
					if len(chunkHash) >= 8 {
						s.logger.Warn().Err(err).Int("shard", i).Str("hash", chunkHash[:8]).Msg("failed to cache reconstructed chunk")
					} else {
						s.logger.Warn().Err(err).Int("shard", i).Str("hash", chunkHash).Msg("failed to cache reconstructed chunk")
					}
				}
			}
		}
	}()

	return io.NopCloser(bytes.NewReader(reconstructedData)), meta, nil
}

// putObjectWithErasureCoding stores an object using Reed-Solomon erasure coding.
// The entire file is buffered in memory, encoded into data+parity shards, then stored.
// Data shards are CDC chunked (preserves deduplication), parity shards are stored directly.
//
// NOTE: This is Phase 1 implementation with full buffering. Streaming encoder will be added in Phase 6.
//
//nolint:gocyclo // Complexity will be reduced when streaming encoder is added (Phase 6)
func (s *Store) putObjectWithErasureCoding(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string, metadata map[string]string, bucketMeta *BucketMeta) (*ObjectMeta, error) {
	// Caller must hold s.mu.Lock()

	k := bucketMeta.ErasureCoding.DataShards
	m := bucketMeta.ErasureCoding.ParityShards

	// Validate shard configuration (defense against malicious/corrupted bucket metadata)
	// While validateErasureCodingPolicy already checked this, re-validate at upload time
	// to prevent issues if metadata was corrupted or modified externally
	if k < 1 || k > 32 || m < 1 || m > 32 || k+m > 64 {
		return nil, fmt.Errorf("invalid erasure coding config: k=%d, m=%d (max 32 each, 64 total)", k, m)
	}

	// Acquire semaphore to limit concurrent erasure coding operations (memory safety)
	// Each operation buffers up to 100MB, so limiting concurrency prevents OOM
	select {
	case s.erasureCodingSemaphore <- struct{}{}:
		defer func() { <-s.erasureCodingSemaphore }()
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for erasure coding slot: %w", ctx.Err())
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

	// Buffer entire file into memory (required for Reed-Solomon encoding)
	data, err := io.ReadAll(io.LimitReader(reader, size))
	if err != nil {
		return nil, fmt.Errorf("read file data: %w", err)
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("file size mismatch: expected %d bytes, got %d", size, len(data))
	}

	// Generate version ID before encoding (needed for tracking shards)
	versionID := generateVersionID()

	// Check for cancellation before expensive encoding operation
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context canceled before encoding: %w", ctx.Err())
	default:
	}

	// Encode file into Reed-Solomon shards
	dataShards, parityShards, err := EncodeFile(data, k, m)
	if err != nil {
		return nil, fmt.Errorf("encode file with erasure coding (k=%d,m=%d,size=%d): %w", k, m, size, err)
	}

	// Calculate shard size for metadata
	shardSize := int64(0)
	if len(dataShards) > 0 && len(dataShards[0]) > 0 {
		shardSize = int64(len(dataShards[0]))
	}

	now := time.Now().UTC()
	coordID := s.coordinatorID

	// File-level version vector
	fileVersionVector := make(map[string]uint64)
	if coordID != "" {
		fileVersionVector[coordID] = 1
	}

	// Track all chunks (data shard chunks + parity shards)
	var chunks []string
	chunkMetadata := make(map[string]*ChunkMetadata)
	var dataHashes []string // Original CDC chunk hashes from data shards
	var parityHashes []string

	// Cleanup on failure: remove all written chunks from CAS and registry
	// This prevents orphaned data that would waste space until GC runs
	var success bool
	defer func() {
		if !success && len(chunks) > 0 {
			// Best-effort cleanup - errors are logged but not propagated
			for _, hash := range chunks {
				if err := s.cas.DeleteChunk(context.Background(), hash); err != nil {
					s.logger.Warn().Str("hash", hash[:8]).Err(err).Msg("failed to cleanup chunk during rollback")
				}
				if s.chunkRegistry != nil {
					if err := s.chunkRegistry.UnregisterChunk(hash); err != nil {
						s.logger.Warn().Str("hash", hash[:8]).Err(err).Msg("failed to unregister chunk during rollback")
					}
				}
			}
		}
	}()

	// Process data shards: chunk them using CDC to preserve deduplication
	// Each data shard is treated as a separate "mini-file" that gets chunked
	md5Hasher := md5.New()
	for i, shard := range dataShards {
		// Hash the original data for ETag calculation
		md5Hasher.Write(shard)

		// Chunk this data shard using CDC
		shardChunker := NewStreamingChunker(bytes.NewReader(shard))
		chunkSeq := 0 // Track chunk order within this shard
		for {
			chunk, chunkHash, err := shardChunker.NextChunk()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("chunk data shard %d/%d (versionID=%s): %w", i, k, versionID, err)
			}

			// Write chunk to CAS
			if _, err := s.cas.WriteChunk(ctx, chunk); err != nil {
				return nil, fmt.Errorf("write data shard %d/%d chunk %s (versionID=%s): %w", i, k, chunkHash[:8], versionID, err)
			}

			// Register chunk ownership
			if s.chunkRegistry != nil {
				// Register as data shard chunk
				if err := s.chunkRegistry.RegisterShardChunk(chunkHash, int64(len(chunk)), versionID, "data", i, bucketMeta.ReplicationFactor); err != nil {
					if os.Getenv("DEBUG") != "" || os.Getenv("TUNNELMESH_DEBUG") != "" {
						fmt.Fprintf(os.Stderr, "chunk registry warning: failed to register data shard chunk %s: %v\n", chunkHash[:8], err)
					}
				}
			}

			// Track chunk metadata
			versionVector := make(map[string]uint64)
			if coordID != "" {
				versionVector[coordID] = 1
			}
			var owners []string
			if coordID != "" {
				owners = []string{coordID}
			}

			chunkMetadata[chunkHash] = &ChunkMetadata{
				Hash:          chunkHash,
				Size:          int64(len(chunk)),
				VersionVector: versionVector,
				Owners:        owners,
				FirstSeen:     now,
				LastModified:  now,
				ShardType:     "data",
				ShardIndex:    i,
				ChunkSequence: chunkSeq,
				ParentFileID:  versionID,
			}

			chunks = append(chunks, chunkHash)
			dataHashes = append(dataHashes, chunkHash)
			chunkSeq++
		}
	}

	// Process parity shards: store directly without CDC chunking
	// (parity shards are already optimally sized and don't benefit from dedup)
	for i, shard := range parityShards {
		// Write parity shard directly to CAS (it will compute SHA-256 hash)
		parityHash, err := s.cas.WriteChunk(ctx, shard)
		if err != nil {
			return nil, fmt.Errorf("write parity shard %d/%d (versionID=%s): %w", i, m, versionID, err)
		}

		// Register parity shard ownership
		if s.chunkRegistry != nil {
			if err := s.chunkRegistry.RegisterShardChunk(parityHash, int64(len(shard)), versionID, "parity", i, bucketMeta.ReplicationFactor); err != nil {
				if os.Getenv("DEBUG") != "" || os.Getenv("TUNNELMESH_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "chunk registry warning: failed to register parity shard %s: %v\n", parityHash[:8], err)
				}
			}
		}

		// Track parity shard metadata
		versionVector := make(map[string]uint64)
		if coordID != "" {
			versionVector[coordID] = 1
		}
		var owners []string
		if coordID != "" {
			owners = []string{coordID}
		}

		chunkMetadata[parityHash] = &ChunkMetadata{
			Hash:          parityHash,
			Size:          int64(len(shard)),
			VersionVector: versionVector,
			Owners:        owners,
			FirstSeen:     now,
			LastModified:  now,
			ShardType:     "parity",
			ShardIndex:    i,
			ParentFileID:  versionID,
		}

		chunks = append(chunks, parityHash)
		parityHashes = append(parityHashes, parityHash)
	}

	// Generate ETag from MD5 hash of original data (S3-compatible format)
	hash := md5Hasher.Sum(nil)
	etag := fmt.Sprintf("\"%s\"", hex.EncodeToString(hash))

	// Update quota tracking
	var quotaUpdated bool
	if s.quota != nil {
		if oldSize > 0 {
			s.quota.Update(bucket, oldSize, size)
		} else {
			s.quota.Allocate(bucket, size)
		}
		quotaUpdated = true
	}

	// Create object metadata with erasure coding info
	objMeta := ObjectMeta{
		Key:           key,
		Size:          size, // Original file size (before encoding)
		ContentType:   contentType,
		ETag:          etag,
		LastModified:  now,
		Metadata:      metadata,
		VersionID:     versionID,
		Chunks:        chunks,
		ChunkMetadata: chunkMetadata,
		VersionVector: fileVersionVector,
		ErasureCoding: &ErasureCodingInfo{
			Enabled:      true,
			DataShards:   k,
			ParityShards: m,
			ShardSize:    shardSize,
			DataHashes:   dataHashes,
			ParityHashes: parityHashes,
		},
	}

	// Set expiry if configured (skip for system bucket)
	if s.defaultObjectExpiryDays > 0 && bucket != SystemBucket {
		expiry := now.AddDate(0, 0, s.defaultObjectExpiryDays)
		objMeta.Expires = &expiry
	}

	// Serialize and write metadata
	metaData, err := json.MarshalIndent(objMeta, "", "  ")
	if err != nil {
		// Rollback quota on failure
		if quotaUpdated {
			if oldSize > 0 {
				s.quota.Update(bucket, size, oldSize)
			} else {
				s.quota.Release(bucket, size)
			}
		}
		return nil, fmt.Errorf("marshal object meta: %w", err)
	}

	if err := syncedWriteFile(metaPath, metaData, 0644); err != nil {
		// Rollback quota on failure
		if quotaUpdated {
			if oldSize > 0 {
				s.quota.Update(bucket, size, oldSize)
			} else {
				s.quota.Release(bucket, size)
			}
		}
		return nil, fmt.Errorf("write object meta: %w", err)
	}

	// Prune expired versions (lazy cleanup).
	// Chunk GC for pruned versions is deferred to the periodic GC pass.
	s.pruneExpiredVersions(ctx, bucket, key)

	// Update bucket size
	sizeDelta := size - oldSize
	if sizeDelta != 0 {
		if err := s.updateBucketSize(bucket, sizeDelta); err != nil {
			// Log error but don't fail the put operation
			_ = err
		}
	}

	// Mark as successful to prevent cleanup rollback
	success = true

	return &objMeta, nil
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

	// Check bucket exists and get metadata (including replication factor)
	bucketMeta, err := s.getBucketMeta(bucket)
	if err != nil {
		return nil, err
	}

	// Get bucket's replication factor for chunk registration
	replicationFactor := bucketMeta.ReplicationFactor

	// Check if erasure coding is enabled and file size is suitable
	useErasureCoding := bucketMeta.ErasureCoding != nil &&
		bucketMeta.ErasureCoding.Enabled &&
		size > 0 &&
		size <= MaxErasureCodingFileSize

	if useErasureCoding {
		// Use erasure coding path (buffer entire file)
		return s.putObjectWithErasureCoding(ctx, bucket, key, reader, size, contentType, metadata, bucketMeta)
	}

	// Use standard streaming path for non-erasure-coded objects or large files
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
	chunkMetadata := make(map[string]*ChunkMetadata)
	var written int64
	md5Hasher := md5.New()
	now := time.Now().UTC()

	// Read coordinatorID once to avoid repeated field access
	// Safe because s.mu.Lock() is already held (line 626)
	coordID := s.coordinatorID

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

		// Register chunk ownership in distributed registry with bucket's replication factor
		if s.chunkRegistry != nil {
			if err := s.chunkRegistry.RegisterChunkWithReplication(chunkHash, int64(len(chunk)), replicationFactor); err != nil {
				// Don't fail upload if registry update fails (eventual consistency)
				// but log to stderr in debug mode for troubleshooting
				if os.Getenv("DEBUG") != "" || os.Getenv("TUNNELMESH_DEBUG") != "" {
					fmt.Fprintf(os.Stderr, "chunk registry warning: failed to register %s: %v\n", chunkHash[:8], err)
				}
			}
		}

		// Update MD5 for ETag
		md5Hasher.Write(chunk)

		// Create per-chunk metadata with version vector
		versionVector := make(map[string]uint64)
		if coordID != "" {
			versionVector[coordID] = 1 // Initial version
		}

		// Create owners array (only if coordinator ID is set)
		var owners []string
		if coordID != "" {
			owners = []string{coordID}
		}

		chunkMetadata[chunkHash] = &ChunkMetadata{
			Hash:          chunkHash,
			Size:          int64(len(chunk)),
			VersionVector: versionVector,
			Owners:        owners,
			FirstSeen:     now,
			LastModified:  now,
		}

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

	// Create file-level version vector
	fileVersionVector := make(map[string]uint64)
	if coordID != "" {
		fileVersionVector[coordID] = 1 // Initial version
	}

	// Write object metadata
	objMeta := ObjectMeta{
		Key:           key,
		Size:          written,
		ContentType:   contentType,
		ETag:          etag,
		LastModified:  now,
		Metadata:      metadata,
		VersionID:     generateVersionID(),
		Chunks:        chunks,
		ChunkMetadata: chunkMetadata,
		VersionVector: fileVersionVector,
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

	// Prune expired versions (lazy cleanup).
	// Chunk GC for pruned versions is deferred to the periodic GC pass.
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
// Uses a two-phase approach: collect metadata and remove files under lock,
// then check and delete chunks outside the lock to avoid holding the mutex
// during expensive global filesystem scans.
func (s *Store) PurgeObject(ctx context.Context, bucket, key string) error {
	// Phase 1: Hold lock briefly to collect chunk info and remove metadata
	s.mu.Lock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		s.mu.Unlock()
		return err
	}

	// Get current object metadata for quota release and chunk cleanup
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		s.mu.Unlock()
		return err
	}

	// Collect version chunks and remove version files
	versionChunks := s.collectAndDeleteAllVersions(bucket, key)

	// Preallocate for current + version chunks
	chunksToCheck := make([]string, 0, len(meta.Chunks)+len(versionChunks))
	chunksToCheck = append(chunksToCheck, meta.Chunks...)
	chunksToCheck = append(chunksToCheck, versionChunks...)

	// Remove current metadata.
	// On Windows, file handles may be transiently held by OS processes
	// (antivirus, search indexer), causing Remove to fail. Retry briefly.
	metaPath := s.objectMetaPath(bucket, key)
	retries := 1
	if runtime.GOOS == "windows" {
		retries = windowsFileRetries
	}
	for i := 0; i < retries; i++ {
		if removeErr := os.Remove(metaPath); removeErr == nil || os.IsNotExist(removeErr) {
			break
		} else if i < retries-1 {
			s.logger.Warn().Err(removeErr).
				Str("path", metaPath).
				Int("attempt", i+1).
				Msg("Failed to remove object metadata, retrying")
			time.Sleep(windowsFileRetryDelay)
		}
	}

	// Release quota
	if s.quota != nil && meta.Size > 0 {
		s.quota.Release(bucket, meta.Size)
	}

	// Decrement bucket size (object is actually being removed now)
	if meta.Size > 0 {
		_ = s.updateBucketSize(bucket, -meta.Size)
	}

	s.mu.Unlock()

	// Phase 2: Delete unreferenced chunks WITHOUT holding the lock.
	// This is best-effort: the object is already purged (metadata removed in Phase 1).
	// If context is cancelled, orphaned chunks will be cleaned by the next GC cycle.
	if s.cas != nil {
		for _, hash := range chunksToCheck {
			select {
			case <-ctx.Done():
				return nil // Object already purged; chunk cleanup deferred to GC
			default:
			}
			if !s.isChunkReferencedGlobally(ctx, hash) {
				_ = s.cas.DeleteChunk(ctx, hash)
			}
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

// PurgeAllTombstonedObjects removes all tombstoned objects regardless of retention period.
// Returns the number of objects purged.
func (s *Store) PurgeAllTombstonedObjects(ctx context.Context) int {
	purgedCount := 0

	buckets, err := s.ListBuckets(ctx)
	if err != nil {
		return 0
	}

	for _, bucket := range buckets {
		select {
		case <-ctx.Done():
			return purgedCount
		default:
		}

		marker := ""
		for {
			select {
			case <-ctx.Done():
				return purgedCount
			default:
			}

			objects, isTruncated, nextMarker, err := s.ListObjects(ctx, bucket.Name, "", marker, 1000)
			if err != nil {
				break
			}

			for _, obj := range objects {
				if obj.IsTombstoned() {
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

// ==== Phase 4: Chunk-Level Replication Interface Methods ====

// GetObjectMeta retrieves object metadata without loading chunk data.
// This is used by the replicator to determine which chunks to send.
func (s *Store) GetObjectMeta(ctx context.Context, bucket, key string) (*ObjectMeta, error) {
	// Validate names
	if err := validateName(bucket); err != nil {
		return nil, fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return nil, fmt.Errorf("invalid key: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check bucket exists
	if _, err := s.getBucketMeta(bucket); err != nil {
		return nil, err
	}

	// Get object metadata (includes chunk list and chunk metadata)
	meta, err := s.getObjectMeta(bucket, key)
	if err != nil {
		return nil, err
	}

	// Return copy to prevent external modification
	metaCopy := *meta
	return &metaCopy, nil
}

// ReadChunk reads a chunk from CAS by its hash.
// This is used by the replicator to fetch chunk data for sending to peers.
func (s *Store) ReadChunk(ctx context.Context, hash string) ([]byte, error) {
	if s.cas == nil {
		return nil, fmt.Errorf("CAS not initialized")
	}

	return s.cas.ReadChunk(ctx, hash)
}

// WriteChunkDirect writes chunk data directly to CAS.
// This is used by the replication receiver to store incoming chunks.
// Unlike WriteChunk via PutObject, this doesn't create object metadata.
func (s *Store) WriteChunkDirect(ctx context.Context, hash string, data []byte) error {
	if s.cas == nil {
		return fmt.Errorf("CAS not initialized")
	}

	// Validate hash for security - don't trust sender
	computedHash := ContentHash(data)
	if computedHash != hash {
		return fmt.Errorf("hash mismatch: expected %s, got %s", hash, computedHash)
	}

	// CAS WriteChunk is idempotent - if chunk exists, it returns immediately
	_, err := s.cas.WriteChunk(ctx, data)
	if err != nil {
		return fmt.Errorf("write chunk to CAS: %w", err)
	}

	return nil
}

// ImportObjectMeta writes object metadata directly without processing chunks.
// This is used by the replication receiver to create the metadata file so the
// remote coordinator can serve reads for objects whose chunks arrive separately.
// bucketOwner is used when auto-creating the bucket (empty string defaults to "system").
func (s *Store) ImportObjectMeta(ctx context.Context, bucket, key string, metaJSON []byte, bucketOwner string) error {
	// Validate names
	if err := validateName(bucket); err != nil {
		return fmt.Errorf("invalid bucket name: %w", err)
	}
	if err := validateName(key); err != nil {
		return fmt.Errorf("invalid key: %w", err)
	}

	// Validate that metaJSON is valid ObjectMeta
	var meta ObjectMeta
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return fmt.Errorf("invalid object meta JSON: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure bucket exists — create bucket meta if needed
	bucketMeta, err := s.getBucketMeta(bucket)
	if err != nil {
		// Bucket doesn't exist — create minimal bucket meta
		if bucketOwner == "" {
			bucketOwner = "system"
		}
		bucketMeta = &BucketMeta{
			Name:              bucket,
			CreatedAt:         time.Now(),
			Owner:             bucketOwner,
			ReplicationFactor: 2, // Auto-created during replication → multi-coordinator setup
		}
		bucketDir := s.bucketPath(bucket)
		if mkErr := os.MkdirAll(filepath.Join(bucketDir, "meta"), 0755); mkErr != nil {
			return fmt.Errorf("create bucket directories: %w", mkErr)
		}
		bmData, marshalErr := json.Marshal(bucketMeta)
		if marshalErr != nil {
			return fmt.Errorf("marshal bucket meta: %w", marshalErr)
		}
		if writeErr := syncedWriteFile(s.bucketMetaPath(bucket), bmData, 0644); writeErr != nil {
			return fmt.Errorf("write bucket meta: %w", writeErr)
		}
	}

	// Ensure meta directory exists
	metaDir := filepath.Join(s.bucketPath(bucket), "meta")
	if mkErr := os.MkdirAll(metaDir, 0755); mkErr != nil {
		return fmt.Errorf("create meta directory: %w", mkErr)
	}

	// Write the object metadata file
	metaPath := s.objectMetaPath(bucket, key)

	// Ensure parent directory exists (for nested keys like "dir/file.txt")
	if mkErr := os.MkdirAll(filepath.Dir(metaPath), 0755); mkErr != nil {
		return fmt.Errorf("create meta parent directory: %w", mkErr)
	}

	// Check if object already exists (for idempotent retries)
	// Subtract old size before adding new to prevent double-counting
	if oldMeta, err := s.getObjectMeta(bucket, key); err == nil {
		bucketMeta.SizeBytes -= oldMeta.Size
	}

	if writeErr := syncedWriteFile(metaPath, metaJSON, 0644); writeErr != nil {
		return fmt.Errorf("write object meta: %w", writeErr)
	}

	// Update bucket size tracking (idempotent: old size subtracted above)
	bucketMeta.SizeBytes += meta.Size
	bmData, marshalErr := json.Marshal(bucketMeta)
	if marshalErr != nil {
		return fmt.Errorf("marshal bucket meta: %w", marshalErr)
	}
	if writeErr := syncedWriteFile(s.bucketMetaPath(bucket), bmData, 0644); writeErr != nil {
		return fmt.Errorf("update bucket meta: %w", writeErr)
	}

	return nil
}

// DeleteChunk removes a chunk from CAS by its hash.
// This is used by the replicator to clean up non-assigned chunks after replication.
func (s *Store) DeleteChunk(ctx context.Context, hash string) error {
	if s.cas == nil {
		return fmt.Errorf("CAS not initialized")
	}

	return s.cas.DeleteChunk(ctx, hash)
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
func (s *Store) pruneExpiredVersions(ctx context.Context, bucket, key string) (int, []string) {
	versionDir := s.versionDir(bucket, key)
	entries, err := os.ReadDir(versionDir)
	if err != nil {
		return 0, nil
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
		return 0, nil
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

	// Delete versions not in keep set, collect chunks for deferred GC
	prunedCount := 0
	chunksToCheck := make([]string, 0, len(versions))
	seen := make(map[string]struct{})
	for i, v := range versions {
		if keep[i] {
			continue
		}

		// Delete version metadata
		_ = os.Remove(v.path)
		prunedCount++

		// Collect chunks for deferred GC (done outside the lock by caller)
		for _, hash := range v.meta.Chunks {
			if _, ok := seen[hash]; !ok {
				seen[hash] = struct{}{}
				chunksToCheck = append(chunksToCheck, hash)
			}
		}
	}

	return prunedCount, chunksToCheck
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

		// Check all current objects in this bucket (walk to include subdirectories)
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		found := false
		parseError := false
		_ = filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				parseError = true
				return filepath.SkipAll
			}
			var meta ObjectMeta
			if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr != nil {
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
			return true
		}

		// Check all versions directory
		versionsDir := filepath.Join(bucketsDir, bucket, "versions")
		found = false
		parseError = false
		_ = filepath.Walk(versionsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				parseError = true
				return filepath.SkipAll
			}
			var meta ObjectMeta
			if unmarshalErr := json.Unmarshal(data, &meta); unmarshalErr != nil {
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
	VersionsPruned           int   // Number of version metadata files deleted
	ChunksDeleted            int   // Number of orphaned chunks deleted
	BytesReclaimed           int64 // Approximate bytes reclaimed from chunk deletion
	ObjectsScanned           int   // Number of objects scanned
	BucketsProcessed         int   // Number of buckets processed
	ChunksSkippedShared      int   // Chunks skipped because owned by other coordinators (Phase 6)
	ChunksSkippedGracePeriod int   // Chunks skipped due to grace period (Phase 6)
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

	// Phase 1: Prune expired versions across all buckets.
	// Returns chunks from pruned versions that may now be unreferenced.
	chunksToCheck := s.pruneAllExpiredVersionsSimple(ctx, &stats)

	// Phase 2: Build reference set ONCE after pruning (lock-free filesystem scan).
	// This single scan serves both pruned-version chunk cleanup and orphan detection,
	// avoiding the previous double-scan that held RLock for extended periods.
	referencedChunks := s.buildChunkReferenceSet(ctx)

	// Phase 2b: Delete chunks from pruned versions that are no longer referenced
	for _, hash := range chunksToCheck {
		select {
		case <-ctx.Done():
			return stats
		default:
		}
		if _, ok := referencedChunks[hash]; !ok {
			_ = s.cas.DeleteChunk(ctx, hash)
		}
	}

	// Phase 3: Delete orphaned chunks (not in reference set)
	// CAS has its own locking; we don't need Store lock here
	s.deleteOrphanedChunks(ctx, &stats, referencedChunks, gcStartTime)

	// Update quota if tracking
	// Note: calculateQuotaUsage acquires s.mu.Lock() internally,
	// so we must NOT hold the lock here.
	if s.quota != nil {
		_ = s.calculateQuotaUsage()
	}

	return stats
}

// buildChunkReferenceSet scans all objects and versions to find referenced chunks.
// No Store lock is needed because:
//   - s.dataDir is immutable after construction
//   - All file writes use syncedWriteFile (atomic temp+rename), so reads always
//     see complete content
//   - Chunks written during the scan are newer than gcStartTime and will be
//     skipped by deleteOrphanedChunks
func (s *Store) buildChunkReferenceSet(ctx context.Context) map[string]struct{} {
	referencedChunks := make(map[string]struct{})

	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return referencedChunks
	}

	for _, bucketEntry := range bucketEntries {
		select {
		case <-ctx.Done():
			return referencedChunks
		default:
		}
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()

		// Scan current objects (walk to include subdirectories like meta/auth/, meta/dns/)
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		_ = filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			var meta ObjectMeta
			if json.Unmarshal(data, &meta) == nil {
				for _, h := range meta.Chunks {
					referencedChunks[h] = struct{}{}
				}
			}
			return nil
		})

		// Scan versions
		versionsDir := filepath.Join(bucketsDir, bucket, "versions")
		_ = filepath.Walk(versionsDir, func(path string, info os.FileInfo, err error) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
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
// Takes brief read locks per object to minimize contention.
// Returns chunks from pruned versions that may now be unreferenced;
// the caller is responsible for building a reference set and deleting
// unreferenced chunks (avoiding duplicate filesystem scans).
func (s *Store) pruneAllExpiredVersionsSimple(ctx context.Context, stats *GCStats) []string {
	bucketsDir := filepath.Join(s.dataDir, "buckets")
	bucketEntries, err := os.ReadDir(bucketsDir)
	if err != nil {
		return nil
	}

	// Collect all chunks from pruned versions for deferred cleanup
	var allChunksToCheck []string

	for _, bucketEntry := range bucketEntries {
		select {
		case <-ctx.Done():
			return allChunksToCheck
		default:
		}
		if !bucketEntry.IsDir() {
			continue
		}
		bucket := bucketEntry.Name()
		stats.BucketsProcessed++

		// Find all objects in this bucket (walk to include subdirectories)
		metaDir := filepath.Join(bucketsDir, bucket, "meta")
		var objectKeys []string
		_ = filepath.Walk(metaDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			// Get key from path relative to metaDir (remove .json suffix)
			relPath, err := filepath.Rel(metaDir, path)
			if err != nil || len(relPath) <= 5 {
				return nil
			}
			key := relPath[:len(relPath)-5] // Remove .json
			objectKeys = append(objectKeys, key)
			return nil
		})

		for _, key := range objectKeys {
			select {
			case <-ctx.Done():
				return allChunksToCheck
			default:
			}
			stats.ObjectsScanned++

			// Brief RLock: pruneExpiredVersions only reads config fields
			// (versionRetentionPolicy, maxVersionsPerObject, versionRetentionDays)
			// and does filesystem ops on unique version file paths.
			s.mu.RLock()
			pruned, chunksToCheck := s.pruneExpiredVersions(ctx, bucket, key)
			s.mu.RUnlock()

			stats.VersionsPruned += pruned
			allChunksToCheck = append(allChunksToCheck, chunksToCheck...)
		}
	}

	return allChunksToCheck
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

		// Grace period: Only delete chunks older than GCGracePeriod (Phase 6)
		// This prevents deleting chunks that are being replicated or uploaded.
		//
		// See GCGracePeriod constant documentation for tuning guidance.
		if time.Since(info.ModTime()) < GCGracePeriod {
			stats.ChunksSkippedGracePeriod++
			return nil
		}

		// Extract hash from path (chunks are stored as chunks/ab/abcdef...)
		hash := filepath.Base(path)
		if _, referenced := referencedChunks[hash]; !referenced {
			// Chunk is not referenced locally - check if it's safe to delete

			// Phase 6: Registry-aware deletion
			// Query registry to see if other coordinators still own this chunk
			s.mu.RLock()
			registry := s.chunkRegistry
			coordID := s.coordinatorID
			s.mu.RUnlock()

			if registry != nil {
				owners, err := registry.GetOwners(hash)
				if err == nil && len(owners) > 0 {
					// Fast path: sole owner check
					if len(owners) == 1 && owners[0] == coordID {
						// We're the sole owner - safe to delete
					} else {
						// Check if any other coordinator owns this chunk
						hasOtherOwners := false
						for _, owner := range owners {
							if owner != coordID {
								hasOtherOwners = true
								break
							}
						}

						if hasOtherOwners {
							// Other coordinators still own this chunk - don't delete locally
							stats.ChunksSkippedShared++
							return nil
						}

						// Owners list exists but doesn't include us - registry might be out of sync
						// Don't delete to be safe
						stats.ChunksSkippedShared++
						return nil
					}
				} else {
					// Registry returned empty or error - chunk might be orphaned
					// Self-healing: re-register ourselves as owner to prevent premature deletion
					if coordID != "" {
						if err := registry.RegisterChunk(hash, info.Size()); err != nil {
							s.logger.Warn().Err(err).Str("chunk_hash", hash[:8]+"...").
								Msg("Failed to re-register orphaned chunk during GC")
						} else {
							s.logger.Debug().Str("chunk_hash", hash[:8]+"...").
								Msg("Re-registered orphaned chunk in registry (self-healing GC)")
							stats.ChunksSkippedShared++
							return nil // Don't delete - we just claimed ownership
						}
					}
					// If re-registration fails or coordID is empty, proceed with deletion (fail-safe)
				}
			}

			// Safe to delete: not referenced locally and no other owners
			stats.BytesReclaimed += info.Size()

			// CAS.DeleteChunk has its own locking
			if s.cas.DeleteChunk(ctx, hash) == nil {
				stats.ChunksDeleted++

				// Unregister from chunk registry
				if registry != nil {
					_ = registry.UnregisterChunk(hash)
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

	// Check if object uses erasure coding
	if meta.ErasureCoding != nil && meta.ErasureCoding.Enabled {
		return s.getObjectContentWithErasureCoding(ctx, bucket, key, meta)
	}

	// Use distributed chunk reader if replicator is configured (Phase 5)
	// This enables fetching missing chunks from remote coordinators
	// Note: Reading these pointers doesn't require locking as they're only
	// set once during initialization and never modified after that
	if s.replicator != nil && s.chunkRegistry != nil {
		// Distributed reads: can fetch chunks from remote peers
		reader := NewDistributedChunkReader(ctx, DistributedChunkReaderConfig{
			Chunks:     meta.Chunks,
			LocalCAS:   s.cas,
			Registry:   s.chunkRegistry,
			Replicator: s.replicator,
			Logger:     s.logger,
			TotalSize:  meta.Size,
			Prefetch:   PrefetchConfig{WindowSize: 8, Parallelism: 4},
		})
		return reader, meta, nil
	}

	// Local-only reads: all chunks must be local
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

	// Prune expired versions.
	// Chunk GC for pruned versions is deferred to the periodic GC pass.
	s.pruneExpiredVersions(ctx, bucket, key)

	return &newMeta, nil
}

// collectAndDeleteAllVersions removes all version files for an object and returns
// the chunk hashes that were referenced by those versions. The caller is responsible
// for checking global references and deleting unreferenced chunks.
// Must be called with s.mu held.
func (s *Store) collectAndDeleteAllVersions(bucket, key string) []string {
	versionDir := s.versionDir(bucket, key)

	// Collect all chunk hashes from versions
	var chunks []string
	seen := make(map[string]struct{})
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
				if _, ok := seen[h]; !ok {
					seen[h] = struct{}{}
					chunks = append(chunks, h)
				}
			}
		}
	}

	// Remove version directory
	_ = os.RemoveAll(versionDir)

	return chunks
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
