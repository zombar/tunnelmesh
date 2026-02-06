package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ConcentratorConfig holds the configuration for the WireGuard concentrator.
type ConcentratorConfig struct {
	ServerURL    string        // Coordination server URL
	AuthToken    string        // JWT token for authentication
	ListenPort   int           // WireGuard UDP port
	DataDir      string        // Directory for keys and state
	SyncInterval time.Duration // How often to sync clients from server
	MeshCIDR     string        // Mesh network CIDR
}

// Validate validates the concentrator configuration.
func (c *ConcentratorConfig) Validate() error {
	if c.ServerURL == "" {
		return errors.New("server URL is required")
	}
	if c.AuthToken == "" {
		return errors.New("auth token is required")
	}
	if c.ListenPort <= 0 || c.ListenPort > 65535 {
		return errors.New("listen port must be between 1 and 65535")
	}
	return nil
}

// Client represents a WireGuard client from the coordination server.
type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"`
	MeshIP    string    `json:"mesh_ip"`
	DNSName   string    `json:"dns_name"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

// ToPeerConfig converts a Client to a WireGuard PeerConfig.
func (c *Client) ToPeerConfig() PeerConfig {
	return PeerConfig{
		PublicKey:  c.PublicKey,
		AllowedIPs: []string{c.MeshIP + "/32"},
	}
}

// ClientListResponse is the response from the coordination server.
type ClientListResponse struct {
	Clients               []Client `json:"clients"`
	ConcentratorPublicKey string   `json:"concentrator_public_key,omitempty"`
}

// FetchClients fetches the list of WireGuard clients from the coordination server.
func FetchClients(ctx context.Context, serverURL, authToken string) ([]Client, error) {
	url := serverURL + "/api/v1/wireguard/clients"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+authToken)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch clients: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("unauthorized: invalid token")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var listResp ClientListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return listResp.Clients, nil
}

// DiffClients compares old and new client lists and returns added and removed clients.
func DiffClients(old, new []Client) (added, removed []Client) {
	oldMap := make(map[string]Client)
	for _, c := range old {
		oldMap[c.ID] = c
	}

	newMap := make(map[string]Client)
	for _, c := range new {
		newMap[c.ID] = c
	}

	// Find added clients
	for _, c := range new {
		if _, exists := oldMap[c.ID]; !exists {
			added = append(added, c)
		}
	}

	// Find removed clients
	for _, c := range old {
		if _, exists := newMap[c.ID]; !exists {
			removed = append(removed, c)
		}
	}

	return added, removed
}

// Concentrator manages WireGuard clients and routes packets to the mesh.
type Concentrator struct {
	cfg     *ConcentratorConfig
	keys    *ServerKeys
	clients []Client
	mu      sync.RWMutex

	// WireGuard device (nil if device not started)
	device *WGDevice

	// Callbacks for integration with the mesh
	onClientsUpdated func(clients []Client)
	onPacketFromWG   func(packet []byte) // Called when packet arrives from WG peers
}

// NewConcentrator creates a new WireGuard concentrator.
func NewConcentrator(cfg *ConcentratorConfig) (*Concentrator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Load or generate server keys
	keys, err := LoadOrGenerateServerKeys(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("load keys: %w", err)
	}

	return &Concentrator{
		cfg:  cfg,
		keys: keys,
	}, nil
}

// PublicKey returns the concentrator's public key.
func (c *Concentrator) PublicKey() string {
	return c.keys.PublicKey
}

// Clients returns the current list of clients.
func (c *Concentrator) Clients() []Client {
	return c.clients
}

// SetOnClientsUpdated sets the callback for when clients are updated.
func (c *Concentrator) SetOnClientsUpdated(fn func([]Client)) {
	c.onClientsUpdated = fn
}

// Sync fetches clients from the coordination server and updates the local list.
func (c *Concentrator) Sync(ctx context.Context) error {
	newClients, err := FetchClients(ctx, c.cfg.ServerURL, c.cfg.AuthToken)
	if err != nil {
		return err
	}

	added, removed := DiffClients(c.clients, newClients)

	c.clients = newClients

	// Notify if clients changed
	if len(added) > 0 || len(removed) > 0 {
		if c.onClientsUpdated != nil {
			c.onClientsUpdated(c.clients)
		}
	}

	return nil
}

// GetDeviceConfig returns the WireGuard device configuration.
func (c *Concentrator) GetDeviceConfig() *DeviceConfig {
	peers := make([]PeerConfig, 0, len(c.clients))
	for _, client := range c.clients {
		if client.Enabled {
			peers = append(peers, client.ToPeerConfig())
		}
	}

	return &DeviceConfig{
		PrivateKey: c.keys.PrivateKey,
		ListenPort: c.cfg.ListenPort,
		Peers:      peers,
	}
}

// SetOnPacketFromWG sets the callback for packets received from WireGuard peers.
// These packets should be forwarded to the mesh.
func (c *Concentrator) SetOnPacketFromWG(fn func(packet []byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPacketFromWG = fn
}

// StartDevice creates and starts the userspace WireGuard device.
// This must be called after loading clients to configure peer allowed IPs.
func (c *Concentrator) StartDevice(interfaceName, address string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.device != nil {
		return errors.New("device already started")
	}

	cfg := &WGDeviceConfig{
		InterfaceName: interfaceName,
		PrivateKey:    c.keys.PrivateKey,
		ListenPort:    c.cfg.ListenPort,
		MTU:           1420,
		Address:       address,
	}

	device, err := NewWGDevice(cfg)
	if err != nil {
		return fmt.Errorf("create WG device: %w", err)
	}

	// Set up packet handler to forward WG packets to mesh
	device.SetPacketHandler(func(packet []byte) {
		c.mu.RLock()
		handler := c.onPacketFromWG
		c.mu.RUnlock()

		if handler != nil {
			handler(packet)
		}
	})

	// Add all current clients as peers
	if err := device.UpdatePeers(c.clients); err != nil {
		_ = device.Close()
		return fmt.Errorf("update peers: %w", err)
	}

	c.device = device

	// Start reading packets from the WG device
	go device.ReadPackets()

	log.Info().
		Str("interface", interfaceName).
		Int("port", c.cfg.ListenPort).
		Int("peers", len(c.clients)).
		Msg("WireGuard device started")

	return nil
}

// StopDevice stops and closes the WireGuard device.
func (c *Concentrator) StopDevice() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.device == nil {
		return nil
	}

	err := c.device.Close()
	c.device = nil

	log.Info().Msg("WireGuard device stopped")

	return err
}

// SendPacket sends a packet to a WireGuard peer.
// This is called when the mesh receives a packet destined for a WG client.
func (c *Concentrator) SendPacket(packet []byte) error {
	c.mu.RLock()
	device := c.device
	c.mu.RUnlock()

	if device == nil {
		return errors.New("WG device not started")
	}

	return device.WritePacket(packet)
}

// UpdateClients updates the concentrator's client list and refreshes the WG device peers.
func (c *Concentrator) UpdateClients(clients []Client) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.clients = clients

	if c.device != nil {
		if err := c.device.UpdatePeers(clients); err != nil {
			return fmt.Errorf("update WG peers: %w", err)
		}
	}

	if c.onClientsUpdated != nil {
		c.onClientsUpdated(clients)
	}

	return nil
}

// IsDeviceRunning returns true if the WireGuard device is started.
func (c *Concentrator) IsDeviceRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.device != nil
}
