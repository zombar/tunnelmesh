// Package tun provides TUN interface management for tunnelmesh.
package tun

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/songgao/water"
)

// Config holds TUN device configuration.
type Config struct {
	Name    string // Interface name (e.g., "tun-mesh0")
	MTU     int    // Maximum transmission unit
	Address string // IP address with CIDR (e.g., "10.99.0.1/16")
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("interface name is required")
	}
	if c.MTU < 576 || c.MTU > 65535 {
		return fmt.Errorf("MTU must be between 576 and 65535")
	}
	if c.Address == "" {
		return fmt.Errorf("address is required")
	}
	// Validate address format
	_, _, err := net.ParseCIDR(c.Address)
	if err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	return nil
}

// Device represents a TUN network interface.
type Device struct {
	iface   *water.Interface
	name    string
	ip      net.IP
	network *net.IPNet
	mtu     int
}

// Create creates and configures a new TUN device.
func Create(cfg Config) (*Device, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Parse the address
	ip, network, err := net.ParseCIDR(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("parse address: %w", err)
	}

	// Create TUN interface
	tunCfg := water.Config{
		DeviceType: water.TUN,
	}

	// Set platform-specific options
	switch runtime.GOOS {
	case "darwin":
		// macOS uses utun devices
		// water will auto-assign a utun number
	case "linux":
		tunCfg.Name = cfg.Name
	case "windows":
		// Windows requires wintun.dll
		tunCfg.Name = cfg.Name
	}

	iface, err := water.New(tunCfg)
	if err != nil {
		return nil, fmt.Errorf("create TUN interface: %w", err)
	}

	dev := &Device{
		iface:   iface,
		name:    iface.Name(),
		ip:      ip,
		network: network,
		mtu:     cfg.MTU,
	}

	// Configure the interface
	if err := dev.configure(); err != nil {
		iface.Close()
		return nil, fmt.Errorf("configure interface: %w", err)
	}

	log.Info().
		Str("name", dev.name).
		Str("ip", ip.String()).
		Str("network", network.String()).
		Int("mtu", cfg.MTU).
		Msg("TUN device created")

	return dev, nil
}

// configure sets up the TUN interface with IP and routes.
func (d *Device) configure() error {
	switch runtime.GOOS {
	case "darwin":
		return d.configureDarwin()
	case "linux":
		return d.configureLinux()
	case "windows":
		return d.configureWindows()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func (d *Device) configureDarwin() error {
	// Set IP address
	mask := d.network.Mask
	maskIP := net.IP(mask).String()

	cmd := exec.Command("ifconfig", d.name, d.ip.String(), d.ip.String(), "netmask", maskIP, "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig failed: %s: %w", string(out), err)
	}

	// Set MTU
	cmd = exec.Command("ifconfig", d.name, "mtu", fmt.Sprintf("%d", d.mtu))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("set MTU failed: %s: %w", string(out), err)
	}

	// Add route for the mesh network
	ones, _ := d.network.Mask.Size()
	cmd = exec.Command("route", "add", "-net", d.network.IP.String()+"/"+fmt.Sprintf("%d", ones), "-interface", d.name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Route might already exist
		log.Debug().Str("output", string(out)).Msg("route add (may already exist)")
	}

	return nil
}

func (d *Device) configureLinux() error {
	// Set IP address
	ones, _ := d.network.Mask.Size()
	addr := fmt.Sprintf("%s/%d", d.ip.String(), ones)

	cmd := exec.Command("ip", "addr", "add", addr, "dev", d.name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add failed: %s: %w", string(out), err)
	}

	// Set MTU and bring up
	cmd = exec.Command("ip", "link", "set", d.name, "mtu", fmt.Sprintf("%d", d.mtu), "up")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set failed: %s: %w", string(out), err)
	}

	return nil
}

func (d *Device) configureWindows() error {
	// Windows configuration using netsh
	mask := net.IP(d.network.Mask).String()

	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		d.name, "static", d.ip.String(), mask)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh failed: %s: %w", string(out), err)
	}

	// Set MTU
	cmd = exec.Command("netsh", "interface", "ipv4", "set", "subinterface",
		d.name, fmt.Sprintf("mtu=%d", d.mtu))
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Warn().Str("output", string(out)).Msg("set MTU (may fail on some Windows versions)")
	}

	// Add route
	cmd = exec.Command("route", "add", d.network.IP.String(), "mask", mask, d.ip.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Debug().Str("output", string(out)).Msg("route add (may already exist)")
	}

	return nil
}

