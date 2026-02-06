package wireguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServerKeysGeneration(t *testing.T) {
	tmpDir := t.TempDir()

	keys, err := LoadOrGenerateServerKeys(tmpDir)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	if keys.PrivateKey == "" {
		t.Error("private key should not be empty")
	}
	if keys.PublicKey == "" {
		t.Error("public key should not be empty")
	}
	if keys.CreatedAt.IsZero() {
		t.Error("created_at should be set")
	}

	// Keys should be persisted
	keyFile := filepath.Join(tmpDir, "keys.json")
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		t.Error("key file should be created")
	}
}

func TestServerKeysPersistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate keys
	keys1, err := LoadOrGenerateServerKeys(tmpDir)
	if err != nil {
		t.Fatalf("failed to generate keys: %v", err)
	}

	// Load again - should get same keys
	keys2, err := LoadOrGenerateServerKeys(tmpDir)
	if err != nil {
		t.Fatalf("failed to load keys: %v", err)
	}

	if keys1.PrivateKey != keys2.PrivateKey {
		t.Error("private key should persist")
	}
	if keys1.PublicKey != keys2.PublicKey {
		t.Error("public key should persist")
	}
}

func TestDeviceConfigGeneration(t *testing.T) {
	cfg := &DeviceConfig{
		PrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
		ListenPort: 51820,
		Peers: []PeerConfig{
			{
				PublicKey:  "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
				AllowedIPs: []string{"172.30.100.1/32"},
			},
		},
	}

	uapi := cfg.ToUAPI()

	// Should contain private key
	if !containsLine(uapi, "private_key=") {
		t.Error("UAPI should contain private_key")
	}

	// Should contain listen port
	if !containsLine(uapi, "listen_port=51820") {
		t.Error("UAPI should contain listen_port")
	}

	// Should contain peer section
	if !containsLine(uapi, "public_key=") {
		t.Error("UAPI should contain public_key for peer")
	}

	if !containsLine(uapi, "allowed_ip=172.30.100.1/32") {
		t.Error("UAPI should contain allowed_ip for peer")
	}
}

func TestDeviceConfigNoPeers(t *testing.T) {
	cfg := &DeviceConfig{
		PrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
		ListenPort: 51820,
		Peers:      []PeerConfig{},
	}

	uapi := cfg.ToUAPI()

	// Should still have private key and listen port
	if !containsLine(uapi, "private_key=") {
		t.Error("UAPI should contain private_key")
	}
	if !containsLine(uapi, "listen_port=51820") {
		t.Error("UAPI should contain listen_port")
	}
}

func TestPeerConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		peer    PeerConfig
		wantErr bool
	}{
		{
			name: "valid peer",
			peer: PeerConfig{
				PublicKey:  "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
				AllowedIPs: []string{"172.30.100.1/32"},
			},
			wantErr: false,
		},
		{
			name: "missing public key",
			peer: PeerConfig{
				AllowedIPs: []string{"172.30.100.1/32"},
			},
			wantErr: true,
		},
		{
			name: "missing allowed IPs",
			peer: PeerConfig{
				PublicKey: "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.peer.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func containsLine(s, substr string) bool {
	for _, line := range splitLines(s) {
		if len(line) >= len(substr) && line[:len(substr)] == substr {
			return true
		}
		if line == substr {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	var current string
	for _, c := range s {
		if c == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
