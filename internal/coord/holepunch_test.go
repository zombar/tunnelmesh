package coord

import "testing"

func TestIsLocalOrPrivateIP(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		// Loopback
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},

		// Private RFC 1918 ranges
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"10.16.0.5", true}, // DigitalOcean VPC
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.255.255", true},

		// Public IPs
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"167.99.90.202", false},
		{"203.0.113.1", false},

		// Edge cases
		{"172.15.255.255", false}, // Just below private range
		{"172.32.0.0", false},     // Just above private range
		{"11.0.0.1", false},       // Just above 10.x range
		{"192.167.255.255", false},

		// Invalid
		{"", false},
		{"invalid", false},
		{"256.1.1.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			result := isLocalOrPrivateIP(tt.ip)
			if result != tt.expected {
				t.Errorf("isLocalOrPrivateIP(%q) = %v, want %v", tt.ip, result, tt.expected)
			}
		})
	}
}
