package routing

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

const (
	// MaxPacketSize is the maximum IP packet size we handle.
	MaxPacketSize = 65535
	// DefaultMTU is the default MTU for the mesh network.
	DefaultMTU = 1400
	// FrameHeaderSize is the size of the frame header.
	FrameHeaderSize = 3

	// Protocol types for frame header
	ProtoMeshPacket = 0x01 // Regular mesh traffic
	ProtoExitPacket = 0x02 // Exit node traffic (forward to internet)
)

// TunnelProvider provides access to tunnels by peer name.
type TunnelProvider interface {
	Get(name string) (io.ReadWriteCloser, bool)
}

// TUNDevice represents a TUN device interface.
type TUNDevice interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// ForwarderStats contains forwarding statistics.
type ForwarderStats struct {
	PacketsSent     uint64
	PacketsReceived uint64
	BytesSent       uint64
	BytesReceived   uint64
	DroppedNoRoute  uint64
	DroppedNoTunnel uint64
	DroppedNonIPv4  uint64
	Errors          uint64
}

// ExitHandler is called when exit traffic needs to be handled.
// The handler receives the packet and the peer name that sent it.
// It should forward the packet to the internet and route responses back.
type ExitHandler interface {
	HandleExitPacket(peerName string, packet []byte) error
}

// Forwarder routes packets between the TUN device and tunnels.
type Forwarder struct {
	router    *Router
	tunnels   TunnelProvider
	tun       TUNDevice
	bufPool   *PacketBufferPool
	framePool *sync.Pool // Pool for frame buffers (header + MTU)
	stats     ForwarderStats
	tunMu     sync.RWMutex
	localIP   net.IP
	localIPMu sync.RWMutex

	// Exit node state
	exitPeerName   string       // Name of exit node peer (empty = disabled)
	exitExceptions []*net.IPNet // CIDRs that bypass exit node
	isExitNode     bool         // True if this node is an exit node
	exitHandler    ExitHandler  // Handler for exit traffic (set when isExitNode is true)
	exitMu         sync.RWMutex
}

// NewForwarder creates a new packet forwarder.
func NewForwarder(router *Router, tunnels TunnelProvider) *Forwarder {
	return &Forwarder{
		router:  router,
		tunnels: tunnels,
		bufPool: NewPacketBufferPool(MaxPacketSize),
		framePool: &sync.Pool{
			New: func() interface{} {
				// Allocate buffer for header + max packet size
				// Use pointer to slice to avoid allocation on Put (SA6002)
				buf := make([]byte, FrameHeaderSize+MaxPacketSize)
				return &buf
			},
		},
	}
}

// SetTUN sets the TUN device.
func (f *Forwarder) SetTUN(tun TUNDevice) {
	f.tunMu.Lock()
	defer f.tunMu.Unlock()
	f.tun = tun
}

// SetLocalIP sets the local mesh IP address.
func (f *Forwarder) SetLocalIP(ip net.IP) {
	f.localIPMu.Lock()
	defer f.localIPMu.Unlock()
	f.localIP = ip
}

// SetExitNode configures exit node routing.
// peerName is the name of the peer to use as exit node (empty to disable).
// exceptions are CIDRs that bypass the exit node.
func (f *Forwarder) SetExitNode(peerName string, exceptions []string) error {
	var parsed []*net.IPNet
	for _, cidr := range exceptions {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
		parsed = append(parsed, ipNet)
	}

	f.exitMu.Lock()
	f.exitPeerName = peerName
	f.exitExceptions = parsed
	f.exitMu.Unlock()

	if peerName != "" {
		log.Info().
			Str("exit_peer", peerName).
			Int("exceptions", len(exceptions)).
			Msg("exit node routing enabled")
	} else {
		log.Info().Msg("exit node routing disabled")
	}

	return nil
}

// SetIsExitNode configures this node to act as an exit node.
func (f *Forwarder) SetIsExitNode(enabled bool, handler ExitHandler) {
	f.exitMu.Lock()
	f.isExitNode = enabled
	f.exitHandler = handler
	f.exitMu.Unlock()

	if enabled {
		log.Info().Msg("exit node mode enabled")
	} else {
		log.Info().Msg("exit node mode disabled")
	}
}

