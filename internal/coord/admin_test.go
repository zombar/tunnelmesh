package coord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/routing"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestAdminOverview_IncludesLocation(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Locations = true // Enable location tracking for this test

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })
	require.NotNil(t, srv.adminMux, "adminMux should be created when JoinMesh is configured")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register a peer with location
	client := NewClient(ts.URL, "test-token")
	location := &proto.GeoLocation{
		Latitude:  51.5074,
		Longitude: -0.1278,
		Accuracy:  100,
		Source:    "manual",
		City:      "London",
		Country:   "United Kingdom",
	}
	_, err = client.Register("geonode", "SHA256:abc123", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", location, "", false, nil, false)
	require.NoError(t, err)

	// Fetch admin overview from adminMux (internal mesh only)
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var overview AdminOverview
	err = json.NewDecoder(rec.Body).Decode(&overview)
	require.NoError(t, err)

	// Verify location is included in peer info
	require.Len(t, overview.Peers, 1)
	require.NotNil(t, overview.Peers[0].Location)
	assert.Equal(t, 51.5074, overview.Peers[0].Location.Latitude)
	assert.Equal(t, -0.1278, overview.Peers[0].Location.Longitude)
	assert.Equal(t, "manual", overview.Peers[0].Location.Source)
	assert.Equal(t, "London", overview.Peers[0].Location.City)
	assert.Equal(t, "United Kingdom", overview.Peers[0].Location.Country)
}

func TestAdminOverview_ExitPeerInfo(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, err := NewServer(ctx, cfg)
	require.NoError(t, err)
	require.NotNil(t, srv.adminMux, "adminMux should be created for coordinators")

	ts := httptest.NewServer(srv)
	defer ts.Close()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// Register an exit node
	client := NewClient(ts.URL, "test-token")
	_, err = client.Register("exit-node", "SHA256:exitkey", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", true, nil, false)
	require.NoError(t, err)

	// Register a client that uses the exit node
	_, err = client.Register("client1", "SHA256:client1key", []string{"5.6.7.8"}, nil, 2223, 0, false, "v1.0.0", nil, "exit-node", false, nil, false)
	require.NoError(t, err)

	// Fetch admin overview from adminMux (internal mesh only)
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var overview AdminOverview
	err = json.NewDecoder(rec.Body).Decode(&overview)
	require.NoError(t, err)

	// Find peers by name
	var exitNodeInfo, clientInfo *AdminPeerInfo
	for i := range overview.Peers {
		if overview.Peers[i].Name == "exit-node" {
			exitNodeInfo = &overview.Peers[i]
		}
		if overview.Peers[i].Name == "client1" {
			clientInfo = &overview.Peers[i]
		}
	}

	require.NotNil(t, exitNodeInfo, "exit-node should be in peers")
	require.NotNil(t, clientInfo, "client1 should be in peers")

	// Verify exit node info
	assert.True(t, exitNodeInfo.AllowsExitTraffic, "exit-node should allow exit traffic")
	assert.Equal(t, "", exitNodeInfo.ExitPeer, "exit-node should not have an exit node")
	assert.Contains(t, exitNodeInfo.ExitClients, "client1", "exit-node should have client1 as exit client")

	// Verify client info
	assert.False(t, clientInfo.AllowsExitTraffic, "client1 should not allow exit traffic")
	assert.Equal(t, "exit-node", clientInfo.ExitPeer, "client1 should use exit-node")
	assert.Empty(t, clientInfo.ExitClients, "client1 should not have exit clients")
}

func TestAdminOverview_ConnectionTypes(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })
	require.NotNil(t, srv.adminMux, "adminMux should be created when JoinMesh is configured")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register a peer
	client := NewClient(ts.URL, "test-token")
	_, err = client.Register("peer1", "SHA256:peer1key", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false)
	require.NoError(t, err)

	// Directly set stats on the server (simulating heartbeat)
	// This is necessary because heartbeats are sent via WebSocket in production
	srv.peersMu.Lock()
	if peerInfo, ok := srv.peers["peer1"]; ok {
		peerInfo.stats = &proto.PeerStats{
			PacketsSent: 100,
			Connections: map[string]string{
				"peer2": "ssh",
				"peer3": "udp",
			},
		}
	}
	srv.peersMu.Unlock()

	// Fetch admin overview from adminMux (internal mesh only)
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var overview AdminOverview
	err = json.NewDecoder(rec.Body).Decode(&overview)
	require.NoError(t, err)

	// Find peer1
	var peer1Info *AdminPeerInfo
	for i := range overview.Peers {
		if overview.Peers[i].Name == "peer1" {
			peer1Info = &overview.Peers[i]
			break
		}
	}

	require.NotNil(t, peer1Info, "peer1 should be in peers")
	require.NotNil(t, peer1Info.Connections, "peer1 should have connections")
	assert.Equal(t, "ssh", peer1Info.Connections["peer2"])
	assert.Equal(t, "udp", peer1Info.Connections["peer3"])
}

