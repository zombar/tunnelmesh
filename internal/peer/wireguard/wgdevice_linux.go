package wireguard

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
)

// configureInterfaceAddr sets the IP address on the interface (Linux).
func configureInterfaceAddr(iface *net.Interface, prefix netip.Prefix, mtu int) error {
	addrCIDR := prefix.String()

	// Use ip command to set the address
	cmd := exec.Command("ip", "addr", "add", addrCIDR, "dev", iface.Name)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add: %w: %s", err, output)
	}

	// Set MTU
	cmd = exec.Command("ip", "link", "set", iface.Name, "mtu", fmt.Sprintf("%d", mtu))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set mtu: %w: %s", err, output)
	}

	// Bring up the interface
	cmd = exec.Command("ip", "link", "set", iface.Name, "up")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up: %w: %s", err, output)
	}

	return nil
}
