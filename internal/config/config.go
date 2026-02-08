// Package config handles configuration loading and validation for tunnelmesh.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/tunnelmesh/tunnelmesh/pkg/bytesize"
	"gopkg.in/yaml.v3"
)

// AdminConfig holds configuration for the admin web interface.
// Admin is only accessible from inside the mesh via HTTPS on mesh IP.
// Requires join_mesh to be configured.
type AdminConfig struct {
	Enabled    bool             `yaml:"enabled"`
	Port       int              `yaml:"port"`       // Port for admin server on mesh IP (default: 443)
	Monitoring MonitoringConfig `yaml:"monitoring"` // Reverse proxy config for Prometheus/Grafana
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

// S3Config holds configuration for the S3-compatible storage service.
type S3Config struct {
	Enabled                bool                   `yaml:"enabled"`                  // Enable S3 storage (default: false)
	DataDir                string                 `yaml:"data_dir"`                 // Storage directory for S3 objects (default: {data_dir}/s3)
	MaxSize                bytesize.Size          `yaml:"max_size"`                 // Maximum storage size (e.g., "10Gi", "500Mi") - required
	Port                   int                    `yaml:"port"`                     // S3 API port (default: 9000)
	ObjectExpiryDays       int                    `yaml:"object_expiry_days"`       // Days until objects expire (default: 9125 = 25 years)
	ShareExpiryDays        int                    `yaml:"share_expiry_days"`        // Days until file shares expire (default: 365 = 1 year)
	TombstoneRetentionDays int                    `yaml:"tombstone_retention_days"` // Days to keep tombstoned items before deletion (default: 90)
	VersionRetentionDays   int                    `yaml:"version_retention_days"`   // Days to keep object versions (default: 30)
	MaxVersionsPerObject   int                    `yaml:"max_versions_per_object"`  // Max versions to keep per object (default: 100, 0 = unlimited)
	VersionRetention       VersionRetentionConfig `yaml:"version_retention"`        // Tiered version retention policy
}

// VersionRetentionConfig configures smart tiered version retention.
// Older versions are kept at decreasing granularity to save space while maintaining history.
type VersionRetentionConfig struct {
	RecentDays    int `yaml:"recent_days"`    // Keep all versions from last N days (default: 7)
	WeeklyWeeks   int `yaml:"weekly_weeks"`   // Then keep one version per week for N weeks (default: 4)
	MonthlyMonths int `yaml:"monthly_months"` // Then keep one version per month for N months (default: 6)
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
	Listen             string                `yaml:"listen"`
	AuthToken          string                `yaml:"auth_token"`
	DataDir            string                `yaml:"data_dir"`             // Data directory for persistence (default: /var/lib/tunnelmesh)
	HeartbeatInterval  string                `yaml:"heartbeat_interval"`   // Heartbeat interval (default: 10s)
	UserExpirationDays int                   `yaml:"user_expiration_days"` // Days until user expires after last seen (default: 270 = 9 months)
	Locations          bool                  `yaml:"locations"`            // Enable node location tracking (requires external IP geolocation API)
	LogLevel           string                `yaml:"log_level"`            // trace, debug, info, warn, error (default: info)
	Admin              AdminConfig           `yaml:"admin"`
	Relay              RelayConfig           `yaml:"relay"`
	WireGuard          WireGuardServerConfig `yaml:"wireguard"`
	S3                 S3Config              `yaml:"s3"`            // S3-compatible storage configuration
	Filter             FilterConfig          `yaml:"filter"`        // Global packet filter rules for all peers
	ServicePorts       []uint16              `yaml:"service_ports"` // Service ports to auto-allow on peers (default: [9443] for metrics)
	JoinMesh           *PeerConfig           `yaml:"join_mesh,omitempty"`
}

// PeerConfig holds configuration for a peer node.
type PeerConfig struct {
	Name              string              `yaml:"name"`
	Server            string              `yaml:"server"`
	AuthToken         string              `yaml:"auth_token"`
	SSHPort           int                 `yaml:"ssh_port"`
	PrivateKey        string              `yaml:"private_key"`
	ControlSocket     string              `yaml:"control_socket"`     // Unix socket path for CLI commands (default: /var/run/tunnelmesh.sock)
	HeartbeatInterval string              `yaml:"heartbeat_interval"` // Heartbeat interval (default: 10s)
	MetricsEnabled    *bool               `yaml:"metrics_enabled"`    // Enable Prometheus metrics (default: true). Disable for 10Gbps+ high-performance networks.
	MetricsPort       int                 `yaml:"metrics_port"`       // Prometheus metrics port on mesh IP (default: 9443)
	LogLevel          string              `yaml:"log_level"`          // trace, debug, info, warn, error (default: info)
	TUN               TUNConfig           `yaml:"tun"`
	DNS               DNSConfig           `yaml:"dns"`
	WireGuard         WireGuardPeerConfig `yaml:"wireguard"`
	Geolocation       GeolocationConfig   `yaml:"geolocation"`        // Manual geolocation coordinates
	ExitNode          string              `yaml:"exit_node"`          // Name of peer to route internet traffic through
	AllowExitTraffic  bool                `yaml:"allow_exit_traffic"` // Allow this node to act as exit node for other peers
	Filter            FilterConfig        `yaml:"filter"`             // Local packet filter rules
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
	URL           string `yaml:"url"`            // Loki push URL (e.g., "http://172.30.0.1:3100")
	BatchSize     int    `yaml:"batch_size"`     // Max entries before flush (default: 100)
	FlushInterval string `yaml:"flush_interval"` // Flush interval (default: "5s")
}

// FilterRule represents a single packet filter rule for config files.
type FilterRule struct {
	Port       uint16 `yaml:"port"`        // Port number (1-65535)
	Protocol   string `yaml:"protocol"`    // "tcp" or "udp"
	Action     string `yaml:"action"`      // "allow" or "deny"
	SourcePeer string `yaml:"source_peer"` // Source peer name (empty = any peer)
}

// ProtocolNumber returns the IP protocol number (6 for TCP, 17 for UDP).
func (r *FilterRule) ProtocolNumber() uint8 {
	switch strings.ToLower(r.Protocol) {
	case "tcp":
		return 6
	case "udp":
		return 17
	default:
		return 0
	}
}

// FilterConfig holds packet filter configuration.
// When default_deny is true (allowlist mode), all ports are blocked unless explicitly allowed.
type FilterConfig struct {
	DefaultDeny *bool        `yaml:"default_deny"` // Block all by default (default: true)
	Rules       []FilterRule `yaml:"rules"`        // Filter rules
}

// IsDefaultDeny returns whether the filter defaults to denying traffic.
// Returns true by default (allowlist mode) if not explicitly set.
func (f *FilterConfig) IsDefaultDeny() bool {
	return f.DefaultDeny == nil || *f.DefaultDeny
}

// Validate checks if filter configuration is valid.
func (f *FilterConfig) Validate() error {
	for i, rule := range f.Rules {
		if rule.Port == 0 {
			return fmt.Errorf("filter.rules[%d].port is required", i)
		}
		proto := strings.ToLower(rule.Protocol)
		if proto != "tcp" && proto != "udp" {
			return fmt.Errorf("filter.rules[%d].protocol must be 'tcp' or 'udp', got %q", i, rule.Protocol)
		}
		if rule.Action != "allow" && rule.Action != "deny" {
			return fmt.Errorf("filter.rules[%d].action must be 'allow' or 'deny', got %q", i, rule.Action)
		}
	}
	return nil
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
	// Admin defaults (only accessible from mesh via HTTPS)
	cfg.Admin.Enabled = true
	if cfg.Admin.Port == 0 {
		cfg.Admin.Port = 443 // Standard HTTPS port for mesh-only admin
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
	if len(cfg.ServicePorts) == 0 {
		cfg.ServicePorts = []uint16{9443} // Default: metrics port for Prometheus scraping
	}
	if cfg.UserExpirationDays == 0 {
		cfg.UserExpirationDays = 270 // Default: 9 months
	}

	// S3 defaults
	if cfg.S3.Enabled {
		if cfg.S3.DataDir == "" {
			cfg.S3.DataDir = filepath.Join(cfg.DataDir, "s3")
		}
		if cfg.S3.Port == 0 {
			cfg.S3.Port = 9000
		}
		if cfg.S3.ObjectExpiryDays == 0 {
			cfg.S3.ObjectExpiryDays = 9125 // Default: 25 years
		}
		if cfg.S3.ShareExpiryDays == 0 {
			cfg.S3.ShareExpiryDays = 365 // Default: 1 year
		}
		if cfg.S3.TombstoneRetentionDays == 0 {
			cfg.S3.TombstoneRetentionDays = 90 // Default: 90 days
		}
		if cfg.S3.VersionRetentionDays == 0 {
			cfg.S3.VersionRetentionDays = 30 // Default: 30 days
		}
		if cfg.S3.MaxVersionsPerObject == 0 {
			cfg.S3.MaxVersionsPerObject = 100 // Default: 100 versions per object
		}
		// Smart retention defaults (tiered pruning)
		if cfg.S3.VersionRetention.RecentDays == 0 {
			cfg.S3.VersionRetention.RecentDays = 7 // Keep all versions from last 7 days
		}
		if cfg.S3.VersionRetention.WeeklyWeeks == 0 {
			cfg.S3.VersionRetention.WeeklyWeeks = 4 // Keep weekly versions for 4 weeks
		}
		if cfg.S3.VersionRetention.MonthlyMonths == 0 {
			cfg.S3.VersionRetention.MonthlyMonths = 6 // Keep monthly versions for 6 months
		}
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
	// Validate filter config
	if err := c.Filter.Validate(); err != nil {
		return err
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

// IsMetricsEnabled returns whether Prometheus metrics collection is enabled.
// Returns true by default (when MetricsEnabled is nil or explicitly true).
// Disable for 10Gbps+ high-performance networks where atomic counter overhead matters.
func (c *PeerConfig) IsMetricsEnabled() bool {
	return c.MetricsEnabled == nil || *c.MetricsEnabled
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
	// Validate filter config
	if err := c.Filter.Validate(); err != nil {
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

// ApplyLogLevel parses the log level string and sets zerolog's global level.
// Returns true if the level was successfully applied, false if the level string
// was empty or invalid.
func ApplyLogLevel(level string) bool {
	if level == "" {
		return false
	}
	parsed, err := zerolog.ParseLevel(level)
	if err != nil {
		return false
	}
	zerolog.SetGlobalLevel(parsed)
	return true
}
