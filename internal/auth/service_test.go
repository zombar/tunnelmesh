package auth

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveServiceKeypair(t *testing.T) {
	// Use a test CA private key (32 bytes seed)
	caPrivKey := make([]byte, ed25519.SeedSize)
	for i := range caPrivKey {
		caPrivKey[i] = byte(i)
	}

	serviceName := "coordinator"

	pub, priv, err := DeriveServiceKeypair(caPrivKey, serviceName)
	require.NoError(t, err)

	assert.Len(t, pub, ed25519.PublicKeySize)
	assert.Len(t, priv, ed25519.PrivateKeySize)

	// Should be deterministic
	pub2, priv2, err := DeriveServiceKeypair(caPrivKey, serviceName)
	require.NoError(t, err)
	assert.Equal(t, pub, pub2)
	assert.Equal(t, priv, priv2)
}

func TestDeriveServiceKeypairDifferentServices(t *testing.T) {
	caPrivKey := make([]byte, ed25519.SeedSize)
	for i := range caPrivKey {
		caPrivKey[i] = byte(i)
	}

	pub1, _, err := DeriveServiceKeypair(caPrivKey, "coordinator")
	require.NoError(t, err)

	pub2, _, err := DeriveServiceKeypair(caPrivKey, "backup-agent")
	require.NoError(t, err)

	assert.NotEqual(t, pub1, pub2, "different services should have different keys")
}

func TestNewServicePeer(t *testing.T) {
	caPrivKey := make([]byte, ed25519.SeedSize)
	for i := range caPrivKey {
		caPrivKey[i] = byte(i)
	}

	peer, err := NewServicePeer(caPrivKey, "coordinator")
	require.NoError(t, err)

	assert.Equal(t, "svc:coordinator", peer.ID)
	assert.Equal(t, "Coordinator Service", peer.Name)
	assert.NotEmpty(t, peer.PublicKey)
	assert.True(t, peer.IsService())
}

func TestServicePeerID(t *testing.T) {
	assert.Equal(t, "svc:coordinator", ServicePeerID("coordinator"))
	assert.Equal(t, "svc:backup-agent", ServicePeerID("backup-agent"))
}

func TestIsServicePeer(t *testing.T) {
	assert.True(t, IsServicePeer("svc:coordinator"))
	assert.True(t, IsServicePeer("svc:anything"))
	assert.False(t, IsServicePeer("alice"))
	assert.False(t, IsServicePeer("abc123def456"))
}
