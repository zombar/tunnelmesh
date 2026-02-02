// Package routing provides exit node handling for the mesh network.
package routing

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ExitConfig holds configuration for the exit handler.
type ExitConfig struct {
	// ExitIP is the public IP address used for SNAT (defaults to auto-detect)
	ExitIP net.IP
	// NATTimeout is how long to keep NAT entries alive
	NATTimeout time.Duration
	// CleanupInterval is how often to clean up stale NAT entries
	CleanupInterval time.Duration
}

// DefaultExitHandler implements ExitHandler using a raw socket or TUN device.
type DefaultExitHandler struct {
	config    ExitConfig
	natTable  *NATTable
	tunnels   TunnelProvider
	forwarder *Forwarder

	// Exit TUN device for sending/receiving internet traffic
	exitTUN   TUNDevice
	exitTUNMu sync.RWMutex

	// Track which peer a response should go to
	peerMu sync.RWMutex

	// Context for cleanup goroutine
	ctx    context.Context
	cancel context.CancelFunc
}

// NewExitHandler creates a new exit handler.
func NewExitHandler(cfg ExitConfig, tunnels TunnelProvider, forwarder *Forwarder) *DefaultExitHandler {
	if cfg.NATTimeout == 0 {
		cfg.NATTimeout = NATTimeout
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = NATCleanupInterval
	}

	ctx, cancel := context.WithCancel(context.Background())

	h := &DefaultExitHandler{
		config:    cfg,
		natTable:  NewNATTable(cfg.ExitIP, cfg.NATTimeout),
		tunnels:   tunnels,
		forwarder: forwarder,
		ctx:       ctx,
		cancel:    cancel,
	}

	// Start cleanup goroutine
	go h.cleanupLoop()

	return h
}

// SetExitTUN sets the TUN device for exit traffic.
func (h *DefaultExitHandler) SetExitTUN(tun TUNDevice) {
	h.exitTUNMu.Lock()
	h.exitTUN = tun
	h.exitTUNMu.Unlock()
}

// SetExitIP updates the exit IP address.
func (h *DefaultExitHandler) SetExitIP(ip net.IP) {
	h.config.ExitIP = ip
	h.natTable.SetExitIP(ip)
}

// HandleExitPacket processes a packet received from a peer for internet routing.
// This is called by the forwarder when it receives a ProtoExitPacket.
func (h *DefaultExitHandler) HandleExitPacket(peerName string, packet []byte) error {
	// Perform NAT translation
	translated, err := h.natTable.TranslateOutbound(packet, peerName)
	if err != nil {
		return fmt.Errorf("NAT translation failed: %w", err)
	}

	// Send to internet via exit TUN
	h.exitTUNMu.RLock()
	tun := h.exitTUN
	h.exitTUNMu.RUnlock()

	if tun == nil {
		return fmt.Errorf("exit TUN not configured")
	}

	_, err = tun.Write(translated)
	if err != nil {
		return fmt.Errorf("write to exit TUN: %w", err)
	}

	log.Trace().
		Str("peer", peerName).
		Int("len", len(packet)).
		Msg("forwarded exit packet to internet")

	return nil
}

// Run starts the exit handler loop that reads responses from the internet.
func (h *DefaultExitHandler) Run(ctx context.Context) error {
	h.exitTUNMu.RLock()
	tun := h.exitTUN
	h.exitTUNMu.RUnlock()

	if tun == nil {
		return fmt.Errorf("exit TUN not set")
	}

	log.Info().Msg("starting exit handler")

	buf := make([]byte, MaxPacketSize)

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("exit handler stopped")
			return ctx.Err()
		default:
		}

		// Read packet from exit TUN (internet responses)
		n, err := tun.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error().Err(err).Msg("exit TUN read error")
			continue
		}

		if n == 0 {
			continue
		}

		packet := buf[:n]

		// Check if this is IPv4
		if len(packet) < 1 || packet[0]>>4 != 4 {
			continue
		}

		// Perform reverse NAT translation
		peerName, translated, err := h.natTable.TranslateInbound(packet)
		if err != nil {
			// Not a tracked connection, ignore
			log.Trace().Err(err).Msg("no NAT entry for inbound packet")
			continue
		}

		// Send back to the peer via tunnel
		if err := h.sendToPeer(peerName, translated); err != nil {
			log.Debug().Err(err).Str("peer", peerName).Msg("failed to send response to peer")
		}
	}
}

// sendToPeer sends a translated response packet back to the originating peer.
func (h *DefaultExitHandler) sendToPeer(peerName string, packet []byte) error {
	tunnel, ok := h.tunnels.Get(peerName)
	if !ok {
		return fmt.Errorf("no tunnel to peer %s", peerName)
	}

	// Write as mesh packet (the peer's forwarder will write to its TUN)
	if err := h.writeFrame(tunnel, packet, ProtoMeshPacket); err != nil {
		return fmt.Errorf("write to tunnel: %w", err)
	}

	log.Trace().
		Str("peer", peerName).
		Int("len", len(packet)).
		Msg("sent exit response to peer")

	return nil
}

// writeFrame writes a framed packet to the tunnel.
func (h *DefaultExitHandler) writeFrame(w io.Writer, packet []byte, protoType byte) error {
	// Frame format: [2 bytes length][1 byte proto][payload]
	frameLen := len(packet) + 1
	header := make([]byte, FrameHeaderSize)
	header[0] = byte(frameLen >> 8)
	header[1] = byte(frameLen)
	header[2] = protoType

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(packet); err != nil {
		return err
	}
	return nil
}

// cleanupLoop periodically cleans up stale NAT entries.
func (h *DefaultExitHandler) cleanupLoop() {
	ticker := time.NewTicker(h.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.natTable.Cleanup()
		}
	}
}

// Stop stops the exit handler and cleanup goroutine.
func (h *DefaultExitHandler) Stop() {
	h.cancel()
}

// Stats returns exit handler statistics.
func (h *DefaultExitHandler) Stats() (natEntries int, created, expired uint64) {
	return h.natTable.Stats()
}
