package coord

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func newTestServer(t *testing.T) *Server {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Admin:        config.AdminConfig{Enabled: true},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	return srv
}

func TestServer_Health(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}

func TestServer_Register_Success(t *testing.T) {
	srv := newTestServer(t)

	regReq := proto.RegisterRequest{
		Name:       "testnode",
		PublicKey:  "SHA256:abc123",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp proto.RegisterResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.NotEmpty(t, resp.MeshIP)
	assert.Equal(t, "10.99.0.0/16", resp.MeshCIDR)
	assert.Equal(t, ".tunnelmesh", resp.Domain)
	assert.NotEmpty(t, resp.Token, "should return JWT token for relay auth")

	// Verify token is valid
	claims, err := srv.ValidateToken(resp.Token)
	require.NoError(t, err)
	assert.Equal(t, "testnode", claims.PeerName)
	assert.Equal(t, resp.MeshIP, claims.MeshIP)
}

func TestServer_Register_Unauthorized(t *testing.T) {
	srv := newTestServer(t)

	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServer_Register_InvalidToken(t *testing.T) {
	srv := newTestServer(t)

	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServer_Register_DuplicateName(t *testing.T) {
	srv := newTestServer(t)

	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)

	// First registration
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Second registration with same name - should update
	body, _ = json.Marshal(regReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServer_Peers(t *testing.T) {
	srv := newTestServer(t)

	// Register a peer first
	regReq := proto.RegisterRequest{
		Name:       "node1",
		PublicKey:  "SHA256:abc123",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// List peers
	req = httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp proto.PeerListResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Len(t, resp.Peers, 1)
	assert.Equal(t, "node1", resp.Peers[0].Name)
}

func TestServer_Heartbeat(t *testing.T) {
	srv := newTestServer(t)

	// Register first
	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Send heartbeat
	hbReq := proto.HeartbeatRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
	}
	body, _ = json.Marshal(hbReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp proto.HeartbeatResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.True(t, resp.OK)
}

func TestServer_Deregister(t *testing.T) {
	srv := newTestServer(t)

	// Register first
	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Verify peer exists
	req = httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var listResp proto.PeerListResponse
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	assert.Len(t, listResp.Peers, 1)

	// Deregister
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/peers/testnode", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Verify peer is gone
	req = httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	assert.Len(t, listResp.Peers, 0)
}

func TestServer_IPAllocation(t *testing.T) {
	srv := newTestServer(t)

	// Register multiple peers and verify unique IPs
	ips := make(map[string]bool)

	for i := 0; i < 5; i++ {
		regReq := proto.RegisterRequest{
			Name:      "node" + string(rune('A'+i)),
			PublicKey: "SHA256:key" + string(rune('A'+i)),
			SSHPort:   2222,
		}
		body, _ := json.Marshal(regReq)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		srv.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp proto.RegisterResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)

		// Verify IP is unique
		assert.False(t, ips[resp.MeshIP], "IP should be unique: %s", resp.MeshIP)
		ips[resp.MeshIP] = true

		// Verify IP is in mesh range
		assert.Contains(t, resp.MeshIP, "10.99.")
	}
}

func TestServer_AdminOverview_NoPeers(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp AdminOverview
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 0, resp.TotalPeers)
	assert.Equal(t, 0, resp.OnlinePeers)
	assert.Equal(t, "10.99.0.0/16", resp.MeshCIDR)
	assert.Empty(t, resp.Peers)
}

func TestServer_AdminOverview_WithPeers(t *testing.T) {
	srv := newTestServer(t)

	// Register a peer first
	regReq := proto.RegisterRequest{
		Name:       "node1",
		PublicKey:  "SHA256:abc123",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
	}
	body, _ := json.Marshal(regReq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Get admin overview
	req = httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp AdminOverview
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 1, resp.TotalPeers)
	assert.Equal(t, 1, resp.OnlinePeers)
	assert.Len(t, resp.Peers, 1)
	assert.Equal(t, "node1", resp.Peers[0].Name)
	assert.True(t, resp.Peers[0].Online)
}

func TestServer_HeartbeatWithStats(t *testing.T) {
	srv := newTestServer(t)

	// Register first
	regReq := proto.RegisterRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Send heartbeat with stats
	hbReq := proto.HeartbeatRequest{
		Name:      "testnode",
		PublicKey: "SHA256:abc123",
		Stats: &proto.PeerStats{
			PacketsSent:     100,
			PacketsReceived: 200,
			BytesSent:       10000,
			BytesReceived:   20000,
			ActiveTunnels:   2,
		},
	}
	body, _ = json.Marshal(hbReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify stats are reflected in admin overview
	req = httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp AdminOverview
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Len(t, resp.Peers, 1)
	assert.NotNil(t, resp.Peers[0].Stats)
	assert.Equal(t, uint64(100), resp.Peers[0].Stats.PacketsSent)
	assert.Equal(t, uint64(200), resp.Peers[0].Stats.PacketsReceived)
	assert.Equal(t, uint64(10000), resp.Peers[0].Stats.BytesSent)
	assert.Equal(t, uint64(20000), resp.Peers[0].Stats.BytesReceived)
	assert.Equal(t, 2, resp.Peers[0].Stats.ActiveTunnels)
}

func TestServer_AdminStaticFiles(t *testing.T) {
	srv := newTestServer(t)

	// Test index.html redirect
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusMovedPermanently, w.Code)

	// Test index.html
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "TunnelMesh Admin")

	// Test CSS
	req = httptest.NewRequest(http.MethodGet, "/admin/css/style.css", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Test JS
	req = httptest.NewRequest(http.MethodGet, "/admin/js/app.js", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestServer_GetNetworkSettings_Empty(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/network-settings", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp networkSettings
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Empty(t, resp.ExitNodePeer)
	assert.Empty(t, resp.Exceptions)
}

func TestServer_SetNetworkSettings(t *testing.T) {
	srv := newTestServer(t)

	// Register the exit node peer first (required for validation)
	regReq := proto.RegisterRequest{
		Name:      "exit-node",
		PublicKey: "SHA256:exitkey",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Set network settings
	settings := networkSettings{
		ExitNodePeer: "exit-node",
		Exceptions:   []string{"192.168.0.0/16", "10.0.0.0/8"},
	}
	body, _ = json.Marshal(settings)

	req = httptest.NewRequest(http.MethodPost, "/admin/api/network-settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Get and verify
	req = httptest.NewRequest(http.MethodGet, "/admin/api/network-settings", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp networkSettings
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "exit-node", resp.ExitNodePeer)
	assert.Equal(t, []string{"192.168.0.0/16", "10.0.0.0/8"}, resp.Exceptions)
}

func TestServer_SetNetworkSettings_ClearExitNode(t *testing.T) {
	srv := newTestServer(t)

	// Register the exit node peer first (required for validation)
	regReq := proto.RegisterRequest{
		Name:      "exit-node",
		PublicKey: "SHA256:exitkey",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// First, set exit node
	settings := networkSettings{
		ExitNodePeer: "exit-node",
	}
	body, _ = json.Marshal(settings)

	req = httptest.NewRequest(http.MethodPost, "/admin/api/network-settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Clear exit node
	settings = networkSettings{
		ExitNodePeer: "",
		Exceptions:   []string{"100.64.0.0/10"},
	}
	body, _ = json.Marshal(settings)

	req = httptest.NewRequest(http.MethodPost, "/admin/api/network-settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Verify
	req = httptest.NewRequest(http.MethodGet, "/admin/api/network-settings", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp networkSettings
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Empty(t, resp.ExitNodePeer)
	assert.Equal(t, []string{"100.64.0.0/10"}, resp.Exceptions)
}

func TestServer_PeersIncludeNetworkSettings(t *testing.T) {
	srv := newTestServer(t)

	// Register the exit node peer first (required for validation)
	regReq := proto.RegisterRequest{
		Name:      "exit-node",
		PublicKey: "SHA256:exitkey",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Set network settings
	settings := networkSettings{
		ExitNodePeer: "exit-node",
		Exceptions:   []string{"192.168.0.0/16"},
	}
	body, _ = json.Marshal(settings)

	req = httptest.NewRequest(http.MethodPost, "/admin/api/network-settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Get peers - should include network settings
	req = httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp proto.PeerListResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.NetworkSettings)
	assert.Equal(t, "exit-node", resp.NetworkSettings.ExitNodePeer)
	assert.Equal(t, []string{"192.168.0.0/16"}, resp.NetworkSettings.Exceptions)
}

func TestServer_AdminOverviewIncludesNetworkSettings(t *testing.T) {
	srv := newTestServer(t)

	// Register the exit node peer first (required for validation)
	regReq := proto.RegisterRequest{
		Name:      "vps-exit",
		PublicKey: "SHA256:exitkey",
		SSHPort:   2222,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Set network settings
	settings := networkSettings{
		ExitNodePeer: "vps-exit",
		Exceptions:   []string{"10.0.0.0/8"},
	}
	body, _ = json.Marshal(settings)

	req = httptest.NewRequest(http.MethodPost, "/admin/api/network-settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Get admin overview - should include network settings
	req = httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp AdminOverview
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	require.NotNil(t, resp.NetworkSettings)
	assert.Equal(t, "vps-exit", resp.NetworkSettings.ExitNodePeer)
	assert.Equal(t, []string{"10.0.0.0/8"}, resp.NetworkSettings.Exceptions)
}
