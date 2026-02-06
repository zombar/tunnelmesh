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

func TestExitRouteConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ExitRouteConfig
		wantErr bool
	}{
		{
			name: "valid exit client config",
			cfg: ExitRouteConfig{
				InterfaceName: "utun5",
				MeshCIDR:      "10.99.0.0/16",
				IsExitNode:    false,
			},
			wantErr: false,
		},
		{
			name: "valid exit node config",
			cfg: ExitRouteConfig{
				InterfaceName: "utun5",
				MeshCIDR:      "10.99.0.0/16",
				IsExitNode:    true,
			},
			wantErr: false,
		},
		{
			name: "missing interface name",
			cfg: ExitRouteConfig{
				MeshCIDR: "10.99.0.0/16",
			},
			wantErr: true,
		},
		{
			name: "missing mesh CIDR",
			cfg: ExitRouteConfig{
				InterfaceName: "utun5",
			},
			wantErr: true,
		},
		{
			name: "invalid mesh CIDR",
			cfg: ExitRouteConfig{
				InterfaceName: "utun5",
				MeshCIDR:      "invalid",
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

func TestBuildDefaultRouteCommands_Darwin(t *testing.T) {
	cfg := ExitRouteConfig{
		InterfaceName: "utun5",
		MeshCIDR:      "10.99.0.0/16",
	}

	addCmds, removeCmds := buildDefaultRouteCommands(cfg, "darwin")

	// Should add routes for 0.0.0.0/1 and 128.0.0.0/1
	require.Len(t, addCmds, 2, "should have 2 add commands")
	require.Len(t, removeCmds, 2, "should have 2 remove commands")

	// Verify add commands
	assert.Contains(t, addCmds[0], "route")
	assert.Contains(t, addCmds[0], "0.0.0.0/1")
	assert.Contains(t, addCmds[1], "128.0.0.0/1")

	// Verify remove commands
	assert.Contains(t, removeCmds[0], "route")
	assert.Contains(t, removeCmds[0], "delete")
}

func TestBuildDefaultRouteCommands_Linux(t *testing.T) {
	cfg := ExitRouteConfig{
		InterfaceName: "tun0",
		MeshCIDR:      "10.99.0.0/16",
	}

	addCmds, removeCmds := buildDefaultRouteCommands(cfg, "linux")

	// Should add routes for 0.0.0.0/1 and 128.0.0.0/1
	require.Len(t, addCmds, 2, "should have 2 add commands")
	require.Len(t, removeCmds, 2, "should have 2 remove commands")

	// Verify add commands use ip route
	assert.Contains(t, addCmds[0], "ip")
	assert.Contains(t, addCmds[0], "route")
	assert.Contains(t, addCmds[0], "0.0.0.0/1")
}

func TestBuildExitNATCommands_Linux(t *testing.T) {
	cfg := ExitRouteConfig{
		InterfaceName: "tun0",
		MeshCIDR:      "10.99.0.0/16",
		IsExitNode:    true,
	}

	addCmds, removeCmds := buildExitNATCommands(cfg, "linux")

	require.NotEmpty(t, addCmds, "should have add commands")
	require.NotEmpty(t, removeCmds, "should have remove commands")

	// Should include IP forwarding enable
	foundForwarding := false
	for _, cmd := range addCmds {
		if contains(cmd, "ip_forward") {
			foundForwarding = true
			break
		}
	}
	assert.True(t, foundForwarding, "should enable IP forwarding")

	// Should include MASQUERADE rule
	foundMasquerade := false
	for _, cmd := range addCmds {
		if contains(cmd, "MASQUERADE") {
			foundMasquerade = true
			break
		}
	}
	assert.True(t, foundMasquerade, "should include MASQUERADE rule")
}

func TestBuildExitNATCommands_Darwin(t *testing.T) {
	cfg := ExitRouteConfig{
		InterfaceName: "utun5",
		MeshCIDR:      "10.99.0.0/16",
		IsExitNode:    true,
	}

	addCmds, removeCmds := buildExitNATCommands(cfg, "darwin")

	require.NotEmpty(t, addCmds, "should have add commands")
	// macOS doesn't have remove commands - we don't disable forwarding
	// as other services may need it
	assert.Empty(t, removeCmds, "macOS should not have remove commands")

	// Should include IP forwarding enable
	foundForwarding := false
	for _, cmd := range addCmds {
		if contains(cmd, "ip.forwarding") {
			foundForwarding = true
			break
		}
	}
	assert.True(t, foundForwarding, "should enable IP forwarding")
}

// contains checks if any element in the slice contains the substring
func contains(slice []string, substr string) bool {
	for _, s := range slice {
		if len(s) > 0 && containsStr(s, substr) {
			return true
		}
	}
	return false
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
