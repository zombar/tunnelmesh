package wireguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
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
	defer resp.Body.Close()

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

	// Callbacks for integration with the mesh
	onClientsUpdated func(clients []Client)
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
