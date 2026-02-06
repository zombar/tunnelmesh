package proto

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"192.168.255.255", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"172.15.0.1", false}, // Just outside 172.16.0.0/12
		{"172.32.0.1", false}, // Just outside 172.16.0.0/12
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip).To4()
			assert.Equal(t, tt.private, isPrivateIP(ip))
		})
	}
}

func TestGetLocalIPs(t *testing.T) {
	public, private, behindNAT := GetLocalIPs()

	// At least we should have some IPs (likely private ones)
	// This test just verifies the function doesn't panic
	t.Logf("Public IPs: %v", public)
	t.Logf("Private IPs: %v", private)
	t.Logf("Behind NAT: %v", behindNAT)

	// Verify all returned IPs are valid
	for _, ip := range public {
		assert.NotNil(t, net.ParseIP(ip), "public IP should be valid: %s", ip)
	}
	for _, ip := range private {
		assert.NotNil(t, net.ParseIP(ip), "private IP should be valid: %s", ip)
	}
}

func TestBytesGreaterOrEqual(t *testing.T) {
	tests := []struct {
		a, b     string
		expected bool
	}{
		{"10.0.0.1", "10.0.0.0", true},
		{"10.0.0.0", "10.0.0.1", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"192.168.1.1", "10.0.0.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			a := net.ParseIP(tt.a)
			b := net.ParseIP(tt.b)
			assert.Equal(t, tt.expected, bytesGreaterOrEqual(a, b))
		})
	}
}

func TestGeoLocation_JSONSerialization(t *testing.T) {
	loc := GeoLocation{
		Latitude:  51.5074,
		Longitude: -0.1278,
		Accuracy:  50000,
		Source:    "ip",
		City:      "London",
		Region:    "England",
		Country:   "United Kingdom",
		UpdatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
	}

	// Test marshaling
	data, err := json.Marshal(loc)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded GeoLocation
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, loc.Latitude, decoded.Latitude)
	assert.Equal(t, loc.Longitude, decoded.Longitude)
	assert.Equal(t, loc.Accuracy, decoded.Accuracy)
	assert.Equal(t, loc.Source, decoded.Source)
	assert.Equal(t, loc.City, decoded.City)
	assert.Equal(t, loc.Region, decoded.Region)
	assert.Equal(t, loc.Country, decoded.Country)
}

func TestGeoLocation_OmitEmpty(t *testing.T) {
	// Empty location should serialize to minimal JSON (only zero-value time)
	loc := GeoLocation{}
	data, err := json.Marshal(loc)
	require.NoError(t, err)

	// Numeric zero values and empty strings should be omitted
	// Only time.Time zero value appears (Go's JSON encoder doesn't omit zero time)
	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)

	// Should not have latitude, longitude, accuracy, source, city, region, country
	_, hasLat := m["latitude"]
	_, hasLon := m["longitude"]
	_, hasAcc := m["accuracy"]
	_, hasSrc := m["source"]
	assert.False(t, hasLat, "latitude should be omitted")
	assert.False(t, hasLon, "longitude should be omitted")
	assert.False(t, hasAcc, "accuracy should be omitted")
	assert.False(t, hasSrc, "source should be omitted")
}

