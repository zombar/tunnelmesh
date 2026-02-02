// Package routing handles packet routing for the mesh network.
package routing

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
)

// Protocol constants for IP packets.
const (
	ProtoICMP = 1
	ProtoTCP  = 6
	ProtoUDP  = 17
)

// ipv4Key is a fixed-size key for IPv4 addresses (avoids string allocation).
type ipv4Key [4]byte

// Router manages routes from mesh IPs to peer IDs.
// Uses copy-on-write for lock-free reads in the hot path.
type Router struct {
	routes atomic.Pointer[map[ipv4Key]string] // Lock-free reads
	mu     sync.Mutex                         // Serializes writes only
}

// NewRouter creates a new Router.
func NewRouter() *Router {
	r := &Router{}
	routes := make(map[ipv4Key]string)
	r.routes.Store(&routes)
	return r
}

// parseIPv4Key converts an IP string to a binary key.
func parseIPv4Key(ip string) (ipv4Key, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ipv4Key{}, false
	}
	v4 := parsed.To4()
	if v4 == nil {
		return ipv4Key{}, false
	}
	var key ipv4Key
	copy(key[:], v4)
	return key, true
}

// netIPToKey converts a net.IP to a binary key.
func netIPToKey(ip net.IP) ipv4Key {
	var key ipv4Key
	v4 := ip.To4()
	if v4 != nil {
		copy(key[:], v4)
	}
	return key
}

// AddRoute adds a route for an IP to a peer.
// Uses copy-on-write: creates a new map with the addition.
func (r *Router) AddRoute(ip string, peerID string) {
	key, ok := parseIPv4Key(ip)
	if !ok {
		return // Invalid IP, silently ignore
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Copy-on-write: create new map with the addition
	oldRoutes := r.routes.Load()
	newRoutes := make(map[ipv4Key]string, len(*oldRoutes)+1)
	for k, v := range *oldRoutes {
		newRoutes[k] = v
	}
	newRoutes[key] = peerID
	r.routes.Store(&newRoutes)
}

// RemoveRoute removes a route for an IP.
// Uses copy-on-write: creates a new map without the route.
func (r *Router) RemoveRoute(ip string) {
	key, ok := parseIPv4Key(ip)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	oldRoutes := r.routes.Load()
	if _, exists := (*oldRoutes)[key]; !exists {
		return // Nothing to remove
	}

	// Copy-on-write: create new map without the route
	newRoutes := make(map[ipv4Key]string, len(*oldRoutes))
	for k, v := range *oldRoutes {
		if k != key {
			newRoutes[k] = v
		}
	}
	r.routes.Store(&newRoutes)
}

// Lookup finds the peer ID for a destination IP.
// Lock-free: uses atomic load for maximum throughput in hot path.
func (r *Router) Lookup(ip net.IP) (string, bool) {
	key := netIPToKey(ip)
	routes := r.routes.Load()
	peerID, ok := (*routes)[key]
	return peerID, ok
}

// Count returns the number of routes.
// Lock-free: uses atomic load.
func (r *Router) Count() int {
	routes := r.routes.Load()
	return len(*routes)
}

// UpdateRoutes replaces all routes with a new set.
// Atomically swaps in the new routing table.
func (r *Router) UpdateRoutes(routes map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	newRoutes := make(map[ipv4Key]string, len(routes))
	for ip, peerID := range routes {
		if key, ok := parseIPv4Key(ip); ok {
			newRoutes[key] = peerID
		}
	}
	r.routes.Store(&newRoutes)
}

// ListRoutes returns a copy of all routes.
// Lock-free: uses atomic load.
func (r *Router) ListRoutes() map[string]string {
	routes := r.routes.Load()
	result := make(map[string]string, len(*routes))
	for key, peerID := range *routes {
		ip := net.IP(key[:]).String()
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

// ZeroCopyBuffer is a buffer that reserves space for frame headers,
// enabling zero-copy packet forwarding by writing the packet data
// after the reserved header space.
type ZeroCopyBuffer struct {
	data         []byte
	headerOffset int // Start of reserved header space
	dataOffset   int // Start of packet data (after header)
	length       int // Length of valid packet data
}

// NewZeroCopyBuffer creates a new ZeroCopyBuffer with space for both
// the frame header and packet data.
func NewZeroCopyBuffer(headerSize, capacity int) *ZeroCopyBuffer {
	return &ZeroCopyBuffer{
		data:         make([]byte, headerSize+capacity),
		headerOffset: 0,
		dataOffset:   headerSize,
		length:       0,
	}
}

// DataSlice returns the slice where packet data should be written.
// This starts after the reserved header space.
func (b *ZeroCopyBuffer) DataSlice() []byte {
	return b.data[b.dataOffset:]
}

// SetLength sets the length of valid packet data.
func (b *ZeroCopyBuffer) SetLength(n int) {
	b.length = n
}

// Frame writes the frame header and returns the complete frame
// (header + packet data) ready for transmission. This is zero-copy
// because the packet data was already written at the correct offset.
func (b *ZeroCopyBuffer) Frame(packetLen int) []byte {
	// Write frame header directly before the packet data
	// Frame format: [2 bytes length][1 byte proto][payload]
	frameLen := packetLen + 1 // +1 for protocol byte
	b.data[0] = byte(frameLen >> 8)
	b.data[1] = byte(frameLen)
	b.data[2] = 0x01 // IP packet protocol marker

	return b.data[:b.dataOffset+packetLen]
}

// PacketData returns just the packet data portion (without header).
func (b *ZeroCopyBuffer) PacketData() []byte {
	return b.data[b.dataOffset : b.dataOffset+b.length]
}

// Reset clears the buffer.
func (b *ZeroCopyBuffer) Reset() {
	b.length = 0
}

// ZeroCopyBufferPool is a pool of reusable zero-copy buffers.
type ZeroCopyBufferPool struct {
	pool       sync.Pool
	headerSize int
	capacity   int
}

// NewZeroCopyBufferPool creates a new zero-copy buffer pool.
func NewZeroCopyBufferPool(headerSize, capacity int) *ZeroCopyBufferPool {
	return &ZeroCopyBufferPool{
		headerSize: headerSize,
		capacity:   capacity,
		pool: sync.Pool{
			New: func() interface{} {
				return NewZeroCopyBuffer(headerSize, capacity)
			},
		},
	}
}

// Get retrieves a buffer from the pool.
func (p *ZeroCopyBufferPool) Get() *ZeroCopyBuffer {
	buf := p.pool.Get().(*ZeroCopyBuffer)
	buf.Reset()
	return buf
}

// Put returns a buffer to the pool.
func (p *ZeroCopyBufferPool) Put(buf *ZeroCopyBuffer) {
	p.pool.Put(buf)
}
