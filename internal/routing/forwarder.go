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

// Forwarder routes packets between the TUN device and tunnels.
type Forwarder struct {
	router      *Router
	tunnels     TunnelProvider
	tun         TUNDevice
	bufPool     *PacketBufferPool
	zeroCopyPool *ZeroCopyBufferPool // Pool for zero-copy buffers
	framePool   *sync.Pool           // Pool for frame buffers (header + MTU)
	stats       ForwarderStats
	tunMu       sync.RWMutex
	localIP     net.IP
	localIPMu   sync.RWMutex
}

// NewForwarder creates a new packet forwarder.
func NewForwarder(router *Router, tunnels TunnelProvider) *Forwarder {
	return &Forwarder{
		router:       router,
		tunnels:      tunnels,
		bufPool:      NewPacketBufferPool(MaxPacketSize),
		zeroCopyPool: NewZeroCopyBufferPool(FrameHeaderSize, MaxPacketSize),
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
		atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
		return fmt.Errorf("no route for %s", info.DstIP)
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

// writeFrame writes a length-prefixed frame to the tunnel.
func (f *Forwarder) writeFrame(w io.Writer, packet []byte) error {
	// Frame format: [2 bytes length][1 byte proto][payload]
	// Use pooled buffer to avoid allocations and combine into single write
	bufPtr := f.framePool.Get().(*[]byte)
	buf := *bufPtr
	defer f.framePool.Put(bufPtr)

	frameLen := len(packet) + 1
	binary.BigEndian.PutUint16(buf[0:2], uint16(frameLen))
	buf[2] = 0x01 // IP packet
	copy(buf[FrameHeaderSize:], packet)

	_, err := w.Write(buf[:FrameHeaderSize+len(packet)])
	return err
}

// writeFrameZeroCopy writes a pre-framed packet using zero-copy buffer.
// The packet data must already be positioned after the frame header space.
func (f *Forwarder) writeFrameZeroCopy(w io.Writer, zcBuf *ZeroCopyBuffer, packetLen int) error {
	// Get the complete frame with header already written
	frame := zcBuf.Frame(packetLen)
	_, err := w.Write(frame)
	return err
}

// ForwardPacketZeroCopy forwards a packet using zero-copy optimization.
// The packet must be in a ZeroCopyBuffer with the data positioned after header space.
func (f *Forwarder) ForwardPacketZeroCopy(zcBuf *ZeroCopyBuffer, packetLen int) error {
	packet := zcBuf.PacketData()[:packetLen]

	// Check packet length and version
	if packetLen < 1 {
		return nil // Silently drop empty packets
	}

	version := packet[0] >> 4
	if version != 4 {
		atomic.AddUint64(&f.stats.DroppedNonIPv4, 1)
		return nil
	}

	// Parse the packet
	info, err := ParseIPv4Packet(packet)
	if err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("parse packet: %w", err)
	}

	// Handle packets destined for our own IP
	f.localIPMu.RLock()
	localIP := f.localIP
	f.localIPMu.RUnlock()
	if localIP != nil && info.DstIP.Equal(localIP) {
		return f.ReceivePacket(packet)
	}

	// Look up the route
	peerName, ok := f.router.Lookup(info.DstIP)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
		return fmt.Errorf("no route for %s", info.DstIP)
	}

	// Get the tunnel
	tunnel, ok := f.tunnels.Get(peerName)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoTunnel, 1)
		return fmt.Errorf("no tunnel for peer %s", peerName)
	}

	// Zero-copy write: frame header is already in place
	if err := f.writeFrameZeroCopy(tunnel, zcBuf, packetLen); err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("write to tunnel: %w", err)
	}

	atomic.AddUint64(&f.stats.PacketsSent, 1)
	atomic.AddUint64(&f.stats.BytesSent, uint64(packetLen))

	log.Trace().
		Str("src", info.SrcIP.String()).
		Str("dst", info.DstIP.String()).
		Str("peer", peerName).
		Int("len", packetLen).
		Msg("forwarded packet (zero-copy)")

	return nil
}

// readFrame reads a length-prefixed frame from the tunnel into the provided buffer.
// Returns the number of payload bytes read (excluding protocol byte).
func (f *Forwarder) readFrame(r io.Reader, buf []byte) (int, error) {
	// Use stack-allocated header to avoid allocation
	var header [FrameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, err
	}

	frameLen := int(binary.BigEndian.Uint16(header[0:2]))
	if frameLen < 1 {
		return 0, fmt.Errorf("invalid frame length: %d", frameLen)
	}

	// frameLen includes the protocol byte
	payloadLen := frameLen - 1
	if payloadLen > len(buf) {
		return 0, fmt.Errorf("frame too large: %d bytes", payloadLen)
	}

	// Read payload directly into buffer
	if _, err := io.ReadFull(r, buf[:payloadLen]); err != nil {
		return 0, err
	}

	return payloadLen, nil
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

	// Use zero-copy buffer for optimal performance
	zcBuf := f.zeroCopyPool.Get()
	defer f.zeroCopyPool.Put(zcBuf)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("packet forwarder stopped")
			return ctx.Err()
		default:
		}

		// Read packet from TUN into zero-copy buffer (after header space)
		// This allows us to prepend the frame header without copying
		n, err := tun.Read(zcBuf.DataSlice())
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

		zcBuf.SetLength(n)

		// Forward the packet using zero-copy path
		if err := f.ForwardPacketZeroCopy(zcBuf, n); err != nil {
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
		n, err := f.readFrame(tunnel, buf.Data())
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

		// Write to TUN (use slice of buffer, no copy needed)
		if err := f.ReceivePacket(buf.Data()[:n]); err != nil {
			log.Debug().Err(err).Msg("receive packet failed")
		}
	}
}
