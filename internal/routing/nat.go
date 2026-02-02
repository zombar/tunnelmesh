// Package routing provides packet routing and NAT functionality for the mesh network.
package routing

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// NATPortMin is the minimum NAT port to use
	NATPortMin = 10000
	// NATPortMax is the maximum NAT port to use
	NATPortMax = 65535
	// NATTimeout is the default timeout for NAT entries
	NATTimeout = 5 * time.Minute
	// NATCleanupInterval is how often to clean up stale entries
	NATCleanupInterval = 30 * time.Second
)

// NATEntry tracks a single NAT translation.
type NATEntry struct {
	OriginalSrcIP   net.IP
	OriginalSrcPort uint16
	OriginalDstIP   net.IP
	OriginalDstPort uint16
	Protocol        uint8 // TCP=6, UDP=17, ICMP=1
	NATPort         uint16
	PeerName        string    // Peer that sent this traffic
	LastSeen        time.Time
	Created         time.Time
}

// NATTable manages NAT translations for exit node traffic.
type NATTable struct {
	mu sync.RWMutex

	// Keyed by NAT port for reverse lookups (internet -> peer)
	byNATPort map[uint16]*NATEntry

	// Keyed by original connection for forward lookups (peer -> internet)
	// Key format: "proto:srcIP:srcPort:dstIP:dstPort"
	byOriginal map[string]*NATEntry

	nextPort uint16
	timeout  time.Duration
	exitIP   net.IP // The exit node's public IP for SNAT

	// Stats
	entriesCreated uint64
	entriesExpired uint64
}

// NewNATTable creates a new NAT table.
func NewNATTable(exitIP net.IP, timeout time.Duration) *NATTable {
	if timeout == 0 {
		timeout = NATTimeout
	}

	return &NATTable{
		byNATPort:  make(map[uint16]*NATEntry),
		byOriginal: make(map[string]*NATEntry),
		nextPort:   NATPortMin,
		timeout:    timeout,
		exitIP:     exitIP,
	}
}

// SetExitIP updates the exit node's public IP address.
func (n *NATTable) SetExitIP(ip net.IP) {
	n.mu.Lock()
	n.exitIP = ip
	n.mu.Unlock()
}

// allocatePort finds an available NAT port.
func (n *NATTable) allocatePort() (uint16, error) {
	startPort := n.nextPort
	for {
		port := n.nextPort
		n.nextPort++
		if n.nextPort > NATPortMax {
			n.nextPort = NATPortMin
		}

		if _, exists := n.byNATPort[port]; !exists {
			return port, nil
		}

		// Wrapped around without finding a port
		if n.nextPort == startPort {
			return 0, fmt.Errorf("no available NAT ports")
		}
	}
}

// connectionKey generates a key for the byOriginal map.
func connectionKey(proto uint8, srcIP net.IP, srcPort uint16, dstIP net.IP, dstPort uint16) string {
	return fmt.Sprintf("%d:%s:%d:%s:%d", proto, srcIP.String(), srcPort, dstIP.String(), dstPort)
}

