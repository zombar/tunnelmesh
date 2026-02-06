package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// PCP protocol constants (RFC 6887).
const (
	pcpVersion     = 2
	pcpDefaultPort = 5351

	pcpOpAnnounce = 0
	pcpOpMap      = 1
	pcpOpReply    = 0x80

	pcpCodeOK              = 0
	pcpCodeNotAuthorized   = 2
	pcpCodeAddressMismatch = 12

	pcpUDPProtocol = 17
	pcpTCPProtocol = 6
)

// PCPClient implements the Client interface using PCP (RFC 6887).
type PCPClient struct {
	gateway   net.IP
	localIP   net.IP
	port      uint16
	epoch     uint32
	lastNonce [12]byte
}

// NewPCPClient creates a new PCP client.
// If port is 0, the default PCP port (5351) is used.
func NewPCPClient(gateway, localIP net.IP, port uint16) *PCPClient {
	if port == 0 {
		port = pcpDefaultPort
	}
	return &PCPClient{
		gateway: gateway,
		localIP: localIP,
		port:    port,
	}
}

func (c *PCPClient) Name() string {
	return "pcp"
}

// Probe checks if a PCP server is available at the gateway.
func (c *PCPClient) Probe(ctx context.Context) (net.IP, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	// Set deadline from context
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	// Build announce packet
	pkt := c.buildAnnouncePacket()

	// Send to gateway
	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	if _, err := conn.WriteTo(pkt, gwAddr); err != nil {
		return nil, err
	}

	// Read response
	buf := make([]byte, 256)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}

	// Parse response
	resp, err := c.parseAnnounceResponse(buf[:n])
	if err != nil {
		return nil, err
	}

	c.epoch = resp.epoch
	return c.gateway, nil
}

// GetExternalAddress returns the external IP via PCP ANNOUNCE.
// PCP doesn't directly return external IP in announce, so we need to do a mapping.
func (c *PCPClient) GetExternalAddress(ctx context.Context) (net.IP, error) {
	// PCP requires a mapping request to discover external address
	// Use a short-lived mapping on an ephemeral port
	mapping, err := c.RequestMapping(ctx, UDP, 0, 60*time.Second)
	if err != nil {
		return nil, err
	}
	// Delete the mapping immediately
	_ = c.DeleteMapping(ctx, mapping)
	return mapping.ExternalIP, nil
}

// RequestMapping requests a new port mapping via PCP MAP.
func (c *PCPClient) RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	// Build MAP request
	proto := uint8(pcpUDPProtocol)
	if protocol == TCP {
		proto = pcpTCPProtocol
	}

	pkt := c.buildMapPacket(uint16(internalPort), 0, uint32(lifetime.Seconds()), proto, net.IPv4zero)

	// Send request
	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	if _, err := conn.WriteTo(pkt, gwAddr); err != nil {
		return nil, err
	}

	// Read response
	buf := make([]byte, 256)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}

	// Parse response
	mapping, err := c.parseMapResponse(buf[:n], protocol)
	if err != nil {
		return nil, err
	}

	mapping.InternalPort = internalPort
	mapping.Gateway = c.gateway
	return mapping, nil
}

// RefreshMapping refreshes an existing mapping.
func (c *PCPClient) RefreshMapping(ctx context.Context, existing *Mapping) (*Mapping, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	proto := uint8(pcpUDPProtocol)
	if existing.Protocol == TCP {
		proto = pcpTCPProtocol
	}

	// Request with previous external port and IP as hints
	pkt := c.buildMapPacket(
		uint16(existing.InternalPort),
		uint16(existing.ExternalPort),
		uint32(existing.Lifetime.Seconds()),
		proto,
		existing.ExternalIP,
	)

	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	if _, err := conn.WriteTo(pkt, gwAddr); err != nil {
		return nil, err
	}

	buf := make([]byte, 256)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}

	mapping, err := c.parseMapResponse(buf[:n], existing.Protocol)
	if err != nil {
		return nil, err
	}

	mapping.InternalPort = existing.InternalPort
	mapping.Gateway = c.gateway
	return mapping, nil
}

// DeleteMapping removes an existing mapping by requesting with lifetime=0.
func (c *PCPClient) DeleteMapping(ctx context.Context, mapping *Mapping) error {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}

	proto := uint8(pcpUDPProtocol)
	if mapping.Protocol == TCP {
		proto = pcpTCPProtocol
	}

	// Lifetime 0 = delete mapping
	pkt := c.buildMapPacket(
		uint16(mapping.InternalPort),
		uint16(mapping.ExternalPort),
		0,
		proto,
		mapping.ExternalIP,
	)

	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	_, err = conn.WriteTo(pkt, gwAddr)
	return err
}

func (c *PCPClient) Close() error {
	return nil
}

// buildAnnouncePacket creates a PCP ANNOUNCE request.
func (c *PCPClient) buildAnnouncePacket() []byte {
	pkt := make([]byte, 24)
	pkt[0] = pcpVersion
	pkt[1] = pcpOpAnnounce

	// Copy local IP as IPv4-mapped IPv6
	ip16 := ipTo16(c.localIP)
	copy(pkt[8:24], ip16[:])

	return pkt
}

