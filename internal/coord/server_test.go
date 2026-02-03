package coord

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Note: TestServer_Heartbeat removed - HTTP heartbeat endpoint replaced by WebSocket
// See relay_test.go for WebSocket heartbeat tests

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

// Note: TestServer_HeartbeatWithStats removed - HTTP heartbeat endpoint replaced by WebSocket
// See relay_test.go for WebSocket heartbeat tests

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

func TestServer_HolePunchBidirectionalCoordination(t *testing.T) {
	srv := newTestServer(t)

	// Register two peers
	for _, name := range []string{"peerA", "peerB"} {
		regReq := proto.RegisterRequest{
			Name:       name,
			PublicKey:  "SHA256:" + name,
			PublicIPs:  []string{"1.2.3.4"},
			PrivateIPs: []string{"192.168.1.100"},
			SSHPort:    2222,
			UDPPort:    2223,
		}
		body, _ := json.Marshal(regReq)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Register UDP endpoints for both peers
	for _, peerData := range []struct {
		name         string
		externalAddr string
	}{
		{"peerA", "1.1.1.1:5000"},
		{"peerB", "2.2.2.2:5001"},
	} {
		udpReq := RegisterUDPRequest{
			PeerName:  peerData.name,
			LocalAddr: "0.0.0.0:51820",
			UDPPort:   51820,
		}
		body, _ := json.Marshal(udpReq)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/udp/register", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", peerData.externalAddr[:strings.Index(peerData.externalAddr, ":")])
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// PeerA initiates hole-punch to PeerB
	holePunchReq := HolePunchRequest{
		FromPeer:     "peerA",
		ToPeer:       "peerB",
		LocalAddr:    "0.0.0.0:51820",
		ExternalAddr: "1.1.1.1:5000",
	}
	body, _ := json.Marshal(holePunchReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/udp/holepunch", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var holePunchResp HolePunchResponse
	err := json.Unmarshal(w.Body.Bytes(), &holePunchResp)
	require.NoError(t, err)
	assert.True(t, holePunchResp.OK)
	assert.True(t, holePunchResp.Ready)

	// Note: Hole-punch notifications are now delivered via WebSocket push instead of HTTP heartbeat.
	// See relay_test.go TestRelayManager_NotifyHolePunch for WebSocket notification tests.
}

func TestServer_DualStackUDPEndpointRegistration(t *testing.T) {
	srv := newTestServer(t)

	// Register a peer
	regReq := proto.RegisterRequest{
		Name:       "dual-stack-peer",
		PublicKey:  "SHA256:dual-stack-peer",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
		UDPPort:    2223,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Register IPv4 UDP endpoint
	udpReq := RegisterUDPRequest{
		PeerName:  "dual-stack-peer",
		LocalAddr: "0.0.0.0:51820",
		UDPPort:   51820,
	}
	body, _ = json.Marshal(udpReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/udp/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "203.0.113.50") // IPv4 address
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Register IPv6 UDP endpoint (same peer, different address family)
	body, _ = json.Marshal(udpReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/udp/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "2001:db8::1") // IPv6 address
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Get the endpoint and verify both addresses are stored
	req = httptest.NewRequest(http.MethodGet, "/api/v1/udp/endpoint/dual-stack-peer", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var endpoint UDPEndpoint
	err := json.Unmarshal(w.Body.Bytes(), &endpoint)
	require.NoError(t, err)

	assert.Equal(t, "dual-stack-peer", endpoint.PeerName)
	assert.Equal(t, "203.0.113.50:51820", endpoint.ExternalAddr4, "IPv4 address should be stored in ExternalAddr4")
	assert.Equal(t, "[2001:db8::1]:51820", endpoint.ExternalAddr6, "IPv6 address should be stored in ExternalAddr6")
}

func TestServer_IPv4OnlyEndpointRegistration(t *testing.T) {
	srv := newTestServer(t)

	// Register a peer
	regReq := proto.RegisterRequest{
		Name:       "ipv4-only-peer",
		PublicKey:  "SHA256:ipv4-only-peer",
		PublicIPs:  []string{"1.2.3.4"},
		PrivateIPs: []string{"192.168.1.100"},
		SSHPort:    2222,
		UDPPort:    2223,
	}
	body, _ := json.Marshal(regReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Register only IPv4 UDP endpoint
	udpReq := RegisterUDPRequest{
		PeerName:  "ipv4-only-peer",
		LocalAddr: "0.0.0.0:51820",
		UDPPort:   51820,
	}
	body, _ = json.Marshal(udpReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/udp/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Real-IP", "198.51.100.25") // IPv4 only
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Get the endpoint and verify only IPv4 is stored
	req = httptest.NewRequest(http.MethodGet, "/api/v1/udp/endpoint/ipv4-only-peer", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var endpoint UDPEndpoint
	err := json.Unmarshal(w.Body.Bytes(), &endpoint)
	require.NoError(t, err)

	assert.Equal(t, "198.51.100.25:51820", endpoint.ExternalAddr4, "IPv4 address should be stored")
	assert.Empty(t, endpoint.ExternalAddr6, "IPv6 address should be empty")
}

// newTestServerWithWireGuard creates a test server with WireGuard enabled.
func newTestServerWithWireGuard(t *testing.T) *Server {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Admin:        config.AdminConfig{Enabled: true},
		WireGuard: config.WireGuardServerConfig{
			Enabled:  true,
			Endpoint: "wg.example.com:51820",
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	return srv
}

func TestServer_WireGuardDNSIntegration(t *testing.T) {
	// Skip: WireGuard client management has moved to the concentrator peer.
	// The coordinator now proxies API requests to the concentrator via relay.
	// This test would need a mock concentrator to function.
	t.Skip("WireGuard client management now proxied to concentrator")

	srv := newTestServerWithWireGuard(t)

	// Create a WireGuard client
	createReq := map[string]string{"name": "iPhone"}
	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/wireguard/clients", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	// Parse response to get client ID and DNS name
	var createResp map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &createResp)
	require.NoError(t, err)

	client := createResp["client"].(map[string]interface{})
	clientID := client["id"].(string)
	dnsName := client["dns_name"].(string)
	meshIP := client["mesh_ip"].(string)

	// Verify client appears in DNS records
	req = httptest.NewRequest(http.MethodGet, "/api/v1/dns", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var dnsResp proto.DNSUpdateNotification
	err = json.Unmarshal(w.Body.Bytes(), &dnsResp)
	require.NoError(t, err)

	// Find the WG client in DNS records
	found := false
	for _, record := range dnsResp.Records {
		if record.Hostname == dnsName && record.MeshIP == meshIP {
			found = true
			break
		}
	}
	assert.True(t, found, "WireGuard client should appear in DNS records")

	// Delete the client
	req = httptest.NewRequest(http.MethodDelete, "/admin/api/wireguard/clients/"+clientID, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Verify client is removed from DNS records
	req = httptest.NewRequest(http.MethodGet, "/api/v1/dns", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	err = json.Unmarshal(w.Body.Bytes(), &dnsResp)
	require.NoError(t, err)

	// Verify the WG client is no longer in DNS records
	found = false
	for _, record := range dnsResp.Records {
		if record.Hostname == dnsName {
			found = true
			break
		}
	}
	assert.False(t, found, "WireGuard client should be removed from DNS records after deletion")
}

// newTestServerWithAdminToken creates a test server with admin authentication enabled.
func newTestServerWithAdminToken(t *testing.T) *Server {
	cfg := &config.ServerConfig{
		Listen:       ":0",
		AuthToken:    "test-token",
		MeshCIDR:     "10.99.0.0/16",
		DomainSuffix: ".tunnelmesh",
		Admin:        config.AdminConfig{Enabled: true, Token: "admin-secret"},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	return srv
}

func TestServer_AdminAuth_Required(t *testing.T) {
	srv := newTestServerWithAdminToken(t)

	// Request without auth should return 401
	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Header().Get("WWW-Authenticate"), "Basic")
}

func TestServer_AdminAuth_InvalidPassword(t *testing.T) {
	srv := newTestServerWithAdminToken(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	req.SetBasicAuth("admin", "wrong-password")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestServer_AdminAuth_ValidPassword(t *testing.T) {
	srv := newTestServerWithAdminToken(t)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/overview", nil)
	req.SetBasicAuth("admin", "admin-secret") // Username is ignored, only password matters
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp AdminOverview
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 0, resp.TotalPeers)
}

func TestServer_AdminAuth_StaticFilesProtected(t *testing.T) {
	srv := newTestServerWithAdminToken(t)

	// Static files should also require auth
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// With auth should work
	req = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.SetBasicAuth("", "admin-secret")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
