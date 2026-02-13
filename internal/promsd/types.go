// Package promsd provides Prometheus service discovery for TunnelMesh peers.
package promsd

import "time"

// Target represents a Prometheus file_sd target entry.
type Target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Peer represents a peer from the coordination server API.
type Peer struct {
	Name          string    `json:"name"`
	MeshIP        string    `json:"mesh_ip"`
	LastSeen      time.Time `json:"last_seen"`
	IsCoordinator bool      `json:"is_coordinator,omitempty"`
}

// PeersResponse represents the API response from /api/v1/peers.
type PeersResponse struct {
	Peers []Peer `json:"peers"`
}

// Config holds the configuration for the SD generator.
type Config struct {
	CoordURL        string
	AuthToken       string
	PollInterval    time.Duration
	OutputFile      string
	CoordOutputFile string // Output file for coordinator targets (port 443)
	MetricsPort     string
	TLSSkipVerify   bool
	OnlineThreshold time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		CoordURL:        "https://localhost:443",
		PollInterval:    30 * time.Second,
		OutputFile:      "/targets/peers.json",
		CoordOutputFile: "/targets/coordinators.json",
		MetricsPort:     "9443",
		OnlineThreshold: 2 * time.Minute,
	}
}