// buildMapPacket creates a PCP MAP request.
func (c *PCPClient) buildMapPacket(internalPort, suggestedPort uint16, lifetimeSec uint32, protocol uint8, suggestedIP net.IP) []byte {
	// 24 byte header + 36 byte MAP opcode
	pkt := make([]byte, 60)

	// Header
	pkt[0] = pcpVersion
	pkt[1] = pcpOpMap
	binary.BigEndian.PutUint32(pkt[4:8], lifetimeSec)

	// Client IP (IPv4-mapped IPv6)
	ip16 := ipTo16(c.localIP)
	copy(pkt[8:24], ip16[:])

	// MAP opcode data
	mapData := pkt[24:]

	// Generate and store nonce
	if _, err := rand.Read(c.lastNonce[:]); err != nil {
		// Fallback to zero nonce if crypto/rand fails (unlikely)
		for i := range c.lastNonce {
			c.lastNonce[i] = 0
		}
	}
	copy(mapData[0:12], c.lastNonce[:])

	// Protocol
	mapData[12] = protocol

	// Reserved (3 bytes)
	// mapData[13:16] = 0

	// Internal port
	binary.BigEndian.PutUint16(mapData[16:18], internalPort)

	// Suggested external port
	binary.BigEndian.PutUint16(mapData[18:20], suggestedPort)

	// Suggested external IP (IPv4-mapped IPv6)
	extIP16 := ipTo16(suggestedIP)
	copy(mapData[20:36], extIP16[:])

	return pkt
}

type pcpAnnounceResponse struct {
	resultCode uint8
	lifetime   uint32
	epoch      uint32
}

func (c *PCPClient) parseAnnounceResponse(data []byte) (*pcpAnnounceResponse, error) {
	if len(data) < 24 {
		return nil, fmt.Errorf("pcp: response too short (%d bytes)", len(data))
	}

	if data[0] != pcpVersion {
		return nil, fmt.Errorf("pcp: unexpected version %d", data[0])
	}

	if data[1] != (pcpOpAnnounce | pcpOpReply) {
		return nil, fmt.Errorf("pcp: unexpected opcode %d", data[1])
	}

	resultCode := data[3]
	if resultCode != pcpCodeOK {
		return nil, fmt.Errorf("pcp: error code %d", resultCode)
	}

	return &pcpAnnounceResponse{
		resultCode: resultCode,
		lifetime:   binary.BigEndian.Uint32(data[4:8]),
		epoch:      binary.BigEndian.Uint32(data[8:12]),
	}, nil
}

func (c *PCPClient) parseMapResponse(data []byte, protocol Protocol) (*Mapping, error) {
	if len(data) < 60 {
		return nil, fmt.Errorf("pcp: map response too short (%d bytes)", len(data))
	}

	if data[0] != pcpVersion {
		return nil, fmt.Errorf("pcp: unexpected version %d", data[0])
	}

	if data[1] != (pcpOpMap | pcpOpReply) {
		return nil, fmt.Errorf("pcp: unexpected opcode %d", data[1])
	}

	resultCode := data[3]
	if resultCode == pcpCodeNotAuthorized {
		return nil, fmt.Errorf("pcp: not authorized (PCP disabled on router)")
	}
	if resultCode != pcpCodeOK {
		return nil, fmt.Errorf("pcp: error code %d", resultCode)
	}

	lifetime := binary.BigEndian.Uint32(data[4:8])
	c.epoch = binary.BigEndian.Uint32(data[8:12])

	// Parse MAP response data
	mapData := data[24:]

	// External port is at offset 18-20 in MAP response
	externalPort := binary.BigEndian.Uint16(mapData[18:20])

	// External IP is at offset 20-36 (IPv4-mapped IPv6)
	var ip16 [16]byte
	copy(ip16[:], mapData[20:36])
	externalIP := ipFrom16(ip16)

	return &Mapping{
		Protocol:     protocol,
		ExternalPort: int(externalPort),
		ExternalIP:   externalIP,
		Lifetime:     time.Duration(lifetime) * time.Second,
	}, nil
}

// ipTo16 converts an IP to a 16-byte IPv4-mapped IPv6 representation.
func ipTo16(ip net.IP) [16]byte {
	var result [16]byte
	if ip == nil || ip.Equal(net.IPv4zero) {
		return result
	}

	ip4 := ip.To4()
	if ip4 != nil {
		// IPv4-mapped IPv6: ::ffff:a.b.c.d
		result[10] = 0xff
		result[11] = 0xff
		copy(result[12:16], ip4)
	} else {
		// IPv6
		ip16 := ip.To16()
		if ip16 != nil {
			copy(result[:], ip16)
		}
	}
	return result
}

// ipFrom16 extracts an IP from a 16-byte representation.
func ipFrom16(ip16 [16]byte) net.IP {
	// Check if it's an IPv4-mapped IPv6 (::ffff:a.b.c.d)
	isIPv4Mapped := true
	for i := 0; i < 10; i++ {
		if ip16[i] != 0 {
			isIPv4Mapped = false
			break
		}
	}
	if isIPv4Mapped && ip16[10] == 0xff && ip16[11] == 0xff {
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15])
	}

	// Return as IPv6
	return net.IP(ip16[:])
}
