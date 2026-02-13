package coord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestClient_Register(t *testing.T) {
	// Create test server
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create client
	client := NewClient(ts.URL, "test-token")

	// Register
	resp, err := client.Register("mynode", "SHA256:abc123", []string{"1.2.3.4"}, []string{"192.168.1.1"}, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	assert.Contains(t, resp.MeshIP, "10.42.") // IP is hash-based, just check it's in mesh range
	assert.Equal(t, "10.42.0.0/16", resp.MeshCIDR)
	assert.Equal(t, ".tunnelmesh", resp.Domain)
}

func TestClient_RegisterWithLocation(t *testing.T) {
	// Create test server
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Locations = true // Enable location tracking for this test

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create client
	client := NewClient(ts.URL, "test-token")

	// Register with location
	location := &proto.GeoLocation{
		Latitude:  51.5074,
		Longitude: -0.1278,
		Source:    "manual",
	}
	resp, err := client.Register("geonode", "SHA256:abc123", []string{"1.2.3.4"}, []string{"192.168.1.1"}, 2222, 0, false, "v1.0.0", location, "", false, nil, false, false)
	require.NoError(t, err)

	assert.Contains(t, resp.MeshIP, "10.42.")

	// Verify the peer has location stored
	peers, err := client.ListPeers()
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.NotNil(t, peers[0].Location)
	assert.Equal(t, 51.5074, peers[0].Location.Latitude)
	assert.Equal(t, -0.1278, peers[0].Location.Longitude)
	assert.Equal(t, "manual", peers[0].Location.Source)
}

func TestClient_ListPeers(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register a peer
	_, err = client.Register("node1", "SHA256:key1", nil, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// List peers
	peers, err := client.ListPeers()
	require.NoError(t, err)

	assert.Len(t, peers, 1)
	assert.Equal(t, "node1", peers[0].Name)
}

// Note: TestClient_Heartbeat and TestClient_HeartbeatNotFound removed.
// Heartbeats are now sent via WebSocket using PersistentRelay.SendHeartbeat().
// See internal/tunnel/persistent_relay_test.go for WebSocket heartbeat tests.

func TestClient_Deregister(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register first
	_, err = client.Register("mynode", "SHA256:key", nil, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// Verify registered
	peers, _ := client.ListPeers()
	assert.Len(t, peers, 1)

	// Deregister
	err = client.Deregister("mynode")
	assert.NoError(t, err)

	// Verify gone
	peers, _ = client.ListPeers()
	assert.Len(t, peers, 0)
}

func TestClient_GetDNSRecords(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register peers
	_, err = client.Register("node1", "SHA256:key1", nil, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)
	_, err = client.Register("node2", "SHA256:key2", nil, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// Get DNS records
	records, err := client.GetDNSRecords()
	require.NoError(t, err)

	assert.Len(t, records, 2)
}

func TestClient_RegisterWithRetry_SuccessOnFirstTry(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	ctx := context.Background()
	retryCfg := RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}

	resp, err := client.RegisterWithRetry(ctx, "mynode", "SHA256:abc123", []string{"1.2.3.4"}, []string{}, 2222, 2223, false, "v1.0.0", nil, "", false, []string{}, false, false, retryCfg)
	require.NoError(t, err)

	assert.Contains(t, resp.MeshIP, "10.42.")
	assert.Equal(t, "10.42.0.0/16", resp.MeshCIDR)
}

func TestClient_RegisterWithRetry_SuccessAfterFailures(t *testing.T) {
	var attempts atomic.Int32

	// Create a server that fails the first 2 attempts then succeeds
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error": "unavailable", "message": "server starting"}`))
			return
		}

		// Success on 3rd attempt
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"mesh_ip": "10.42.1.1",
			"mesh_cidr": "10.42.0.0/16",
			"domain": ".tunnelmesh",
			"token": "jwt-token"
		}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	ctx := context.Background()
	retryCfg := RetryConfig{
		MaxRetries:     5,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	}

	resp, err := client.RegisterWithRetry(ctx, "mynode", "SHA256:abc123", []string{}, []string{}, 2222, 2223, false, "v1.0.0", nil, "", false, []string{}, false, false, retryCfg)
	require.NoError(t, err)

	assert.Equal(t, int32(3), attempts.Load(), "should have taken 3 attempts")
	assert.Equal(t, "10.42.1.1", resp.MeshIP)
}

func TestClient_RegisterWithRetry_MaxRetriesExceeded(t *testing.T) {
	var attempts atomic.Int32

	// Create a server that always fails
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": "unavailable", "message": "server down"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	ctx := context.Background()
	retryCfg := RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	}

	_, err := client.RegisterWithRetry(ctx, "mynode", "SHA256:abc123", []string{}, []string{}, 2222, 2223, false, "v1.0.0", nil, "", false, []string{}, false, false, retryCfg)
	require.Error(t, err)

	assert.Equal(t, int32(3), attempts.Load(), "should have made exactly 3 attempts")
	assert.Contains(t, err.Error(), "after 3 attempts")
}

