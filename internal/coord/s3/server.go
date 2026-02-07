package s3

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Server provides an S3-compatible HTTP interface.
type Server struct {
	store      *Store
	authorizer Authorizer
	metrics    *S3Metrics
}

// Authorizer is the interface for checking S3 permissions.
type Authorizer interface {
	// AuthorizeRequest authenticates and authorizes an S3 request.
	// Returns the user ID if authorized, or an error if not.
	AuthorizeRequest(r *http.Request, verb, resource, bucket string) (userID string, err error)
}

// NewServer creates a new S3 server.
// If metrics is nil, metrics will not be recorded.
func NewServer(store *Store, authorizer Authorizer, metrics *S3Metrics) *Server {
	return &Server{
		store:      store,
		authorizer: authorizer,
		metrics:    metrics,
	}
}

// Handler returns the HTTP handler for S3 requests.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleRequest)
}

// handleRequest routes S3 requests based on path and method.
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Parse bucket and key from path
	// Path format: /{bucket} or /{bucket}/{key...}
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)
	bucket := ""
	key := ""

	if len(parts) >= 1 && parts[0] != "" {
		bucket = parts[0]
	}
	if len(parts) >= 2 {
		key = parts[1]
	}

	log.Debug().
		Str("method", r.Method).
		Str("path", path).
		Str("bucket", bucket).
		Str("key", key).
		Msg("S3 request")

	// Route based on path and method
	switch {
	case bucket == "":
		// Service-level operations (list buckets)
		s.handleService(w, r)
	case key == "":
		// Bucket-level operations
		s.handleBucket(w, r, bucket)
	default:
		// Object-level operations
		s.handleObject(w, r, bucket, key)
	}
}

// handleService handles service-level operations (GET /).
func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
		return
	}

	// Authorize: list buckets
	userID, err := s.authorizer.AuthorizeRequest(r, "list", "buckets", "")
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	buckets, err := s.store.ListBuckets()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	// Build XML response
	resp := ListAllMyBucketsResult{
		Owner: Owner{
			ID:          userID,
			DisplayName: userID,
		},
	}

	for _, b := range buckets {
		resp.Buckets.Bucket = append(resp.Buckets.Bucket, BucketInfo{
			Name:         b.Name,
			CreationDate: b.CreatedAt.Format(time.RFC3339),
		})
	}

	s.writeXML(w, http.StatusOK, resp)
}

// handleBucket handles bucket-level operations.
func (s *Server) handleBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	switch r.Method {
	case http.MethodGet:
		// Check for list-type query param (ListObjectsV2)
		if r.URL.Query().Get("list-type") == "2" {
			s.listObjectsV2(w, r, bucket)
			return
		}
		// Otherwise list objects (V1)
		s.listObjects(w, r, bucket)
	case http.MethodPut:
		s.createBucket(w, r, bucket)
	case http.MethodDelete:
		s.deleteBucket(w, r, bucket)
	case http.MethodHead:
		s.headBucket(w, r, bucket)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
	}
}

// handleObject handles object-level operations.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	switch r.Method {
	case http.MethodGet:
		s.getObject(w, r, bucket, key)
	case http.MethodPut:
		s.putObject(w, r, bucket, key)
	case http.MethodDelete:
		s.deleteObject(w, r, bucket, key)
	case http.MethodHead:
		s.headObject(w, r, bucket, key)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method not allowed")
	}
}

