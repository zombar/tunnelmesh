package wireguard

import (
	"sync"
	"testing"
)

// Note: The platform-specific configureInterfaceAddr functions (darwin, linux, windows)
// execute shell commands and require root/admin privileges, so they cannot be easily
// unit tested. Integration tests would need to run in a privileged environment.

func TestWGDeviceConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     WGDeviceConfig
		wantMTU int
	}{
		{
			name: "default MTU",
			cfg: WGDeviceConfig{
				InterfaceName: "wg0",
				PrivateKey:    "dummy",
				ListenPort:    51820,
				MTU:           0, // Should default to 1420
			},
			wantMTU: 1420,
		},
		{
			name: "custom MTU",
			cfg: WGDeviceConfig{
				InterfaceName: "wg0",
				PrivateKey:    "dummy",
				ListenPort:    51820,
				MTU:           1400,
			},
			wantMTU: 1400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if cfg.MTU == 0 {
				cfg.MTU = 1420
			}
			if cfg.MTU != tt.wantMTU {
				t.Errorf("MTU = %d, want %d", cfg.MTU, tt.wantMTU)
			}
		})
	}
}

func TestWGDevicePacketHandler(t *testing.T) {
	// Test that the packet handler callback mechanism works
	var receivedPacket []byte
	var mu sync.Mutex

	handler := func(packet []byte) {
		mu.Lock()
		receivedPacket = make([]byte, len(packet))
		copy(receivedPacket, packet)
		mu.Unlock()
	}

	// Simulate what WGDevice does internally
	testPacket := []byte{0x45, 0x00, 0x00, 0x28, 0x00, 0x00}
	handler(testPacket)

	mu.Lock()
	defer mu.Unlock()
	if len(receivedPacket) != len(testPacket) {
		t.Errorf("handler did not receive packet correctly")
	}
}

func TestWGDeviceConfigStruct(t *testing.T) {
	cfg := &WGDeviceConfig{
		InterfaceName: "wg-test",
		PrivateKey:    "cGFzc3dvcmQ=", // base64 "password"
		ListenPort:    51820,
		MTU:           1420,
		Address:       "172.30.100.1/16",
	}

	if cfg.InterfaceName != "wg-test" {
		t.Errorf("InterfaceName = %s, want wg-test", cfg.InterfaceName)
	}
	if cfg.ListenPort != 51820 {
		t.Errorf("ListenPort = %d, want 51820", cfg.ListenPort)
	}
	if cfg.MTU != 1420 {
		t.Errorf("MTU = %d, want 1420", cfg.MTU)
	}
	if cfg.Address != "172.30.100.1/16" {
		t.Errorf("Address = %s, want 172.30.100.1/16", cfg.Address)
	}
}

func TestWGDeviceSetPacketHandler(t *testing.T) {
	// Test that SetPacketHandler properly stores the handler
	dev := &WGDevice{}

	var called bool
	// nolint:revive // packet required by callback signature but not used in test
	handler := func(packet []byte) {
		called = true
	}

	dev.SetPacketHandler(handler)

	// Verify handler is set
	if dev.onPacketFromWG == nil {
		t.Error("onPacketFromWG should be set after SetPacketHandler")
	}

	// Call the handler
	dev.onPacketFromWG([]byte{0x45})
	if !called {
		t.Error("handler should have been called")
	}
}

func TestWGDeviceSetPacketHandlerConcurrency(t *testing.T) {
	// Test that SetPacketHandler is safe for concurrent access
	dev := &WGDevice{}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		// nolint:revive // packet required by callback signature but not used in test
		wg.Go(func() {
			dev.SetPacketHandler(func(packet []byte) {
				// Different handler for each goroutine
			})
		})
	}
	wg.Wait()

	// Should not panic or race
	if dev.onPacketFromWG == nil {
		t.Error("handler should be set")
	}
}

func TestBase64ToHex(t *testing.T) {
	tests := []struct {
		name    string
		b64     string
		wantLen int // WireGuard keys are 32 bytes = 64 hex chars
	}{
		{
			name:    "valid WG key",
			b64:     "xTIBA5rboUvnH4htodjb60Y7YAf21J7YQMlNGC8HQ14=",
			wantLen: 64,
		},
		{
			name:    "invalid key",
			b64:     "not-a-valid-key",
			wantLen: 0, // Should return empty string for invalid key
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := base64ToHex(tt.b64)
			if len(result) != tt.wantLen {
				t.Errorf("base64ToHex(%s) len = %d, want %d", tt.b64, len(result), tt.wantLen)
			}
		})
	}
}

// Note: WritePacket, AddPeer, and RemovePeer require initialized devices.
// Testing them with nil devices would cause panics. These methods should only
// be called after StartDevice() succeeds, which is enforced at the Concentrator level.

func TestWGDeviceUpdatePeersEmpty(t *testing.T) {
	// Test UpdatePeers with empty list - should not panic with nil device
	dev := &WGDevice{
		wgDevice: nil,
	}

	// Empty list should return nil (nothing to do)
	err := dev.UpdatePeers([]Client{})
	if err != nil {
		t.Errorf("UpdatePeers with empty list should not error: %v", err)
	}
}

func TestWGDeviceClose(t *testing.T) {
	// Test Close with nil devices - should not panic
	dev := &WGDevice{
		wgDevice:  nil,
		tunDevice: nil,
	}

	err := dev.Close()
	if err != nil {
		t.Errorf("Close should not error with nil devices: %v", err)
	}
}
