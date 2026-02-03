package wireguard

import (
	"fmt"
	"net"
	"net/netip"
	"os/exec"
)

// configureInterfaceAddr sets the IP address on the interface (Windows).
func configureInterfaceAddr(iface *net.Interface, prefix netip.Prefix, mtu int) error {
	addr := prefix.Addr().String()
	maskBits := prefix.Bits()

	// Calculate netmask from prefix bits
	mask := net.CIDRMask(maskBits, 32)
	netmask := fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])

	// On Windows, use netsh to set the address
	// netsh interface ip set address "interface name" static ip netmask
	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		iface.Name, "static", addr, netmask)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh set address: %w: %s", err, output)
	}

	// Set MTU using netsh
	cmd = exec.Command("netsh", "interface", "ipv4", "set", "subinterface",
		iface.Name, fmt.Sprintf("mtu=%d", mtu), "store=persistent")
	if output, err := cmd.CombinedOutput(); err != nil {
		// MTU setting may fail on some Windows versions, just warn
		_ = output // Log this in production
	}

	return nil
}
