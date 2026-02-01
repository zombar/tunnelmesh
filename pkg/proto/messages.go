// Package proto defines shared protocol messages for tunnelmesh.
package proto

import (
	"net"
	"time"
)

// Peer represents a node in the mesh network.
type Peer struct {
	Name        string    `json:"name"`
	PublicKey   string    `json:"public_key"`  // SSH public key (base64 encoded wire format)
	PublicIPs   []string  `json:"public_ips"`  // Externally reachable IPs
	PrivateIPs  []string  `json:"private_ips"` // Internal network IPs
	SSHPort     int       `json:"ssh_port"`    // SSH server port
	MeshIP      string    `json:"mesh_ip"`     // Assigned mesh network IP (10.99.x.x)
	LastSeen    time.Time `json:"last_seen"`   // Last heartbeat time
	Connectable bool      `json:"connectable"` // Can accept incoming connections
}

// RegisterRequest is sent by a peer to join the mesh.
type RegisterRequest struct {
	Name       string   `json:"name"`
	PublicKey  string   `json:"public_key"`
	PublicIPs  []string `json:"public_ips"`
	PrivateIPs []string `json:"private_ips"`
	SSHPort    int      `json:"ssh_port"`
}

// RegisterResponse is returned after successful registration.
type RegisterResponse struct {
	MeshIP   string `json:"mesh_ip"`   // Assigned mesh IP address
	MeshCIDR string `json:"mesh_cidr"` // Full mesh CIDR for routing
	Domain   string `json:"domain"`    // Domain suffix (e.g., ".mesh")
}

// PeerStats contains traffic statistics reported by peers.
type PeerStats struct {
	PacketsSent     uint64 `json:"packets_sent"`
	PacketsReceived uint64 `json:"packets_received"`
	BytesSent       uint64 `json:"bytes_sent"`
	BytesReceived   uint64 `json:"bytes_received"`
	DroppedNoRoute  uint64 `json:"dropped_no_route"`
	DroppedNoTunnel uint64 `json:"dropped_no_tunnel"`
	Errors          uint64 `json:"errors"`
	ActiveTunnels   int    `json:"active_tunnels"`
}

// HeartbeatRequest is sent periodically to maintain presence.
type HeartbeatRequest struct {
	Name      string     `json:"name"`
	PublicKey string     `json:"public_key"`
	Stats     *PeerStats `json:"stats,omitempty"`
}

// HeartbeatResponse is returned after successful heartbeat.
type HeartbeatResponse struct {
	OK bool `json:"ok"`
}

// PeerListResponse contains the list of all mesh peers.
type PeerListResponse struct {
	Peers []Peer `json:"peers"`
}

// DNSRecord represents a hostname to IP mapping.
type DNSRecord struct {
	Hostname string `json:"hostname"`
	MeshIP   string `json:"mesh_ip"`
}

// DNSUpdateNotification is sent when DNS records change.
type DNSUpdateNotification struct {
	Records []DNSRecord `json:"records"`
}

// ConnectionHint suggests how to connect to a peer.
type ConnectionHint struct {
	PeerName   string `json:"peer_name"`
	Strategy   string `json:"strategy"`    // "direct_public", "direct_private", "reverse"
	Address    string `json:"address"`     // IP:port to connect to
	RequireNAT bool   `json:"require_nat"` // True if NAT traversal needed
}

// ErrorResponse represents an API error.
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// GetLocalIPs returns the local IP addresses of the machine.
func GetLocalIPs() (public []string, private []string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil || ip.IsLoopback() {
				continue
			}

			// Only consider IPv4 for now
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}

			ipStr := ip4.String()
			if isPrivateIP(ip4) {
				private = append(private, ipStr)
			} else {
				public = append(public, ipStr)
			}
		}
	}

	return public, private
}

// isPrivateIP checks if an IP is in a private range.
func isPrivateIP(ip net.IP) bool {
	private := []struct {
		start net.IP
		end   net.IP
	}{
		{net.IPv4(10, 0, 0, 0), net.IPv4(10, 255, 255, 255)},
		{net.IPv4(172, 16, 0, 0), net.IPv4(172, 31, 255, 255)},
		{net.IPv4(192, 168, 0, 0), net.IPv4(192, 168, 255, 255)},
	}

	for _, r := range private {
		if bytesGreaterOrEqual(ip, r.start) && bytesGreaterOrEqual(r.end, ip) {
			return true
		}
	}
	return false
}

func bytesGreaterOrEqual(a, b net.IP) bool {
	a = a.To4()
	b = b.To4()
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < 4; i++ {
		if a[i] > b[i] {
			return true
		}
		if a[i] < b[i] {
			return false
		}
	}
	return true
}
