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
	Errors          uint64
}

// Forwarder routes packets between the TUN device and tunnels.
type Forwarder struct {
	router    *Router
	tunnels   TunnelProvider
	tun       TUNDevice
	bufPool   *PacketBufferPool
	stats     ForwarderStats
	statsMu   sync.RWMutex
	tunMu     sync.RWMutex
	localIP   net.IP
	localIPMu sync.RWMutex
}

// NewForwarder creates a new packet forwarder.
func NewForwarder(router *Router, tunnels TunnelProvider) *Forwarder {
	return &Forwarder{
		router:  router,
		tunnels: tunnels,
		bufPool: NewPacketBufferPool(MaxPacketSize),
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
	// Parse the packet
	info, err := ParseIPv4Packet(packet)
	if err != nil {
		atomic.AddUint64(&f.stats.Errors, 1)
		return fmt.Errorf("parse packet: %w", err)
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
	frameLen := len(packet) + 1
	header := make([]byte, FrameHeaderSize)
	binary.BigEndian.PutUint16(header[0:2], uint16(frameLen))
	header[2] = 0x01 // IP packet

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(packet); err != nil {
		return err
	}
	return nil
}

// readFrame reads a length-prefixed frame from the tunnel.
func (f *Forwarder) readFrame(r io.Reader) ([]byte, error) {
	header := make([]byte, FrameHeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	frameLen := int(binary.BigEndian.Uint16(header[0:2]))
	if frameLen < 1 {
		return nil, fmt.Errorf("invalid frame length: %d", frameLen)
	}

	// frameLen includes the protocol byte
	payloadLen := frameLen - 1
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	return payload, nil
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

		packet := make([]byte, n)
		copy(packet, buf.Data()[:n])

		// Forward the packet
		if err := f.ForwardPacket(packet); err != nil {
			log.Debug().Err(err).Msg("forward packet failed")
		}
	}
}

// HandleTunnel reads packets from a tunnel and writes them to the TUN device.
func (f *Forwarder) HandleTunnel(ctx context.Context, peerName string, tunnel io.ReadWriteCloser) {
	log.Info().Str("peer", peerName).Msg("handling tunnel")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Read frame from tunnel
		packet, err := f.readFrame(tunnel)
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

		// Write to TUN
		if err := f.ReceivePacket(packet); err != nil {
			log.Debug().Err(err).Msg("receive packet failed")
		}
	}
}
