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
	reader, meta, err := a.store.GetObject(ctx, bucket, key)
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

	_, err := a.store.PutObject(ctx, bucket, key, reader, size, contentType, metadata)
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}

	return nil
}

// List lists all objects in a bucket with pagination support.
func (a *S3StoreAdapter) List(ctx context.Context, bucket string) ([]string, error) {
	var allKeys []string
	marker := ""
	const maxKeys = 1000

	// Paginate through all objects
	for {
		objects, _, nextMarker, err := a.store.ListObjects(ctx, bucket, "", marker, maxKeys)
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}

		// Collect keys from this page
		for _, obj := range objects {
			allKeys = append(allKeys, obj.Key)
		}

		// Check if there are more pages
		if nextMarker == "" {
			break
		}
		marker = nextMarker

		// Check context cancellation between pages
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	return allKeys, nil
}

// Delete removes an object from S3.
func (a *S3StoreAdapter) Delete(ctx context.Context, bucket, key string) error {
	err := a.store.DeleteObject(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}

	return nil
}

// ListBuckets lists all buckets.
func (a *S3StoreAdapter) ListBuckets(ctx context.Context) ([]string, error) {
	buckets, err := a.store.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}

	names := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		names = append(names, bucket.Name)
	}

	return names, nil
}