func TestGeoLocation_Validate(t *testing.T) {
	tests := []struct {
		name    string
		loc     GeoLocation
		wantErr bool
	}{
		{
			name: "valid manual location",
			loc: GeoLocation{
				Latitude:  40.7128,
				Longitude: -74.0060,
				Source:    "manual",
			},
			wantErr: false,
		},
		{
			name: "valid IP location",
			loc: GeoLocation{
				Latitude:  51.5074,
				Longitude: -0.1278,
				Accuracy:  50000,
				Source:    "ip",
				City:      "London",
			},
			wantErr: false,
		},
		{
			name: "latitude too high",
			loc: GeoLocation{
				Latitude:  91.0,
				Longitude: 0,
				Source:    "manual",
			},
			wantErr: true,
		},
		{
			name: "latitude too low",
			loc: GeoLocation{
				Latitude:  -91.0,
				Longitude: 0,
				Source:    "manual",
			},
			wantErr: true,
		},
		{
			name: "longitude too high",
			loc: GeoLocation{
				Latitude:  0,
				Longitude: 181.0,
				Source:    "manual",
			},
			wantErr: true,
		},
		{
			name: "longitude too low",
			loc: GeoLocation{
				Latitude:  0,
				Longitude: -181.0,
				Source:    "manual",
			},
			wantErr: true,
		},
		{
			name: "zero location is valid (null island)",
			loc: GeoLocation{
				Latitude:  0,
				Longitude: 0,
				Source:    "manual",
			},
			wantErr: false,
		},
		{
			name: "edge case: max valid values",
			loc: GeoLocation{
				Latitude:  90.0,
				Longitude: 180.0,
				Source:    "manual",
			},
			wantErr: false,
		},
		{
			name: "edge case: min valid values",
			loc: GeoLocation{
				Latitude:  -90.0,
				Longitude: -180.0,
				Source:    "manual",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.loc.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGeoLocation_IsSet(t *testing.T) {
	tests := []struct {
		name   string
		loc    GeoLocation
		expect bool
	}{
		{
			name:   "empty location",
			loc:    GeoLocation{},
			expect: false,
		},
		{
			name: "only latitude set",
			loc: GeoLocation{
				Latitude: 51.5074,
			},
			expect: false,
		},
		{
			name: "only longitude set",
			loc: GeoLocation{
				Longitude: -0.1278,
			},
			expect: false,
		},
		{
			name: "both set",
			loc: GeoLocation{
				Latitude:  51.5074,
				Longitude: -0.1278,
			},
			expect: true,
		},
		{
			name: "zero coordinates (null island) is valid",
			loc: GeoLocation{
				Latitude:  0,
				Longitude: 0,
				Source:    "manual", // Need source to distinguish from unset
			},
			expect: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, tt.loc.IsSet())
		})
	}
}

func TestPeer_WithLocation(t *testing.T) {
	peer := Peer{
		Name:   "test-peer",
		MeshIP: "10.99.0.1",
		Location: &GeoLocation{
			Latitude:  40.7128,
			Longitude: -74.0060,
			Source:    "manual",
		},
	}

	// Test JSON serialization includes location
	data, err := json.Marshal(peer)
	require.NoError(t, err)

	var decoded Peer
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.NotNil(t, decoded.Location)
	assert.Equal(t, 40.7128, decoded.Location.Latitude)
	assert.Equal(t, -74.0060, decoded.Location.Longitude)
	assert.Equal(t, "manual", decoded.Location.Source)
}

func TestRegisterRequest_WithLocation(t *testing.T) {
	req := RegisterRequest{
		Name:      "test-peer",
		PublicKey: "ssh-ed25519 AAAA...",
		SSHPort:   2222,
		Location: &GeoLocation{
			Latitude:  51.5074,
			Longitude: -0.1278,
			Source:    "manual",
		},
	}

	// Test JSON serialization includes location
	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded RegisterRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.NotNil(t, decoded.Location)
	assert.Equal(t, 51.5074, decoded.Location.Latitude)
	assert.Equal(t, -0.1278, decoded.Location.Longitude)
}

// Exit Node Feature Tests

func TestPeerStats_ConnectionsJSON(t *testing.T) {
	stats := PeerStats{
		PacketsSent:   100,
		ActiveTunnels: 3,
		Connections: map[string]string{
			"peer-a": "udp",
			"peer-b": "ssh",
			"peer-c": "relay",
		},
	}

	// Test marshaling
	data, err := json.Marshal(stats)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded PeerStats
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, stats.PacketsSent, decoded.PacketsSent)
	assert.Equal(t, stats.ActiveTunnels, decoded.ActiveTunnels)
	require.NotNil(t, decoded.Connections)
	assert.Len(t, decoded.Connections, 3)
	assert.Equal(t, "udp", decoded.Connections["peer-a"])
	assert.Equal(t, "ssh", decoded.Connections["peer-b"])
	assert.Equal(t, "relay", decoded.Connections["peer-c"])
}

func TestPeerStats_ConnectionsOmitEmpty(t *testing.T) {
	// Empty connections should be omitted from JSON
	stats := PeerStats{
		PacketsSent:   50,
		ActiveTunnels: 1,
	}

	data, err := json.Marshal(stats)
	require.NoError(t, err)

	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)

	_, hasConnections := m["connections"]
	assert.False(t, hasConnections, "connections should be omitted when nil")
}

func TestPeer_ExitNodeFields(t *testing.T) {
	peer := Peer{
		Name:              "exit-node-1",
		MeshIP:            "10.99.0.5",
		AllowsExitTraffic: true,
	}

	// Test marshaling
	data, err := json.Marshal(peer)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded Peer
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "exit-node-1", decoded.Name)
	assert.True(t, decoded.AllowsExitTraffic)
}

func TestPeer_UsingExitNode(t *testing.T) {
	peer := Peer{
		Name:     "client-1",
		MeshIP:   "10.99.0.10",
		ExitNode: "exit-node-1",
	}

	// Test marshaling
	data, err := json.Marshal(peer)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded Peer
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "client-1", decoded.Name)
	assert.Equal(t, "exit-node-1", decoded.ExitNode)
}

func TestPeer_ExitFieldsOmitEmpty(t *testing.T) {
	// Exit fields should be omitted when false/empty
	peer := Peer{
		Name:   "regular-peer",
		MeshIP: "10.99.0.1",
	}

	data, err := json.Marshal(peer)
	require.NoError(t, err)

	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)

	_, hasAllowsExit := m["allows_exit_traffic"]
	_, hasExitNode := m["exit_node"]
	assert.False(t, hasAllowsExit, "allows_exit_traffic should be omitted when false")
	assert.False(t, hasExitNode, "exit_node should be omitted when empty")
}

func TestRegisterRequest_ExitNodeFields(t *testing.T) {
	req := RegisterRequest{
		Name:              "exit-node",
		PublicKey:         "ssh-ed25519 AAAA...",
		SSHPort:           2222,
		AllowsExitTraffic: true,
	}

	// Test marshaling
	data, err := json.Marshal(req)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded RegisterRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "exit-node", decoded.Name)
	assert.True(t, decoded.AllowsExitTraffic)
}

func TestRegisterRequest_UsingExitNode(t *testing.T) {
	req := RegisterRequest{
		Name:      "client",
		PublicKey: "ssh-ed25519 BBBB...",
		SSHPort:   2222,
		ExitNode:  "exit-node",
	}

	// Test marshaling
	data, err := json.Marshal(req)
	require.NoError(t, err)

	// Test unmarshaling
	var decoded RegisterRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "client", decoded.Name)
	assert.Equal(t, "exit-node", decoded.ExitNode)
}
