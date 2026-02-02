// Package relay implements the WebSocket relay transport for tunnelmesh.
package relay

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/transport"
	"github.com/tunnelmesh/tunnelmesh/internal/tunnel"
)

// Transport implements the relay transport via coordination server.
type Transport struct {
	serverURL string
	jwtToken  string
}

// Config holds relay transport configuration.
type Config struct {
	ServerURL string
	JWTToken  string
}

// New creates a new relay transport.
func New(cfg Config) (*Transport, error) {
	if cfg.ServerURL == "" {
		return nil, fmt.Errorf("server URL is required")
	}

	return &Transport{
		serverURL: cfg.ServerURL,
		jwtToken:  cfg.JWTToken,
	}, nil
}

// SetJWTToken updates the JWT token (called after registration).
func (t *Transport) SetJWTToken(token string) {
	t.jwtToken = token
}

// Type returns the transport type.
func (t *Transport) Type() transport.TransportType {
	return transport.TransportRelay
}

// Dial creates an outbound relay connection to the peer.
func (t *Transport) Dial(ctx context.Context, opts transport.DialOptions) (transport.Connection, error) {
	serverURL := opts.ServerURL
	if serverURL == "" {
		serverURL = t.serverURL
	}

	jwtToken := opts.JWTToken
	if jwtToken == "" {
		jwtToken = t.jwtToken
	}

	if jwtToken == "" {
		return nil, fmt.Errorf("JWT token is required for relay")
	}

	relayTunnel, err := tunnel.NewRelayTunnel(ctx, serverURL, opts.PeerName, jwtToken)
	if err != nil {
		return nil, err
	}

	return &Connection{
		relay:    relayTunnel,
		peerName: opts.PeerName,
	}, nil
}

// Listen is not supported for relay transport (relay is server-mediated).
func (t *Transport) Listen(ctx context.Context, opts transport.ListenOptions) (transport.Listener, error) {
	// Relay transport doesn't support direct listening.
	// The coordination server handles connection pairing.
	return nil, nil
}

// Probe tests if the relay is available.
// For relay, we just check that we have valid config.
func (t *Transport) Probe(ctx context.Context, opts transport.ProbeOptions) (time.Duration, error) {
	// Relay is always "available" as a fallback if we have credentials
	if t.jwtToken == "" {
		return 0, fmt.Errorf("JWT token not set")
	}
	// Return a high latency to de-prioritize relay
	return 100 * time.Millisecond, nil
}

// Close shuts down the transport.
func (t *Transport) Close() error {
	return nil
}

// Connection wraps a RelayTunnel as a transport.Connection.
type Connection struct {
	relay    *tunnel.RelayTunnel
	peerName string
}

// Read reads data from the connection.
func (c *Connection) Read(p []byte) (int, error) {
	return c.relay.Read(p)
}

// Write writes data to the connection.
func (c *Connection) Write(p []byte) (int, error) {
	return c.relay.Write(p)
}

// Close closes the connection.
func (c *Connection) Close() error {
	return c.relay.Close()
}

// PeerName returns the peer name.
func (c *Connection) PeerName() string {
	return c.peerName
}

// Type returns the transport type.
func (c *Connection) Type() transport.TransportType {
	return transport.TransportRelay
}

// LocalAddr returns nil (relay has no direct local address).
func (c *Connection) LocalAddr() net.Addr {
	return nil
}

// RemoteAddr returns nil (relay has no direct remote address).
func (c *Connection) RemoteAddr() net.Addr {
	return nil
}

// IsHealthy returns true if the relay connection is open and ready for data.
func (c *Connection) IsHealthy() bool {
	return !c.relay.IsClosed()
}
