package coord

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

// newTestServerWithShare creates a test server with a file share and uploaded files.
func newTestServerWithShare(t *testing.T, shareName, ownerID string, guestRead bool, files map[string]string) *Server {
	t.Helper()
	srv := newTestServer(t)

	// Create the share
	opts := &s3.FileShareOptions{
		GuestRead:         guestRead,
		GuestReadSet:      true,
		ReplicationFactor: 1,
	}
	_, err := srv.fileShareMgr.Create(context.Background(), shareName, "Test share", ownerID, 0, opts)
	require.NoError(t, err)

	// Upload files
	bucketName := srv.fileShareMgr.BucketName(shareName)
	for key, content := range files {
		var contentType string
		switch {
		case strings.HasSuffix(key, ".html"):
			contentType = "text/html"
		case strings.HasSuffix(key, ".css"):
			contentType = "text/css"
		case strings.HasSuffix(key, ".js"):
			contentType = "application/javascript"
		}
		_, err := srv.s3Store.PutObject(context.Background(), bucketName, key,
			bytes.NewReader([]byte(content)), int64(len(content)), contentType, nil)
		require.NoError(t, err)
	}

	return srv
}

func TestPeerSite_ServeFile(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"hello.txt": "Hello, World!",
		"style.css": "body { color: red; }",
		"app.js":    "console.log('hi')",
		"image.png": "fake-png-data",
		"icon.svg":  "<svg></svg>",
		"page.html": "<html><body>Hi</body></html>",
		"data.json": `{"key":"value"}`,
		"readme":    "no extension file",
	})

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantType   string
		wantBody   string
	}{
		{"text file", "/peers/alice/share/hello.txt", 200, "text/plain", "Hello, World!"},
		{"css file", "/peers/alice/share/style.css", 200, "text/css", "body { color: red; }"},
		{"js file", "/peers/alice/share/app.js", 200, "application/javascript", "console.log('hi')"},
		{"html file", "/peers/alice/share/page.html", 200, "text/html", "<html><body>Hi</body></html>"},
		{"svg file", "/peers/alice/share/icon.svg", 200, "image/svg+xml", "<svg></svg>"},
		{"no extension", "/peers/alice/share/readme", 200, "application/octet-stream", "no extension file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			srv.adminMux.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), tt.wantType)
			assert.Equal(t, tt.wantBody, w.Body.String())
		})
	}
}

func TestPeerSite_IndexHTML(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_blog", "alice-id", true, map[string]string{
		"index.html":      "<html>Welcome</html>",
		"docs/index.html": "<html>Docs</html>",
		"docs/guide.txt":  "A guide",
	})

	tests := []struct {
		name     string
		path     string
		wantBody string
	}{
		{"root index", "/peers/alice/blog/", "<html>Welcome</html>"},
		{"subdir index", "/peers/alice/blog/docs/", "<html>Docs</html>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()
			srv.adminMux.ServeHTTP(w, req)

			assert.Equal(t, 200, w.Code)
			assert.Equal(t, tt.wantBody, w.Body.String())
		})
	}
}

func TestPeerSite_DirectoryListing(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"file1.txt":      "content1",
		"file2.txt":      "content2",
		"docs/readme.md": "docs readme",
		"docs/guide.txt": "docs guide",
		"images/cat.png": "cat",
	})

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/share/", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	body := w.Body.String()
	// Should list directories and files
	assert.Contains(t, body, "docs/")
	assert.Contains(t, body, "images/")
	assert.Contains(t, body, "file1.txt")
	assert.Contains(t, body, "file2.txt")
}

func TestPeerSite_SubdirectoryListing(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"docs/readme.md":     "readme",
		"docs/guide.txt":     "guide",
		"docs/api/spec.yaml": "spec",
	})

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/share/docs/", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "readme.md")
	assert.Contains(t, body, "guide.txt")
	assert.Contains(t, body, "api/")
	// Should have parent link
	assert.Contains(t, body, "../")
}

func TestPeerSite_PathTraversal(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"secret.txt": "secret data",
	})

	paths := []string{
		"/peers/alice/share/../../etc/passwd",
		"/peers/alice/share/../../../etc/shadow",
		"/peers/alice/share/%2e%2e/etc/passwd",
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			w := httptest.NewRecorder()
			srv.adminMux.ServeHTTP(w, req)

			// Should not serve /etc/passwd - either 404 or redirected to safe path
			body := w.Body.String()
			assert.NotContains(t, body, "root:x:")
		})
	}
}

func TestPeerSite_NotFound(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"hello.txt": "hello",
	})

	// Nonexistent share
	req := httptest.NewRequest(http.MethodGet, "/peers/alice/nosuchshare/file.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestPeerSite_HeadRequest(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"hello.txt": "Hello, World!",
	})

	req := httptest.NewRequest(http.MethodHead, "/peers/alice/share/hello.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.NotEmpty(t, w.Header().Get("Content-Type"))
	assert.NotEmpty(t, w.Header().Get("Content-Length"))
	assert.Empty(t, w.Body.String(), "HEAD should not return body")
}

func TestPeerSite_CacheHeaders(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"hello.txt": "Hello",
	})

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/share/hello.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
	assert.NotEmpty(t, w.Header().Get("ETag"))
	assert.NotEmpty(t, w.Header().Get("Last-Modified"))
}

