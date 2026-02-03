package wireguard

import (
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/rs/zerolog/log"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// WGDevice manages a userspace WireGuard device.
type WGDevice struct {
	mu         sync.RWMutex
	tunDevice  tun.Device
	wgDevice   *device.Device
	listenPort int
	privateKey string
	mtu        int

	// Packet handling
	onPacketFromWG func(packet []byte) // Called when packet arrives from WG peers
}

// WGDeviceConfig holds configuration for creating a WireGuard device.
type WGDeviceConfig struct {
	InterfaceName string
	PrivateKey    string
	ListenPort    int
	MTU           int
	Address       string // IP address for the interface (e.g., "10.99.100.1/16")
}

// NewWGDevice creates a new userspace WireGuard device.
func NewWGDevice(cfg *WGDeviceConfig) (*WGDevice, error) {
	if cfg.MTU == 0 {
		cfg.MTU = 1420
	}

	// Create TUN device
	tunDev, err := tun.CreateTUN(cfg.InterfaceName, cfg.MTU)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}

	// Get the actual interface name (may differ on some platforms)
	actualName, err := tunDev.Name()
	if err != nil {
		tunDev.Close()
		return nil, fmt.Errorf("get tun name: %w", err)
	}
	log.Debug().Str("name", actualName).Msg("created tun device")

	// Create logger for wireguard-go
	wgLogger := device.NewLogger(device.LogLevelError, fmt.Sprintf("wg(%s): ", actualName))

	// Create UDP bind
	bind := conn.NewDefaultBind()

	// Create WireGuard device
	wgDev := device.NewDevice(tunDev, bind, wgLogger)

	// Configure device with private key and listen port
	uapiConfig := fmt.Sprintf("private_key=%s\nlisten_port=%d\n",
		base64ToHex(cfg.PrivateKey),
		cfg.ListenPort)

	if err := wgDev.IpcSet(uapiConfig); err != nil {
		wgDev.Close()
		tunDev.Close()
		return nil, fmt.Errorf("configure device: %w", err)
	}

	// Bring up the device
	if err := wgDev.Up(); err != nil {
		wgDev.Close()
		tunDev.Close()
		return nil, fmt.Errorf("bring up device: %w", err)
	}

	// Configure IP address on the interface
	if cfg.Address != "" {
		if err := configureInterface(actualName, cfg.Address, cfg.MTU); err != nil {
			log.Warn().Err(err).Str("addr", cfg.Address).Msg("failed to configure interface address")
			// Don't fail - address might be configured externally
		}
	}

	dev := &WGDevice{
		tunDevice:  tunDev,
		wgDevice:   wgDev,
		listenPort: cfg.ListenPort,
		privateKey: cfg.PrivateKey,
		mtu:        cfg.MTU,
	}

	log.Info().
		Str("interface", actualName).
		Int("port", cfg.ListenPort).
		Int("mtu", cfg.MTU).
		Msg("WireGuard device created")

	return dev, nil
}

// SetPacketHandler sets the callback for packets received from WireGuard peers.
func (d *WGDevice) SetPacketHandler(handler func(packet []byte)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onPacketFromWG = handler
}

// ReadPackets reads packets from the WireGuard tun device and dispatches them.
// This should be called in a goroutine.
func (d *WGDevice) ReadPackets() {
	buf := make([]byte, d.mtu+100) // Extra space for headers
	sizes := make([]int, 1)

	for {
		// Read packet - the size is returned in sizes[0]
		n, err := d.tunDevice.Read([][]byte{buf}, sizes, 0)
		if err != nil {
			log.Debug().Err(err).Msg("WG tun read error")
			return
		}
		if n == 0 {
			continue
		}

		// Get packet size from the sizes array
		packetSize := sizes[0]
		if packetSize == 0 || packetSize > len(buf) {
			continue
		}

		// Copy packet data
		packet := make([]byte, packetSize)
		copy(packet, buf[:packetSize])

		// Dispatch to handler
		d.mu.RLock()
		handler := d.onPacketFromWG
		d.mu.RUnlock()

		if handler != nil {
			handler(packet)
		}
	}
}

// WritePacket writes a packet to the WireGuard tun device (to be sent to a WG peer).
func (d *WGDevice) WritePacket(packet []byte) error {
	bufs := [][]byte{packet}
	_, err := d.tunDevice.Write(bufs, 0)
	return err
}

// AddPeer adds or updates a WireGuard peer.
func (d *WGDevice) AddPeer(publicKey string, allowedIPs []string) error {
	config := fmt.Sprintf("public_key=%s\nreplace_allowed_ips=true\n",
		base64ToHex(publicKey))

	for _, ip := range allowedIPs {
		config += fmt.Sprintf("allowed_ip=%s\n", ip)
	}

	if err := d.wgDevice.IpcSet(config); err != nil {
		return fmt.Errorf("add peer: %w", err)
	}

	log.Debug().
		Str("public_key", publicKey[:8]+"...").
		Strs("allowed_ips", allowedIPs).
		Msg("added WireGuard peer")

	return nil
}

// RemovePeer removes a WireGuard peer.
func (d *WGDevice) RemovePeer(publicKey string) error {
	config := fmt.Sprintf("public_key=%s\nremove=true\n",
		base64ToHex(publicKey))

	if err := d.wgDevice.IpcSet(config); err != nil {
		return fmt.Errorf("remove peer: %w", err)
	}

	log.Debug().
		Str("public_key", publicKey[:8]+"...").
		Msg("removed WireGuard peer")

	return nil
}

// UpdatePeers updates the peer list to match the given clients.
func (d *WGDevice) UpdatePeers(clients []Client) error {
	for _, client := range clients {
		if !client.Enabled {
			continue
		}

		allowedIPs := []string{client.MeshIP + "/32"}
		if err := d.AddPeer(client.PublicKey, allowedIPs); err != nil {
			log.Warn().Err(err).Str("client", client.Name).Msg("failed to add WG peer")
		}
	}
	return nil
}

// Close shuts down the WireGuard device.
func (d *WGDevice) Close() error {
	if d.wgDevice != nil {
		d.wgDevice.Close()
	}
	if d.tunDevice != nil {
		d.tunDevice.Close()
	}
	return nil
}

// configureInterface sets the IP address on the interface.
func configureInterface(ifname, address string, mtu int) error {
	// Parse the address
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		return fmt.Errorf("parse address: %w", err)
	}

	// Get the interface
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return fmt.Errorf("get interface: %w", err)
	}

	// Use platform-specific code to configure the address
	return configureInterfaceAddr(iface, prefix, mtu)
}
