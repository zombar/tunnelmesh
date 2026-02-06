package client

import (
	"context"
	"net"
	"time"
)

// DiscoveryClient tries multiple port mapping protocols in order of preference.
// It probes PCP first, then falls back to NAT-PMP if PCP is unavailable.
type DiscoveryClient struct {
	gateway   net.IP
	localIP   net.IP
	pxpPort   uint16 // Port for PCP/NAT-PMP (default 5351)
	active    Client
	clients   []Client
}

// NewDiscoveryClient creates a new discovery client that will try PCP and NAT-PMP.
// The gateway is the IP address of the router/gateway to contact.
// The localIP is the local IP address used for PCP client identification.
// If pxpPort is 0, the default port (5351) is used.
func NewDiscoveryClient(gateway, localIP net.IP, pxpPort uint16) *DiscoveryClient {
	if pxpPort == 0 {
		pxpPort = 5351
	}

	// Create clients in order of preference
	pcpClient := NewPCPClient(gateway, localIP, pxpPort)
	natpmpClient := NewNATMPClient(gateway, pxpPort)

	return &DiscoveryClient{
		gateway: gateway,
		localIP: localIP,
		pxpPort: pxpPort,
		clients: []Client{pcpClient, natpmpClient},
	}
}

// Name returns the name of the active client, or "discovery" if none active.
func (d *DiscoveryClient) Name() string {
	if d.active != nil {
		return d.active.Name()
	}
	return "discovery"
}

// probeTimeout is the per-client timeout for probing.
// Each client gets this much time to respond before we move to the next.
const probeTimeout = 1500 * time.Millisecond

// Probe tries each protocol client in order until one succeeds.
// On success, the working client becomes the active client for subsequent operations.
// Each client gets a limited time (probeTimeout) to respond before we try the next.
func (d *DiscoveryClient) Probe(ctx context.Context) (net.IP, error) {
	var lastErr error

	for _, c := range d.clients {
		// Give each client its own timeout so a non-responding protocol
		// doesn't consume the entire context timeout
		clientCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		gw, err := c.Probe(clientCtx)
		cancel()

		if err == nil {
			d.active = c
			return gw, nil
		}
		lastErr = err

		// Check if parent context is done
		if ctx.Err() != nil {
			break
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNoGatewayFound
}

// GetExternalAddress uses the active client to get the external address.
func (d *DiscoveryClient) GetExternalAddress(ctx context.Context) (net.IP, error) {
	if d.active == nil {
		return nil, ErrNoActiveClient
	}
	return d.active.GetExternalAddress(ctx)
}

// RequestMapping uses the active client to request a mapping.
func (d *DiscoveryClient) RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error) {
	if d.active == nil {
		return nil, ErrNoActiveClient
	}
	return d.active.RequestMapping(ctx, protocol, internalPort, lifetime)
}

// RefreshMapping uses the active client to refresh a mapping.
func (d *DiscoveryClient) RefreshMapping(ctx context.Context, mapping *Mapping) (*Mapping, error) {
	if d.active == nil {
		return nil, ErrNoActiveClient
	}
	return d.active.RefreshMapping(ctx, mapping)
}

// DeleteMapping uses the active client to delete a mapping.
func (d *DiscoveryClient) DeleteMapping(ctx context.Context, mapping *Mapping) error {
	if d.active == nil {
		return ErrNoActiveClient
	}
	return d.active.DeleteMapping(ctx, mapping)
}

// Close closes all underlying clients.
func (d *DiscoveryClient) Close() error {
	var lastErr error
	for _, c := range d.clients {
		if err := c.Close(); err != nil {
			lastErr = err
		}
	}
	d.active = nil
	return lastErr
}

// ActiveProtocol returns the name of the active protocol, or empty string if none.
func (d *DiscoveryClient) ActiveProtocol() string {
	if d.active == nil {
		return ""
	}
	return d.active.Name()
}

// Gateway returns the gateway IP address.
func (d *DiscoveryClient) Gateway() net.IP {
	return d.gateway
}
