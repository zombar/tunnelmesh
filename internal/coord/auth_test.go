package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_GenerateAndValidateToken(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.AuthToken = "test-secret-key-12345"
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Listen = ":8080"

	srv, err := NewServer(cfg)
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
	cfg1 := newTestConfig(t)
	cfg1.Name = "test-coord1"
	cfg1.AuthToken = "secret-key-1"
	cfg1.Coordinator.Enabled = true
	cfg1.Coordinator.Listen = ":8080"

	srv1, err := NewServer(cfg1)
	require.NoError(t, err)
	defer func() { _ = srv1.Shutdown() }()

	cfg2 := newTestConfig(t)
	cfg2.Name = "test-coord2"
	cfg2.AuthToken = "secret-key-2" // Different key
	cfg2.Coordinator.Enabled = true
	cfg2.Coordinator.Listen = ":8080"

	srv2, err := NewServer(cfg2)
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
	cfg := newTestConfig(t)
	cfg.AuthToken = "test-secret-key"
	cfg.Coordinator.Enabled = true
	cfg.Coordinator.Listen = ":8080"

	srv, err := NewServer(cfg)
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