func TestPeerSite_PeerIndex(t *testing.T) {
	srv := newTestServer(t)

	// Create shares for multiple peers
	opts := &s3.FileShareOptions{GuestRead: true, GuestReadSet: true, ReplicationFactor: 1}
	_, err := srv.fileShareMgr.Create(context.Background(), "alice_share", "Alice share", "alice-id", 0, opts)
	require.NoError(t, err)
	_, err = srv.fileShareMgr.Create(context.Background(), "alice_blog", "Alice blog", "alice-id", 0, opts)
	require.NoError(t, err)
	_, err = srv.fileShareMgr.Create(context.Background(), "bob_share", "Bob share", "bob-id", 0, opts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/peers/", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "alice")
	assert.Contains(t, body, "bob")
	assert.Contains(t, body, "share")
	assert.Contains(t, body, "blog")
}

func TestPeerSite_ShareIndex(t *testing.T) {
	srv := newTestServer(t)

	opts := &s3.FileShareOptions{GuestRead: true, GuestReadSet: true, ReplicationFactor: 1}
	_, err := srv.fileShareMgr.Create(context.Background(), "alice_share", "", "alice-id", 0, opts)
	require.NoError(t, err)
	_, err = srv.fileShareMgr.Create(context.Background(), "alice_blog", "", "alice-id", 0, opts)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "share")
	assert.Contains(t, body, "blog")
}

func TestPeerSite_GuestReadAccess(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"public.txt": "public content",
	})

	// Any peer can access GuestRead=true shares
	req := httptest.NewRequest(http.MethodGet, "/peers/alice/share/public.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "public content", w.Body.String())
}

func TestPeerSite_GuestReadFalse_Blocked(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_private", "alice-id", false, map[string]string{
		"secret.txt": "secret content",
	})

	// Anonymous request (no TLS cert, no mesh IP) should be blocked
	req := httptest.NewRequest(http.MethodGet, "/peers/alice/private/secret.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestPeerSite_GuestReadFalse_OwnerAccess(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_private", "alice-id", false, map[string]string{
		"secret.txt": "secret content",
	})

	// Simulate owner access by setting up peer info
	srv.peersMu.Lock()
	srv.peers["alice"] = &peerInfo{
		peer:   &proto.Peer{Name: "alice", MeshIP: "10.42.0.100"},
		peerID: "alice-id",
	}
	srv.peersMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/private/secret.txt", nil)
	req.RemoteAddr = "10.42.0.100:12345"
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "secret content", w.Body.String())
}

func TestPeerSite_GuestReadFalse_RBACAccess(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_private", "alice-id", false, map[string]string{
		"shared.txt": "shared content",
	})

	// Set up Bob with explicit bucket-read binding
	srv.peersMu.Lock()
	srv.peers["bob"] = &peerInfo{
		peer:   &proto.Peer{Name: "bob", MeshIP: "10.42.0.101"},
		peerID: "bob-id",
	}
	srv.peersMu.Unlock()

	bucketName := srv.fileShareMgr.BucketName("alice_private")
	binding := auth.NewRoleBinding("bob-id", auth.RoleBucketRead, bucketName)
	srv.s3Authorizer.Bindings.Add(binding)

	req := httptest.NewRequest(http.MethodGet, "/peers/alice/private/shared.txt", nil)
	req.RemoteAddr = "10.42.0.101:12345"
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "shared content", w.Body.String())
}

func TestPeerSite_MethodNotAllowed(t *testing.T) {
	srv := newTestServerWithShare(t, "alice_share", "alice-id", true, map[string]string{
		"hello.txt": "hello",
	})

	req := httptest.NewRequest(http.MethodPost, "/peers/alice/share/hello.txt", nil)
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestCreatePeerShare_AutoCreation(t *testing.T) {
	srv := newTestServer(t)

	// Simulate share creation
	srv.createPeerShare("peer-123", "testpeer")

	// Give it a moment since it's checking internally
	time.Sleep(100 * time.Millisecond)

	// Verify share was created
	share := srv.fileShareMgr.Get("testpeer_share")
	require.NotNil(t, share, "auto-created share should exist")
	assert.Equal(t, "testpeer_share", share.Name)
	assert.Equal(t, "peer-123", share.Owner)
	assert.True(t, share.GuestRead)
}

func TestCreatePeerShare_SkipsDuplicate(t *testing.T) {
	srv := newTestServer(t)

	// Create a share first
	opts := &s3.FileShareOptions{GuestRead: true, GuestReadSet: true, ReplicationFactor: 1}
	_, err := srv.fileShareMgr.Create(context.Background(), "testpeer_share", "Manual", "peer-123", 0, opts)
	require.NoError(t, err)

	// Try auto-creating again - should not error
	srv.createPeerShare("peer-123", "testpeer")

	// Should still have just one share with original description
	share := srv.fileShareMgr.Get("testpeer_share")
	require.NotNil(t, share)
	assert.Equal(t, "Manual", share.Description)
}

func TestShareCreate_AutoPrefixed(t *testing.T) {
	srv := newTestServer(t)

	// Set up peer for ownership resolution
	srv.peersMu.Lock()
	srv.peers["alice"] = &peerInfo{
		peer:   &proto.Peer{Name: "alice", MeshIP: "10.42.0.100"},
		peerID: "alice-id",
	}
	srv.peersMu.Unlock()

	// Update peer name cache
	nameMap := map[string]string{"alice-id": "alice"}
	srv.peerNameCache.Store(&nameMap)

	// Create share via API
	body := `{"name": "blog", "description": "My blog"}`
	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.42.0.100:12345"
	w := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	// Verify share was created with prefixed name
	share := srv.fileShareMgr.Get("alice_blog")
	require.NotNil(t, share, "share should be created with prefixed name")
	assert.Equal(t, "alice_blog", share.Name)
}
