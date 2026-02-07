package coord

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/pkg/proto"
)

func TestDownsampleHistory(t *testing.T) {
	// Create sample data
	now := time.Now()
	data := make([]StatsDataPoint, 100)
	for i := 0; i < 100; i++ {
		data[i] = StatsDataPoint{
			Timestamp:     now.Add(time.Duration(i) * time.Second),
			BytesSentRate: float64(i * 100),
		}
	}

	tests := []struct {
		name         string
		input        []StatsDataPoint
		targetPoints int
		wantLen      int
	}{
		{
			name:         "downsample 100 to 10",
			input:        data,
			targetPoints: 10,
			wantLen:      10,
		},
		{
			name:         "target larger than input returns input",
			input:        data[:5],
			targetPoints: 10,
			wantLen:      5,
		},
		{
			name:         "target equal to input returns input",
			input:        data[:10],
			targetPoints: 10,
			wantLen:      10,
		},
		{
			name:         "target 0 returns input",
			input:        data,
			targetPoints: 0,
			wantLen:      100,
		},
		{
			name:         "empty input returns empty",
			input:        []StatsDataPoint{},
			targetPoints: 10,
			wantLen:      0,
		},
		{
			name:         "nil input returns nil",
			input:        nil,
			targetPoints: 10,
			wantLen:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := downsampleHistory(tt.input, tt.targetPoints)
			if len(result) != tt.wantLen {
				t.Errorf("downsampleHistory() len = %d, want %d", len(result), tt.wantLen)
			}

			// For downsampled results, verify first and last points are preserved
			if tt.wantLen > 0 && len(tt.input) > 0 && tt.targetPoints > 0 && tt.targetPoints < len(tt.input) {
				if result[0].BytesSentRate != tt.input[0].BytesSentRate {
					t.Errorf("first point not preserved: got %f, want %f",
						result[0].BytesSentRate, tt.input[0].BytesSentRate)
				}
				if result[len(result)-1].BytesSentRate != tt.input[len(tt.input)-1].BytesSentRate {
					t.Errorf("last point not preserved: got %f, want %f",
						result[len(result)-1].BytesSentRate, tt.input[len(tt.input)-1].BytesSentRate)
				}
			}
		})
	}
}

func TestAdminOverview_IncludesLocation(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:    ":0",
		AuthToken: "test-token",
		Locations: true, // Enable location tracking for this test
		Admin: config.AdminConfig{
			Enabled: true,
		},
		JoinMesh: &config.PeerConfig{
			Name: "test-coord",
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
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
	_, err = client.Register("geonode", "SHA256:abc123", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", location, "", false, nil)
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

func TestAdminOverview_ExitNodeInfo(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:    ":0",
		AuthToken: "test-token",
		Admin: config.AdminConfig{
			Enabled: true,
		},
		JoinMesh: &config.PeerConfig{
			Name: "test-coord",
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, srv.adminMux, "adminMux should be created when JoinMesh is configured")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register an exit node
	client := NewClient(ts.URL, "test-token")
	_, err = client.Register("exit-node", "SHA256:exitkey", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", true, nil)
	require.NoError(t, err)

	// Register a client that uses the exit node
	_, err = client.Register("client1", "SHA256:client1key", []string{"5.6.7.8"}, nil, 2223, 0, false, "v1.0.0", nil, "exit-node", false, nil)
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
	assert.Equal(t, "", exitNodeInfo.ExitNode, "exit-node should not have an exit node")
	assert.Contains(t, exitNodeInfo.ExitClients, "client1", "exit-node should have client1 as exit client")

	// Verify client info
	assert.False(t, clientInfo.AllowsExitTraffic, "client1 should not allow exit traffic")
	assert.Equal(t, "exit-node", clientInfo.ExitNode, "client1 should use exit-node")
	assert.Empty(t, clientInfo.ExitClients, "client1 should not have exit clients")
}

func TestAdminOverview_ConnectionTypes(t *testing.T) {
	cfg := &config.ServerConfig{
		Listen:    ":0",
		AuthToken: "test-token",
		Admin: config.AdminConfig{
			Enabled: true,
		},
		JoinMesh: &config.PeerConfig{
			Name: "test-coord",
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, srv.adminMux, "adminMux should be created when JoinMesh is configured")

	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Register a peer
	client := NewClient(ts.URL, "test-token")
	_, err = client.Register("peer1", "SHA256:peer1key", []string{"1.2.3.4"}, nil, 2222, 0, false, "v1.0.0", nil, "", false, nil)
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
	// Create a server with JoinMesh configured to enable adminMux
	cfg := &config.ServerConfig{
		Listen:    ":0",
		AuthToken: "test-token",
		Admin: config.AdminConfig{
			Enabled: true,
		},
		JoinMesh: &config.PeerConfig{
			Name: "test-coord",
		},
	}
	srv, err := NewServer(cfg)
	require.NoError(t, err)
	require.NotNil(t, srv.adminMux, "adminMux should be created when JoinMesh is configured")

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

func TestDownsampleHistory_Uniformity(t *testing.T) {
	// Test that sampling is roughly uniform
	now := time.Now()
	data := make([]StatsDataPoint, 100)
	for i := 0; i < 100; i++ {
		data[i] = StatsDataPoint{
			Timestamp:     now.Add(time.Duration(i) * time.Second),
			BytesSentRate: float64(i),
		}
	}

	result := downsampleHistory(data, 10)
	if len(result) != 10 {
		t.Fatalf("expected 10 points, got %d", len(result))
	}

	// Check that indices are roughly evenly spaced
	// With 100 points downsampled to 10, expect indices 0, 11, 22, 33, ... 99
	expectedStep := float64(99) / float64(9) // ~11
	for i := 0; i < len(result); i++ {
		expectedValue := float64(int(float64(i) * expectedStep))
		if result[i].BytesSentRate != expectedValue {
			t.Errorf("point %d: got %f, expected %f", i, result[i].BytesSentRate, expectedValue)
		}
	}
}