func TestSetupMonitoringProxies_AdminMux(t *testing.T) {
	// Create a server with coordinator enabled to have adminMux
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })
	require.NotNil(t, srv.adminMux, "adminMux should be created for coordinators")

	// Start mock Prometheus and Grafana servers
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("prometheus"))
	}))
	defer promServer.Close()

	grafanaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("grafana"))
	}))
	defer grafanaServer.Close()

	// Setup monitoring proxies
	srv.SetupMonitoringProxies(MonitoringProxyConfig{
		PrometheusURL: promServer.URL,
		GrafanaURL:    grafanaServer.URL,
	})

	// Test adminMux has the prometheus route (admin is mesh-internal only)
	req := httptest.NewRequest(http.MethodGet, "/prometheus/", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "prometheus", rec.Body.String())

	// Test adminMux has the grafana route
	req = httptest.NewRequest(http.MethodGet, "/grafana/", nil)
	rec = httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "grafana", rec.Body.String())

	// Verify main mux does NOT have the monitoring routes (admin is mesh-internal only)
	req = httptest.NewRequest(http.MethodGet, "/prometheus/", nil)
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/grafana/", nil)
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// --- S3 Explorer API Tests ---

func newTestServerWithS3AndBucket(t *testing.T) *Server {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.DataDir = t.TempDir()

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	// Create a test bucket
	err = srv.s3Store.CreateBucket(context.Background(), "test-bucket", "admin")
	require.NoError(t, err)

	return srv
}

func TestS3Proxy_ListBuckets(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp S3BucketsResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	// Server creates system bucket automatically, plus our test bucket
	require.Len(t, resp.Buckets, 2)

	// Verify quota info is present
	assert.Greater(t, resp.Quota.MaxBytes, int64(0))

	// Find test bucket and verify it's writable
	var testBucket *S3BucketInfo
	for i := range resp.Buckets {
		if resp.Buckets[i].Name == "test-bucket" {
			testBucket = &resp.Buckets[i]
			break
		}
	}
	require.NotNil(t, testBucket)
	assert.True(t, testBucket.Writable)
}

func TestS3Proxy_ListBuckets_SystemBucketReadOnly(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// System bucket is created automatically by the server
	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp S3BucketsResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	// Find system bucket and verify it's read-only
	var systemBucket *S3BucketInfo
	for i := range resp.Buckets {
		if resp.Buckets[i].Name == auth.SystemBucket {
			systemBucket = &resp.Buckets[i]
			break
		}
	}
	require.NotNil(t, systemBucket)
	assert.False(t, systemBucket.Writable)
}

func TestS3Proxy_ListObjects(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Add some objects
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "file1.txt", bytes.NewReader([]byte("hello")), 5, "text/plain", nil)
	require.NoError(t, err)
	_, err = srv.s3Store.PutObject(context.Background(), "test-bucket", "folder/file2.txt", bytes.NewReader([]byte("world")), 5, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets/test-bucket/objects", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var objects []S3ObjectInfo
	err = json.NewDecoder(rec.Body).Decode(&objects)
	require.NoError(t, err)
	assert.Len(t, objects, 2)
}

