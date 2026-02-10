package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

func TestServer_GenerateAndValidateToken(t *testing.T) {
	srv, err := NewServer(&config.PeerConfig{
		Name:      "test-coord",
		Servers:   []string{"http://localhost:8080"},
		AuthToken: "test-secret-key-12345",
		TUN: config.TUNConfig{
			MTU: 1400,
		},
		Coordinator: config.CoordinatorConfig{
			Enabled: true,
			Listen:  ":8080",
		},
	})
	require.NoError(t, err)
	defer func() { _ = srv.Shutdown() }()

	// Generate token
	token, err := srv.GenerateToken("peer1", "172.30.0.1")
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// Validate token
	claims, err := srv.ValidateToken(token)
	require.NoError(t, err)
	assert.Equal(t, "peer1", claims.PeerName)
	assert.Equal(t, "172.30.0.1", claims.MeshIP)
	assert.Equal(t, "tunnelmesh", claims.Issuer)
}

func TestServer_ValidateToken_InvalidSignature(t *testing.T) {
	srv1, err := NewServer(&config.PeerConfig{
		Name:      "test-coord1",
		Servers:   []string{"http://localhost:8080"},
		AuthToken: "secret-key-1",
		TUN: config.TUNConfig{
			MTU: 1400,
		},
		Coordinator: config.CoordinatorConfig{
			Enabled: true,
			Listen:  ":8080",
		},
	})
	require.NoError(t, err)
	defer func() { _ = srv1.Shutdown() }()

	srv2, err := NewServer(&config.PeerConfig{
		Name:      "test-coord2",
		Servers:   []string{"http://localhost:8080"},
		AuthToken: "secret-key-2", // Different key
		TUN: config.TUNConfig{
			MTU: 1400,
		},
		Coordinator: config.CoordinatorConfig{
			Enabled: true,
			Listen:  ":8080",
		},
	})
	require.NoError(t, err)
	defer func() { _ = srv2.Shutdown() }()

	// Generate token with one server
	token, err := srv1.GenerateToken("peer1", "172.30.0.1")
	require.NoError(t, err)

	// Try to validate with different server (different signing key)
	_, err = srv2.ValidateToken(token)
	assert.Error(t, err)
}

func TestServer_ValidateToken_InvalidToken(t *testing.T) {
	srv, err := NewServer(&config.PeerConfig{
		Name:      "test-coord",
		Servers:   []string{"http://localhost:8080"},
		AuthToken: "test-secret-key",
		TUN: config.TUNConfig{
			MTU: 1400,
		},
		Coordinator: config.CoordinatorConfig{
			Enabled: true,
			Listen:  ":8080",
		},
	})
	require.NoError(t, err)
	defer func() { _ = srv.Shutdown() }()

	// Test with invalid token
	_, err = srv.ValidateToken("invalid-token")
	assert.Error(t, err)

	// Test with empty token
	_, err = srv.ValidateToken("")
	assert.Error(t, err)
}

func TestTokenExpiry(t *testing.T) {
	// Verify the token expiry constant is reasonable
	assert.Equal(t, 24*time.Hour, TokenExpiry)
}
