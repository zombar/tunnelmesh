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
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"` // Authentication token for admin access (optional, but recommended)
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
	Listen       string                `yaml:"listen"`
	AuthToken    string                `yaml:"auth_token"`
	MeshCIDR     string                `yaml:"mesh_cidr"`
	DomainSuffix string                `yaml:"domain_suffix"`
	DataDir      string                `yaml:"data_dir"` // Data directory for persistence (default: /var/lib/tunnelmesh)
	Admin        AdminConfig           `yaml:"admin"`
	Relay        RelayConfig           `yaml:"relay"`
	WireGuard    WireGuardServerConfig `yaml:"wireguard"`
	JoinMesh     *PeerConfig           `yaml:"join_mesh,omitempty"`
}

// PeerConfig holds configuration for a peer node.
type PeerConfig struct {
	Name       string              `yaml:"name"`
	Server     string              `yaml:"server"`
	AuthToken  string              `yaml:"auth_token"`
	SSHPort    int                 `yaml:"ssh_port"`
	PrivateKey string              `yaml:"private_key"`
	TUN        TUNConfig           `yaml:"tun"`
	DNS        DNSConfig           `yaml:"dns"`
	WireGuard  WireGuardPeerConfig `yaml:"wireguard"`
}

// TUNConfig holds configuration for the TUN interface.
type TUNConfig struct {
	Name string `yaml:"name"`
	MTU  int    `yaml:"mtu"`
}

// DNSConfig holds configuration for the local DNS resolver.
type DNSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Listen   string `yaml:"listen"`
	CacheTTL int    `yaml:"cache_ttl"`
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
	// Admin enabled by default
	cfg.Admin.Enabled = true
	// Relay enabled by default
	if !cfg.Relay.Enabled {
		cfg.Relay.Enabled = true
	}
	if cfg.Relay.PairTimeout == "" {
		cfg.Relay.PairTimeout = "90s"
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

	// Expand home directory in private key path
	if strings.HasPrefix(cfg.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			cfg.PrivateKey = filepath.Join(homeDir, cfg.PrivateKey[2:])
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
	return nil
}