func TestS3Proxy_ListObjects_WithDelimiter(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Add objects in a folder structure
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "file1.txt", bytes.NewReader([]byte("hello")), 5, "text/plain", nil)
	require.NoError(t, err)
	_, err = srv.s3Store.PutObject(context.Background(), "test-bucket", "folder/file2.txt", bytes.NewReader([]byte("world")), 5, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets/test-bucket/objects?delimiter=/", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var objects []S3ObjectInfo
	err = json.NewDecoder(rec.Body).Decode(&objects)
	require.NoError(t, err)

	// Should have file1.txt and folder/ prefix
	assert.Len(t, objects, 2)

	// Check for prefix
	var hasPrefix bool
	for _, obj := range objects {
		if obj.IsPrefix && obj.Key == "folder/" {
			hasPrefix = true
		}
	}
	assert.True(t, hasPrefix, "should have folder/ prefix")
}

func TestS3Proxy_GetObject(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Add an object
	content := []byte("hello world")
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "test.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets/test-bucket/objects/test.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	assert.Equal(t, content, body)
}

func TestS3Proxy_GetObject_NotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets/test-bucket/objects/nonexistent.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestS3Proxy_PutObject(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	content := []byte("new content")
	req := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/test-bucket/objects/new.txt", bytes.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("ETag"))

	// Verify object was created
	reader, _, err := srv.s3Store.GetObject(context.Background(), "test-bucket", "new.txt")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	data, _ := io.ReadAll(reader)
	assert.Equal(t, content, data)
}

func TestS3Proxy_PutObject_SystemBucketForbidden(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// System bucket is created automatically by the server
	content := []byte("malicious content")
	req := httptest.NewRequest(http.MethodPut, "/api/s3/buckets/"+auth.SystemBucket+"/objects/hack.txt", bytes.NewReader(content))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestS3Proxy_DeleteObject(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Add an object
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "to-delete.txt", bytes.NewReader([]byte("bye")), 3, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/api/s3/buckets/test-bucket/objects/to-delete.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)

	// Verify object is tombstoned (soft-deleted), not removed
	meta, err := srv.s3Store.HeadObject(context.Background(), "test-bucket", "to-delete.txt")
	require.NoError(t, err)
	assert.True(t, meta.IsTombstoned(), "object should be tombstoned")
}

