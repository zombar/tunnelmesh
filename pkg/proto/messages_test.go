package proto

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"192.168.255.255", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"172.15.0.1", false}, // Just outside 172.16.0.0/12
		{"172.32.0.1", false}, // Just outside 172.16.0.0/12
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip).To4()
			assert.Equal(t, tt.private, isPrivateIP(ip))
		})
	}
}

func TestGetLocalIPs(t *testing.T) {
	public, private := GetLocalIPs()

	// At least we should have some IPs (likely private ones)
	// This test just verifies the function doesn't panic
	t.Logf("Public IPs: %v", public)
	t.Logf("Private IPs: %v", private)

	// Verify all returned IPs are valid
	for _, ip := range public {
		assert.NotNil(t, net.ParseIP(ip), "public IP should be valid: %s", ip)
	}
	for _, ip := range private {
		assert.NotNil(t, net.ParseIP(ip), "private IP should be valid: %s", ip)
	}
}

func TestBytesGreaterOrEqual(t *testing.T) {
	tests := []struct {
		a, b     string
		expected bool
	}{
		{"10.0.0.1", "10.0.0.0", true},
		{"10.0.0.0", "10.0.0.1", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"192.168.1.1", "10.0.0.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			a := net.ParseIP(tt.a)
			b := net.ParseIP(tt.b)
			assert.Equal(t, tt.expected, bytesGreaterOrEqual(a, b))
		})
	}
}