// Name returns the interface name.
func (d *Device) Name() string {
	return d.name
}

// IP returns the device's IP address.
func (d *Device) IP() net.IP {
	return d.ip
}

// Network returns the device's network.
func (d *Device) Network() *net.IPNet {
	return d.network
}

// Read reads a packet from the TUN device.
func (d *Device) Read(p []byte) (int, error) {
	return d.iface.Read(p)
}

// Write writes a packet to the TUN device.
func (d *Device) Write(p []byte) (int, error) {
	return d.iface.Write(p)
}

// Close closes the TUN device.
func (d *Device) Close() error {
	log.Info().Str("name", d.name).Msg("closing TUN device")
	return d.iface.Close()
}

// ReadWriteCloser returns the underlying interface as io.ReadWriteCloser.
func (d *Device) ReadWriteCloser() io.ReadWriteCloser {
	return d.iface
}

// ParseIPConfig parses an IP address and CIDR and returns the IP and mask.
func ParseIPConfig(ipStr, cidrStr string) (net.IP, net.IPMask, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil, nil, fmt.Errorf("invalid IP address: %s", ipStr)
	}
	ip = ip.To4()
	if ip == nil {
		return nil, nil, fmt.Errorf("only IPv4 supported: %s", ipStr)
	}

	_, network, err := net.ParseCIDR(cidrStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid CIDR: %w", err)
	}

	return ip, network.Mask, nil
}

// ExtractDestIP extracts the destination IP from an IPv4 packet.
func ExtractDestIP(packet []byte) net.IP {
	if len(packet) < 20 {
		return nil
	}
	// IPv4 header: destination IP is at bytes 16-19
	version := packet[0] >> 4
	if version != 4 {
		return nil
	}
	return net.IP(packet[16:20])
}

// ExtractSrcIP extracts the source IP from an IPv4 packet.
func ExtractSrcIP(packet []byte) net.IP {
	if len(packet) < 20 {
		return nil
	}
	version := packet[0] >> 4
	if version != 4 {
		return nil
	}
	return net.IP(packet[12:16])
}

// ExtractProtocol extracts the protocol number from an IPv4 packet.
func ExtractProtocol(packet []byte) uint8 {
	if len(packet) < 20 {
		return 0
	}
	return packet[9]
}

// Protocol constants
const (
	ProtoICMP = 1
	ProtoTCP  = 6
	ProtoUDP  = 17
)

// ProtocolName returns a human-readable name for a protocol number.
func ProtocolName(proto uint8) string {
	switch proto {
	case ProtoICMP:
		return "ICMP"
	case ProtoTCP:
		return "TCP"
	case ProtoUDP:
		return "UDP"
	default:
		return fmt.Sprintf("proto-%d", proto)
	}
}

// IsInNetwork checks if an IP is within a network.
func IsInNetwork(ip net.IP, network *net.IPNet) bool {
	return network.Contains(ip)
}

// AddRoute adds a route to the system routing table.
func AddRoute(dest *net.IPNet, gateway net.IP, iface string) error {
	switch runtime.GOOS {
	case "darwin":
		ones, _ := dest.Mask.Size()
		cmd := exec.Command("route", "add", "-net",
			fmt.Sprintf("%s/%d", dest.IP.String(), ones),
			gateway.String())
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "exists") {
			return fmt.Errorf("route add failed: %s: %w", string(out), err)
		}
	case "linux":
		ones, _ := dest.Mask.Size()
		cmd := exec.Command("ip", "route", "add",
			fmt.Sprintf("%s/%d", dest.IP.String(), ones),
			"via", gateway.String())
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "exists") {
			return fmt.Errorf("ip route add failed: %s: %w", string(out), err)
		}
	case "windows":
		mask := net.IP(dest.Mask).String()
		cmd := exec.Command("route", "add", dest.IP.String(), "mask", mask, gateway.String())
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("route add failed: %s: %w", string(out), err)
		}
	}
	return nil
}
