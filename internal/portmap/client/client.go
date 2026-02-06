// Package client provides protocol implementations for port mapping.
package client

import (
	"context"
	"net"
	"time"
)

// Protocol identifies the transport protocol for port mapping.
type Protocol int

const (
	// UDP requests a UDP port mapping.
	UDP Protocol = iota
	// TCP requests a TCP port mapping.
	TCP
)

// Mapping represents an active port mapping.
type Mapping struct {
	// Protocol is the transport protocol (UDP or TCP).
	Protocol Protocol

	// InternalPort is the local port that was mapped.
	InternalPort int

	// ExternalPort is the port on the gateway's external interface.
	ExternalPort int

	// ExternalIP is the gateway's external IP address.
	ExternalIP net.IP

	// Gateway is the IP address of the gateway device.
	Gateway net.IP

	// Lifetime is the duration the mapping is valid for.
	Lifetime time.Duration
}

// Client is the interface for port mapping protocol implementations.
// Implementations include PCP, NAT-PMP, and UPnP clients.
type Client interface {
	// Name returns the name of the protocol (e.g., "pcp", "natpmp", "upnp").
	Name() string

	// Probe checks if a compatible gateway is available.
	// Returns the gateway address if found, or an error.
	Probe(ctx context.Context) (gateway net.IP, err error)

	// GetExternalAddress discovers the external address without creating a mapping.
	// This is useful for STUN-like address discovery.
	GetExternalAddress(ctx context.Context) (net.IP, error)

	// RequestMapping requests a new port mapping from the gateway.
	// The lifetime parameter is a hint; the gateway may return a different lifetime.
	RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error)

	// RefreshMapping refreshes an existing mapping to extend its lifetime.
	// The existing mapping details are used to request the same external port.
	RefreshMapping(ctx context.Context, mapping *Mapping) (*Mapping, error)

	// DeleteMapping removes an existing mapping.
	// This is best-effort; some protocols don't support explicit deletion.
	DeleteMapping(ctx context.Context, mapping *Mapping) error

	// Close releases any resources held by the client.
	Close() error
}

// Factory creates a Client for the given gateway.
type Factory interface {
	// Create creates a new client for the given gateway.
	Create(ctx context.Context, gateway net.IP) (Client, error)
}

// MultiClient wraps multiple clients and tries them in order.
type MultiClient struct {
	clients []Client
	active  Client
}

// NewMultiClient creates a new MultiClient from the given clients.
// Clients are tried in the order provided.
func NewMultiClient(clients ...Client) *MultiClient {
	return &MultiClient{clients: clients}
}

// Name returns "multi" to indicate this is a multi-protocol client.
func (m *MultiClient) Name() string {
	if m.active != nil {
		return m.active.Name()
	}
	return "multi"
}

// Probe tries each client in order until one succeeds.
func (m *MultiClient) Probe(ctx context.Context) (net.IP, error) {
	var lastErr error
	for _, c := range m.clients {
		gateway, err := c.Probe(ctx)
		if err == nil {
			m.active = c
			return gateway, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// GetExternalAddress uses the active client to get the external address.
func (m *MultiClient) GetExternalAddress(ctx context.Context) (net.IP, error) {
	if m.active == nil {
		return nil, ErrNoActiveClient
	}
	return m.active.GetExternalAddress(ctx)
}

// RequestMapping uses the active client to request a mapping.
func (m *MultiClient) RequestMapping(ctx context.Context, protocol Protocol, internalPort int, lifetime time.Duration) (*Mapping, error) {
	if m.active == nil {
		return nil, ErrNoActiveClient
	}
	return m.active.RequestMapping(ctx, protocol, internalPort, lifetime)
}

// RefreshMapping uses the active client to refresh a mapping.
func (m *MultiClient) RefreshMapping(ctx context.Context, mapping *Mapping) (*Mapping, error) {
	if m.active == nil {
		return nil, ErrNoActiveClient
	}
	return m.active.RefreshMapping(ctx, mapping)
}

// DeleteMapping uses the active client to delete a mapping.
func (m *MultiClient) DeleteMapping(ctx context.Context, mapping *Mapping) error {
	if m.active == nil {
		return ErrNoActiveClient
	}
	return m.active.DeleteMapping(ctx, mapping)
}

// Close closes all clients.
func (m *MultiClient) Close() error {
	var lastErr error
	for _, c := range m.clients {
		if err := c.Close(); err != nil {
			lastErr = err
		}
	}
	m.active = nil
	return lastErr
}

// Common errors.
var (
	ErrNoActiveClient     = clientError("no active client")
	ErrNoGatewayFound     = clientError("no gateway found")
	ErrMappingFailed      = clientError("mapping request failed")
	ErrUnsupported        = clientError("operation not supported")
	ErrTimeout            = clientError("operation timed out")
	ErrNetworkUnreachable = clientError("network unreachable")
)

type clientError string

func (e clientError) Error() string { return string(e) }
