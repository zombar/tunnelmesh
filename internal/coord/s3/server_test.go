package s3

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAuthorizer is a test authorizer that allows all requests.
type mockAuthorizer struct {
	userID    string
	allowAll  bool
	denyAll   bool
	allowVerb map[string]bool
}

func (m *mockAuthorizer) AuthorizeRequest(r *http.Request, verb, resource, bucket string) (string, error) {
	if m.denyAll {
		return "", ErrAccessDenied
	}
	if m.allowAll {
		return m.userID, nil
	}
	if m.allowVerb != nil && m.allowVerb[verb] {
		return m.userID, nil
	}
	return "", ErrAccessDenied
}

func newTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store := newTestStore(t)
	auth := &mockAuthorizer{userID: "alice", allowAll: true}
	server := NewServer(store, auth, nil) // nil metrics for tests
	return server, store
}

func TestListBuckets(t *testing.T) {
	server, store := newTestServer(t)

	// Create some buckets
	require.NoError(t, store.CreateBucket("bucket-a", "alice"))
	require.NoError(t, store.CreateBucket("bucket-b", "bob"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/xml")

	var resp ListAllMyBucketsResult
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))

	assert.Len(t, resp.Buckets.Bucket, 2)
}

func TestListBucketsEmpty(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ListAllMyBucketsResult
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Buckets.Bucket)
}

func TestCreateBucket(t *testing.T) {
	server, store := newTestServer(t)

	req := httptest.NewRequest(http.MethodPut, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify bucket exists
	meta, err := store.HeadBucket("my-bucket")
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", meta.Name)
}

func TestCreateBucketAlreadyExists(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodPut, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "BucketAlreadyExists", resp.Code)
}

func TestDeleteBucket(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodDelete, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	// Verify bucket is gone
	_, err := store.HeadBucket("my-bucket")
	assert.ErrorIs(t, err, ErrBucketNotFound)
}

func TestDeleteBucketNotFound(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/nonexistent", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "NoSuchBucket", resp.Code)
}

func TestDeleteBucketNotEmpty(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))
	_, err := store.PutObject("my-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "BucketNotEmpty", resp.Code)
}

func TestHeadBucket(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodHead, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHeadBucketNotFound(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodHead, "/nonexistent", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPutObject(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	content := []byte("hello world")
	req := httptest.NewRequest(http.MethodPut, "/my-bucket/greeting.txt", bytes.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("ETag"))

	// Verify object exists
	meta, err := store.HeadObject("my-bucket", "greeting.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), meta.Size)
}

func TestPutObjectBucketNotFound(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPut, "/nonexistent/file.txt", bytes.NewReader([]byte("data")))
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "NoSuchBucket", resp.Code)
}

func TestPutObjectWithMetadata(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodPut, "/my-bucket/doc.txt", bytes.NewReader([]byte("data")))
	req.Header.Set("X-Amz-Meta-Author", "alice")
	req.Header.Set("X-Amz-Meta-Version", "1.0")
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	meta, err := store.HeadObject("my-bucket", "doc.txt")
	require.NoError(t, err)
	assert.Equal(t, "alice", meta.Metadata["X-Amz-Meta-Author"])
}

func TestGetObject(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))
	content := []byte("hello world")
	_, err := store.PutObject("my-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/my-bucket/greeting.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain", w.Header().Get("Content-Type"))
	assert.NotEmpty(t, w.Header().Get("ETag"))
	assert.NotEmpty(t, w.Header().Get("Last-Modified"))
	assert.Equal(t, content, w.Body.Bytes())
}

func TestGetObjectNotFound(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodGet, "/my-bucket/nonexistent.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "NoSuchKey", resp.Code)
}

func TestDeleteObject(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))
	_, err := store.PutObject("my-bucket", "file.txt", bytes.NewReader([]byte("data")), 4, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/my-bucket/file.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	// Verify object is gone
	_, err = store.HeadObject("my-bucket", "file.txt")
	assert.ErrorIs(t, err, ErrObjectNotFound)
}

func TestDeleteObjectNotFound(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	// S3 returns 204 even for non-existent objects
	req := httptest.NewRequest(http.MethodDelete, "/my-bucket/nonexistent.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHeadObject(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))
	content := []byte("hello world")
	_, err := store.PutObject("my-bucket", "greeting.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodHead, "/my-bucket/greeting.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain", w.Header().Get("Content-Type"))
	assert.Equal(t, "11", w.Header().Get("Content-Length"))
	assert.NotEmpty(t, w.Header().Get("ETag"))
	assert.Empty(t, w.Body.Bytes()) // HEAD has no body
}

func TestHeadObjectNotFound(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	req := httptest.NewRequest(http.MethodHead, "/my-bucket/nonexistent.txt", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestListObjects(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	// Add some objects
	for _, key := range []string{"a.txt", "b.txt", "c.txt"} {
		_, err := store.PutObject("my-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/my-bucket", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ListBucketResult
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "my-bucket", resp.Name)
	assert.Len(t, resp.Contents, 3)
}

func TestListObjectsWithPrefix(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	// Add objects with different prefixes
	for _, key := range []string{"docs/a.txt", "docs/b.txt", "images/c.png"} {
		_, err := store.PutObject("my-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/my-bucket?prefix=docs/", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ListBucketResult
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "docs/", resp.Prefix)
	assert.Len(t, resp.Contents, 2)
}

func TestListObjectsV2(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	// Add some objects
	for _, key := range []string{"a.txt", "b.txt"} {
		_, err := store.PutObject("my-bucket", key, bytes.NewReader([]byte("data")), 4, "text/plain", nil)
		require.NoError(t, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/my-bucket?list-type=2", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ListBucketResultV2
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "my-bucket", resp.Name)
	assert.Equal(t, 2, resp.KeyCount)
	assert.Len(t, resp.Contents, 2)
}

func TestListObjectsBucketNotFound(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "NoSuchBucket", resp.Code)
}

func TestAccessDenied(t *testing.T) {
	store := newTestStore(t)
	auth := &mockAuthorizer{denyAll: true}
	server := NewServer(store, auth, nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)

	var resp ErrorResponse
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "AccessDenied", resp.Code)
}

func TestNestedObjectKey(t *testing.T) {
	server, store := newTestServer(t)
	require.NoError(t, store.CreateBucket("my-bucket", "alice"))

	content := []byte("nested content")
	req := httptest.NewRequest(http.MethodPut, "/my-bucket/path/to/deep/file.txt", bytes.NewReader(content))
	w := httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Retrieve it
	req = httptest.NewRequest(http.MethodGet, "/my-bucket/path/to/deep/file.txt", nil)
	w = httptest.NewRecorder()

	server.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	data, _ := io.ReadAll(w.Body)
	assert.Equal(t, content, data)
}
