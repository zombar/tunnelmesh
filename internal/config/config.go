// Package config handles configuration loading and validation for tunnelmesh.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// AdminConfig holds configuration for the admin web interface.
type AdminConfig struct {
	Enabled       bool             `yaml:"enabled"`
	BindAddress   string           `yaml:"bind_address"`    // Bind address for admin server (default: "127.0.0.1" - localhost only)
	Port          int              `yaml:"port"`            // Port for admin server (default: 8080)
	MeshOnlyAdmin *bool            `yaml:"mesh_only_admin"` // When join_mesh is set: true=HTTPS on mesh IP only, false=HTTP externally (default: true)
	Monitoring    MonitoringConfig `yaml:"monitoring"`      // Reverse proxy config for Prometheus/Grafana
}

// MonitoringConfig holds configuration for reverse proxying to monitoring services.
type MonitoringConfig struct {
	PrometheusURL string `yaml:"prometheus_url"` // URL to proxy /prometheus/ to (e.g., "http://localhost:9090")
	GrafanaURL    string `yaml:"grafana_url"`    // URL to proxy /grafana/ to (e.g., "http://localhost:3000")
}

// RelayConfig holds configuration for the relay server.
type RelayConfig struct {
	Enabled     bool   `yaml:"enabled"`
	PairTimeout string `yaml:"pair_timeout"` // Duration string, e.g. "30s"
}

// WireGuardServerConfig holds configuration for WireGuard client management.
type WireGuardServerConfig struct {
	Enabled  bool   `yaml:"enabled"`  // Enable WireGuard client management
	Endpoint string `yaml:"endpoint"` // Public endpoint for clients (concentrator address:port)
}

// WireGuardPeerConfig holds configuration for the WireGuard concentrator mode.
type WireGuardPeerConfig struct {
	Enabled      bool   `yaml:"enabled"`       // Run as WireGuard concentrator
	ListenPort   int    `yaml:"listen_port"`   // WireGuard UDP port (default: 51820)
	Endpoint     string `yaml:"endpoint"`      // Public endpoint for clients (host:port)
	Interface    string `yaml:"interface"`     // Interface name (default: "wg0")
	MTU          int    `yaml:"mtu"`           // MTU (default: 1420)
	DataDir      string `yaml:"data_dir"`      // Server key storage directory
	SyncInterval string `yaml:"sync_interval"` // Config sync interval (default: "30s")
}

// ServerConfig holds configuration for the coordination server.
type ServerConfig struct {
	Listen            string                `yaml:"listen"`
	AuthToken         string                `yaml:"auth_token"`
	MeshCIDR          string                `yaml:"mesh_cidr"`
	DomainSuffix      string                `yaml:"domain_suffix"`
	DataDir           string                `yaml:"data_dir"`            // Data directory for persistence (default: /var/lib/tunnelmesh)
	HeartbeatInterval string                `yaml:"heartbeat_interval"`  // Heartbeat interval (default: 10s)
	Locations         bool                  `yaml:"locations"`           // Enable node location tracking (requires external IP geolocation API)
	Admin             AdminConfig           `yaml:"admin"`
	Relay             RelayConfig           `yaml:"relay"`
	WireGuard         WireGuardServerConfig `yaml:"wireguard"`
	JoinMesh          *PeerConfig           `yaml:"join_mesh,omitempty"`
}

// PeerConfig holds configuration for a peer node.
type PeerConfig struct {
	Name              string              `yaml:"name"`
	Server            string              `yaml:"server"`
	AuthToken         string              `yaml:"auth_token"`
	SSHPort           int                 `yaml:"ssh_port"`
	PrivateKey        string              `yaml:"private_key"`
	HeartbeatInterval string              `yaml:"heartbeat_interval"` // Heartbeat interval (default: 10s)
	MetricsPort       int                 `yaml:"metrics_port"`       // Prometheus metrics port on mesh IP (default: 9443)
	TUN               TUNConfig           `yaml:"tun"`
	DNS               DNSConfig           `yaml:"dns"`
	WireGuard         WireGuardPeerConfig `yaml:"wireguard"`
	Geolocation       GeolocationConfig   `yaml:"geolocation"`       // Manual geolocation coordinates
	ExitNode          string              `yaml:"exit_node"`          // Name of peer to route internet traffic through
	AllowExitTraffic  bool                `yaml:"allow_exit_traffic"` // Allow this node to act as exit node for other peers
	Loki              LokiConfig          `yaml:"loki"`               // Loki log shipping configuration
}

// TUNConfig holds configuration for the TUN interface.
type TUNConfig struct {
	Name string `yaml:"name"`
	MTU  int    `yaml:"mtu"`
}

// DNSConfig holds configuration for the local DNS resolver.
type DNSConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Listen   string   `yaml:"listen"`
	CacheTTL int      `yaml:"cache_ttl"`
	Aliases  []string `yaml:"aliases,omitempty"` // Custom DNS aliases for this peer
}

