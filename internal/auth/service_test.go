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

func TestNewServiceUser(t *testing.T) {
	caPrivKey := make([]byte, ed25519.SeedSize)
	for i := range caPrivKey {
		caPrivKey[i] = byte(i)
	}

	user, err := NewServiceUser(caPrivKey, "coordinator")
	require.NoError(t, err)

	assert.Equal(t, "svc:coordinator", user.ID)
	assert.Equal(t, "Coordinator Service", user.Name)
	assert.NotEmpty(t, user.PublicKey)
	assert.True(t, user.IsService())
}

func TestServiceUserID(t *testing.T) {
	assert.Equal(t, "svc:coordinator", ServiceUserID("coordinator"))
	assert.Equal(t, "svc:backup-agent", ServiceUserID("backup-agent"))
}

func TestIsServiceUser(t *testing.T) {
	assert.True(t, IsServiceUser("svc:coordinator"))
	assert.True(t, IsServiceUser("svc:anything"))
	assert.False(t, IsServiceUser("alice"))
	assert.False(t, IsServiceUser("abc123def456"))
}