// isExcepted checks if a destination IP should bypass the exit node.
func (f *Forwarder) isExcepted(ip net.IP) bool {
	for _, cidr := range f.exitExceptions {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ForwardPacket forwards a packet from the TUN to the appropriate tunnel.
func (f *Forwarder) ForwardPacket(packet []byte) error {
	// Check packet length and version
	if len(packet) < 1 {
		return nil // Silently drop empty packets
	}

	version := packet[0] >> 4
	if version != 4 {
		// Silently drop non-IPv4 packets (IPv6, etc.) - the mesh only supports IPv4
		atomic.AddUint64(&f.stats.DroppedNonIPv4, 1)
		return nil
	}

	// Parse the packet
	info, err := ParseIPv4Packet(packet)
	if err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("parse packet: %w", err)
	}

	// Handle packets destined for our own IP (local traffic)
	f.localIPMu.RLock()
	localIP := f.localIP
	f.localIPMu.RUnlock()
	if localIP != nil && info.DstIP.Equal(localIP) {
		// Write back to TUN so kernel delivers it locally
		return f.ReceivePacket(packet)
	}

	// Look up the route
	peerName, ok := f.router.Lookup(info.DstIP)
	if !ok {
		// Not a mesh destination - check exit node routing
		f.exitMu.RLock()
		exitPeer := f.exitPeerName
		exceptions := f.exitExceptions
		f.exitMu.RUnlock()

		if exitPeer == "" {
			// No exit node configured, drop as before
			atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
			return fmt.Errorf("no route for %s", info.DstIP)
		}

		// Check if destination is in exception list
		excepted := false
		for _, cidr := range exceptions {
			if cidr.Contains(info.DstIP) {
				excepted = true
				break
			}
		}
		if excepted {
			// Let this go out the normal way (OS routing)
			atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
			return nil // Silent drop - OS will handle via default route
		}

		// Route via exit node
		return f.forwardToExitNode(packet, exitPeer)
	}

	// Get the tunnel
	tunnel, ok := f.tunnels.Get(peerName)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoTunnel, 1)
		return fmt.Errorf("no tunnel for peer %s", peerName)
	}

	// Encode and send the packet
	if err := f.writeFrame(tunnel, packet); err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("write to tunnel: %w", err)
	}

	atomic.AddUint64(&f.stats.PacketsSent, 1)
	atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))

	log.Trace().
		Str("src", info.SrcIP.String()).
		Str("dst", info.DstIP.String()).
		Str("peer", peerName).
		Int("len", len(packet)).
		Msg("forwarded packet")

	return nil
}

// ReceivePacket writes a received packet to the TUN device.
func (f *Forwarder) ReceivePacket(packet []byte) error {
	f.tunMu.RLock()
	tun := f.tun
	f.tunMu.RUnlock()

	if tun == nil {
		return fmt.Errorf("TUN device not set")
	}

	_, err := tun.Write(packet)
	if err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("write to TUN: %w", err)
	}

	atomic.AddUint64(&f.stats.PacketsReceived, 1)
	atomic.AddUint64(&f.stats.BytesReceived, uint64(len(packet)))

	return nil
}

// writeFrame writes a length-prefixed frame to the tunnel with mesh protocol type.
func (f *Forwarder) writeFrame(w io.Writer, packet []byte) error {
	return f.writeFrameWithProto(w, packet, ProtoMeshPacket)
}

// writeFrameWithProto writes a length-prefixed frame to the tunnel with specified protocol.
func (f *Forwarder) writeFrameWithProto(w io.Writer, packet []byte, protoType byte) error {
	// Frame format: [2 bytes length][1 byte proto][payload]
	// Use pooled buffer to avoid allocations and combine into single write
	bufPtr := f.framePool.Get().(*[]byte)
	buf := *bufPtr
	defer f.framePool.Put(bufPtr)

	frameLen := len(packet) + 1
	binary.BigEndian.PutUint16(buf[0:2], uint16(frameLen))
	buf[2] = protoType
	copy(buf[FrameHeaderSize:], packet)

	_, err := w.Write(buf[:FrameHeaderSize+len(packet)])
	return err
}

// forwardToExitNode forwards a packet to the exit node for internet routing.
func (f *Forwarder) forwardToExitNode(packet []byte, exitPeer string) error {
	tunnel, ok := f.tunnels.Get(exitPeer)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoTunnel, 1)
		return fmt.Errorf("no tunnel for exit peer %s", exitPeer)
	}

	// Send with exit protocol type
	if err := f.writeFrameWithProto(tunnel, packet, ProtoExitPacket); err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("write to exit tunnel: %w", err)
	}

	atomic.AddUint64(&f.stats.PacketsSent, 1)
	atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))

	log.Trace().
		Str("exit_peer", exitPeer).
		Int("len", len(packet)).
		Msg("forwarded packet to exit node")

	return nil
}