// TranslateOutbound performs SNAT on an outgoing packet.
// Returns the translated packet with source IP/port rewritten.
func (n *NATTable) TranslateOutbound(packet []byte, peerName string) ([]byte, error) {
	if len(packet) < 20 {
		return nil, fmt.Errorf("packet too short")
	}

	// Parse IPv4 header
	info, err := ParseIPv4Packet(packet)
	if err != nil {
		return nil, fmt.Errorf("parse packet: %w", err)
	}

	// Get or create NAT entry
	n.mu.Lock()
	defer n.mu.Unlock()

	var srcPort, dstPort uint16
	var protoOffset int

	switch info.Protocol {
	case 6: // TCP
		if len(packet) < int(info.HeaderLen)+4 {
			return nil, fmt.Errorf("TCP packet too short")
		}
		protoOffset = int(info.HeaderLen)
		srcPort = binary.BigEndian.Uint16(packet[protoOffset : protoOffset+2])
		dstPort = binary.BigEndian.Uint16(packet[protoOffset+2 : protoOffset+4])

	case 17: // UDP
		if len(packet) < int(info.HeaderLen)+4 {
			return nil, fmt.Errorf("UDP packet too short")
		}
		protoOffset = int(info.HeaderLen)
		srcPort = binary.BigEndian.Uint16(packet[protoOffset : protoOffset+2])
		dstPort = binary.BigEndian.Uint16(packet[protoOffset+2 : protoOffset+4])

	case 1: // ICMP
		// ICMP uses ID field instead of ports
		if len(packet) < int(info.HeaderLen)+8 {
			return nil, fmt.Errorf("ICMP packet too short")
		}
		protoOffset = int(info.HeaderLen)
		// ICMP ID is at offset 4-5 within ICMP header
		srcPort = binary.BigEndian.Uint16(packet[protoOffset+4 : protoOffset+6])
		dstPort = 0

	default:
		return nil, fmt.Errorf("unsupported protocol: %d", info.Protocol)
	}

	key := connectionKey(info.Protocol, info.SrcIP, srcPort, info.DstIP, dstPort)
	entry, exists := n.byOriginal[key]

	if !exists {
		// Create new NAT entry
		natPort, err := n.allocatePort()
		if err != nil {
			return nil, err
		}

		entry = &NATEntry{
			OriginalSrcIP:   info.SrcIP,
			OriginalSrcPort: srcPort,
			OriginalDstIP:   info.DstIP,
			OriginalDstPort: dstPort,
			Protocol:        info.Protocol,
			NATPort:         natPort,
			PeerName:        peerName,
			LastSeen:        time.Now(),
			Created:         time.Now(),
		}

		n.byOriginal[key] = entry
		n.byNATPort[natPort] = entry
		n.entriesCreated++

		log.Debug().
			Str("peer", peerName).
			Str("src", fmt.Sprintf("%s:%d", info.SrcIP, srcPort)).
			Str("dst", fmt.Sprintf("%s:%d", info.DstIP, dstPort)).
			Uint16("nat_port", natPort).
			Msg("created NAT entry")
	} else {
		entry.LastSeen = time.Now()
	}

	// Create translated packet
	translated := make([]byte, len(packet))
	copy(translated, packet)

	// Rewrite source IP to exit node IP
	if n.exitIP != nil {
		copy(translated[12:16], n.exitIP.To4())
	}

	// Rewrite source port to NAT port
	switch info.Protocol {
	case 6, 17: // TCP, UDP
		binary.BigEndian.PutUint16(translated[protoOffset:protoOffset+2], entry.NATPort)
	case 1: // ICMP
		binary.BigEndian.PutUint16(translated[protoOffset+4:protoOffset+6], entry.NATPort)
	}

	// Recalculate IP header checksum
	recalculateIPChecksum(translated)

	// Recalculate transport layer checksum
	switch info.Protocol {
	case 6: // TCP
		recalculateTCPChecksum(translated, int(info.HeaderLen))
	case 17: // UDP
		recalculateUDPChecksum(translated, int(info.HeaderLen))
	case 1: // ICMP
		recalculateICMPChecksum(translated, int(info.HeaderLen))
	}

	return translated, nil
}

// TranslateInbound performs reverse NAT on an incoming response packet.
// Returns the peer name and the translated packet with destination restored.
func (n *NATTable) TranslateInbound(packet []byte) (string, []byte, error) {
	if len(packet) < 20 {
		return "", nil, fmt.Errorf("packet too short")
	}

	// Parse IPv4 header
	info, err := ParseIPv4Packet(packet)
	if err != nil {
		return "", nil, fmt.Errorf("parse packet: %w", err)
	}

	var dstPort uint16
	var protoOffset int

	switch info.Protocol {
	case 6, 17: // TCP, UDP
		if len(packet) < int(info.HeaderLen)+4 {
			return "", nil, fmt.Errorf("packet too short for protocol")
		}
		protoOffset = int(info.HeaderLen)
		dstPort = binary.BigEndian.Uint16(packet[protoOffset+2 : protoOffset+4])

	case 1: // ICMP
		if len(packet) < int(info.HeaderLen)+8 {
			return "", nil, fmt.Errorf("ICMP packet too short")
		}
		protoOffset = int(info.HeaderLen)
		// ICMP ID is at offset 4-5
		dstPort = binary.BigEndian.Uint16(packet[protoOffset+4 : protoOffset+6])

	default:
		return "", nil, fmt.Errorf("unsupported protocol: %d", info.Protocol)
	}

	// Look up NAT entry by destination port
	n.mu.RLock()
	entry, exists := n.byNATPort[dstPort]
	if exists {
		entry.LastSeen = time.Now()
	}
	n.mu.RUnlock()

	if !exists {
		return "", nil, fmt.Errorf("no NAT entry for port %d", dstPort)
	}

	// Create translated packet
	translated := make([]byte, len(packet))
	copy(translated, packet)

	// Rewrite destination IP to original source IP
	copy(translated[16:20], entry.OriginalSrcIP.To4())

	// Rewrite destination port to original source port
	switch info.Protocol {
	case 6, 17: // TCP, UDP
		binary.BigEndian.PutUint16(translated[protoOffset+2:protoOffset+4], entry.OriginalSrcPort)
	case 1: // ICMP
		binary.BigEndian.PutUint16(translated[protoOffset+4:protoOffset+6], entry.OriginalSrcPort)
	}

	// Recalculate checksums
	recalculateIPChecksum(translated)
	switch info.Protocol {
	case 6:
		recalculateTCPChecksum(translated, int(info.HeaderLen))
	case 17:
		recalculateUDPChecksum(translated, int(info.HeaderLen))
	case 1:
		recalculateICMPChecksum(translated, int(info.HeaderLen))
	}

	return entry.PeerName, translated, nil
}

