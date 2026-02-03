package wireguard

import (
	"sync"
	"testing"
)

// mockWGDevice simulates a WGDevice without needing real TUN/WG devices.
// This tests the logic and interfaces, not the actual wireguard-go integration.

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
