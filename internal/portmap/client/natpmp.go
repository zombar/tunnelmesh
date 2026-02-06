package client

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// NAT-PMP protocol constants (RFC 6886).
const (
	pmpVersion         = 0
	pmpDefaultPort     = 5351
	pmpMapLifetimeSec  = 7200
	pmpMapLifetimeZero = 0

	pmpOpMapPublicAddr = 0
	pmpOpMapUDP        = 1
	pmpOpMapTCP        = 2
	pmpOpReply         = 0x80

	pmpResultSuccess            = 0
	pmpResultUnsupportedVersion = 1
	pmpResultNotAuthorized      = 2
	pmpResultNetworkFailure     = 3
	pmpResultOutOfResources     = 4
	pmpResultUnsupportedOpcode  = 5
)

// NATMPClient implements the Client interface using NAT-PMP (RFC 6886).
type NATMPClient struct {
	gateway net.IP
	port    uint16
	epoch   uint32
	pubIP   net.IP
}

// NewNATMPClient creates a new NAT-PMP client.
// If port is 0, the default NAT-PMP port (5351) is used.
func NewNATMPClient(gateway net.IP, port uint16) *NATMPClient {
	if port == 0 {
		port = pmpDefaultPort
	}
	return &NATMPClient{
		gateway: gateway,
		port:    port,
	}
}

func (c *NATMPClient) Name() string {
	return "natpmp"
}

// Probe checks if a NAT-PMP server is available at the gateway.
func (c *NATMPClient) Probe(ctx context.Context) (net.IP, error) {
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

	// Send public address request (opcode 0)
	pkt := []byte{pmpVersion, pmpOpMapPublicAddr}
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
	pubIP, epoch, err := c.parsePublicAddrResponse(buf[:n])
	if err != nil {
		return nil, err
	}

	c.pubIP = pubIP
	c.epoch = epoch
	return c.gateway, nil
}

// GetExternalAddress returns the external IP address.
func (c *NATMPClient) GetExternalAddress(ctx context.Context) (net.IP, error) {
	if c.pubIP != nil {
		return c.pubIP, nil
	}

	_, err := c.Probe(ctx)
	if err != nil {
		return nil, err
	}
	return c.pubIP, nil
}

// RequestMapping requests a new port mapping.
func (c *NATMPClient) RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error) {
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

	// Build mapping request
	opcode := pmpOpMapUDP
	if protocol == TCP {
		opcode = pmpOpMapTCP
	}

	pkt := c.buildMappingPacket(opcode, uint16(internalPort), 0, uint32(lifetime.Seconds()))

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
	mapping, err := c.parseMappingResponse(buf[:n], protocol)
	if err != nil {
		return nil, err
	}

	mapping.Gateway = c.gateway
	if c.pubIP != nil {
		mapping.ExternalIP = c.pubIP
	}
	return mapping, nil
}

// RefreshMapping refreshes an existing mapping.
func (c *NATMPClient) RefreshMapping(ctx context.Context, existing *Mapping) (*Mapping, error) {
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

	opcode := pmpOpMapUDP
	if existing.Protocol == TCP {
		opcode = pmpOpMapTCP
	}

	// Use previous external port as suggestion
	pkt := c.buildMappingPacket(opcode, uint16(existing.InternalPort), uint16(existing.ExternalPort), uint32(existing.Lifetime.Seconds()))

	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	if _, err := conn.WriteTo(pkt, gwAddr); err != nil {
		return nil, err
	}

	buf := make([]byte, 256)
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		return nil, err
	}

	mapping, err := c.parseMappingResponse(buf[:n], existing.Protocol)
	if err != nil {
		return nil, err
	}

	mapping.Gateway = c.gateway
	if c.pubIP != nil {
		mapping.ExternalIP = c.pubIP
	}
	return mapping, nil
}

// DeleteMapping removes an existing mapping by requesting with lifetime=0.
func (c *NATMPClient) DeleteMapping(ctx context.Context, mapping *Mapping) error {
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

	opcode := pmpOpMapUDP
	if mapping.Protocol == TCP {
		opcode = pmpOpMapTCP
	}

	// Lifetime 0 = delete mapping
	pkt := c.buildMappingPacket(opcode, uint16(mapping.InternalPort), uint16(mapping.ExternalPort), 0)

	gwAddr := &net.UDPAddr{IP: c.gateway, Port: int(c.port)}
	_, err = conn.WriteTo(pkt, gwAddr)
	return err
}

func (c *NATMPClient) Close() error {
	return nil
}

// buildMappingPacket creates a NAT-PMP mapping request packet.
func (c *NATMPClient) buildMappingPacket(opcode int, internalPort, suggestedExternalPort uint16, lifetimeSec uint32) []byte {
	pkt := make([]byte, 12)
	pkt[0] = pmpVersion
	pkt[1] = byte(opcode)
	// pkt[2:4] = reserved
	binary.BigEndian.PutUint16(pkt[4:6], internalPort)
	binary.BigEndian.PutUint16(pkt[6:8], suggestedExternalPort)
	binary.BigEndian.PutUint32(pkt[8:12], lifetimeSec)
	return pkt
}

// parsePublicAddrResponse parses a NAT-PMP public address response.
func (c *NATMPClient) parsePublicAddrResponse(data []byte) (net.IP, uint32, error) {
	if len(data) < 12 {
		return nil, 0, fmt.Errorf("natpmp: response too short (%d bytes)", len(data))
	}

	if data[0] != pmpVersion {
		return nil, 0, fmt.Errorf("natpmp: unexpected version %d", data[0])
	}

	opcode := data[1]
	if opcode != (pmpOpMapPublicAddr | pmpOpReply) {
		return nil, 0, fmt.Errorf("natpmp: unexpected opcode %d", opcode)
	}

	resultCode := binary.BigEndian.Uint16(data[2:4])
	if resultCode != pmpResultSuccess {
		return nil, 0, fmt.Errorf("natpmp: error code %d", resultCode)
	}

	epoch := binary.BigEndian.Uint32(data[4:8])
	pubIP := net.IPv4(data[8], data[9], data[10], data[11])

	return pubIP, epoch, nil
}

// parseMappingResponse parses a NAT-PMP mapping response.
func (c *NATMPClient) parseMappingResponse(data []byte, protocol Protocol) (*Mapping, error) {
	if len(data) < 16 {
		return nil, fmt.Errorf("natpmp: mapping response too short (%d bytes)", len(data))
	}

	if data[0] != pmpVersion {
		return nil, fmt.Errorf("natpmp: unexpected version %d", data[0])
	}

	expectedOp := pmpOpMapUDP | pmpOpReply
	if protocol == TCP {
		expectedOp = pmpOpMapTCP | pmpOpReply
	}
	if data[1] != byte(expectedOp) {
		return nil, fmt.Errorf("natpmp: unexpected opcode %d", data[1])
	}

	resultCode := binary.BigEndian.Uint16(data[2:4])
	if resultCode != pmpResultSuccess {
		return nil, fmt.Errorf("natpmp: error code %d", resultCode)
	}

	c.epoch = binary.BigEndian.Uint32(data[4:8])
	internalPort := binary.BigEndian.Uint16(data[8:10])
	externalPort := binary.BigEndian.Uint16(data[10:12])
	lifetime := binary.BigEndian.Uint32(data[12:16])

	return &Mapping{
		Protocol:     protocol,
		InternalPort: int(internalPort),
		ExternalPort: int(externalPort),
		Lifetime:     time.Duration(lifetime) * time.Second,
	}, nil
}
