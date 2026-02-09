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
	Address string // IP address with CIDR (e.g., "172.30.0.1/16")
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
	configurePlatformTUN(&tunCfg, cfg.Name, cfg.Address)

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
		_ = iface.Close()
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
	routeNet := d.network.IP.String() + "/" + fmt.Sprintf("%d", ones)

	// Delete ALL existing routes to this network (there could be multiple from other VPNs)
	// Keep deleting until no more routes exist
	for i := 0; i < 50; i++ { // Max 50 attempts to avoid infinite loop
		cmd = exec.Command("route", "delete", "-net", routeNet)
		out, err := cmd.CombinedOutput()
		outStr := strings.TrimSpace(string(out))
		// Check both error and output - macOS route delete returns 0 even when route doesn't exist
		if err != nil || strings.Contains(outStr, "not in table") {
			log.Debug().Int("deleted", i).Msg("cleared existing network routes")
			break
		}
		log.Debug().Str("output", outStr).Msg("deleted existing route")
	}

	// Add our route
	cmd = exec.Command("route", "add", "-net", routeNet, "-interface", d.name)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Warn().Str("output", string(out)).Msg("route add failed")
	} else {
		log.Debug().Str("route", routeNet).Str("interface", d.name).Msg("route added")
	}

	// Verify the route was added correctly
	cmd = exec.Command("route", "-n", "get", d.network.IP.String())
	if out, err := cmd.CombinedOutput(); err == nil {
		outStr := string(out)
		if strings.Contains(outStr, d.name) {
			log.Info().Str("route", routeNet).Str("interface", d.name).Msg("route verified")
		} else {
			log.Warn().Str("output", outStr).Str("expected", d.name).Msg("route may not be using correct interface")
		}
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

	// Add route - delete first to avoid conflicts
	cmd = exec.Command("route", "delete", d.network.IP.String(), "mask", mask)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Debug().Str("output", string(out)).Msg("route delete (may not exist)")
	}

	cmd = exec.Command("route", "add", d.network.IP.String(), "mask", mask, d.ip.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Warn().Str("output", string(out)).Msg("route add failed")
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

// ExitRouteConfig holds configuration for exit node routing.
type ExitRouteConfig struct {
	InterfaceName string // TUN interface name
	MeshCIDR      string // Mesh network CIDR (for NAT exclusion)
	IsExitPeer    bool   // True if this node accepts exit traffic
}

// Validate checks if the exit route configuration is valid.
func (c *ExitRouteConfig) Validate() error {
	if c.InterfaceName == "" {
		return fmt.Errorf("interface name is required")
	}
	if c.MeshCIDR == "" {
		return fmt.Errorf("mesh CIDR is required")
	}
	_, _, err := net.ParseCIDR(c.MeshCIDR)
	if err != nil {
		return fmt.Errorf("invalid mesh CIDR: %w", err)
	}
	return nil
}

// buildDefaultRouteCommands builds OS-specific commands for adding/removing default routes.
// Returns (addCommands, removeCommands).
func buildDefaultRouteCommands(cfg ExitRouteConfig, goos string) ([][]string, [][]string) {
	var addCmds, removeCmds [][]string

	switch goos {
	case "darwin":
		// macOS: route add -net <cidr> -interface <iface>
		addCmds = [][]string{
			{"route", "add", "-net", "0.0.0.0/1", "-interface", cfg.InterfaceName},
			{"route", "add", "-net", "128.0.0.0/1", "-interface", cfg.InterfaceName},
		}
		removeCmds = [][]string{
			{"route", "delete", "-net", "0.0.0.0/1"},
			{"route", "delete", "-net", "128.0.0.0/1"},
		}
	case "linux":
		// Linux: ip route add <cidr> dev <iface>
		addCmds = [][]string{
			{"ip", "route", "add", "0.0.0.0/1", "dev", cfg.InterfaceName},
			{"ip", "route", "add", "128.0.0.0/1", "dev", cfg.InterfaceName},
		}
		removeCmds = [][]string{
			{"ip", "route", "delete", "0.0.0.0/1", "dev", cfg.InterfaceName},
			{"ip", "route", "delete", "128.0.0.0/1", "dev", cfg.InterfaceName},
		}
	case "windows":
		// Windows: route add <net> mask <mask> <gateway>
		// For exit routing, we route through the TUN gateway
		addCmds = [][]string{
			{"route", "add", "0.0.0.0", "mask", "128.0.0.0", "0.0.0.0", "if", cfg.InterfaceName},
			{"route", "add", "128.0.0.0", "mask", "128.0.0.0", "0.0.0.0", "if", cfg.InterfaceName},
		}
		removeCmds = [][]string{
			{"route", "delete", "0.0.0.0", "mask", "128.0.0.0"},
			{"route", "delete", "128.0.0.0", "mask", "128.0.0.0"},
		}
	}

	return addCmds, removeCmds
}

// buildExitNATCommands builds OS-specific commands for configuring NAT on exit nodes.
// Returns (addCommands, removeCommands).
func buildExitNATCommands(cfg ExitRouteConfig, goos string) ([][]string, [][]string) {
	var addCmds, removeCmds [][]string

	switch goos {
	case "darwin":
		// macOS: Enable IP forwarding and configure pf NAT
		addCmds = [][]string{
			{"sysctl", "-w", "net.inet.ip.forwarding=1"},
			// pf configuration would typically go through pfctl
			// For now, we'll enable forwarding - full pf NAT requires anchor setup
		}
		removeCmds = [][]string{
			// Don't disable forwarding on removal as other services may need it
		}
	case "linux":
		// Linux: Enable IP forwarding and add iptables MASQUERADE rule
		addCmds = [][]string{
			{"sysctl", "-w", "net.ipv4.ip_forward=1"},
			{"iptables", "-t", "nat", "-A", "POSTROUTING", "-s", cfg.MeshCIDR, "!", "-d", cfg.MeshCIDR, "-j", "MASQUERADE"},
		}
		removeCmds = [][]string{
			{"iptables", "-t", "nat", "-D", "POSTROUTING", "-s", cfg.MeshCIDR, "!", "-d", cfg.MeshCIDR, "-j", "MASQUERADE"},
		}
	case "windows":
		// Windows: Enable routing through registry
		addCmds = [][]string{
			{"netsh", "interface", "ipv4", "set", "interface", cfg.InterfaceName, "forwarding=enabled"},
		}
		removeCmds = [][]string{
			{"netsh", "interface", "ipv4", "set", "interface", cfg.InterfaceName, "forwarding=disabled"},
		}
	}

	return addCmds, removeCmds
}

// ConfigureExitRoutes sets up default routes for exit node clients.
// This routes all internet traffic (0.0.0.0/1 and 128.0.0.0/1) through the TUN interface.
func ConfigureExitRoutes(cfg ExitRouteConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	addCmds, _ := buildDefaultRouteCommands(cfg, runtime.GOOS)

	for _, cmdArgs := range addCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Warn().
				Str("cmd", strings.Join(cmdArgs, " ")).
				Str("output", string(out)).
				Msg("failed to add exit route")
			// Continue trying other routes
		} else {
			log.Info().Str("cmd", strings.Join(cmdArgs, " ")).Msg("exit route added")
		}
	}

	return nil
}

