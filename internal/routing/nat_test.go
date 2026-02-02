package routing

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNATTable_NewNATTable(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	assert.NotNil(t, table)
	assert.Equal(t, uint16(NATPortMin), table.nextPort)
}

func TestNATTable_SetExitIP(t *testing.T) {
	table := NewNATTable(nil, 5*time.Minute)

	newIP := net.ParseIP("198.51.100.1")
	table.SetExitIP(newIP)

	table.mu.RLock()
	defer table.mu.RUnlock()
	assert.Equal(t, newIP, table.exitIP)
}

// buildTCPPacket creates a minimal TCP/IP packet for testing
func buildTCPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16) []byte {
	// Create 20-byte IP header + 20-byte TCP header (minimum)
	packet := make([]byte, 40)

	// IPv4 header (20 bytes)
	packet[0] = 0x45 // Version 4, IHL 5 (20 bytes)
	packet[1] = 0    // TOS
	binary.BigEndian.PutUint16(packet[2:4], 40)  // Total length
	binary.BigEndian.PutUint16(packet[4:6], 0)   // ID
	binary.BigEndian.PutUint16(packet[6:8], 0)   // Flags/Fragment
	packet[8] = 64                               // TTL
	packet[9] = 6                                // Protocol (TCP)
	packet[10] = 0                               // Checksum (will be calculated)
	packet[11] = 0
	copy(packet[12:16], srcIP.To4())
	copy(packet[16:20], dstIP.To4())

	// TCP header (20 bytes minimum)
	binary.BigEndian.PutUint16(packet[20:22], srcPort) // Source port
	binary.BigEndian.PutUint16(packet[22:24], dstPort) // Dest port
	binary.BigEndian.PutUint32(packet[24:28], 0)       // Sequence
	binary.BigEndian.PutUint32(packet[28:32], 0)       // Ack
	packet[32] = 5 << 4                                // Data offset (5 = 20 bytes)
	packet[33] = 0x02                                  // SYN flag
	binary.BigEndian.PutUint16(packet[34:36], 65535)   // Window
	binary.BigEndian.PutUint16(packet[36:38], 0)       // Checksum
	binary.BigEndian.PutUint16(packet[38:40], 0)       // Urgent pointer

	return packet
}

// buildUDPPacket creates a minimal UDP/IP packet for testing
func buildUDPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16) []byte {
	// Create 20-byte IP header + 8-byte UDP header
	packet := make([]byte, 28)

	// IPv4 header (20 bytes)
	packet[0] = 0x45 // Version 4, IHL 5 (20 bytes)
	packet[1] = 0    // TOS
	binary.BigEndian.PutUint16(packet[2:4], 28)  // Total length
	binary.BigEndian.PutUint16(packet[4:6], 0)   // ID
	binary.BigEndian.PutUint16(packet[6:8], 0)   // Flags/Fragment
	packet[8] = 64                               // TTL
	packet[9] = 17                               // Protocol (UDP)
	packet[10] = 0                               // Checksum (will be calculated)
	packet[11] = 0
	copy(packet[12:16], srcIP.To4())
	copy(packet[16:20], dstIP.To4())

	// UDP header (8 bytes)
	binary.BigEndian.PutUint16(packet[20:22], srcPort) // Source port
	binary.BigEndian.PutUint16(packet[22:24], dstPort) // Dest port
	binary.BigEndian.PutUint16(packet[24:26], 8)       // Length
	binary.BigEndian.PutUint16(packet[26:28], 0)       // Checksum

	return packet
}

