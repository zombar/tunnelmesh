package wireguard

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var (
// ErrClientNotFound is returned when a WireGuard client cannot be found.

	ErrClientNotFound = errors.New("client not found")
	ErrIPExhausted    = errors.New("no available IPs in WireGuard client range")
)

// Store manages WireGuard client storage in memory.
type Store struct {
	mu       sync.RWMutex
	clients  map[string]*Client // keyed by ID
	meshCIDR string
	nextIP   uint32 // next IP to allocate in the WG client range
}

// NewStore creates a new WireGuard client store.
func NewStore(meshCIDR string) *Store {
	return &Store{
		clients:  make(map[string]*Client),
		meshCIDR: meshCIDR,
		nextIP:   0, // Will start from .100.1
	}
}

// Create creates a new WireGuard client with generated keys.
// Returns the client (without private key).
func (s *Store) Create(name string) (*Client, error) {
	client, _, err := s.CreateWithPrivateKey(name)
	return client, err
}

// CreateWithPrivateKey creates a new WireGuard client and returns the private key.
// The private key is only returned once at creation time.
func (s *Store) CreateWithPrivateKey(name string) (*Client, string, error) {
	// Generate WireGuard key pair
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate private key: %w", err)
	}
	publicKey := privateKey.PublicKey()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Allocate mesh IP
	meshIP, err := s.allocateIP()
	if err != nil {
		return nil, "", err
	}

	// Generate DNS name
	dnsName := generateDNSName(name)

	client := &Client{
		ID:        uuid.New().String(),
		Name:      name,
		PublicKey: publicKey.String(),
		MeshIP:    meshIP,
		DNSName:   dnsName,
		Enabled:   true,
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}

	s.clients[client.ID] = client

	return client, privateKey.String(), nil
}

// Get retrieves a client by ID.
func (s *Store) Get(id string) (*Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	client, ok := s.clients[id]
	if !ok {
		return nil, ErrClientNotFound
	}
	return client, nil
}

// List returns all clients.
func (s *Store) List() []Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, *c)
	}
	return clients
}

// Update updates a client's settings.
func (s *Store) Update(id string, req *UpdateClientRequest) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return nil, ErrClientNotFound
	}

	if req.Enabled != nil {
		client.Enabled = *req.Enabled
	}

	return client, nil
}

// Delete removes a client.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[id]; !ok {
		return ErrClientNotFound
	}

	delete(s.clients, id)
	return nil
}

// UpdateLastSeen updates the last seen timestamp for a client.
func (s *Store) UpdateLastSeen(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return ErrClientNotFound
	}

	client.LastSeen = t
	return nil
}

// allocateIP allocates the next available IP in the WireGuard client range.
// WG clients use 172.30.100.0 - 172.30.199.255 (third octet 100-199).
// Must be called with lock held.
func (s *Store) allocateIP() (string, error) {
	// Parse mesh CIDR to get base network
	_, ipNet, err := net.ParseCIDR(s.meshCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid mesh CIDR: %w", err)
	}

	// Get base IP (e.g., 172.30.0.0)
	baseIP := ipNet.IP.To4()
	if baseIP == nil {
		return "", errors.New("only IPv4 supported")
	}

	// WG client range: base[0].base[1].100.1 to base[0].base[1].199.254
	// Calculate next IP
	maxIPs := 100 * 254 // 100 subnets (100-199) * 254 hosts each

	// Find an unused IP
	for i := 0; i < maxIPs; i++ {
		offset := (s.nextIP + uint32(i)) % uint32(maxIPs)
		thirdOctet := 100 + (offset / 254)
		fourthOctet := 1 + (offset % 254) // 1-254, skip .0 and .255

		ip := fmt.Sprintf("%d.%d.%d.%d", baseIP[0], baseIP[1], thirdOctet, fourthOctet)

		// Check if IP is already in use
		inUse := false
		for _, c := range s.clients {
			if c.MeshIP == ip {
				inUse = true
				break
			}
		}

		if !inUse {
			s.nextIP = offset + 1
			return ip, nil
		}
	}

	return "", ErrIPExhausted
}

// generateDNSName generates a DNS-safe name from the client name.
func generateDNSName(name string) string {
	// Convert to lowercase
	dns := strings.ToLower(name)

	// Replace spaces and special chars with hyphens
	dns = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(dns, "-")

	// Trim leading/trailing hyphens
	dns = strings.Trim(dns, "-")

	// Ensure not empty
	if dns == "" {
		dns = "client"
	}

	return dns
}
