package replication

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

// S3StoreAdapter adapts the internal s3.Store to the replication.S3Store interface.
type S3StoreAdapter struct {
	store *s3.Store
}

// NewS3StoreAdapter creates a new S3 store adapter for replication.
func NewS3StoreAdapter(store *s3.Store) *S3StoreAdapter {
	return &S3StoreAdapter{store: store}
}

// Get retrieves an object from S3.
func (a *S3StoreAdapter) Get(ctx context.Context, bucket, key string) ([]byte, map[string]string, error) {
	reader, meta, err := a.store.GetObject(bucket, key)
	if err != nil {
		return nil, nil, fmt.Errorf("get object: %w", err)
	}
	defer func() { _ = reader.Close() }()

	// Read all data
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("read object data: %w", err)
	}

	// Extract metadata
	metadata := make(map[string]string)
	if meta.Metadata != nil {
		for k, v := range meta.Metadata {
			metadata[k] = v
		}
	}

	return data, metadata, nil
}

// Put stores an object in S3.
func (a *S3StoreAdapter) Put(ctx context.Context, bucket, key string, data []byte, contentType string, metadata map[string]string) error {
	reader := bytes.NewReader(data)
	size := int64(len(data))

	_, err := a.store.PutObject(bucket, key, reader, size, contentType, metadata)
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}

	return nil
}

// List lists all objects in a bucket.
func (a *S3StoreAdapter) List(ctx context.Context, bucket string) ([]string, error) {
	// ListObjects uses pagination, we'll get up to 1000 objects
	objects, _, _, err := a.store.ListObjects(bucket, "", "", 1000)
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}

	keys := make([]string, 0, len(objects))
	for _, obj := range objects {
		keys = append(keys, obj.Key)
	}

	return keys, nil
}

// ListBuckets lists all buckets.
func (a *S3StoreAdapter) ListBuckets(ctx context.Context) ([]string, error) {
	buckets, err := a.store.ListBuckets()
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}

	names := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		names = append(names, bucket.Name)
	}

	return names, nil
}