// buildICMPPacket creates a minimal ICMP/IP packet for testing
func buildICMPPacket(srcIP, dstIP net.IP, icmpID uint16) []byte {
	// Create 20-byte IP header + 8-byte ICMP header
	packet := make([]byte, 28)

	// IPv4 header (20 bytes)
	packet[0] = 0x45 // Version 4, IHL 5 (20 bytes)
	packet[1] = 0    // TOS
	binary.BigEndian.PutUint16(packet[2:4], 28)  // Total length
	binary.BigEndian.PutUint16(packet[4:6], 0)   // ID
	binary.BigEndian.PutUint16(packet[6:8], 0)   // Flags/Fragment
	packet[8] = 64                               // TTL
	packet[9] = 1                                // Protocol (ICMP)
	packet[10] = 0                               // Checksum (will be calculated)
	packet[11] = 0
	copy(packet[12:16], srcIP.To4())
	copy(packet[16:20], dstIP.To4())

	// ICMP header (8 bytes minimum)
	packet[20] = 8 // Echo Request
	packet[21] = 0 // Code
	binary.BigEndian.PutUint16(packet[22:24], 0)       // Checksum
	binary.BigEndian.PutUint16(packet[24:26], icmpID)  // ID
	binary.BigEndian.PutUint16(packet[26:28], 1)       // Sequence

	return packet
}

func TestNATTable_TranslateOutbound_TCP(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildTCPPacket(srcIP, dstIP, 12345, 80)

	translated, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)
	require.NotNil(t, translated)

	// Verify source IP was changed to exit IP
	newSrcIP := net.IP(translated[12:16])
	assert.True(t, newSrcIP.Equal(exitIP.To4()), "Source IP should be exit IP")

	// Verify destination IP unchanged
	newDstIP := net.IP(translated[16:20])
	assert.True(t, newDstIP.Equal(dstIP.To4()), "Destination IP should be unchanged")

	// Verify NAT entry was created
	entries, created, _ := table.Stats()
	assert.Equal(t, 1, entries)
	assert.Equal(t, uint64(1), created)
}

func TestNATTable_TranslateOutbound_UDP(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildUDPPacket(srcIP, dstIP, 54321, 53)

	translated, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)
	require.NotNil(t, translated)

	// Verify source IP was changed to exit IP
	newSrcIP := net.IP(translated[12:16])
	assert.True(t, newSrcIP.Equal(exitIP.To4()), "Source IP should be exit IP")
}

func TestNATTable_TranslateOutbound_ICMP(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildICMPPacket(srcIP, dstIP, 1234)

	translated, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)
	require.NotNil(t, translated)

	// Verify source IP was changed to exit IP
	newSrcIP := net.IP(translated[12:16])
	assert.True(t, newSrcIP.Equal(exitIP.To4()), "Source IP should be exit IP")
}

func TestNATTable_TranslateOutbound_ReuseEntry(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildTCPPacket(srcIP, dstIP, 12345, 80)

	// First translation
	translated1, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)
	natPort1 := binary.BigEndian.Uint16(translated1[20:22])

	// Second translation (same connection)
	translated2, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)
	natPort2 := binary.BigEndian.Uint16(translated2[20:22])

	// Should reuse the same NAT port
	assert.Equal(t, natPort1, natPort2)

	// Only one entry should exist
	entries, _, _ := table.Stats()
	assert.Equal(t, 1, entries)
}

func TestNATTable_TranslateInbound_TCP(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	// Create outbound packet to establish NAT entry
	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	outPacket := buildTCPPacket(srcIP, dstIP, 12345, 80)

	translated, err := table.TranslateOutbound(outPacket, "peer1")
	require.NoError(t, err)

	// Get the NAT port assigned
	natPort := binary.BigEndian.Uint16(translated[20:22])

	// Create inbound response packet
	responsePacket := buildTCPPacket(dstIP, exitIP, 80, natPort)

	// Translate inbound
	peerName, inTranslated, err := table.TranslateInbound(responsePacket)
	require.NoError(t, err)
	assert.Equal(t, "peer1", peerName)

	// Verify destination IP was restored to original source
	newDstIP := net.IP(inTranslated[16:20])
	assert.True(t, newDstIP.Equal(srcIP.To4()), "Destination should be original source IP")

	// Verify destination port was restored
	newDstPort := binary.BigEndian.Uint16(inTranslated[22:24])
	assert.Equal(t, uint16(12345), newDstPort)
}

func TestNATTable_TranslateInbound_NoEntry(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	// Create inbound packet without prior outbound
	srcIP := net.ParseIP("8.8.8.8")
	dstIP := net.ParseIP("203.0.113.1")
	packet := buildTCPPacket(srcIP, dstIP, 80, 50000)

	_, _, err := table.TranslateInbound(packet)
	assert.Error(t, err)
}