// GeolocationConfig holds manual geolocation coordinates for a peer.
type GeolocationConfig struct {
	Latitude  float64 `yaml:"latitude"`  // Manual latitude (-90 to 90)
	Longitude float64 `yaml:"longitude"` // Manual longitude (-180 to 180)
	City      string  `yaml:"city"`      // Optional city name for display
}

// LokiConfig holds configuration for shipping logs to Loki.
type LokiConfig struct {
	Enabled       bool   `yaml:"enabled"`        // Enable Loki log shipping
	URL           string `yaml:"url"`            // Loki push URL (e.g., "http://10.99.0.1:3100")
	BatchSize     int    `yaml:"batch_size"`     // Max entries before flush (default: 100)
	FlushInterval string `yaml:"flush_interval"` // Flush interval (default: "5s")
}

// Validate checks if the geolocation coordinates are within valid ranges.
func (g *GeolocationConfig) Validate() error {
	if g.Latitude < -90 || g.Latitude > 90 {
		return fmt.Errorf("geolocation.latitude must be between -90 and 90, got %f", g.Latitude)
	}
	if g.Longitude < -180 || g.Longitude > 180 {
		return fmt.Errorf("geolocation.longitude must be between -180 and 180, got %f", g.Longitude)
	}
	return nil
}

// IsSet returns true if both latitude and longitude are configured (non-zero).
func (g *GeolocationConfig) IsSet() bool {
	return g.Latitude != 0 && g.Longitude != 0
}

// LoadServerConfig loads server configuration from a YAML file.
func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &ServerConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Apply defaults
	if cfg.MeshCIDR == "" {
		cfg.MeshCIDR = "10.99.0.0/16"
	}
	if cfg.DomainSuffix == "" {
		cfg.DomainSuffix = ".tunnelmesh"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tunnelmesh"
	}
	// Expand home directory in data dir
	if strings.HasPrefix(cfg.DataDir, "~/") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			cfg.DataDir = filepath.Join(homeDir, cfg.DataDir[2:])
		}
	}
	// Admin defaults
	cfg.Admin.Enabled = true
	if cfg.Admin.BindAddress == "" {
		cfg.Admin.BindAddress = "127.0.0.1" // Secure by default - localhost only
	}
	if cfg.Admin.Port == 0 {
		if cfg.JoinMesh != nil {
			cfg.Admin.Port = 443 // Standard HTTPS port for mesh-only admin
		} else {
			cfg.Admin.Port = 8080 // HTTP on localhost
		}
	}
	// Relay enabled by default
	if !cfg.Relay.Enabled {
		cfg.Relay.Enabled = true
	}
	if cfg.Relay.PairTimeout == "" {
		cfg.Relay.PairTimeout = "90s"
	}
	if cfg.HeartbeatInterval == "" {
		cfg.HeartbeatInterval = "10s"
	}

	// WireGuard defaults are applied on demand (endpoint must be set manually)

	// Apply defaults to JoinMesh if configured
	if cfg.JoinMesh != nil {
		if cfg.JoinMesh.SSHPort == 0 {
			cfg.JoinMesh.SSHPort = 2222
		}
		if cfg.JoinMesh.TUN.Name == "" {
			cfg.JoinMesh.TUN.Name = "tun-mesh0"
		}
		if cfg.JoinMesh.TUN.MTU == 0 {
			cfg.JoinMesh.TUN.MTU = 1400
		}
		if cfg.JoinMesh.DNS.Listen == "" {
			cfg.JoinMesh.DNS.Listen = "127.0.0.53:5353"
		}
		if cfg.JoinMesh.DNS.CacheTTL == 0 {
			cfg.JoinMesh.DNS.CacheTTL = 300
		}
		if cfg.JoinMesh.MetricsPort == 0 {
			cfg.JoinMesh.MetricsPort = 9443
		}
		// Loki defaults for JoinMesh
		if cfg.JoinMesh.Loki.Enabled {
			if cfg.JoinMesh.Loki.BatchSize == 0 {
				cfg.JoinMesh.Loki.BatchSize = 100
			}
			if cfg.JoinMesh.Loki.FlushInterval == "" {
				cfg.JoinMesh.Loki.FlushInterval = "5s"
			}
		}
		// Expand home directory in private key path
		if strings.HasPrefix(cfg.JoinMesh.PrivateKey, "~/") {
			homeDir, err := os.UserHomeDir()
			if err == nil {
				cfg.JoinMesh.PrivateKey = filepath.Join(homeDir, cfg.JoinMesh.PrivateKey[2:])
			}
		}
	}

	return cfg, nil
}