func TestClient_RegisterWithRetry_ContextCancelled(t *testing.T) {
	var attempts atomic.Int32

	// Create a server that always fails
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error": "unavailable"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	ctx, cancel := context.WithCancel(context.Background())
	retryCfg := RetryConfig{
		MaxRetries:     10,
		InitialBackoff: 100 * time.Millisecond, // Long enough to cancel during wait
		MaxBackoff:     1 * time.Second,
	}

	// Cancel after a short delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := client.RegisterWithRetry(ctx, "mynode", "SHA256:abc123", []string{}, []string{}, 2222, 2223, false, "v1.0.0", nil, "", false, []string{}, false, false, retryCfg)
	require.Error(t, err)

	assert.ErrorIs(t, err, context.Canceled, "error should wrap context.Canceled")
	assert.LessOrEqual(t, attempts.Load(), int32(2), "should stop early due to cancellation")
}

func TestClient_RegisterWithRetry_ConnectionRefused(t *testing.T) {
	// Client pointing to a closed server (connection refused)
	client := NewClient("http://127.0.0.1:59999", "test-token")

	ctx := context.Background()
	retryCfg := RetryConfig{
		MaxRetries:     2,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	}

	_, err := client.RegisterWithRetry(ctx, "mynode", "SHA256:abc123", []string{}, []string{}, 2222, 2223, false, "v1.0.0", nil, "", false, []string{}, false, false, retryCfg)
	require.Error(t, err)

	assert.Contains(t, err.Error(), "after 2 attempts")
}

func TestServer_GeolocationOnlyOnNewOrChangedIP(t *testing.T) {
	// Track geolocation API calls
	var geoLookupCount atomic.Int32
	var lastLookedUpIP atomic.Value

	// Create mock ip-api.com server
	geoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geoLookupCount.Add(1)
		ip := r.URL.Path[len("/json/"):]
		lastLookedUpIP.Store(ip)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"lat": 51.5074,
			"lon": -0.1278,
			"city": "London",
			"regionName": "England",
			"country": "United Kingdom"
		}`))
	}))
	defer geoServer.Close()

	// Create test server with custom geolocation cache
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Locations = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	// Replace the geolocation cache with one pointing to our mock server
	srv.ipGeoCache = NewIPGeoCache(geoServer.URL + "/json/")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Test 1: First registration should trigger geolocation lookup
	_, err = client.Register("node1", "SHA256:key1", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// Wait for background geolocation to complete
	time.Sleep(100 * time.Millisecond)

	initialCount := geoLookupCount.Load()
	assert.Equal(t, int32(1), initialCount, "first registration should trigger one geolocation lookup")

	// Verify location was set
	peers, err := client.ListPeers()
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.NotNil(t, peers[0].Location)
	assert.Equal(t, "ip", peers[0].Location.Source)

	// Test 2: Re-registration with SAME IP should NOT trigger new lookup
	_, err = client.Register("node1", "SHA256:key1", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, initialCount, geoLookupCount.Load(), "re-registration with same IP should not trigger new lookup")

	// Test 3: Re-registration with DIFFERENT IP should trigger new lookup
	_, err = client.Register("node1", "SHA256:key1", []string{"5.6.7.8"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, initialCount+1, geoLookupCount.Load(), "re-registration with different IP should trigger new lookup")
	assert.Equal(t, "5.6.7.8", lastLookedUpIP.Load().(string))
}

func TestServer_ManualLocationPreservedOnIPChange(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Locations = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register with manual location
	manualLoc := &proto.GeoLocation{
		Latitude:  40.7128,
		Longitude: -74.0060,
		Source:    "manual",
		City:      "New York",
	}
	_, err = client.Register("node1", "SHA256:key1", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", manualLoc, "", false, nil, false, false)
	require.NoError(t, err)

	// Verify location
	peers, err := client.ListPeers()
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.NotNil(t, peers[0].Location)
	assert.Equal(t, "manual", peers[0].Location.Source)
	assert.Equal(t, 40.7128, peers[0].Location.Latitude)

	// Re-register with different IP but NO location (simulating reconnect)
	_, err = client.Register("node1", "SHA256:key1", []string{"5.6.7.8"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// Manual location should be preserved
	peers, err = client.ListPeers()
	require.NoError(t, err)
	require.Len(t, peers, 1)
	require.NotNil(t, peers[0].Location)
	assert.Equal(t, "manual", peers[0].Location.Source, "manual location should be preserved even when IP changes")
	assert.Equal(t, 40.7128, peers[0].Location.Latitude)
}
