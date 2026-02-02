// Package transport provides a pluggable transport abstraction layer
// supporting multiple connection types (SSH, UDP, Relay) per peer.
package transport

import (
	"context"
	"io"
	"net"
	"time"
)

// TransportType identifies a transport implementation.
type TransportType string

const (
	TransportSSH   TransportType = "ssh"
	TransportUDP   TransportType = "udp"
	TransportRelay TransportType = "relay"
	TransportAuto  TransportType = "auto"
)

// Connection represents an established transport connection.
// Extends io.ReadWriteCloser with transport-specific metadata.
type Connection interface {
	io.ReadWriteCloser

	// PeerName returns the name of the remote peer.
	PeerName() string

	// Type returns the transport type of this connection.
	Type() TransportType

	// LocalAddr returns the local network address, if applicable.
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address, if applicable.
	RemoteAddr() net.Addr

	// IsHealthy returns true if the connection is ready to send/receive data.
	// For UDP, this means the crypto session is established.
	// For SSH, this means the channel is open.
	IsHealthy() bool
}

// Transport is the factory interface for creating connections.
type Transport interface {
	// Type returns the transport type identifier.
	Type() TransportType

	// Dial creates an outbound connection to the peer.
	Dial(ctx context.Context, opts DialOptions) (Connection, error)

	// Listen returns a listener for incoming connections.
	// Returns nil, nil if this transport doesn't support incoming connections.
	Listen(ctx context.Context, opts ListenOptions) (Listener, error)

	// Probe tests if the peer is reachable via this transport.
	// Returns estimated latency if successful.
	Probe(ctx context.Context, opts ProbeOptions) (time.Duration, error)

	// Close shuts down the transport and releases resources.
	Close() error
}

// Listener accepts incoming transport connections.
type Listener interface {
	// Accept waits for and returns the next connection.
	Accept(ctx context.Context) (Connection, error)

	// Addr returns the listener's network address.
	Addr() net.Addr

	// Close stops listening and releases resources.
	Close() error
}

// DialOptions contains options for dialing a peer.
type DialOptions struct {
	LocalName  string        // Our own identity name (sent to peer during handshake)
	PeerName   string        // Name of the peer we're connecting to
	PeerInfo   *PeerInfo
	Timeout    time.Duration
	ServerURL  string // Coordination server URL (for relay)
	JWTToken   string // Auth token (for relay)
}

// ListenOptions contains options for listening for connections.
type ListenOptions struct {
	Address string
	Port    int
}

// ProbeOptions contains options for probing connectivity.
type ProbeOptions struct {
	PeerInfo *PeerInfo
	Timeout  time.Duration
}

// PeerInfo contains network information about a peer.
type PeerInfo struct {
	Name             string
	PublicIPs        []string
	PrivateIPs       []string
	SSHPort          int
	UDPPort          int
	Connectable      bool
	BehindNAT        bool
	PublicKey        string
	PreferredType    TransportType
	ExternalEndpoint string // STUN-discovered external address for UDP
}

// TransportCapabilities describes transport features.
type TransportCapabilities struct {
	SupportsIncoming bool
	SupportsNATPunch bool
	RequiresRelay    bool
	MaxMTU           int
	ReliableDelivery bool
	OrderedDelivery  bool
}