// LoadPeerConfig loads peer configuration from a YAML file.
func LoadPeerConfig(path string) (*PeerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &PeerConfig{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Apply defaults
	if cfg.SSHPort == 0 {
		cfg.SSHPort = 2222
	}
	if cfg.TUN.Name == "" {
		cfg.TUN.Name = "tun-mesh0"
	}
	if cfg.TUN.MTU == 0 {
		cfg.TUN.MTU = 1400
	}
	if cfg.DNS.Listen == "" {
		cfg.DNS.Listen = "127.0.0.53:5353"
	}
	if cfg.DNS.CacheTTL == 0 {
		cfg.DNS.CacheTTL = 300
	}
	// DNS enabled by default
	if !cfg.DNS.Enabled && cfg.DNS.Listen == "127.0.0.53:5353" {
		cfg.DNS.Enabled = true
	}
	if cfg.HeartbeatInterval == "" {
		cfg.HeartbeatInterval = "10s"
	}
	if cfg.MetricsPort == 0 {
		cfg.MetricsPort = 9443
	}

	// Expand home directory in private key path
	if strings.HasPrefix(cfg.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			cfg.PrivateKey = filepath.Join(homeDir, cfg.PrivateKey[2:])
		}
	}

	// Loki defaults
	if cfg.Loki.Enabled {
		if cfg.Loki.BatchSize == 0 {
			cfg.Loki.BatchSize = 100
		}
		if cfg.Loki.FlushInterval == "" {
			cfg.Loki.FlushInterval = "5s"
		}
	}

	// WireGuard concentrator defaults
	if cfg.WireGuard.Enabled {
		if cfg.WireGuard.ListenPort == 0 {
			cfg.WireGuard.ListenPort = 51820
		}
		if cfg.WireGuard.Interface == "" {
			cfg.WireGuard.Interface = "wg0"
		}
		if cfg.WireGuard.MTU == 0 {
			cfg.WireGuard.MTU = 1420
		}
		if cfg.WireGuard.SyncInterval == "" {
			cfg.WireGuard.SyncInterval = "30s"
		}
		// Expand home directory in data dir
		if strings.HasPrefix(cfg.WireGuard.DataDir, "~/") {
			homeDir, err := os.UserHomeDir()
			if err == nil {
				cfg.WireGuard.DataDir = filepath.Join(homeDir, cfg.WireGuard.DataDir[2:])
			}
		}
	}

	return cfg, nil
}

// Validate checks if the server configuration is valid.
func (c *ServerConfig) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	if c.AuthToken == "" {
		return fmt.Errorf("auth_token is required")
	}
	if c.MeshCIDR != "" {
		_, _, err := net.ParseCIDR(c.MeshCIDR)
		if err != nil {
			return fmt.Errorf("invalid mesh_cidr: %w", err)
		}
	}
	// Validate JoinMesh if configured
	if c.JoinMesh != nil {
		if c.JoinMesh.Name == "" {
			return fmt.Errorf("join_mesh.name is required")
		}
		if c.JoinMesh.SSHPort <= 0 || c.JoinMesh.SSHPort > 65535 {
			return fmt.Errorf("join_mesh.ssh_port must be between 1 and 65535")
		}
	}
	return nil
}

// Validate checks if the peer configuration is valid.
func (c *PeerConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.Server == "" {
		return fmt.Errorf("server is required")
	}
	if c.SSHPort <= 0 || c.SSHPort > 65535 {
		return fmt.Errorf("ssh_port must be between 1 and 65535")
	}
	if c.TUN.MTU < 576 || c.TUN.MTU > 65535 {
		return fmt.Errorf("tun.mtu must be between 576 and 65535")
	}
	// Validate geolocation if any coordinate is set
	if c.Geolocation.Latitude != 0 || c.Geolocation.Longitude != 0 {
		if err := c.Geolocation.Validate(); err != nil {
			return err
		}
	}
	// Validate DNS aliases
	if err := c.DNS.ValidateAliases(c.Name); err != nil {
		return err
	}
	return nil
}

// ValidateAliases checks if DNS aliases are valid DNS labels.
func (d *DNSConfig) ValidateAliases(peerName string) error {
	seen := make(map[string]bool)
	for _, alias := range d.Aliases {
		if err := validateDNSLabel(alias); err != nil {
			return fmt.Errorf("dns.aliases: %q is invalid: %w", alias, err)
		}
		if alias == peerName {
			return fmt.Errorf("dns.aliases: %q cannot be the same as peer name", alias)
		}
		if seen[alias] {
			return fmt.Errorf("dns.aliases: duplicate alias %q", alias)
		}
		seen[alias] = true
	}
	return nil
}

// validateDNSLabel checks if a string is a valid DNS label (RFC 1123).
func validateDNSLabel(label string) error {
	if len(label) == 0 {
		return fmt.Errorf("empty label")
	}
	if len(label) > 63 {
		return fmt.Errorf("exceeds 63 characters")
	}
	if label != strings.ToLower(label) {
		return fmt.Errorf("must be lowercase")
	}
	// Must start and end with alphanumeric
	if !isAlphanumeric(label[0]) {
		return fmt.Errorf("must start with alphanumeric character")
	}
	if !isAlphanumeric(label[len(label)-1]) {
		return fmt.Errorf("must end with alphanumeric character")
	}
	// Only alphanumeric and hyphens allowed
	for _, c := range label {
		if !isAlphanumeric(byte(c)) && c != '-' && c != '.' {
			return fmt.Errorf("contains invalid character %q", c)
		}
	}
	return nil
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}
