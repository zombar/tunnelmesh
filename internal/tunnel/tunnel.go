// Package tunnel implements SSH tunnel management for peer-to-peer connections.
package tunnel

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/ssh"
)

const (
	// ChannelType is the SSH channel type for mesh data.
	ChannelType = "tunnelmesh-data"
)

// SSHServer handles incoming SSH connections.
type SSHServer struct {
	config         *ssh.ServerConfig
	authorizedKeys []ssh.PublicKey
	keysMu         sync.RWMutex
}

// SSHConnection represents an established SSH connection.
type SSHConnection struct {
	Conn       *ssh.ServerConn
	Channels   <-chan ssh.NewChannel
	Requests   <-chan *ssh.Request
	PeerName   string
	RemoteAddr net.Addr
}

// NewSSHServer creates a new SSH server.
func NewSSHServer(hostKey ssh.Signer, authorizedKeys []ssh.PublicKey) *SSHServer {
	s := &SSHServer{
		authorizedKeys: authorizedKeys,
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.keysMu.RLock()
			defer s.keysMu.RUnlock()

			keyBytes := key.Marshal()
			for _, authorized := range s.authorizedKeys {
				if string(keyBytes) == string(authorized.Marshal()) {
					return &ssh.Permissions{
						Extensions: map[string]string{
							"pubkey-fp": ssh.FingerprintSHA256(key),
						},
					}, nil
				}
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	config.AddHostKey(hostKey)
	s.config = config

	return s
}

// Accept accepts an incoming connection and performs SSH handshake.
func (s *SSHServer) Accept(conn net.Conn) (*SSHConnection, error) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return nil, fmt.Errorf("SSH handshake failed: %w", err)
	}

	// Handle out-of-band requests (keepalive, etc.)
	go ssh.DiscardRequests(reqs)

	log.Info().
		Str("remote", conn.RemoteAddr().String()).
		Str("user", sshConn.User()).
		Msg("SSH connection established")

	return &SSHConnection{
		Conn:       sshConn,
		Channels:   chans,
		Requests:   reqs,
		RemoteAddr: conn.RemoteAddr(),
	}, nil
}

// AddAuthorizedKey adds a public key to the authorized keys.
func (s *SSHServer) AddAuthorizedKey(key ssh.PublicKey) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()

	// Check if key already exists
	keyBytes := key.Marshal()
	for _, existing := range s.authorizedKeys {
		if string(existing.Marshal()) == string(keyBytes) {
			return // Already authorized
		}
	}

	s.authorizedKeys = append(s.authorizedKeys, key)
	log.Debug().
		Str("fingerprint", ssh.FingerprintSHA256(key)).
		Msg("authorized key added")
}

// SSHClient handles outgoing SSH connections.
type SSHClient struct {
	signer  ssh.Signer
	hostKey ssh.PublicKey
}

// NewSSHClient creates a new SSH client.
func NewSSHClient(signer ssh.Signer, hostKey ssh.PublicKey) *SSHClient {
	return &SSHClient{
		signer:  signer,
		hostKey: hostKey,
	}
}

// Connect establishes an SSH connection to the given address.
func (c *SSHClient) Connect(addr string) (*ssh.Client, error) {
	config := &ssh.ClientConfig{
		User: "tunnelmesh",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(c.signer),
		},
		HostKeyCallback: c.hostKeyCallback(),
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("SSH dial failed: %w", err)
	}

	log.Info().
		Str("addr", addr).
		Msg("SSH connection established")

	return client, nil
}

func (c *SSHClient) hostKeyCallback() ssh.HostKeyCallback {
	if c.hostKey == nil {
		// Insecure mode - accept any host key
		return ssh.InsecureIgnoreHostKey()
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if string(key.Marshal()) != string(c.hostKey.Marshal()) {
			return fmt.Errorf("host key mismatch")
		}
		return nil
	}
}

// Tunnel represents a bidirectional data tunnel over SSH.
type Tunnel struct {
	channel  ssh.Channel
	peerName string
	mu       sync.Mutex
	closed   bool
}

// NewTunnel creates a tunnel from an SSH channel.
func NewTunnel(channel ssh.Channel, peerName string) *Tunnel {
	return &Tunnel{
		channel:  channel,
		peerName: peerName,
	}
}

// Read reads data from the tunnel.
func (t *Tunnel) Read(p []byte) (int, error) {
	return t.channel.Read(p)
}

// Write writes data to the tunnel.
func (t *Tunnel) Write(p []byte) (int, error) {
	return t.channel.Write(p)
}

// Close closes the tunnel.
func (t *Tunnel) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	return t.channel.Close()
}

// PeerName returns the name of the peer at the other end.
func (t *Tunnel) PeerName() string {
	return t.peerName
}

// TunnelManager manages multiple tunnels to peers.
type TunnelManager struct {
	tunnels map[string]*Tunnel
	mu      sync.RWMutex
}

// NewTunnelManager creates a new tunnel manager.
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels: make(map[string]*Tunnel),
	}
}

// Add adds a tunnel to the manager.
func (m *TunnelManager) Add(name string, tunnel *Tunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing tunnel if present
	if existing, ok := m.tunnels[name]; ok {
		existing.Close()
	}

	m.tunnels[name] = tunnel
	log.Debug().Str("peer", name).Msg("tunnel added")
}

// Get returns the tunnel for a peer.
func (m *TunnelManager) Get(name string) (*Tunnel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tunnel, ok := m.tunnels[name]
	return tunnel, ok
}

// Remove removes and closes a tunnel.
func (m *TunnelManager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tunnel, ok := m.tunnels[name]; ok {
		tunnel.Close()
		delete(m.tunnels, name)
		log.Debug().Str("peer", name).Msg("tunnel removed")
	}
}

// List returns the names of all active tunnels.
func (m *TunnelManager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.tunnels))
	for name := range m.tunnels {
		names = append(names, name)
	}
	return names
}

// CloseAll closes all tunnels.
func (m *TunnelManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, tunnel := range m.tunnels {
		tunnel.Close()
		delete(m.tunnels, name)
	}
	log.Debug().Msg("all tunnels closed")
}
