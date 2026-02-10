package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputePeerID(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	peerID := ComputePeerID(pubKey)

	// ID should be 16 hex chars (first 8 bytes of SHA256)
	assert.Len(t, peerID, 16)
	assert.Regexp(t, "^[0-9a-f]{16}$", peerID)
}

func TestComputePeerIDDeterministic(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Same public key should always produce same user ID
	id1 := ComputePeerID(pubKey)
	id2 := ComputePeerID(pubKey)
	assert.Equal(t, id1, id2)
}

func TestComputePeerIDFromBase64(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Encode public key to base64
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)

	// Should produce same ID as ComputePeerID
	id1 := ComputePeerID(pubKey)
	id2, err := ComputePeerIDFromBase64(pubKeyB64)
	require.NoError(t, err)
	assert.Equal(t, id1, id2)
}

func TestComputePeerIDFromBase64_InvalidBase64(t *testing.T) {
	_, err := ComputePeerIDFromBase64("not-valid-base64!!!")
	assert.Error(t, err)
}

func TestComputePeerIDFromBase64_WrongKeySize(t *testing.T) {
	// Encode a key that's too short
	shortKey := base64.StdEncoding.EncodeToString([]byte("tooshort"))
	_, err := ComputePeerIDFromBase64(shortKey)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid public key size")
}

func TestServiceUser(t *testing.T) {
	// Service peers have IDs prefixed with "svc:"
	peer := Peer{
		ID:   "svc:coordinator",
		Name: "Coordinator Service",
	}
	assert.True(t, peer.IsService())

	// Regular peers don't have the prefix
	peer2 := Peer{
		ID:   "abc123def456",
		Name: "Alice",
	}
	assert.False(t, peer2.IsService())
}

// --- User expiration tests ---

func TestPeer_IsExpired_NotExpired(t *testing.T) {
	peer := Peer{
		ID:       "abc123def456",
		Name:     "Alice",
		LastSeen: time.Now().Add(-1 * time.Hour), // Seen 1 hour ago
	}

	assert.False(t, peer.IsExpired())
}

func TestPeer_IsExpired_ExplicitlyExpired(t *testing.T) {
	expiredAt := time.Now().Add(-1 * time.Hour)
	peer := Peer{
		ID:        "abc123def456",
		Name:      "Alice",
		LastSeen:  time.Now().Add(-1 * time.Hour),
		Expired:   true,
		ExpiredAt: &expiredAt,
	}

	assert.True(t, peer.IsExpired())
}

func TestPeer_IsExpired_LastSeenTooOld(t *testing.T) {
	peer := Peer{
		ID:       "abc123def456",
		Name:     "Alice",
		LastSeen: time.Now().Add(-271 * 24 * time.Hour), // Seen 271 days ago (past 9 month default)
	}

	assert.True(t, peer.IsExpired())
}

func TestPeer_IsExpired_ServiceUserNeverExpires(t *testing.T) {
	peer := Peer{
		ID:       "svc:coordinator",
		Name:     "Coordinator Service",
		LastSeen: time.Now().Add(-365 * 24 * time.Hour), // Seen 1 year ago
	}

	// Service peers don't expire based on time
	assert.False(t, peer.IsExpired())
}

func TestPeer_IsExpired_ServiceUserCanBeExplicitlyExpired(t *testing.T) {
	expiredAt := time.Now()
	peer := Peer{
		ID:        "svc:coordinator",
		Name:      "Coordinator Service",
		Expired:   true,
		ExpiredAt: &expiredAt,
	}

	// Service peers can still be explicitly expired (when peer is removed)
	assert.True(t, peer.IsExpired())
}

func TestPeerExpirationDays(t *testing.T) {
	// Verify the default value (9 months)
	assert.Equal(t, 270, GetPeerExpirationDays())

	// Test setting custom value
	SetPeerExpirationDays(30)
	assert.Equal(t, 30, GetPeerExpirationDays())

	// Restore default for other tests
	SetPeerExpirationDays(270)
}
