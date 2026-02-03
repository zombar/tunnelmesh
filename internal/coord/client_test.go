package coord

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestClient_Register(t *testing.T) {
	// Create test server
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Create client
	client := NewClient(ts.URL, "test-token")

	// Register
	resp, err := client.Register("mynode", "SHA256:abc123", []string{"1.2.3.4"}, []string{"192.168.1.1"}, 2222, 0, false, "v1.0.0")
	require.NoError(t, err)

	assert.Contains(t, resp.MeshIP, "10.99.") // IP is hash-based, just check it's in mesh range
	assert.Equal(t, "10.99.0.0/16", resp.MeshCIDR)
	assert.Equal(t, ".tunnelmesh", resp.Domain)
}

func TestClient_ListPeers(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register a peer
	_, err = client.Register("node1", "SHA256:key1", nil, nil, 2222, 0, false, "v1.0.0")
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
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register first
	_, err = client.Register("mynode", "SHA256:key", nil, nil, 2222, 0, false, "v1.0.0")
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
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL, "test-token")

	// Register peers
	_, err = client.Register("node1", "SHA256:key1", nil, nil, 2222, 0, false, "v1.0.0")
	require.NoError(t, err)
	_, err = client.Register("node2", "SHA256:key2", nil, nil, 2222, 0, false, "v1.0.0")
	require.NoError(t, err)

	// Get DNS records
	records, err := client.GetDNSRecords()
	require.NoError(t, err)

	assert.Len(t, records, 2)
}
