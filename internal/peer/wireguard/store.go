package wireguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var (
	ErrClientNotFound = errors.New("client not found")
	ErrIPExhausted    = errors.New("no available IPs in WireGuard client range")
)

// ClientStore manages WireGuard client storage with file persistence.
type ClientStore struct {
	mu       sync.RWMutex
	clients  map[string]*Client // keyed by ID
	meshCIDR string
	nextIP   uint32 // next IP to allocate in the WG client range
	dataDir  string
	filePath string
}

// persistedData is the JSON structure saved to disk.
type persistedData struct {
	Clients []Client `json:"clients"`
	NextIP  uint32   `json:"next_ip"`
}

// NewClientStore creates a new WireGuard client store with file persistence.
func NewClientStore(meshCIDR, dataDir string) (*ClientStore, error) {
	store := &ClientStore{
		clients:  make(map[string]*Client),
		meshCIDR: meshCIDR,
		dataDir:  dataDir,
		filePath: filepath.Join(dataDir, "wireguard_clients.json"),
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	// Load existing data if present
	if err := store.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load store: %w", err)
	}

	return store, nil
}

// load reads the persisted data from disk.
func (s *ClientStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var persisted persistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	s.clients = make(map[string]*Client)
	for i := range persisted.Clients {
		client := persisted.Clients[i]
		s.clients[client.ID] = &client
	}
	s.nextIP = persisted.NextIP

	return nil
}

// save writes the current state to disk.
func (s *ClientStore) save() error {
	clients := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, *c)
	}

	persisted := persistedData{
		Clients: clients,
		NextIP:  s.nextIP,
	}

	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// Write atomically by writing to temp file then renaming
	tempFile := s.filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempFile, s.filePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// CreateWithPrivateKey creates a new WireGuard client and returns the private key.
// The private key is only returned once at creation time.
func (s *ClientStore) CreateWithPrivateKey(name string) (*Client, string, error) {
	// Generate WireGuard key pair
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, "", fmt.Errorf("generate private key: %w", err)
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

	if err := s.save(); err != nil {
		delete(s.clients, client.ID)
		return nil, "", fmt.Errorf("save: %w", err)
	}

	return client, privateKey.String(), nil
}

// Get retrieves a client by ID.
func (s *ClientStore) Get(id string) (*Client, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	client, ok := s.clients[id]
	if !ok {
		return nil, ErrClientNotFound
	}
	// Return a copy
	c := *client
	return &c, nil
}

// List returns all clients.
func (s *ClientStore) List() []Client {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clients := make([]Client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, *c)
	}
	return clients
}

// Update updates a client's settings.
func (s *ClientStore) Update(id string, enabled *bool) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return nil, ErrClientNotFound
	}

	if enabled != nil {
		client.Enabled = *enabled
	}

	if err := s.save(); err != nil {
		return nil, fmt.Errorf("save: %w", err)
	}

	c := *client
	return &c, nil
}

// Delete removes a client.
func (s *ClientStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.clients[id]; !ok {
		return ErrClientNotFound
	}

	delete(s.clients, id)

	if err := s.save(); err != nil {
		return fmt.Errorf("save: %w", err)
	}

	return nil
}

// UpdateLastSeen updates the last seen timestamp for a client.
func (s *ClientStore) UpdateLastSeen(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, ok := s.clients[id]
	if !ok {
		return ErrClientNotFound
	}

	client.LastSeen = t

	// Don't save to disk for last_seen updates (too frequent)
	return nil
}

// allocateIP allocates the next available IP in the WireGuard client range.
// WG clients use 10.99.100.0 - 10.99.199.255 (third octet 100-199).
// Must be called with lock held.
func (s *ClientStore) allocateIP() (string, error) {
	// Parse mesh CIDR to get base network
	_, ipNet, err := net.ParseCIDR(s.meshCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid mesh CIDR: %w", err)
	}

	// Get base IP (e.g., 10.99.0.0)
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
