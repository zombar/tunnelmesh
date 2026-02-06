package wireguard

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

var (
	ErrPacketTooShort = errors.New("packet too short")
	ErrNotIPv4        = errors.New("not an IPv4 packet")
)

// IsWGClientIP checks if an IP is in the WireGuard client range.
// WG clients use the third octet 100-199 within the mesh CIDR.
func IsWGClientIP(ipStr string, meshNet *net.IPNet) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	// Check if in mesh network
	if !meshNet.Contains(ip) {
		return false
	}

	// WG client range is third octet 100-199
	thirdOctet := ip4[2]
	return thirdOctet >= 100 && thirdOctet <= 199
}

// ExtractDestIP extracts the destination IP from an IPv4 packet.
func ExtractDestIP(packet []byte) (string, error) {
	if len(packet) < 20 {
		return "", ErrPacketTooShort
	}

	// Check IPv4 version
	version := packet[0] >> 4
	if version != 4 {
		return "", ErrNotIPv4
	}

	// Destination IP is at bytes 16-19
	destIP := net.IPv4(packet[16], packet[17], packet[18], packet[19])
	return destIP.String(), nil
}

// ExtractSourceIP extracts the source IP from an IPv4 packet.
func ExtractSourceIP(packet []byte) (string, error) {
	if len(packet) < 20 {
		return "", ErrPacketTooShort
	}

	// Check IPv4 version
	version := packet[0] >> 4
	if version != 4 {
		return "", ErrNotIPv4
	}

	// Source IP is at bytes 12-15
	srcIP := net.IPv4(packet[12], packet[13], packet[14], packet[15])
	return srcIP.String(), nil
}

// Router handles packet routing between WireGuard clients and the mesh.
type Router struct {
	mu       sync.RWMutex
	meshCIDR string
	meshNet  *net.IPNet
	clients  map[string]*Client // IP -> client
}

// NewRouter creates a new router for WireGuard traffic.
func NewRouter(meshCIDR string) *Router {
	_, meshNet, _ := net.ParseCIDR(meshCIDR)
	return &Router{
		meshCIDR: meshCIDR,
		meshNet:  meshNet,
		clients:  make(map[string]*Client),
	}
}

// UpdateClients updates the list of WireGuard clients.
func (r *Router) UpdateClients(clients []Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.clients = make(map[string]*Client)
	for i := range clients {
		r.clients[clients[i].MeshIP] = &clients[i]
	}
}

// GetClientByIP returns the client for a given IP address.
func (r *Router) GetClientByIP(ip string) (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	client, ok := r.clients[ip]
	return client, ok
}

// IsWGClientIP checks if an IP is a WireGuard client IP.
func (r *Router) IsWGClientIP(ipStr string) bool {
	if r.meshNet == nil {
		return false
	}
	return IsWGClientIP(ipStr, r.meshNet)
}

// RouteDecision represents where a packet should be routed.
type RouteDecision int

const (
	RouteToMesh     RouteDecision = iota // Forward to mesh via tunnel/relay
	RouteToWGClient                      // Forward to WireGuard client
	RouteDrop                            // Drop the packet
)

// RoutePacket determines where an incoming packet should be routed.
// For packets from WG clients: route to mesh.
// For packets to WG client IPs: route to WG interface.
func (r *Router) RoutePacket(packet []byte, fromWG bool) (RouteDecision, string) {
	if fromWG {
		// Packet from WG client -> extract dest, route to mesh
		destIP, err := ExtractDestIP(packet)
		if err != nil {
			return RouteDrop, ""
		}
		return RouteToMesh, destIP
	}

	// Packet from mesh -> check if dest is WG client
	destIP, err := ExtractDestIP(packet)
	if err != nil {
		return RouteDrop, ""
	}

	if r.IsWGClientIP(destIP) {
		return RouteToWGClient, destIP
	}

	// Not a WG client IP, shouldn't have been sent here
	return RouteDrop, destIP
}

// GetWGClientRange returns the CIDR for WireGuard clients within the mesh.
func (r *Router) GetWGClientRange() string {
	if r.meshNet == nil {
		return ""
	}

	// WG clients use 100-199 in third octet
	// Return a summarized range for routing
	base := r.meshNet.IP.To4()
	if base == nil {
		return ""
	}

	// Return 10.99.100.0/17 which covers 100-127 and 10.99.128.0/18 for 128-191
	// Simplified: return two /8 blocks that cover the range
	return fmt.Sprintf("%d.%d.100.0/24", base[0], base[1])
}

// PacketHandler wraps a Router and Concentrator to implement packet handling.
// This is used by the forwarder to route packets to WireGuard clients.
type PacketHandler struct {
	router       *Router
	concentrator *Concentrator
}

// NewPacketHandler creates a new packet handler for WireGuard traffic.
func NewPacketHandler(router *Router, concentrator *Concentrator) *PacketHandler {
	return &PacketHandler{
		router:       router,
		concentrator: concentrator,
	}
}

// IsWGClientIP returns true if the IP is a WireGuard client IP.
func (h *PacketHandler) IsWGClientIP(ip string) bool {
	return h.router.IsWGClientIP(ip)
}

// SendPacket sends a packet to a WireGuard client.
func (h *PacketHandler) SendPacket(packet []byte) error {
	return h.concentrator.SendPacket(packet)
}
