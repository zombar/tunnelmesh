package wireguard

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
)

// configureInterfaceAddr sets the IP address on the interface (macOS).
func configureInterfaceAddr(iface *net.Interface, prefix netip.Prefix, mtu int) error {
	addr := prefix.Addr().String()
	maskBits := prefix.Bits()

	// Calculate netmask from prefix bits
	mask := net.CIDRMask(maskBits, 32)
	netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])

	// On macOS, use ifconfig to set the address
	// ifconfig utun4 inet 10.99.100.1 10.99.100.1 netmask 255.255.0.0
	cmd := exec.Command("ifconfig", iface.Name, "inet", addr, addr, "netmask", netmask)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig inet: %w: %s", err, output)
	}

	// Set MTU
	cmd = exec.Command("ifconfig", iface.Name, "mtu", fmt.Sprintf("%d", mtu))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ifconfig mtu: %w: %s", err, output)
	}

	return nil
}
