package wireguard

import (
	"errors"
	"fmt"
	"strings"
)

// ClientConfigParams contains the parameters for generating a WireGuard client config.
type ClientConfigParams struct {
	ClientPrivateKey string // Client's WireGuard private key (base64)
	ClientMeshIP     string // Client's mesh IP address
	ServerPublicKey  string // Concentrator's WireGuard public key (base64)
	ServerEndpoint   string // Concentrator's public endpoint (host:port)
	DNSServer        string // DNS server address (optional)
	MeshCIDR         string // Mesh network CIDR for AllowedIPs
	MTU              int    // Interface MTU (optional, defaults to 1420)
}

// Validate validates the client config parameters.
func (p *ClientConfigParams) Validate() error {
	if p.ClientPrivateKey == "" {
		return errors.New("client private key is required")
	}
	if p.ClientMeshIP == "" {
		return errors.New("client mesh IP is required")
	}
	if p.ServerPublicKey == "" {
		return errors.New("server public key is required")
	}
	if p.ServerEndpoint == "" {
		return errors.New("server endpoint is required")
	}
	return nil
}

// GenerateClientConfig generates a WireGuard client configuration file.
func GenerateClientConfig(params ClientConfigParams) string {
	var sb strings.Builder

	// [Interface] section
	sb.WriteString("[Interface]\n")
	sb.WriteString(fmt.Sprintf("PrivateKey = %s\n", params.ClientPrivateKey))
	sb.WriteString(fmt.Sprintf("Address = %s/32\n", params.ClientMeshIP))

	if params.DNSServer != "" {
		sb.WriteString(fmt.Sprintf("DNS = %s\n", params.DNSServer))
	}

	mtu := params.MTU
	if mtu == 0 {
		mtu = 1420
	}
	sb.WriteString(fmt.Sprintf("MTU = %d\n", mtu))

	sb.WriteString("\n")

	// [Peer] section
	sb.WriteString("[Peer]\n")
	sb.WriteString(fmt.Sprintf("PublicKey = %s\n", params.ServerPublicKey))
	sb.WriteString(fmt.Sprintf("Endpoint = %s\n", params.ServerEndpoint))

	allowedIPs := params.MeshCIDR
	if allowedIPs == "" {
		allowedIPs = "172.30.0.0/16" // Default mesh CIDR
	}
	sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", allowedIPs))

	// PersistentKeepalive is important for mobile clients behind NAT
	sb.WriteString("PersistentKeepalive = 25\n")

	return sb.String()
}
