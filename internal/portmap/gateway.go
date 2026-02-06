package portmap

import (
	"fmt"
	"net"
)

// DetectGateway tries to find the default gateway and local IP address.
// Returns gateway IP, local IP, and any error.
//
// This uses a simple heuristic: dial a public address to determine the
// preferred local IP, then assume the gateway is at .1 on that subnet.
// This works for most home/office networks.
func DetectGateway() (gateway net.IP, localIP net.IP, err error) {
	// Get preferred outbound IP by dialing a public address
	// We use UDP so no actual connection is made
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return nil, nil, fmt.Errorf("detect local IP: %w", err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	localIP = localAddr.IP.To4()
	if localIP == nil {
		localIP = localAddr.IP
	}

	// Infer gateway as .1 on the same subnet (common convention)
	// This works for most home/office networks with /24 subnets
	gateway = make(net.IP, len(localIP))
	copy(gateway, localIP)
	if len(gateway) == 4 {
		gateway[3] = 1
	} else if len(gateway) == 16 {
		// IPv6: gateway detection is more complex, skip for now
		return nil, nil, fmt.Errorf("IPv6 gateway detection not implemented")
	}

	return gateway, localIP, nil
}
