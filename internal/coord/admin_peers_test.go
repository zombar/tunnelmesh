package coord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeersMgmt_MethodNotAllowed(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestPeersMgmt_ListPeers(t *testing.T) {
	srv := newTestServerWithS3AndBucket(t)

	// List peers via admin API - should return 200 even with no persisted peers
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()
	srv.adminMux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var peers []PeerInfo
	err := json.NewDecoder(rec.Body).Decode(&peers)
	require.NoError(t, err)
	// May be empty but should be a valid JSON array
	require.NotNil(t, peers)
}

func TestGetPeerByRemoteAddr_Found(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Coordinator.Enabled = true

	srv, err := NewServer(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(t, srv) })

	// Register a peer
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := NewClient(ts.URL, "test-token")
	_, err = client.Register("addr-peer", "SHA256:addrkey", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil, false, false)
	require.NoError(t, err)

	// Look up by mesh IP (peer gets allocated a mesh IP on registration)
	srv.peersMu.RLock()
	peerInfo := srv.peers["addr-peer"]
	meshIP := peerInfo.peer.MeshIP
	srv.peersMu.RUnlock()

	result := srv.getPeerByRemoteAddr(meshIP + ":12345")
	assert.Equal(t, "addr-peer", result)
}

func TestGetPeerByRemoteAddr_NotFound(t *testing.T) {
	srv := newTestServer(t)

	result := srv.getPeerByRemoteAddr("192.168.99.99:12345")
	assert.Empty(t, result)
}
