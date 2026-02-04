package routing

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Sentinel errors for routing failures.
var (
	// ErrNoRoute is returned when no route exists for the destination IP.
	ErrNoRoute = errors.New("no route")
	// ErrNoTunnel is returned when no tunnel or relay is available for the peer.
	ErrNoTunnel = errors.New("no tunnel or relay")
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

// RelayPacketSender can send packets via persistent relay when no direct tunnel exists.
type RelayPacketSender interface {
	SendTo(targetPeer string, data []byte) error
	IsConnected() bool
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

// WGPacketHandler handles packets destined for WireGuard clients.
type WGPacketHandler interface {
	// IsWGClientIP returns true if the IP is a WireGuard client IP.
	IsWGClientIP(ip string) bool
	// SendPacket sends a packet to a WireGuard client.
	SendPacket(packet []byte) error
}

// Forwarder routes packets between the TUN device and tunnels.
type Forwarder struct {
	router             *Router
	tunnels            TunnelProvider
	tun                TUNDevice
	relay              RelayPacketSender   // Optional persistent relay for fallback
	wgHandler          WGPacketHandler     // Optional WireGuard handler for local WG clients
	bufPool            *PacketBufferPool
	zeroCopyPool       *ZeroCopyBufferPool // Pool for zero-copy buffers
	framePool          *sync.Pool          // Pool for frame buffers (header + MTU)
	stats              ForwarderStats
	tunMu              sync.RWMutex
	relayMu            sync.RWMutex
	wgMu               sync.RWMutex
	localIP            net.IP
	localIPMu          sync.RWMutex
	onDeadTunnel       func(peerName string) // Callback when tunnel write fails
	deadTunnelDebounce sync.Map             // peerName → time.Time for debouncing
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

// SetRelay sets the persistent relay for fallback routing.
func (f *Forwarder) SetRelay(relay RelayPacketSender) {
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	oldRelay := f.relay
	f.relay = relay
	// Check for nil interface value (not just nil interface) using reflection
	isNil := relay == nil || reflect.ValueOf(relay).IsNil()
	if !isNil {
		log.Debug().
			Bool("old_was_nil", oldRelay == nil).
			Bool("new_connected", relay.IsConnected()).
			Msg("forwarder relay reference updated")
	} else {
		log.Debug().Bool("old_was_nil", oldRelay == nil).Msg("forwarder relay reference cleared")
	}
}

// SetWGHandler sets the WireGuard packet handler for local WG client routing.
func (f *Forwarder) SetWGHandler(handler WGPacketHandler) {
	f.wgMu.Lock()
	defer f.wgMu.Unlock()
	f.wgHandler = handler
	log.Debug().Bool("handler_set", handler != nil).Msg("forwarder WG handler updated")
}

// SetOnDeadTunnel sets a callback that is called when a tunnel write fails.
// This allows the caller to remove the dead tunnel and trigger reconnection.
func (f *Forwarder) SetOnDeadTunnel(callback func(peerName string)) {
	f.onDeadTunnel = callback
}

// triggerDeadTunnel invokes the dead tunnel callback with debouncing.
// Multiple rapid failures for the same peer will only trigger one callback
// within a 5-second window to prevent callback storms.
func (f *Forwarder) triggerDeadTunnel(peerName string) {
	if f.onDeadTunnel == nil {
		return
	}

	now := time.Now()
	if last, ok := f.deadTunnelDebounce.Load(peerName); ok {
		if now.Sub(last.(time.Time)) < 5*time.Second {
			return // Debounced - already triggered recently
		}
	}

	f.deadTunnelDebounce.Store(peerName, now)
	go f.onDeadTunnel(peerName)
}

// HandleRelayPacket processes a packet received from the persistent relay.
// The packet should be a framed IP packet (with length prefix and protocol byte).
func (f *Forwarder) HandleRelayPacket(sourcePeer string, data []byte) {
	if len(data) < FrameHeaderSize {
		log.Debug().Str("peer", sourcePeer).Int("len", len(data)).Msg("relay packet too short")
		return
	}

	// Parse frame: [2 bytes length][1 byte proto][payload]
	frameLen := int(binary.BigEndian.Uint16(data[0:2]))
	if frameLen < 1 || len(data) < 2+frameLen {
		log.Debug().Str("peer", sourcePeer).Msg("relay packet invalid frame length")
		return
	}

	proto := data[2]
	if proto != 0x01 {
		log.Debug().Str("peer", sourcePeer).Uint8("proto", proto).Msg("relay packet non-IP protocol")
		return
	}

	packet := data[FrameHeaderSize : 2+frameLen]

	// Deliver to TUN
	if err := f.ReceivePacket(packet); err != nil {
		log.Warn().Err(err).Str("peer", sourcePeer).Int("len", len(packet)).Msg("failed to deliver relay packet to TUN")
	} else {
		log.Debug().Str("peer", sourcePeer).Int("len", len(packet)).Msg("delivered relay packet to TUN")
	}
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

	// Check if destination is a local WireGuard client
	f.wgMu.RLock()
	wgHandler := f.wgHandler
	f.wgMu.RUnlock()
	if wgHandler != nil && wgHandler.IsWGClientIP(info.DstIP.String()) {
		if err := wgHandler.SendPacket(packet); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			return fmt.Errorf("send to WG client: %w", err)
		}
		atomic.AddUint64(&f.stats.PacketsSent, 1)
		atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))
		log.Trace().
			Str("src", info.SrcIP.String()).
			Str("dst", info.DstIP.String()).
			Int("len", len(packet)).
			Msg("forwarded packet to WG client")
		return nil
	}

	// Look up the route
	peerName, ok := f.router.Lookup(info.DstIP)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
		return fmt.Errorf("%w for %s", ErrNoRoute, info.DstIP)
	}

	// Get the tunnel
	tunnel, ok := f.tunnels.Get(peerName)
	if ok {
		// Direct tunnel exists - use it
		if err := f.writeFrame(tunnel, packet); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			log.Warn().Err(err).Str("peer", peerName).Msg("tunnel write failed, marking dead and falling back to relay")

			// Signal dead tunnel so it can be removed and reconnection triggered
			f.triggerDeadTunnel(peerName)

			// Fall through to relay fallback below
		} else {
			atomic.AddUint64(&f.stats.PacketsSent, 1)
			atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))

			log.Trace().
				Str("src", info.SrcIP.String()).
				Str("dst", info.DstIP.String()).
				Str("peer", peerName).
				Int("len", len(packet)).
				Msg("forwarded packet via tunnel")

			return nil
		}
	}

	// No direct tunnel or tunnel write failed - try persistent relay
	f.relayMu.RLock()
	relay := f.relay
	f.relayMu.RUnlock()

	if relay != nil && relay.IsConnected() {
		// Build framed packet for relay
		frame := make([]byte, FrameHeaderSize+len(packet))
		binary.BigEndian.PutUint16(frame[0:2], uint16(len(packet)+1))
		frame[2] = 0x01 // IP packet
		copy(frame[FrameHeaderSize:], packet)

		if err := relay.SendTo(peerName, frame); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			log.Warn().Err(err).Str("peer", peerName).Msg("relay send failed")
			return fmt.Errorf("relay to peer %s: %w", peerName, err)
		}

		atomic.AddUint64(&f.stats.PacketsSent, 1)
		atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))

		log.Trace().
			Str("src", info.SrcIP.String()).
			Str("dst", info.DstIP.String()).
			Str("peer", peerName).
			Int("len", len(packet)).
			Msg("forwarded packet via relay")

		return nil
	}

	atomic.AddUint64(&f.stats.DroppedNoTunnel, 1)
	relayNil := relay == nil
	relayConnected := false
	if relay != nil {
		relayConnected = relay.IsConnected()
	}
	log.Debug().
		Str("peer", peerName).
		Str("dst", info.DstIP.String()).
		Bool("relay_nil", relayNil).
		Bool("relay_connected", relayConnected).
		Msg("dropped packet: no tunnel or relay")
	return fmt.Errorf("%w for peer %s", ErrNoTunnel, peerName)
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

	// Check if destination is a local WireGuard client
	f.wgMu.RLock()
	wgHandler := f.wgHandler
	f.wgMu.RUnlock()
	if wgHandler != nil && wgHandler.IsWGClientIP(info.DstIP.String()) {
		if err := wgHandler.SendPacket(packet); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			return fmt.Errorf("send to WG client: %w", err)
		}
		atomic.AddUint64(&f.stats.PacketsSent, 1)
		atomic.AddUint64(&f.stats.BytesSent, uint64(len(packet)))
		log.Trace().
			Str("src", info.SrcIP.String()).
			Str("dst", info.DstIP.String()).
			Int("len", packetLen).
			Msg("forwarded packet to WG client")
		return nil
	}

	// Look up the route
	peerName, ok := f.router.Lookup(info.DstIP)
	if !ok {
		atomic.AddUint64(&f.stats.DroppedNoRoute, 1)
		log.Debug().
			Str("dst", info.DstIP.String()).
			Msg("no route for destination")
		return fmt.Errorf("%w for %s", ErrNoRoute, info.DstIP)
	}

	// Get the tunnel
	tunnel, ok := f.tunnels.Get(peerName)
	if ok {
		log.Debug().
			Str("src", info.SrcIP.String()).
			Str("dst", info.DstIP.String()).
			Str("peer", peerName).
			Int("len", packetLen).
			Msg("forwarding packet to tunnel")

		// Zero-copy write: frame header is already in place
		if err := f.writeFrameZeroCopy(tunnel, zcBuf, packetLen); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			log.Warn().Err(err).Str("peer", peerName).Msg("tunnel write failed, marking dead and falling back to relay")

			// Signal dead tunnel so it can be removed and reconnection triggered
			f.triggerDeadTunnel(peerName)

			// Fall through to relay fallback below
		} else {
			atomic.AddUint64(&f.stats.PacketsSent, 1)
			atomic.AddUint64(&f.stats.BytesSent, uint64(packetLen))

			log.Debug().
				Str("src", info.SrcIP.String()).
				Str("dst", info.DstIP.String()).
				Str("peer", peerName).
				Int("len", packetLen).
				Msg("packet forwarded to tunnel")

			return nil
		}
	}

	// No direct tunnel or tunnel write failed - try persistent relay
	f.relayMu.RLock()
	relay := f.relay
	f.relayMu.RUnlock()

	if relay != nil && relay.IsConnected() {
		// Get the framed data from zero-copy buffer
		frame := zcBuf.Frame(packetLen)

		if err := relay.SendTo(peerName, frame); err != nil {
			atomic.AddUint64(&f.stats.Errors, 1)
			return fmt.Errorf("relay to peer %s: %w", peerName, err)
		}

		atomic.AddUint64(&f.stats.PacketsSent, 1)
		atomic.AddUint64(&f.stats.BytesSent, uint64(packetLen))

		log.Debug().
			Str("src", info.SrcIP.String()).
			Str("dst", info.DstIP.String()).
			Str("peer", peerName).
			Int("len", packetLen).
			Msg("packet forwarded via relay")

		return nil
	}

	atomic.AddUint64(&f.stats.DroppedNoTunnel, 1)
	log.Debug().
		Str("peer", peerName).
		Str("dst", info.DstIP.String()).
		Msg("no tunnel or relay for peer")
	return fmt.Errorf("%w for peer %s", ErrNoTunnel, peerName)
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

		log.Debug().Int("len", n).Msg("read packet from TUN")

		// Forward the packet using zero-copy path
		if err := f.ForwardPacketZeroCopy(zcBuf, n); err != nil {
			log.Debug().Err(err).Int("len", n).Msg("forward packet failed")
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