// readFrame reads a length-prefixed frame from the tunnel into the provided buffer.
// Returns the number of payload bytes read (excluding protocol byte) and the protocol type.
func (f *Forwarder) readFrame(r io.Reader, buf []byte) (int, byte, error) {
	// Use stack-allocated header to avoid allocation
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, 0, err
	}

	frameLen := int(binary.BigEndian.Uint16(header[0:2]))
	if frameLen < 1 {
		return 0, 0, fmt.Errorf("invalid frame length: %d", frameLen)
	}

	protoType := header[2]

	// frameLen includes the protocol byte
	payloadLen := frameLen - 1
	if payloadLen > len(buf) {
		return 0, 0, fmt.Errorf("frame too large: %d bytes", payloadLen)
	}

	// Read payload directly into buffer
	if _, err := io.ReadFull(r, buf[:payloadLen]); err != nil {
		return 0, 0, err
	}

	return payloadLen, protoType, nil
}

// Stats returns a copy of the forwarding statistics.
func (f *Forwarder) Stats() ForwarderStats {
	return ForwarderStats{
		PacketsSent:     atomic.LoadUint64(&f.stats.PacketsSent),
		PacketsReceived: atomic.LoadUint64(&f.stats.PacketsReceived),
		BytesSent:       atomic.LoadUint64(&f.stats.BytesSent),
		BytesReceived:   atomic.LoadUint64(&f.stats.BytesReceived),
		DroppedNoRoute:  atomic.LoadUint64(&f.stats.DroppedNoRoute),
		DroppedNoTunnel: atomic.LoadUint64(&f.stats.DroppedNoTunnel),
		DroppedNonIPv4:  atomic.LoadUint64(&f.stats.DroppedNonIPv4),
		Errors:          atomic.LoadUint64(&f.stats.Errors),
	}
}

// Run starts the packet forwarding loop.
func (f *Forwarder) Run(ctx context.Context) error {
	f.tunMu.RLock()
	tun := f.tun
	f.tunMu.RUnlock()

	if tun == nil {
		return fmt.Errorf("TUN device not set")
	}

	log.Info().Msg("starting packet forwarder")

	buf := f.bufPool.Get()
	defer f.bufPool.Put(buf)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("packet forwarder stopped")
			return ctx.Err()
		default:
		}

		// Read packet from TUN
		n, err := tun.Read(buf.Data())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error().Err(err).Msg("TUN read error")
			continue
		}

		if n == 0 {
			continue
		}

		// Forward the packet directly from buffer (no copy needed,
		// packet is fully processed before next Read)
		if err := f.ForwardPacket(buf.Data()[:n]); err != nil {
			log.Debug().Err(err).Msg("forward packet failed")
		}
	}
}

// HandleTunnel reads packets from a tunnel and writes them to the TUN device.
func (f *Forwarder) HandleTunnel(ctx context.Context, peerName string, tunnel io.ReadWriteCloser) {
	log.Info().Str("peer", peerName).Msg("handling tunnel")

	// Get a buffer from the pool for the lifetime of this tunnel handler
	buf := f.bufPool.Get()
	defer f.bufPool.Put(buf)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Read frame from tunnel into pooled buffer
		n, protoType, err := f.readFrame(tunnel, buf.Data())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if err == io.EOF {
				log.Info().Str("peer", peerName).Msg("tunnel closed")
				return
			}
			log.Error().Err(err).Str("peer", peerName).Msg("tunnel read error")
			return
		}

		packet := buf.Data()[:n]

		switch protoType {
		case ProtoMeshPacket:
			// Regular mesh traffic - write to TUN
			if err := f.ReceivePacket(packet); err != nil {
				log.Debug().Err(err).Msg("receive packet failed")
			}

		case ProtoExitPacket:
			// Exit traffic - forward to internet if we're an exit node
			f.exitMu.RLock()
			isExitNode := f.isExitNode
			handler := f.exitHandler
			f.exitMu.RUnlock()

			if isExitNode && handler != nil {
				if err := handler.HandleExitPacket(peerName, packet); err != nil {
					log.Debug().Err(err).Str("peer", peerName).Msg("exit packet handling failed")
				}
			} else {
				log.Warn().Str("peer", peerName).Msg("received exit packet but not configured as exit node")
			}

		default:
			log.Warn().Uint8("proto", protoType).Msg("unknown protocol type")
		}
	}
}
