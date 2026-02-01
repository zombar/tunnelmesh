// Package routing handles packet routing for the mesh network.
package routing

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// Protocol constants for IP packets.
const (
	ProtoICMP = 1
	ProtoTCP  = 6
	ProtoUDP  = 17
)

// Router manages routes from mesh IPs to peer IDs.
type Router struct {
	mu     sync.RWMutex
	routes map[string]string // IP string -> peer ID
}

// NewRouter creates a new Router.
func NewRouter() *Router {
	return &Router{
		routes: make(map[string]string),
	}
}

// AddRoute adds a route for an IP to a peer.
func (r *Router) AddRoute(ip string, peerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[ip] = peerID
}

// RemoveRoute removes a route for an IP.
func (r *Router) RemoveRoute(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.routes, ip)
}

// Lookup finds the peer ID for a destination IP.
func (r *Router) Lookup(ip net.IP) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	peerID, ok := r.routes[ip.String()]
	return peerID, ok
}

// Count returns the number of routes.
func (r *Router) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// UpdateRoutes replaces all routes with a new set.
func (r *Router) UpdateRoutes(routes map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = make(map[string]string)
	for ip, peerID := range routes {
		r.routes[ip] = peerID
	}
}

// ListRoutes returns a copy of all routes.
func (r *Router) ListRoutes() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]string)
	for ip, peerID := range r.routes {
		result[ip] = peerID
	}
	return result
}

// PacketInfo contains parsed information from an IPv4 packet.
type PacketInfo struct {
	SrcIP    net.IP
	DstIP    net.IP
	Protocol uint8
	Payload  []byte
}

// BuildIPv4Packet constructs a minimal IPv4 packet.
func BuildIPv4Packet(src, dst net.IP, proto uint8, payload []byte) []byte {
	totalLen := 20 + len(payload)
	packet := make([]byte, totalLen)

	// Version (4) and IHL (5 = 20 bytes header)
	packet[0] = 0x45

	// Type of Service
	packet[1] = 0

	// Total Length
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen))

	// Identification
	binary.BigEndian.PutUint16(packet[4:6], 0)

	// Flags and Fragment Offset
	binary.BigEndian.PutUint16(packet[6:8], 0)

	// TTL
	packet[8] = 64

	// Protocol
	packet[9] = proto

	// Header Checksum (set to 0 for calculation)
	packet[10] = 0
	packet[11] = 0

	// Source IP
	copy(packet[12:16], src.To4())

	// Destination IP
	copy(packet[16:20], dst.To4())

	// Calculate and set checksum
	checksum := CalculateIPv4Checksum(packet[:20])
	packet[10] = byte(checksum >> 8)
	packet[11] = byte(checksum)

	// Payload
	copy(packet[20:], payload)

	return packet
}

// ParseIPv4Packet parses an IPv4 packet and returns its components.
func ParseIPv4Packet(packet []byte) (*PacketInfo, error) {
	if len(packet) < 20 {
		return nil, fmt.Errorf("packet too short: %d bytes", len(packet))
	}

	version := packet[0] >> 4
	if version != 4 {
		return nil, fmt.Errorf("not an IPv4 packet: version %d", version)
	}

	ihl := int(packet[0]&0x0F) * 4
	if ihl < 20 || ihl > len(packet) {
		return nil, fmt.Errorf("invalid header length: %d", ihl)
	}

	info := &PacketInfo{
		SrcIP:    net.IP(packet[12:16]),
		DstIP:    net.IP(packet[16:20]),
		Protocol: packet[9],
	}

	if len(packet) > ihl {
		info.Payload = packet[ihl:]
	}

	return info, nil
}

// CalculateIPv4Checksum calculates the IPv4 header checksum.
func CalculateIPv4Checksum(header []byte) uint16 {
	var sum uint32

	for i := 0; i < len(header)-1; i += 2 {
		sum += uint32(header[i])<<8 | uint32(header[i+1])
	}

	// Handle odd length
	if len(header)%2 == 1 {
		sum += uint32(header[len(header)-1]) << 8
	}

	// Fold 32-bit sum to 16 bits
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}

	return ^uint16(sum)
}

// PacketBuffer is a reusable buffer for packet data.
type PacketBuffer struct {
	data   []byte
	length int
}

// NewPacketBuffer creates a new PacketBuffer with the given capacity.
func NewPacketBuffer(capacity int) *PacketBuffer {
	return &PacketBuffer{
		data: make([]byte, capacity),
	}
}

// Data returns the underlying buffer.
func (b *PacketBuffer) Data() []byte {
	return b.data
}

// Bytes returns the valid portion of the buffer.
func (b *PacketBuffer) Bytes() []byte {
	return b.data[:b.length]
}

// Length returns the current length of valid data.
func (b *PacketBuffer) Length() int {
	return b.length
}

// SetLength sets the length of valid data.
func (b *PacketBuffer) SetLength(n int) {
	b.length = n
}

// Reset clears the buffer.
func (b *PacketBuffer) Reset() {
	b.length = 0
}

// PacketBufferPool is a pool of reusable packet buffers.
type PacketBufferPool struct {
	pool     sync.Pool
	capacity int
}

// NewPacketBufferPool creates a new buffer pool.
func NewPacketBufferPool(capacity int) *PacketBufferPool {
	return &PacketBufferPool{
		capacity: capacity,
		pool: sync.Pool{
			New: func() interface{} {
				return NewPacketBuffer(capacity)
			},
		},
	}
}

// Get retrieves a buffer from the pool.
func (p *PacketBufferPool) Get() *PacketBuffer {
	buf := p.pool.Get().(*PacketBuffer)
	buf.Reset()
	return buf
}

// Put returns a buffer to the pool.
func (p *PacketBufferPool) Put(buf *PacketBuffer) {
	p.pool.Put(buf)
}