func TestS3Proxy_DeleteObject_SystemBucketForbidden(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// System bucket is created automatically, add an object to it
	_, err := srv.s3Store.PutObject(context.Background(), auth.SystemBucket, "protected.txt", bytes.NewReader([]byte("secret")), 6, "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodDelete, "/api/s3/buckets/"+auth.SystemBucket+"/objects/protected.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestS3Proxy_HeadObject(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Add an object
	content := []byte("hello world")
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "test.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodHead, "/api/s3/buckets/test-bucket/objects/test.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain", rec.Header().Get("Content-Type"))
	assert.Equal(t, "11", rec.Header().Get("Content-Length"))
	assert.NotEmpty(t, rec.Header().Get("ETag"))
}

func TestS3Proxy_HeadObject_NotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodHead, "/api/s3/buckets/test-bucket/objects/nonexistent.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestS3Proxy_NotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/s3/invalid/path", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestS3Proxy_PathTraversal(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	tests := []struct {
		name         string
		path         string
		expectedCode int
	}{
		// URL-encoded path traversal attempts (bypass HTTP path cleaning)
		{"bucket url-encoded ..", "/api/s3/buckets/%2e%2e/objects/passwd", http.StatusBadRequest},
		{"key url-encoded ..", "/api/s3/buckets/test-bucket/objects/%2e%2e%2fetc%2fpasswd", http.StatusBadRequest},
		// Backslash attempts
		{"bucket with backslash", "/api/s3/buckets/..\\..\\etc/objects/passwd", http.StatusBadRequest},
		// URL-encoded dotdot
		{"bucket is dotdot", "/api/s3/buckets/%2e%2e/objects/test", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			srv.adminMux.ServeHTTP(rec, req)

			// Should be rejected with BadRequest, not return file contents
			assert.Equal(t, tt.expectedCode, rec.Code)
		})
	}
}

func TestS3Proxy_URLEncodedKey(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Create object with special chars in name
	content := []byte("hello world")
	_, err := srv.s3Store.PutObject(context.Background(), "test-bucket", "file with spaces.txt", bytes.NewReader(content), int64(len(content)), "text/plain", nil)
	require.NoError(t, err)

	// Access with URL-encoded key
	req := httptest.NewRequest(http.MethodGet, "/api/s3/buckets/test-bucket/objects/file%20with%20spaces.txt", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello world", rec.Body.String())
}

// --- Panel API Tests ---

func TestPanels_List(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/panels", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Panels []auth.PanelDefinition `json:"panels"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	// Should have built-in panels
	assert.GreaterOrEqual(t, len(resp.Panels), 10)

	// Check that visualizer panel exists
	var found bool
	for _, p := range resp.Panels {
		if p.ID == auth.PanelVisualizer {
			found = true
			assert.Equal(t, "Network Topology", p.Name)
			assert.True(t, p.Builtin)
			break
		}
	}
	assert.True(t, found, "visualizer panel should be present")
}

func TestPanels_ListExternal(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	// Register an external panel first
	panel := auth.PanelDefinition{
		ID:        "ext-test",
		Name:      "External Test",
		Tab:       auth.PanelTabData,
		External:  true,
		PluginURL: "https://example.com/plugin.js",
	}
	err := srv.s3Authorizer.PanelRegistry.Register(panel)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/panels?external=true", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Panels []auth.PanelDefinition `json:"panels"`
	}
	err = json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	// Should only have external panels
	require.Len(t, resp.Panels, 1)
	assert.Equal(t, "ext-test", resp.Panels[0].ID)
	assert.True(t, resp.Panels[0].External)
}

func TestPanels_GetBuiltin(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/panels/visualizer", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var panel auth.PanelDefinition
	err := json.NewDecoder(rec.Body).Decode(&panel)
	require.NoError(t, err)

	assert.Equal(t, auth.PanelVisualizer, panel.ID)
	assert.True(t, panel.Builtin)
}

func TestPanels_GetNotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodGet, "/api/panels/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// makeTestAdmin grants admin access for tests (guest user since no TLS)
func makeTestAdmin(srv *Server) {
	// getRequestOwner returns "" for test requests without TLS
	// Some handlers use "" directly, handleUserPermissions converts to "guest"
	// Bind to both for consistent behavior
	srv.s3Authorizer.Bindings.Add(&auth.RoleBinding{
		Name:     "test-admin-binding-empty",
		PeerID:   "",
		RoleName: auth.RoleAdmin,
	})
	srv.s3Authorizer.Bindings.Add(&auth.RoleBinding{
		Name:     "test-admin-binding-guest",
		PeerID:   "guest",
		RoleName: auth.RoleAdmin,
	})
}

func TestPanels_RegisterExternal(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	panel := auth.PanelDefinition{
		ID:         "test-plugin",
		Name:       "Test Plugin",
		Tab:        auth.PanelTabMesh,
		External:   true,
		PluginURL:  "https://example.com/plugin.js",
		PluginType: "script",
	}
	body, _ := json.Marshal(panel)

	req := httptest.NewRequest(http.MethodPost, "/api/panels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify it was registered
	registered := srv.s3Authorizer.PanelRegistry.Get("test-plugin")
	require.NotNil(t, registered)
	assert.Equal(t, "Test Plugin", registered.Name)
	assert.True(t, registered.External)
}

func TestPanels_RegisterExternal_InvalidURL(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	panel := auth.PanelDefinition{
		ID:        "bad-plugin",
		Name:      "Bad Plugin",
		Tab:       auth.PanelTabMesh,
		External:  true,
		PluginURL: "javascript:alert(1)", // XSS attempt
	}
	body, _ := json.Marshal(panel)

	req := httptest.NewRequest(http.MethodPost, "/api/panels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid plugin URL")
}

func TestPanels_RegisterExternal_NoURL(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	// External panels without PluginURL are allowed (for script-injected panels)
	panel := auth.PanelDefinition{
		ID:   "no-url-plugin",
		Name: "No URL Plugin",
		Tab:  auth.PanelTabMesh,
		// External flag is set by API automatically
	}
	body, _ := json.Marshal(panel)

	req := httptest.NewRequest(http.MethodPost, "/api/panels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify it was registered with External flag set
	registered := srv.s3Authorizer.PanelRegistry.Get("no-url-plugin")
	require.NotNil(t, registered)
	assert.True(t, registered.External)
}

func TestPanels_UpdatePublic(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	// Set visualizer panel to public
	update := map[string]interface{}{
		"public": true,
	}
	body, _ := json.Marshal(update)

	req := httptest.NewRequest(http.MethodPatch, "/api/panels/visualizer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify update
	panel := srv.s3Authorizer.PanelRegistry.Get(auth.PanelVisualizer)
	require.NotNil(t, panel)
	assert.True(t, panel.Public)
}

func TestPanels_UpdateExternal(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	// First register an external panel
	panel := auth.PanelDefinition{
		ID:        "update-test",
		Name:      "Update Test",
		Tab:       auth.PanelTabMesh,
		External:  true,
		PluginURL: "https://example.com/plugin.js",
	}
	err := srv.s3Authorizer.PanelRegistry.Register(panel)
	require.NoError(t, err)

	// Update it
	update := map[string]interface{}{
		"name":        "Updated Name",
		"description": "New description",
	}
	body, _ := json.Marshal(update)

	req := httptest.NewRequest(http.MethodPatch, "/api/panels/update-test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify update
	updated := srv.s3Authorizer.PanelRegistry.Get("update-test")
	require.NotNil(t, updated)
	assert.Equal(t, "Updated Name", updated.Name)
	assert.Equal(t, "New description", updated.Description)
}

func TestPanels_UpdateNotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	update := map[string]interface{}{
		"public": true,
	}
	body, _ := json.Marshal(update)

	req := httptest.NewRequest(http.MethodPatch, "/api/panels/nonexistent", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPanels_DeleteExternal(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	// First register an external panel
	panel := auth.PanelDefinition{
		ID:        "delete-test",
		Name:      "Delete Test",
		Tab:       auth.PanelTabMesh,
		External:  true,
		PluginURL: "https://example.com/plugin.js",
	}
	err := srv.s3Authorizer.PanelRegistry.Register(panel)
	require.NoError(t, err)

	// Delete it
	req := httptest.NewRequest(http.MethodDelete, "/api/panels/delete-test", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify deletion
	deleted := srv.s3Authorizer.PanelRegistry.Get("delete-test")
	assert.Nil(t, deleted)
}

func TestPanels_DeleteBuiltin_Forbidden(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	req := httptest.NewRequest(http.MethodDelete, "/api/panels/visualizer", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	// Returns BadRequest because Unregister returns error for built-in panels
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot unregister built-in panel")
}

func TestPanels_DeleteNotFound(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	req := httptest.NewRequest(http.MethodDelete, "/api/panels/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUserPermissions(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// Request without TLS will have empty user, which becomes "guest"
	req := httptest.NewRequest(http.MethodGet, "/api/user/permissions", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		PeerID  string   `json:"peer_id"`
		IsAdmin bool     `json:"is_admin"`
		Panels  []string `json:"panels"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	// Guest user should not be admin
	assert.Equal(t, "guest", resp.PeerID)
	assert.False(t, resp.IsAdmin)
}

func TestUserPermissions_Admin(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)
	makeTestAdmin(srv)

	req := httptest.NewRequest(http.MethodGet, "/api/user/permissions", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		PeerID  string   `json:"peer_id"`
		IsAdmin bool     `json:"is_admin"`
		Panels  []string `json:"panels"`
	}
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	// makeTestAdmin grants admin to empty user, which becomes "guest"
	assert.Equal(t, "guest", resp.PeerID)
	assert.True(t, resp.IsAdmin)
	assert.GreaterOrEqual(t, len(resp.Panels), 10) // Admin gets all panels
}

// --- Filter API Tests ---

func TestFilterAdd_TTLValidation(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)
	require.NotNil(t, srv.filter)

	tests := []struct {
		name         string
		ttl          int64
		expectedCode int
		errorMsg     string
	}{
		{
			name:         "permanent rule (ttl=0)",
			ttl:          0,
			expectedCode: http.StatusOK,
		},
		{
			name:         "1 hour ttl",
			ttl:          3600,
			expectedCode: http.StatusOK,
		},
		{
			name:         "1 day ttl",
			ttl:          86400,
			expectedCode: http.StatusOK,
		},
		{
			name:         "1 year ttl (max)",
			ttl:          365 * 24 * 3600,
			expectedCode: http.StatusOK,
		},
		{
			name:         "negative ttl",
			ttl:          -1,
			expectedCode: http.StatusBadRequest,
			errorMsg:     "ttl cannot be negative",
		},
		{
			name:         "ttl exceeds maximum",
			ttl:          365*24*3600 + 1,
			expectedCode: http.StatusBadRequest,
			errorMsg:     "ttl exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := FilterRulesRequest{
				PeerName: "test-peer",
				Port:     22,
				Protocol: "tcp",
				Action:   "allow",
				TTL:      tt.ttl,
			}
			body, _ := json.Marshal(reqBody)

			req := httptest.NewRequest(http.MethodPost, "/api/filter/rules", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			srv.adminMux.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedCode, rec.Code)
			if tt.errorMsg != "" {
				assert.Contains(t, rec.Body.String(), tt.errorMsg)
			}
		})
	}
}

func TestFilterAdd_WithTTL(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)
	require.NotNil(t, srv.filter)

	// Add a rule with TTL
	reqBody := FilterRulesRequest{
		PeerName: "test-peer",
		Port:     22,
		Protocol: "tcp",
		Action:   "allow",
		TTL:      3600, // 1 hour
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/filter/rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify rule was added with correct expiry
	rules := srv.filter.ListRules()
	var found bool
	for _, r := range rules {
		if r.Source == routing.SourceTemporary && r.Rule.Port == 22 {
			found = true
			assert.Greater(t, r.Rule.Expires, time.Now().Unix(), "expires should be in the future")
			assert.LessOrEqual(t, r.Rule.Expires, time.Now().Unix()+3601, "expires should be approximately 1 hour from now")
			break
		}
	}
	assert.True(t, found, "rule should have been added")
}

func TestFilterAdd_WithoutTTL(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)
	require.NotNil(t, srv.filter)

	// Add a permanent rule (TTL=0)
	reqBody := FilterRulesRequest{
		PeerName: "test-peer",
		Port:     80,
		Protocol: "tcp",
		Action:   "allow",
		TTL:      0,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/filter/rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify rule was added with no expiry
	rules := srv.filter.ListRules()
	var found bool
	for _, r := range rules {
		if r.Source == routing.SourceTemporary && r.Rule.Port == 80 {
			found = true
			assert.Equal(t, int64(0), r.Rule.Expires, "permanent rule should have expires=0")
			break
		}
	}
	assert.True(t, found, "rule should have been added")
}

func TestFilterAdd_WithSourcePeer(t *testing.T) {
	srv := newTestServerWithS3(t)
	require.NotNil(t, srv.adminMux)
	require.NotNil(t, srv.filter)

	// Add a peer-specific rule
	reqBody := FilterRulesRequest{
		PeerName:   "test-peer",
		Port:       22,
		Protocol:   "tcp",
		Action:     "allow",
		SourcePeer: "trusted-peer",
		TTL:        0,
	}
	body, _ := json.Marshal(reqBody)

	req := httptest.NewRequest(http.MethodPost, "/api/filter/rules", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify rule was added with source peer
	rules := srv.filter.ListRules()
	var found bool
	for _, r := range rules {
		if r.Source == routing.SourceTemporary && r.Rule.Port == 22 && r.Rule.SourcePeer == "trusted-peer" {
			found = true
			break
		}
	}
	assert.True(t, found, "peer-specific rule should have been added")
}
