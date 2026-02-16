package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
)

// S3VersionInfo represents a version for the API response.
type S3VersionInfo struct {
	VersionID    string `json:"version_id"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"last_modified"`
	IsCurrent    bool   `json:"is_current"`
}

// RestoreVersionRequest is the request body for restoring a version.
type RestoreVersionRequest struct {
	VersionID string `json:"version_id"`
}

// MaxS3ObjectSize is the maximum size for S3 object uploads (10MB).
const MaxS3ObjectSize = 10 * 1024 * 1024

// handleS3ListObjects returns objects in a bucket with optional prefix/delimiter.
func (s *Server) handleS3ListObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")

	objects, _, _, err := s.s3Store.ListObjects(r.Context(), bucket, prefix, "", 1000)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to list objects", http.StatusInternalServerError)
		}
		return
	}

	// Get bucket metadata to find owner
	bucketMeta, err := s.s3Store.HeadBucket(r.Context(), bucket)
	var ownerName string
	if err == nil && bucketMeta.Owner != "" {
		// Look up peer name from cached map (falls back to ID if not found)
		ownerName = s.getPeerName(bucketMeta.Owner)
	}

	result := make([]S3ObjectInfo, 0)
	prefixSet := make(map[string]bool)
	prefixSizes := make(map[string]int64) // Track folder sizes for batch calculation

	for _, obj := range objects {
		// Handle delimiter (folder grouping)
		if delimiter != "" {
			keyAfterPrefix := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(keyAfterPrefix, delimiter); idx >= 0 {
				// This is a "folder" - add common prefix
				commonPrefix := prefix + keyAfterPrefix[:idx+1]
				if !prefixSet[commonPrefix] {
					prefixSet[commonPrefix] = true
					// Size will be calculated below
					prefixSizes[commonPrefix] = 0
				}
				continue
			}
		}

		info := S3ObjectInfo{
			Key:          obj.Key,
			Size:         obj.Size,
			LastModified: obj.LastModified.Format(time.RFC3339),
			Owner:        ownerName,
			ContentType:  obj.ContentType,
		}
		if obj.Expires != nil {
			info.Expires = obj.Expires.Format(time.RFC3339)
		}
		result = append(result, info)
	}

	// Merge cached peer listings from the system store listing index.
	// Peer data is maintained by the background indexer — no HTTP calls needed.
	if r.Header.Get("X-TunnelMesh-Forwarded") == "" {
		peerObjs := s.getPeerObjectListing(bucket)
		filtered := filterByPrefixDelimiter(peerObjs, prefix, delimiter)
		result = mergeObjectListings(result, filtered)
	}

	// Calculate sizes for all folders (prefixes) found
	for commonPrefix := range prefixSizes {
		size, err := s.s3Store.CalculatePrefixSize(r.Context(), bucket, commonPrefix)
		if err != nil {
			size = 0 // Gracefully handle errors - show 0 size rather than failing
		}
		prefixSizes[commonPrefix] = size

		// Add folder to result with calculated size
		result = append(result, S3ObjectInfo{
			Key:      commonPrefix,
			Size:     size,
			IsPrefix: true,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleS3Object handles GET/PUT/DELETE for a specific object.
func (s *Server) handleS3Object(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Determine operation name for metrics
	var operation string
	switch r.Method {
	case http.MethodGet:
		operation = "getObject"
	case http.MethodPut:
		operation = "putObject"
	case http.MethodDelete:
		operation = "deleteObject"
	case http.MethodHead:
		operation = "headObject"
	default:
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.withS3AdminMetrics(w, operation, func(w http.ResponseWriter) {
		// Forward writes and deletes proactively to primary coordinator.
		// Reads try local first, then forward on miss (see handleS3GetObject/handleS3HeadObject).
		if (r.Method == http.MethodPut || r.Method == http.MethodDelete) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
				// Buffer body so we can retry locally if forward fails.
				var bodyBuf []byte
				if r.Body != nil {
					var err error
					bodyBuf, err = io.ReadAll(r.Body)
					_ = r.Body.Close()
					if err != nil {
						s.jsonError(w, "failed to read request body", http.StatusInternalServerError)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(bodyBuf))
				}

				rec := &discardResponseWriter{header: make(http.Header)}
				s.forwardS3Request(rec, r, target, bucket)
				if rec.status == http.StatusOK || rec.status == http.StatusNoContent {
					w.WriteHeader(rec.status)
					s.updatePeerListingsAfterForward(bucket, key, target, r)
					return
				}
				// Forward failed — fall through to handle locally.
				log.Warn().Str("target", target).Int("status", rec.status).
					Str("bucket", bucket).Str("key", key).
					Msg("S3 forward failed, handling locally")
				if bodyBuf != nil {
					r.Body = io.NopCloser(bytes.NewReader(bodyBuf))
				}
			}
		}

		switch r.Method {
		case http.MethodGet:
			// Check for versionId query param
			versionID := r.URL.Query().Get("versionId")
			if versionID != "" {
				s.handleS3GetObjectVersion(r.Context(), w, bucket, key, versionID)
			} else {
				s.handleS3GetObject(w, r, bucket, key)
			}
		case http.MethodPut:
			s.handleS3PutObject(w, r, bucket, key)
		case http.MethodDelete:
			s.handleS3DeleteObject(w, r, bucket, key)
		case http.MethodHead:
			s.handleS3HeadObject(w, r, bucket, key)
		}
	})
}

// handleS3GetObject returns the object content.
// Tries local storage first; forwards to primary coordinator on miss.
func (s *Server) handleS3GetObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	reader, meta, err := s.s3Store.GetObject(r.Context(), bucket, key)
	if err != nil {
		// Try forwarding: first to the source coordinator (from listing index),
		// then fall back to the hash-based primary coordinator.
		if (errors.Is(err, s3.ErrObjectNotFound) || errors.Is(err, s3.ErrBucketNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.findObjectSourceIP(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
			if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to get object", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))

	_, _ = io.Copy(w, reader)
}

// handleS3GetObjectVersion returns a specific version of an object.
func (s *Server) handleS3GetObjectVersion(ctx context.Context, w http.ResponseWriter, bucket, key, versionID string) {
	reader, meta, err := s.s3Store.GetObjectVersion(ctx, bucket, key, versionID)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "version not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to get object version", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("X-Version-Id", meta.VersionID)

	_, _ = io.Copy(w, reader)
}

// handleS3PutObject creates or updates an object.
func (s *Server) handleS3PutObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Check bucket write permission (could be extended to full RBAC)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Limit request body size to prevent DoS
	r.Body = http.MaxBytesReader(w, r.Body, MaxS3ObjectSize)

	// Attempt to recover missing bucket for share (no-op if bucket exists).
	// Recovery failure is logged but not returned: the subsequent PutObject will fail
	// with ErrBucketNotFound, giving the caller the correct error for their operation.
	if s.fileShareMgr != nil {
		if err := s.fileShareMgr.EnsureBucketForShare(r.Context(), bucket); err != nil {
			log.Warn().Err(err).Str("bucket", bucket).Msg("bucket recovery attempt failed")
		}
	}

	// If this is a forwarded request and the bucket still doesn't exist, create it
	// using the owner from the forwarding coordinator. This handles the race condition
	// where share metadata hasn't been replicated yet.
	// Only trust the bucket owner header on forwarded requests (X-TunnelMesh-Forwarded
	// is set by our own forwardS3Request). External clients cannot spoof this because
	// the forwarded header is checked at the dispatch level before reaching here.
	if r.Header.Get("X-TunnelMesh-Forwarded") != "" {
		if bucketOwner := r.Header.Get("X-TunnelMesh-Bucket-Owner"); bucketOwner != "" {
			if _, err := s.s3Store.HeadBucket(r.Context(), bucket); err != nil {
				if createErr := s.s3Store.CreateBucket(r.Context(), bucket, bucketOwner, 2, nil); createErr != nil {
					if !errors.Is(createErr, s3.ErrBucketExists) {
						log.Warn().Err(createErr).Str("bucket", bucket).Msg("auto-create bucket from forwarded header failed")
					}
				} else {
					log.Info().Str("bucket", bucket).Str("owner", bucketOwner).Msg("auto-created bucket from forwarded write")
				}
			}
		}
	}

	// Stream body directly to PutObject — avoids buffering the entire object in memory.
	// PutObject uses StreamingChunker internally (~64KB peak memory per upload).
	// r.ContentLength is used for early quota checks; -1 if chunked (quota still enforced post-write).
	meta, err := s.s3Store.PutObject(r.Context(), bucket, key, r.Body, r.ContentLength, contentType, nil)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxBytesErr):
			s.jsonError(w, "object too large (max 10MB)", http.StatusRequestEntityTooLarge)
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		default:
			s.jsonError(w, "failed to store object: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Update listing index incrementally
	putInfo := S3ObjectInfo{
		Key:          meta.Key,
		Size:         meta.Size,
		LastModified: meta.LastModified.Format(time.RFC3339),
		ContentType:  meta.ContentType,
	}
	if meta.Expires != nil {
		putInfo.Expires = meta.Expires.Format(time.RFC3339)
	}
	// Include owner from bucket metadata so listing index has it immediately
	if bucketMeta, err := s.s3Store.HeadBucket(r.Context(), bucket); err == nil && bucketMeta.Owner != "" {
		putInfo.Owner = s.getPeerName(bucketMeta.Owner)
	}
	s.updateListingIndex(bucket, key, &putInfo, "put")

	// Replicate to other coordinators asynchronously using chunk-level replication.
	// Chunks are already stored by PutObject above — ReplicateObject reads them from CAS
	// and only sends chunks the remote peer doesn't already have.
	//
	// Uses WithoutCancel so replication completes even after the HTTP response is sent,
	// while preserving trace context from the original request.
	// 60s timeout accounts for chunk transfer + metadata send + cleanup across all peers.
	if s.replicator != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
			defer cancel()

			peers := s.replicator.GetPeers()
			if len(peers) == 0 {
				return
			}

			// Replicate to all peers in parallel with bounded concurrency.
			// Semaphore acquisition and result collection both respect context
			// cancellation to prevent goroutine leaks on timeout.
			type result struct {
				peerID string
				err    error
			}
			results := make(chan result, len(peers))
			sem := make(chan struct{}, 3)

			for _, peerID := range peers {
				go func(pid string) {
					// Acquire semaphore, bail out if context expires while waiting
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						results <- result{pid, ctx.Err()}
						return
					}
					results <- result{pid, s.replicator.ReplicateObject(ctx, bucket, key, pid)}
				}(peerID)
			}

			allSucceeded := true
			for range peers {
				select {
				case res := <-results:
					if res.err != nil {
						log.Error().Err(res.err).
							Str("peer", res.peerID).
							Str("bucket", bucket).
							Str("key", key).
							Msg("Failed to replicate S3 PUT operation")
						allSucceeded = false
					}
				case <-ctx.Done():
					log.Warn().
						Str("bucket", bucket).
						Str("key", key).
						Msg("Replication timed out, skipping cleanup")
					allSucceeded = false
				}
			}

			// Only cleanup non-assigned chunks if ALL peers received their chunks.
			// If any peer failed, keep all chunks locally so a retry can succeed.
			if allSucceeded {
				if err := s.replicator.CleanupNonAssignedChunks(ctx, bucket, key); err != nil {
					log.Error().Err(err).
						Str("bucket", bucket).
						Str("key", key).
						Msg("Failed to cleanup non-assigned chunks")
				}
			}
		}()
	}

	w.Header().Set("ETag", meta.ETag)
	w.WriteHeader(http.StatusOK)
}

// handleS3DeleteObject deletes an object.
func (s *Server) handleS3DeleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	// Check bucket write permission (could be extended to full RBAC)
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	err := s.s3Store.DeleteObject(r.Context(), bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to delete object", http.StatusInternalServerError)
		}
		return
	}

	// Update listing index: move from objects to recycled
	s.updateListingIndex(bucket, key, nil, "delete")

	// Replicate delete to other coordinators asynchronously.
	// Uses WithoutCancel so replication completes after the HTTP response is sent,
	// while preserving trace context from the original request.
	if s.replicator != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			if err := s.replicator.ReplicateDelete(ctx, bucket, key); err != nil {
				log.Error().Err(err).
					Str("bucket", bucket).
					Str("key", key).
					Msg("Failed to replicate S3 DELETE operation")
			}
		}()
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleS3HeadObject returns object metadata.
// Tries local storage first; forwards to primary coordinator on miss.
func (s *Server) handleS3HeadObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	meta, err := s.s3Store.HeadObject(r.Context(), bucket, key)
	if err != nil {
		// Try forwarding: first to the source coordinator (from listing index),
		// then fall back to the hash-based primary coordinator.
		if (errors.Is(err, s3.ErrBucketNotFound) || errors.Is(err, s3.ErrObjectNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.findObjectSourceIP(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
			if target := s.objectPrimaryCoordinator(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
		switch {
		case errors.Is(err, s3.ErrBucketNotFound), errors.Is(err, s3.ErrObjectNotFound):
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
}

// handleS3ListVersions returns all versions of an object.
func (s *Server) handleS3ListVersions(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	versions, err := s.s3Store.ListVersions(r.Context(), bucket, key)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to list versions", http.StatusInternalServerError)
		}
		return
	}

	// Convert to API response format
	result := make([]S3VersionInfo, 0, len(versions))
	for _, v := range versions {
		result = append(result, S3VersionInfo{
			VersionID:    v.VersionID,
			Size:         v.Size,
			ETag:         v.ETag,
			LastModified: v.LastModified.Format(time.RFC3339),
			IsCurrent:    v.IsCurrent,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleS3RestoreVersion restores a previous version of an object.
func (s *Server) handleS3RestoreVersion(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check bucket write permission
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	var req RestoreVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.VersionID == "" {
		s.jsonError(w, "version_id is required", http.StatusBadRequest)
		return
	}

	meta, err := s.s3Store.RestoreVersion(r.Context(), bucket, key, req.VersionID)
	if err != nil {
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "version not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to restore version: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Update listing index — triggers reconcile since we build info from RestoreVersion meta
	restoredInfo := S3ObjectInfo{
		Key:          meta.Key,
		Size:         meta.Size,
		LastModified: meta.LastModified.Format(time.RFC3339),
		ContentType:  meta.ContentType,
	}
	s.updateListingIndex(bucket, key, &restoredInfo, "put")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "restored",
		"version_id": meta.VersionID,
	})
}

// handleS3UndeleteObject restores a recycled (deleted) object from the recycle bin.
func (s *Server) handleS3UndeleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodPost {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check bucket write permission
	if bucket == auth.SystemBucket {
		s.jsonError(w, "bucket is read-only", http.StatusForbidden)
		return
	}

	// Restore from recycle bin
	if err := s.s3Store.RestoreRecycledObject(r.Context(), bucket, key); err != nil {
		// Try forwarding to the source coordinator if not found locally
		if (errors.Is(err, s3.ErrObjectNotFound) || errors.Is(err, s3.ErrBucketNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.findRecycledObjectSourceIP(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "object not found in recycle bin", http.StatusNotFound)
		case errors.Is(err, s3.ErrAccessDenied):
			s.jsonError(w, "access denied", http.StatusForbidden)
		default:
			s.jsonError(w, "failed to restore object: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Update listing index: move from recycled back to objects
	s.updateListingIndex(bucket, key, nil, "undelete")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "restored",
	})
}

// handleS3ListRecycledObjects returns recycled entries for a bucket.
func (s *Server) handleS3ListRecycledObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	prefix := r.URL.Query().Get("prefix")

	entries, err := s.s3Store.ListRecycledObjects(r.Context(), bucket)
	if err != nil {
		if errors.Is(err, s3.ErrBucketNotFound) {
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		} else {
			s.jsonError(w, "failed to list recycled objects: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Convert to S3ObjectInfo for consistent API response format, filtering by prefix
	result := make([]S3ObjectInfo, 0, len(entries))
	for _, entry := range entries {
		if prefix != "" && !strings.HasPrefix(entry.OriginalKey, prefix) {
			continue
		}
		info := S3ObjectInfo{
			Key:          entry.OriginalKey,
			Size:         entry.Meta.Size,
			LastModified: entry.Meta.LastModified.Format(time.RFC3339),
			ContentType:  entry.Meta.ContentType,
			DeletedAt:    entry.DeletedAt.Format(time.RFC3339),
		}
		result = append(result, info)
	}

	// Merge cached peer recycled listings from the system store listing index.
	if r.Header.Get("X-TunnelMesh-Forwarded") == "" {
		peerRecycled := s.getPeerRecycledListing(bucket)
		if prefix != "" {
			// Filter peer recycled entries by prefix
			var filtered []S3ObjectInfo
			for _, obj := range peerRecycled {
				if strings.HasPrefix(obj.Key, prefix) {
					filtered = append(filtered, obj)
				}
			}
			peerRecycled = filtered
		}
		result = mergeObjectListings(result, peerRecycled)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleS3GetRecycledObject returns the content of a recycled object.
func (s *Server) handleS3GetRecycledObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	if r.Method != http.MethodGet {
		s.jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reader, meta, err := s.s3Store.GetRecycledObject(r.Context(), bucket, key)
	if err != nil {
		// Try forwarding to the source coordinator if not found locally
		if (errors.Is(err, s3.ErrObjectNotFound) || errors.Is(err, s3.ErrBucketNotFound)) &&
			r.Header.Get("X-TunnelMesh-Forwarded") == "" {
			if target := s.findRecycledObjectSourceIP(bucket, key); target != "" {
				s.forwardS3Request(w, r, target, bucket)
				return
			}
		}
		switch {
		case errors.Is(err, s3.ErrBucketNotFound):
			s.jsonError(w, "bucket not found", http.StatusNotFound)
		case errors.Is(err, s3.ErrObjectNotFound):
			s.jsonError(w, "recycled object not found", http.StatusNotFound)
		default:
			s.jsonError(w, "failed to get recycled object", http.StatusInternalServerError)
		}
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.UTC().Format(http.TimeFormat))

	_, _ = io.Copy(w, reader)
}
