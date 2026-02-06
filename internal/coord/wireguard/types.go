// Package wireguard provides WireGuard client management for the coordination server.
package wireguard

import (
	"errors"
	"strings"
	"time"
)

// Client represents a WireGuard client that can connect to the mesh.
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

// CreateClientRequest is the request body for creating a new WireGuard client.
type CreateClientRequest struct {
	Name string `json:"name"`
}

// Validate validates the create client request.
func (r *CreateClientRequest) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

// CreateClientResponse is the response after creating a new WireGuard client.
// It includes the private key which is only shown once at creation time.
type CreateClientResponse struct {
	Client     Client `json:"client"`
	PrivateKey string `json:"private_key"` // Only shown at creation
	Config     string `json:"config"`      // WireGuard .conf file content
	QRCode     string `json:"qr_code"`     // Base64 data URL of QR code
}

// UpdateClientRequest is the request body for updating a WireGuard client.
type UpdateClientRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// Validate validates the update client request.
func (r *UpdateClientRequest) Validate() error {
	if r.Enabled == nil {
		return errors.New("no fields to update")
	}
	return nil
}

// ClientListResponse is the response containing all WireGuard clients.
// Used by the concentrator to sync client configuration.
type ClientListResponse struct {
	Clients               []Client `json:"clients"`
	ConcentratorPublicKey string   `json:"concentrator_public_key,omitempty"`
}

// HandshakeReport is sent by the concentrator to report client handshakes.
type HandshakeReport struct {
	ClientID    string    `json:"client_id"`
	HandshakeAt time.Time `json:"handshake_at"`
	BytesSent   uint64    `json:"bytes_sent"`
	BytesRecvd  uint64    `json:"bytes_received"`
}