// Cleanup removes stale NAT entries.
func (n *NATTable) Cleanup() int {
	n.mu.Lock()
	defer n.mu.Unlock()

	now := time.Now()
	removed := 0

	for key, entry := range n.byOriginal {
		if now.Sub(entry.LastSeen) > n.timeout {
			delete(n.byOriginal, key)
			delete(n.byNATPort, entry.NATPort)
			removed++
			n.entriesExpired++
		}
	}

	if removed > 0 {
		log.Debug().
			Int("removed", removed).
			Int("remaining", len(n.byNATPort)).
			Msg("cleaned up NAT entries")
	}

	return removed
}

// Stats returns NAT table statistics.
func (n *NATTable) Stats() (entries int, created, expired uint64) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.byNATPort), n.entriesCreated, n.entriesExpired
}

// recalculateIPChecksum recalculates the IPv4 header checksum.
func recalculateIPChecksum(packet []byte) {
	// Zero out the checksum field
	packet[10] = 0
	packet[11] = 0

	// Calculate header length
	ihl := int(packet[0]&0x0F) * 4

	// Calculate checksum
	var sum uint32
	for i := 0; i < ihl; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[i : i+2]))
	}

	// Fold 32-bit sum to 16 bits
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}

	// Write one's complement
	binary.BigEndian.PutUint16(packet[10:12], ^uint16(sum))
}

// recalculateTCPChecksum recalculates the TCP checksum.
func recalculateTCPChecksum(packet []byte, ipHeaderLen int) {
	tcpData := packet[ipHeaderLen:]
	if len(tcpData) < 20 {
		return
	}

	// Zero out the checksum
	tcpData[16] = 0
	tcpData[17] = 0

	// Pseudo header: src IP, dst IP, zero, protocol, TCP length
	srcIP := packet[12:16]
	dstIP := packet[16:20]
	tcpLen := len(tcpData)

	var sum uint32
	sum += uint32(srcIP[0])<<8 | uint32(srcIP[1])
	sum += uint32(srcIP[2])<<8 | uint32(srcIP[3])
	sum += uint32(dstIP[0])<<8 | uint32(dstIP[1])
	sum += uint32(dstIP[2])<<8 | uint32(dstIP[3])
	sum += 6 // TCP protocol
	sum += uint32(tcpLen)

	// TCP data
	for i := 0; i < len(tcpData)-1; i += 2 {
		sum += uint32(tcpData[i])<<8 | uint32(tcpData[i+1])
	}
	if len(tcpData)%2 == 1 {
		sum += uint32(tcpData[len(tcpData)-1]) << 8
	}

	// Fold and complement
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}

	binary.BigEndian.PutUint16(tcpData[16:18], ^uint16(sum))
}

// recalculateUDPChecksum recalculates the UDP checksum.
func recalculateUDPChecksum(packet []byte, ipHeaderLen int) {
	udpData := packet[ipHeaderLen:]
	if len(udpData) < 8 {
		return
	}

	// Zero out the checksum
	udpData[6] = 0
	udpData[7] = 0

	// Pseudo header
	srcIP := packet[12:16]
	dstIP := packet[16:20]
	udpLen := len(udpData)

	var sum uint32
	sum += uint32(srcIP[0])<<8 | uint32(srcIP[1])
	sum += uint32(srcIP[2])<<8 | uint32(srcIP[3])
	sum += uint32(dstIP[0])<<8 | uint32(dstIP[1])
	sum += uint32(dstIP[2])<<8 | uint32(dstIP[3])
	sum += 17 // UDP protocol
	sum += uint32(udpLen)

	// UDP data
	for i := 0; i < len(udpData)-1; i += 2 {
		sum += uint32(udpData[i])<<8 | uint32(udpData[i+1])
	}
	if len(udpData)%2 == 1 {
		sum += uint32(udpData[len(udpData)-1]) << 8
	}

	// Fold and complement
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}

	checksum := ^uint16(sum)
	if checksum == 0 {
		checksum = 0xFFFF // UDP checksum of 0 means no checksum
	}
	binary.BigEndian.PutUint16(udpData[6:8], checksum)
}

// recalculateICMPChecksum recalculates the ICMP checksum.
func recalculateICMPChecksum(packet []byte, ipHeaderLen int) {
	icmpData := packet[ipHeaderLen:]
	if len(icmpData) < 8 {
		return
	}

	// Zero out the checksum
	icmpData[2] = 0
	icmpData[3] = 0

	var sum uint32
	for i := 0; i < len(icmpData)-1; i += 2 {
		sum += uint32(icmpData[i])<<8 | uint32(icmpData[i+1])
	}
	if len(icmpData)%2 == 1 {
		sum += uint32(icmpData[len(icmpData)-1]) << 8
	}

	// Fold and complement
	for sum > 0xFFFF {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}

	binary.BigEndian.PutUint16(icmpData[2:4], ^uint16(sum))
}
