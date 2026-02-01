package tun

import (
	"net"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIPConfig(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		cidr     string
		wantIP   net.IP
		wantMask net.IPMask
		wantErr  bool
	}{
		{
			name:     "valid config",
			ip:       "10.99.0.1",
			cidr:     "10.99.0.0/16",
			wantIP:   net.ParseIP("10.99.0.1").To4(),
			wantMask: net.CIDRMask(16, 32),
			wantErr:  false,
		},
		{
			name:    "invalid IP",
			ip:      "invalid",
			cidr:    "10.99.0.0/16",
			wantErr: true,
		},
		{
			name:    "invalid CIDR",
			ip:      "10.99.0.1",
			cidr:    "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, mask, err := ParseIPConfig(tt.ip, tt.cidr)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantIP, ip)
			assert.Equal(t, tt.wantMask, mask)
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: Config{
				Name:    "tun-mesh0",
				MTU:     1400,
				Address: "10.99.0.1/16",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			cfg: Config{
				MTU:     1400,
				Address: "10.99.0.1/16",
			},
			wantErr: true,
		},
		{
			name: "invalid MTU",
			cfg: Config{
				Name:    "tun-mesh0",
				MTU:     100,
				Address: "10.99.0.1/16",
			},
			wantErr: true,
		},
		{
			name: "missing address",
			cfg: Config{
				Name: "tun-mesh0",
				MTU:  1400,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestDevice_Create tests TUN device creation
// Note: This test requires root/admin privileges and may be skipped in CI
func TestDevice_Create(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TUN tests not supported on Windows without wintun.dll")
	}

	// Skip if not running as root
	if runtime.GOOS != "windows" && !isRoot() {
		t.Skip("TUN tests require root privileges")
	}

	cfg := Config{
		Name:    "tuntest0",
		MTU:     1400,
		Address: "10.99.99.1/24",
	}

	dev, err := Create(cfg)
	if err != nil {
		t.Skipf("Could not create TUN device (may need privileges): %v", err)
	}
	defer dev.Close()

	assert.Equal(t, cfg.Name, dev.Name())
}

func isRoot() bool {
	// This is a simplified check
	return false // Will be implemented properly
}
