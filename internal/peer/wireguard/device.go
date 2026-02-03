// Package wireguard provides WireGuard concentrator functionality for tunnelmesh peers.
package wireguard

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ServerKeys contains the WireGuard server key pair.
type ServerKeys struct {
	PrivateKey string    `json:"private_key"`
	PublicKey  string    `json:"public_key"`
	CreatedAt  time.Time `json:"created_at"`
}

// LoadOrGenerateServerKeys loads existing keys or generates new ones.
func LoadOrGenerateServerKeys(dataDir string) (*ServerKeys, error) {
	keyFile := filepath.Join(dataDir, "keys.json")

	// Try to load existing keys
	if data, err := os.ReadFile(keyFile); err == nil {
		var keys ServerKeys
		if err := json.Unmarshal(data, &keys); err == nil {
			return &keys, nil
		}
	}

	// Generate new keys
	privateKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate private key: %w", err)
	}

	keys := &ServerKeys{
		PrivateKey: privateKey.String(),
		PublicKey:  privateKey.PublicKey().String(),
		CreatedAt:  time.Now(),
	}

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// Save keys
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal keys: %w", err)
	}

	if err := os.WriteFile(keyFile, data, 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}

	return keys, nil
}

// DeviceConfig contains the configuration for a WireGuard device.
type DeviceConfig struct {
	PrivateKey string
	ListenPort int
	Peers      []PeerConfig
}

// PeerConfig contains the configuration for a WireGuard peer.
type PeerConfig struct {
	PublicKey  string
	AllowedIPs []string
}

// Validate validates the peer configuration.
func (p *PeerConfig) Validate() error {
	if p.PublicKey == "" {
		return errors.New("public key is required")
	}
	if len(p.AllowedIPs) == 0 {
		return errors.New("allowed IPs are required")
	}
	return nil
}

// ToUAPI converts the device config to WireGuard UAPI format.
func (c *DeviceConfig) ToUAPI() string {
	var sb strings.Builder

	// Convert base64 private key to hex for UAPI
	privKeyHex := base64ToHex(c.PrivateKey)
	sb.WriteString(fmt.Sprintf("private_key=%s\n", privKeyHex))
	sb.WriteString(fmt.Sprintf("listen_port=%d\n", c.ListenPort))

	for _, peer := range c.Peers {
		pubKeyHex := base64ToHex(peer.PublicKey)
		sb.WriteString(fmt.Sprintf("public_key=%s\n", pubKeyHex))
		sb.WriteString("replace_allowed_ips=true\n")
		for _, ip := range peer.AllowedIPs {
			sb.WriteString(fmt.Sprintf("allowed_ip=%s\n", ip))
		}
	}

	return sb.String()
}

// base64ToHex converts a base64-encoded WireGuard key to hex format.
func base64ToHex(b64 string) string {
	// WireGuard keys are 32 bytes
	key, err := wgtypes.ParseKey(b64)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(key[:])
}
