package wireguard

import (
	"net"
	"testing"
)

func TestIsWGClientIP(t *testing.T) {
	_, meshNet, _ := net.ParseCIDR("172.30.0.0/16")

	tests := []struct {
		ip       string
		expected bool
	}{
		// WG client range: 172.30.100.0 - 172.30.199.255
		{"172.30.100.1", true},
		{"172.30.100.254", true},
		{"172.30.150.1", true},
		{"172.30.199.254", true},
		// Outside WG range but in mesh
		{"172.30.0.1", false},
		{"172.30.50.1", false},
		{"172.30.99.254", false},
		{"172.30.200.1", false},
		{"172.30.255.1", false},
		// Outside mesh entirely
		{"192.168.1.1", false},
		{"10.100.100.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := IsWGClientIP(tt.ip, meshNet)
			if result != tt.expected {
				t.Errorf("IsWGClientIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}

func TestExtractDestIP(t *testing.T) {
	tests := []struct {
		name    string
		packet  []byte
		want    string
		wantErr bool
	}{
		{
			name: "valid IPv4 packet",
			// IPv4 header: version=4, IHL=5, dest=172.30.100.1
			packet: []byte{
				0x45, 0x00, 0x00, 0x28, // version, IHL, TOS, total length
				0x00, 0x00, 0x00, 0x00, // id, flags, fragment offset
				0x40, 0x06, 0x00, 0x00, // TTL, protocol (TCP), checksum
				0xac, 0x1e, 0x00, 0x01, // source: 172.30.0.1
				0xac, 0x1e, 0x64, 0x01, // dest: 172.30.100.1
			},
			want:    "172.30.100.1",
			wantErr: false,
		},
		{
			name:    "too short",
			packet:  []byte{0x45, 0x00, 0x00},
			want:    "",
			wantErr: true,
		},
		{
			name: "IPv6 packet",
			// IPv6 header starts with 0x6X
			packet:  []byte{0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractDestIP(tt.packet)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractDestIP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractDestIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractSourceIP(t *testing.T) {
	tests := []struct {
		name    string
		packet  []byte
		want    string
		wantErr bool
	}{
		{
			name: "valid IPv4 packet",
			// IPv4 header: source=172.30.100.1, dest=172.30.0.1
			packet: []byte{
				0x45, 0x00, 0x00, 0x28,
				0x00, 0x00, 0x00, 0x00,
				0x40, 0x06, 0x00, 0x00,
				0xac, 0x1e, 0x64, 0x01, // source: 172.30.100.1
				0xac, 0x1e, 0x00, 0x01, // dest: 172.30.0.1
			},
			want:    "172.30.100.1",
			wantErr: false,
		},
		{
			name:    "too short",
			packet:  []byte{0x45, 0x00},
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractSourceIP(tt.packet)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractSourceIP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractSourceIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRouterClientLookup(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// Add clients
	clients := []Client{
		{ID: "1", Name: "iPhone", PublicKey: "key1", MeshIP: "172.30.100.1"},
		{ID: "2", Name: "Android", PublicKey: "key2", MeshIP: "172.30.100.2"},
	}
	router.UpdateClients(clients)

	// Lookup by IP
	client, ok := router.GetClientByIP("172.30.100.1")
	if !ok {
		t.Fatal("expected to find client for 172.30.100.1")
	}
	if client.Name != "iPhone" {
		t.Errorf("expected iPhone, got %s", client.Name)
	}

	// Lookup missing
	_, ok = router.GetClientByIP("172.30.100.99")
	if ok {
		t.Error("expected not to find client for 172.30.100.99")
	}
}

func TestRouterIsWGTraffic(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// WG client IP
	if !router.IsWGClientIP("172.30.100.1") {
		t.Error("172.30.100.1 should be WG client IP")
	}

	// Regular mesh IP
	if router.IsWGClientIP("172.30.0.1") {
		t.Error("172.30.0.1 should not be WG client IP")
	}
}

func TestRoutePacketFromWG(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// Create a packet from WG client (172.30.100.1) to mesh peer (172.30.0.1)
	packet := []byte{
		0x45, 0x00, 0x00, 0x28,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		0xac, 0x1e, 0x64, 0x01, // source: 172.30.100.1
		0xac, 0x1e, 0x00, 0x01, // dest: 172.30.0.1
	}

	decision, destIP := router.RoutePacket(packet, true) // fromWG = true
	if decision != RouteToMesh {
		t.Errorf("expected RouteToMesh, got %v", decision)
	}
	if destIP != "172.30.0.1" {
		t.Errorf("expected dest 172.30.0.1, got %s", destIP)
	}
}

func TestRoutePacketToWGClient(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// Create a packet from mesh peer (172.30.0.1) to WG client (172.30.100.1)
	packet := []byte{
		0x45, 0x00, 0x00, 0x28,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		0xac, 0x1e, 0x00, 0x01, // source: 172.30.0.1
		0xac, 0x1e, 0x64, 0x01, // dest: 172.30.100.1
	}

	decision, destIP := router.RoutePacket(packet, false) // fromWG = false
	if decision != RouteToWGClient {
		t.Errorf("expected RouteToWGClient, got %v", decision)
	}
	if destIP != "172.30.100.1" {
		t.Errorf("expected dest 172.30.100.1, got %s", destIP)
	}
}

func TestRoutePacketDropNonWG(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// Create a packet to non-WG destination (shouldn't have been sent here)
	packet := []byte{
		0x45, 0x00, 0x00, 0x28,
		0x00, 0x00, 0x00, 0x00,
		0x40, 0x06, 0x00, 0x00,
		0xac, 0x1e, 0x00, 0x01, // source: 172.30.0.1
		0xac, 0x1e, 0x00, 0x02, // dest: 172.30.0.2 (not a WG client)
	}

	decision, _ := router.RoutePacket(packet, false) // fromWG = false
	if decision != RouteDrop {
		t.Errorf("expected RouteDrop for non-WG destination, got %v", decision)
	}
}

func TestRoutePacketInvalidPacket(t *testing.T) {
	router := NewRouter("172.30.0.0/16")

	// Too short packet
	packet := []byte{0x45, 0x00, 0x00}

	decision, _ := router.RoutePacket(packet, false)
	if decision != RouteDrop {
		t.Errorf("expected RouteDrop for invalid packet, got %v", decision)
	}
}

func TestPacketHandlerIsWGClientIP(t *testing.T) {
	router := NewRouter("172.30.0.0/16")
	handler := NewPacketHandler(router, nil)

	tests := []struct {
		ip       string
		expected bool
	}{
		{"172.30.100.1", true},
		{"172.30.150.50", true},
		{"172.30.0.1", false},
		{"172.30.50.1", false},
		{"192.168.1.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := handler.IsWGClientIP(tt.ip)
			if result != tt.expected {
				t.Errorf("IsWGClientIP(%s) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}