func TestNATTable_Cleanup(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 10*time.Millisecond) // Very short timeout for testing

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildTCPPacket(srcIP, dstIP, 12345, 80)

	_, err := table.TranslateOutbound(packet, "peer1")
	require.NoError(t, err)

	// Verify entry exists
	entries, _, _ := table.Stats()
	assert.Equal(t, 1, entries)

	// Wait for entry to expire
	time.Sleep(20 * time.Millisecond)

	// Cleanup should remove the entry
	removed := table.Cleanup()
	assert.Equal(t, 1, removed)

	entries, _, expired := table.Stats()
	assert.Equal(t, 0, entries)
	assert.Equal(t, uint64(1), expired)
}

func TestNATTable_PortAllocation(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")

	// Create multiple connections (different destination ports)
	var ports []uint16
	for i := 0; i < 10; i++ {
		packet := buildTCPPacket(srcIP, dstIP, uint16(12345+i), uint16(80+i))
		translated, err := table.TranslateOutbound(packet, "peer1")
		require.NoError(t, err)
		natPort := binary.BigEndian.Uint16(translated[20:22])
		ports = append(ports, natPort)
	}

	// All ports should be unique
	portSet := make(map[uint16]bool)
	for _, p := range ports {
		assert.False(t, portSet[p], "Duplicate NAT port allocated: %d", p)
		portSet[p] = true
	}

	entries, _, _ := table.Stats()
	assert.Equal(t, 10, entries)
}

func TestNATTable_TranslateOutbound_TooShort(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	// Packet too short
	_, err := table.TranslateOutbound([]byte{0x45, 0x00}, "peer1")
	assert.Error(t, err)
}

func TestNATTable_UnsupportedProtocol(t *testing.T) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	// Create packet with unsupported protocol (GRE = 47)
	packet := make([]byte, 24)
	packet[0] = 0x45 // Version 4, IHL 5
	binary.BigEndian.PutUint16(packet[2:4], 24)
	packet[9] = 47 // GRE protocol
	copy(packet[12:16], net.ParseIP("10.99.0.2").To4())
	copy(packet[16:20], net.ParseIP("8.8.8.8").To4())

	_, err := table.TranslateOutbound(packet, "peer1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported protocol")
}

func TestConnectionKey(t *testing.T) {
	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")

	key := connectionKey(6, srcIP, 12345, dstIP, 80)
	assert.Equal(t, "6:10.99.0.2:12345:8.8.8.8:80", key)
}

func TestRecalculateIPChecksum(t *testing.T) {
	// Build a packet
	srcIP := net.ParseIP("10.99.0.1").To4()
	dstIP := net.ParseIP("10.99.0.2").To4()
	packet := BuildIPv4Packet(srcIP, dstIP, ProtoUDP, []byte("test"))

	// Modify source IP
	copy(packet[12:16], net.ParseIP("192.168.1.1").To4())

	// Recalculate checksum
	recalculateIPChecksum(packet)

	// Verify checksum is valid (should be 0 or 0xFFFF when recalculated with checksum field included)
	verify := CalculateIPv4Checksum(packet[:20])
	assert.True(t, verify == 0 || verify == 0xFFFF, "Checksum should be valid")
}

func BenchmarkNATTranslateOutbound(b *testing.B) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	packet := buildTCPPacket(srcIP, dstIP, 12345, 80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = table.TranslateOutbound(packet, "peer1")
	}
}

func BenchmarkNATTranslateInbound(b *testing.B) {
	exitIP := net.ParseIP("203.0.113.1")
	table := NewNATTable(exitIP, 5*time.Minute)

	srcIP := net.ParseIP("10.99.0.2")
	dstIP := net.ParseIP("8.8.8.8")
	outPacket := buildTCPPacket(srcIP, dstIP, 12345, 80)

	translated, _ := table.TranslateOutbound(outPacket, "peer1")
	natPort := binary.BigEndian.Uint16(translated[20:22])

	responsePacket := buildTCPPacket(dstIP, exitIP, 80, natPort)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = table.TranslateInbound(responsePacket)
	}
}
