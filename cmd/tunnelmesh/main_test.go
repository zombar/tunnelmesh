package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    string
		expectError bool
		errorMsg    string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "hostname without scheme or port",
			input:    "coord.example.com",
			expected: "https://coord.example.com:8443",
		},
		{
			name:     "hostname with port",
			input:    "coord.example.com:9443",
			expected: "https://coord.example.com:9443",
		},
		{
			name:     "https URL without port",
			input:    "https://coord.example.com",
			expected: "https://coord.example.com:8443",
		},
		{
			name:     "https URL with port",
			input:    "https://coord.example.com:9443",
			expected: "https://coord.example.com:9443",
		},
		{
			name:     "IPv4 without port",
			input:    "192.168.1.1",
			expected: "https://192.168.1.1:8443",
		},
		{
			name:     "IPv4 with port",
			input:    "192.168.1.1:9443",
			expected: "https://192.168.1.1:9443",
		},
		{
			name:     "IPv6 without port",
			input:    "[2001:db8::1]",
			expected: "https://[2001:db8::1]:8443",
		},
		{
			name:     "IPv6 with port",
			input:    "[2001:db8::1]:9443",
			expected: "https://[2001:db8::1]:9443",
		},
		{
			name:     "localhost without port",
			input:    "localhost",
			expected: "https://localhost:8443",
		},
		{
			name:     "localhost with port",
			input:    "localhost:9443",
			expected: "https://localhost:9443",
		},
		{
			name:        "URL with path",
			input:       "coord.example.com/api",
			expectError: true,
			errorMsg:    "should not include path",
		},
		{
			name:        "URL with path and port",
			input:       "coord.example.com:8443/api",
			expectError: true,
			errorMsg:    "should not include path",
		},
		{
			name:        "URL with query parameters",
			input:       "coord.example.com?foo=bar",
			expectError: true,
			errorMsg:    "should not include query parameters",
		},
		{
			name:        "HTTP scheme for remote server (insecure)",
			input:       "http://coord.example.com",
			expectError: true,
			errorMsg:    "must use HTTPS for remote servers",
		},
		{
			name:        "HTTP with port for remote server",
			input:       "http://coord.example.com:8080",
			expectError: true,
			errorMsg:    "must use HTTPS for remote servers",
		},
		{
			name:     "HTTP localhost without port",
			input:    "http://localhost",
			expected: "http://localhost:8443",
		},
		{
			name:     "HTTP localhost with port",
			input:    "http://localhost:8081",
			expected: "http://localhost:8081",
		},
		{
			name:     "HTTP 127.0.0.1 without port",
			input:    "http://127.0.0.1",
			expected: "http://127.0.0.1:8443",
		},
		{
			name:     "HTTP 127.0.0.1 with port",
			input:    "http://127.0.0.1:8081",
			expected: "http://127.0.0.1:8081",
		},
		{
			name:     "HTTP IPv6 localhost without port",
			input:    "http://[::1]",
			expected: "http://[::1]:8443",
		},
		{
			name:     "HTTP IPv6 localhost with port",
			input:    "http://[::1]:8081",
			expected: "http://[::1]:8081",
		},
		{
			name:     "HTTP 127.0.0.50 (loopback range)",
			input:    "http://127.0.0.50:8081",
			expected: "http://127.0.0.50:8081",
		},
		{
			name:        "HTTP private IP (should fail)",
			input:       "http://192.168.1.10:8081",
			expectError: true,
			errorMsg:    "must use HTTPS for remote servers",
		},
		{
			name:        "HTTP public IP (should fail)",
			input:       "http://8.8.8.8:8081",
			expectError: true,
			errorMsg:    "must use HTTPS for remote servers",
		},
		{
			name:        "HTTP with empty hostname (malformed URL)",
			input:       "http://:8443",
			expectError: true,
			errorMsg:    "must use HTTPS for remote servers",
		},
		{
			name:        "URL with path (gets HTTPS prefix added)",
			input:       "ht!tp://invalid",
			expectError: true,
			errorMsg:    "should not include path",
		},
		{
			name:     "URL with trailing slash",
			input:    "https://coord.example.com:8443/",
			expected: "https://coord.example.com:8443/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizeServerURL(tt.input)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestIsValidAuthToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "valid 64 hex characters",
			token:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			expected: true,
		},
		{
			name:     "valid 64 hex characters (uppercase)",
			token:    "A1B2C3D4E5F6A7B8C9D0E1F2A3B4C5D6E7F8A9B0C1D2E3F4A5B6C7D8E9F0A1B2",
			expected: true,
		},
		{
			name:     "valid 64 hex characters (mixed case)",
			token:    "a1B2c3D4e5F6a7B8c9D0e1F2a3B4c5D6e7F8a9B0c1D2e3F4a5B6c7D8e9F0a1B2",
			expected: true,
		},
		{
			name:     "valid all zeros",
			token:    "0000000000000000000000000000000000000000000000000000000000000000",
			expected: true,
		},
		{
			name:     "valid all fs",
			token:    "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			expected: true,
		},
		{
			name:     "empty string",
			token:    "",
			expected: false,
		},
		{
			name:     "too short (63 chars)",
			token:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b",
			expected: false,
		},
		{
			name:     "too long (65 chars)",
			token:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c",
			expected: false,
		},
		{
			name:     "non-hex character (g)",
			token:    "g1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2",
			expected: false,
		},
		{
			name:     "non-hex character (space)",
			token:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1 2",
			expected: false,
		},
		{
			name:     "non-hex character (hyphen)",
			token:    "a1b2c3d4-e5f6-a7b8-c9d0-e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0",
			expected: false,
		},
		{
			name:     "special characters",
			token:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1!@",
			expected: false,
		},
		{
			name:     "only numbers",
			token:    "1234567890123456789012345678901234567890123456789012345678901234",
			expected: true,
		},
		{
			name:     "only letters",
			token:    "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidAuthToken(tt.token)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEnsureCoordinatorConfig(t *testing.T) {
	tests := []struct {
		name                    string
		inputServers            []string
		inputCoordinatorEnabled bool
		inputCoordinatorListen  string
		expectedEnabled         bool
		expectedListen          string
	}{
		{
			name:                    "no servers, coordinator not enabled - should enable with default listen",
			inputServers:            []string{},
			inputCoordinatorEnabled: false,
			inputCoordinatorListen:  "",
			expectedEnabled:         true,
			expectedListen:          ":8443",
		},
		{
			name:                    "no servers, coordinator not enabled, custom listen - should enable with custom listen",
			inputServers:            []string{},
			inputCoordinatorEnabled: false,
			inputCoordinatorListen:  ":9443",
			expectedEnabled:         true,
			expectedListen:          ":9443",
		},
		{
			name:                    "has servers - should not enable coordinator",
			inputServers:            []string{"https://coord.example.com:8443"},
			inputCoordinatorEnabled: false,
			inputCoordinatorListen:  "",
			expectedEnabled:         false,
			expectedListen:          "",
		},
		{
			name:                    "coordinator already enabled - should not change",
			inputServers:            []string{},
			inputCoordinatorEnabled: true,
			inputCoordinatorListen:  ":9443",
			expectedEnabled:         true,
			expectedListen:          ":9443",
		},
		{
			name:                    "has servers and coordinator enabled - should not change",
			inputServers:            []string{"https://coord.example.com:8443"},
			inputCoordinatorEnabled: true,
			inputCoordinatorListen:  ":9443",
			expectedEnabled:         true,
			expectedListen:          ":9443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.PeerConfig{
				Servers: tt.inputServers,
				Coordinator: config.CoordinatorConfig{
					Enabled: tt.inputCoordinatorEnabled,
					Listen:  tt.inputCoordinatorListen,
				},
			}

			ensureCoordinatorConfig(cfg)

			assert.Equal(t, tt.expectedEnabled, cfg.Coordinator.Enabled)
			assert.Equal(t, tt.expectedListen, cfg.Coordinator.Listen)
		})
	}
}