// createBucket handles PUT /{bucket}.
func (s *Server) createBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	userID, err := s.authorizer.AuthorizeRequest(r, "create", "buckets", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	if err := s.store.CreateBucket(bucket, userID); err != nil {
		if err == ErrBucketExists {
			s.writeError(w, http.StatusConflict, "BucketAlreadyExists", "Bucket already exists")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

// deleteBucket handles DELETE /{bucket}.
func (s *Server) deleteBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.authorizer.AuthorizeRequest(r, "delete", "buckets", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	if err := s.store.DeleteBucket(bucket); err != nil {
		switch err {
		case ErrBucketNotFound:
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		case ErrBucketNotEmpty:
			s.writeError(w, http.StatusConflict, "BucketNotEmpty", "Bucket is not empty")
		default:
			s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// headBucket handles HEAD /{bucket}.
func (s *Server) headBucket(w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.authorizer.AuthorizeRequest(r, "get", "buckets", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	if _, err := s.store.HeadBucket(bucket); err != nil {
		if err == ErrBucketNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

// listObjects handles GET /{bucket} (V1).
func (s *Server) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.authorizer.AuthorizeRequest(r, "list", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	marker := r.URL.Query().Get("marker")
	maxKeys := 1000 // default
	if mk := r.URL.Query().Get("max-keys"); mk != "" {
		if parsed, err := strconv.Atoi(mk); err == nil && parsed > 0 && parsed <= 1000 {
			maxKeys = parsed
		}
	}

	objects, isTruncated, nextMarker, err := s.store.ListObjects(bucket, prefix, marker, maxKeys)
	if err != nil {
		if err == ErrBucketNotFound {
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	resp := ListBucketResult{
		Name:        bucket,
		Prefix:      prefix,
		Marker:      marker,
		MaxKeys:     maxKeys,
		IsTruncated: isTruncated,
		NextMarker:  nextMarker,
	}

	for _, obj := range objects {
		resp.Contents = append(resp.Contents, ObjectInfo{
			Key:          obj.Key,
			LastModified: obj.LastModified.Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
		})
	}

	s.writeXML(w, http.StatusOK, resp)
}

// listObjectsV2 handles GET /{bucket}?list-type=2.
func (s *Server) listObjectsV2(w http.ResponseWriter, r *http.Request, bucket string) {
	_, err := s.authorizer.AuthorizeRequest(r, "list", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	startAfter := r.URL.Query().Get("start-after")
	continuationToken := r.URL.Query().Get("continuation-token")
	maxKeys := 1000 // default
	if mk := r.URL.Query().Get("max-keys"); mk != "" {
		if parsed, err := strconv.Atoi(mk); err == nil && parsed > 0 && parsed <= 1000 {
			maxKeys = parsed
		}
	}

	// continuation-token takes precedence over start-after
	marker := startAfter
	if continuationToken != "" {
		marker = continuationToken
	}

	objects, isTruncated, nextMarker, err := s.store.ListObjects(bucket, prefix, marker, maxKeys)
	if err != nil {
		if err == ErrBucketNotFound {
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}

	resp := ListBucketResultV2{
		Name:                  bucket,
		Prefix:                prefix,
		StartAfter:            startAfter,
		ContinuationToken:     continuationToken,
		MaxKeys:               maxKeys,
		KeyCount:              len(objects),
		IsTruncated:           isTruncated,
		NextContinuationToken: nextMarker,
	}

	for _, obj := range objects {
		resp.Contents = append(resp.Contents, ObjectInfo{
			Key:          obj.Key,
			LastModified: obj.LastModified.Format(time.RFC3339),
			ETag:         obj.ETag,
			Size:         obj.Size,
		})
	}

	s.writeXML(w, http.StatusOK, resp)
}

// getObject handles GET /{bucket}/{key}.
func (s *Server) getObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	_, err := s.authorizer.AuthorizeRequest(r, "get", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	reader, meta, err := s.store.GetObject(bucket, key)
	if err != nil {
		switch err {
		case ErrBucketNotFound:
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		case ErrObjectNotFound:
			s.writeError(w, http.StatusNotFound, "NoSuchKey", "Object not found")
		default:
			s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}
	defer func() { _ = reader.Close() }()

	// Set response headers
	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.Format(http.TimeFormat))

	// Copy user metadata
	for k, v := range meta.Metadata {
		w.Header().Set(k, v)
	}

	n, err := io.Copy(w, reader)
	if err != nil {
		log.Error().Err(err).Msg("Failed to stream object")
	}
	if s.metrics != nil && n > 0 {
		s.metrics.RecordDownload(n)
	}
}

// putObject handles PUT /{bucket}/{key}.
func (s *Server) putObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	_, err := s.authorizer.AuthorizeRequest(r, "put", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Extract user metadata from headers
	metadata := make(map[string]string)
	for k, v := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") && len(v) > 0 {
			metadata[k] = v[0]
		}
	}

	meta, err := s.store.PutObject(bucket, key, r.Body, r.ContentLength, contentType, metadata)
	if err != nil {
		switch err {
		case ErrBucketNotFound:
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		case ErrQuotaExceeded:
			s.writeError(w, http.StatusForbidden, "QuotaExceeded", "Storage quota exceeded")
		default:
			s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}

	w.Header().Set("ETag", meta.ETag)
	w.WriteHeader(http.StatusOK)

	if s.metrics != nil && meta.Size > 0 {
		s.metrics.RecordUpload(meta.Size)
	}
}

// deleteObject handles DELETE /{bucket}/{key}.
func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	_, err := s.authorizer.AuthorizeRequest(r, "delete", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	if err := s.store.DeleteObject(bucket, key); err != nil {
		switch err {
		case ErrBucketNotFound:
			s.writeError(w, http.StatusNotFound, "NoSuchBucket", "Bucket not found")
		case ErrObjectNotFound:
			// S3 returns 204 even for non-existent objects on DELETE
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			s.writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// headObject handles HEAD /{bucket}/{key}.
func (s *Server) headObject(w http.ResponseWriter, r *http.Request, bucket, key string) {
	_, err := s.authorizer.AuthorizeRequest(r, "get", "objects", bucket)
	if err != nil {
		s.handleAuthError(w, err)
		return
	}

	meta, err := s.store.HeadObject(bucket, key)
	if err != nil {
		switch err {
		case ErrBucketNotFound:
			w.WriteHeader(http.StatusNotFound)
		case ErrObjectNotFound:
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
	w.Header().Set("ETag", meta.ETag)
	w.Header().Set("Last-Modified", meta.LastModified.Format(http.TimeFormat))

	for k, v := range meta.Metadata {
		w.Header().Set(k, v)
	}

	w.WriteHeader(http.StatusOK)
}

// handleAuthError writes the appropriate error response for auth errors.
func (s *Server) handleAuthError(w http.ResponseWriter, err error) {
	if err == ErrAccessDenied {
		s.writeError(w, http.StatusForbidden, "AccessDenied", "Access denied")
		return
	}
	s.writeError(w, http.StatusUnauthorized, "InvalidAccessKeyId", "Authentication failed")
}

// writeError writes an S3-style XML error response.
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	resp := ErrorResponse{
		Code:    code,
		Message: message,
	}

	if err := xml.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("Failed to encode error response")
	}
}

// writeXML writes an XML response.
func (s *Server) writeXML(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)

	if err := xml.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("Failed to encode XML response")
	}
}

// XML response types

// ErrorResponse represents an S3 error.
type ErrorResponse struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// ListAllMyBucketsResult is the response for listing buckets.
type ListAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   Owner    `xml:"Owner"`
	Buckets struct {
		Bucket []BucketInfo `xml:"Bucket"`
	} `xml:"Buckets"`
}

// Owner represents a bucket/object owner.
type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

// BucketInfo represents a bucket in a listing.
type BucketInfo struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

// ListBucketResult is the response for listing objects (V1).
type ListBucketResult struct {
	XMLName     xml.Name     `xml:"ListBucketResult"`
	Name        string       `xml:"Name"`
	Prefix      string       `xml:"Prefix"`
	Marker      string       `xml:"Marker,omitempty"`
	MaxKeys     int          `xml:"MaxKeys"`
	IsTruncated bool         `xml:"IsTruncated"`
	NextMarker  string       `xml:"NextMarker,omitempty"`
	Contents    []ObjectInfo `xml:"Contents"`
}

// ListBucketResultV2 is the response for listing objects (V2).
type ListBucketResultV2 struct {
	XMLName               xml.Name     `xml:"ListBucketResult"`
	Name                  string       `xml:"Name"`
	Prefix                string       `xml:"Prefix"`
	StartAfter            string       `xml:"StartAfter,omitempty"`
	ContinuationToken     string       `xml:"ContinuationToken,omitempty"`
	MaxKeys               int          `xml:"MaxKeys"`
	KeyCount              int          `xml:"KeyCount"`
	IsTruncated           bool         `xml:"IsTruncated"`
	NextContinuationToken string       `xml:"NextContinuationToken,omitempty"`
	Contents              []ObjectInfo `xml:"Contents"`
}

// ObjectInfo represents an object in a listing.
type ObjectInfo struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}