// RemoveExitRoutes removes the default routes for exit node clients.
func RemoveExitRoutes(cfg ExitRouteConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	_, removeCmds := buildDefaultRouteCommands(cfg, runtime.GOOS)

	for _, cmdArgs := range removeCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Debug().
				Str("cmd", strings.Join(cmdArgs, " ")).
				Str("output", string(out)).
				Msg("failed to remove exit route (may not exist)")
		} else {
			log.Info().Str("cmd", strings.Join(cmdArgs, " ")).Msg("exit route removed")
		}
	}

	return nil
}

// ConfigureExitNAT sets up NAT for exit node traffic forwarding.
func ConfigureExitNAT(cfg ExitRouteConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if !cfg.IsExitPeer {
		return nil // Nothing to do
	}

	addCmds, _ := buildExitNATCommands(cfg, runtime.GOOS)

	for _, cmdArgs := range addCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Warn().
				Str("cmd", strings.Join(cmdArgs, " ")).
				Str("output", string(out)).
				Msg("failed to configure exit NAT")
			// Continue trying other commands
		} else {
			log.Info().Str("cmd", strings.Join(cmdArgs, " ")).Msg("exit NAT configured")
		}
	}

	return nil
}

// RemoveExitNAT removes NAT configuration for exit node traffic.
func RemoveExitNAT(cfg ExitRouteConfig) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if !cfg.IsExitPeer {
		return nil // Nothing to do
	}

	_, removeCmds := buildExitNATCommands(cfg, runtime.GOOS)

	for _, cmdArgs := range removeCmds {
		cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Debug().
				Str("cmd", strings.Join(cmdArgs, " ")).
				Str("output", string(out)).
				Msg("failed to remove exit NAT (may not exist)")
		} else {
			log.Info().Str("cmd", strings.Join(cmdArgs, " ")).Msg("exit NAT removed")
		}
	}

	return nil
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
