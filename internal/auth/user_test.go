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

func TestComputeUserID(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	userID := ComputeUserID(pubKey)

	// ID should be 16 hex chars (first 8 bytes of SHA256)
	assert.Len(t, userID, 16)
	assert.Regexp(t, "^[0-9a-f]{16}$", userID)
}

func TestComputeUserIDDeterministic(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Same public key should always produce same user ID
	id1 := ComputeUserID(pubKey)
	id2 := ComputeUserID(pubKey)
	assert.Equal(t, id1, id2)
}

func TestComputeUserIDFromBase64(t *testing.T) {
	// Generate a test key pair
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	// Encode public key to base64
	pubKeyB64 := base64.StdEncoding.EncodeToString(pubKey)

	// Should produce same ID as ComputeUserID
	id1 := ComputeUserID(pubKey)
	id2, err := ComputeUserIDFromBase64(pubKeyB64)
	require.NoError(t, err)
	assert.Equal(t, id1, id2)
}

func TestComputeUserIDFromBase64_InvalidBase64(t *testing.T) {
	_, err := ComputeUserIDFromBase64("not-valid-base64!!!")
	assert.Error(t, err)
}

func TestComputeUserIDFromBase64_WrongKeySize(t *testing.T) {
	// Encode a key that's too short
	shortKey := base64.StdEncoding.EncodeToString([]byte("tooshort"))
	_, err := ComputeUserIDFromBase64(shortKey)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid public key size")
}

func TestServiceUser(t *testing.T) {
	// Service users have IDs prefixed with "svc:"
	user := User{
		ID:   "svc:coordinator",
		Name: "Coordinator Service",
	}
	assert.True(t, user.IsService())

	// Regular users don't have the prefix
	user2 := User{
		ID:   "abc123def456",
		Name: "Alice",
	}
	assert.False(t, user2.IsService())
}

// --- User expiration tests ---

func TestUser_IsExpired_NotExpired(t *testing.T) {
	user := User{
		ID:       "abc123def456",
		Name:     "Alice",
		LastSeen: time.Now().Add(-1 * time.Hour), // Seen 1 hour ago
	}

	assert.False(t, user.IsExpired())
}

func TestUser_IsExpired_ExplicitlyExpired(t *testing.T) {
	user := User{
		ID:        "abc123def456",
		Name:      "Alice",
		LastSeen:  time.Now().Add(-1 * time.Hour),
		Expired:   true,
		ExpiredAt: time.Now().Add(-1 * time.Hour),
	}

	assert.True(t, user.IsExpired())
}

func TestUser_IsExpired_LastSeenTooOld(t *testing.T) {
	user := User{
		ID:       "abc123def456",
		Name:     "Alice",
		LastSeen: time.Now().Add(-271 * 24 * time.Hour), // Seen 271 days ago (past 9 month default)
	}

	assert.True(t, user.IsExpired())
}

func TestUser_IsExpired_ServiceUserNeverExpires(t *testing.T) {
	user := User{
		ID:       "svc:coordinator",
		Name:     "Coordinator Service",
		LastSeen: time.Now().Add(-365 * 24 * time.Hour), // Seen 1 year ago
	}

	// Service users don't expire based on time
	assert.False(t, user.IsExpired())
}

func TestUser_IsExpired_ServiceUserCanBeExplicitlyExpired(t *testing.T) {
	user := User{
		ID:        "svc:coordinator",
		Name:      "Coordinator Service",
		Expired:   true,
		ExpiredAt: time.Now(),
	}

	// Service users can still be explicitly expired (when peer is removed)
	assert.True(t, user.IsExpired())
}

func TestUserExpirationDays(t *testing.T) {
	// Verify the default value (9 months)
	assert.Equal(t, 270, GetUserExpirationDays())

	// Test setting custom value
	SetUserExpirationDays(30)
	assert.Equal(t, 30, GetUserExpirationDays())

	// Restore default for other tests
	SetUserExpirationDays(270)
}
