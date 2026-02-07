package auth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUserFromMnemonic(t *testing.T) {
	mnemonic := "abandon ability able"

	user, err := NewUserFromMnemonic(mnemonic, "Alice")
	require.NoError(t, err)

	assert.NotEmpty(t, user.ID)
	assert.NotEmpty(t, user.PublicKey)
	assert.Equal(t, "Alice", user.Name)
	assert.False(t, user.CreatedAt.IsZero())
	assert.False(t, user.IsService())
}

func TestNewUserFromMnemonicDeterministic(t *testing.T) {
	mnemonic := "abandon ability able"

	user1, err := NewUserFromMnemonic(mnemonic, "Alice")
	require.NoError(t, err)

	user2, err := NewUserFromMnemonic(mnemonic, "Bob")
	require.NoError(t, err)

	// Same mnemonic = same ID and public key (name is metadata only)
	assert.Equal(t, user1.ID, user2.ID)
	assert.Equal(t, user1.PublicKey, user2.PublicKey)
	assert.NotEqual(t, user1.Name, user2.Name)
}

func TestUserID(t *testing.T) {
	mnemonic := "abandon ability able"

	user, err := NewUserFromMnemonic(mnemonic, "")
	require.NoError(t, err)

	// ID should be 16 hex chars (first 8 bytes of SHA256)
	assert.Len(t, user.ID, 16)
	assert.Regexp(t, "^[0-9a-f]{16}$", user.ID)
}

func TestUserPublicKeyEncodeDecode(t *testing.T) {
	mnemonic := "abandon ability able"

	user, err := NewUserFromMnemonic(mnemonic, "")
	require.NoError(t, err)

	// PublicKey should be valid base64
	decoded, err := base64.StdEncoding.DecodeString(user.PublicKey)
	require.NoError(t, err)
	assert.Len(t, decoded, 32) // ED25519 public key is 32 bytes
}

func TestUserIdentitySaveLoad(t *testing.T) {
	tempDir := t.TempDir()
	identityPath := filepath.Join(tempDir, "user.json")

	// Create and save identity
	mnemonic := "abandon ability able"
	identity, err := NewUserIdentity(mnemonic, "TestUser")
	require.NoError(t, err)

	err = identity.Save(identityPath)
	require.NoError(t, err)

	// Load identity
	loaded, err := LoadUserIdentity(identityPath)
	require.NoError(t, err)

	assert.Equal(t, identity.User.ID, loaded.User.ID)
	assert.Equal(t, identity.User.PublicKey, loaded.User.PublicKey)
	assert.Equal(t, identity.User.Name, loaded.User.Name)
}

func TestUserIdentityLoadNotExist(t *testing.T) {
	_, err := LoadUserIdentity("/nonexistent/path/user.json")
	assert.True(t, os.IsNotExist(err))
}

func TestUserIdentitySign(t *testing.T) {
	mnemonic := "abandon ability able"
	identity, err := NewUserIdentity(mnemonic, "")
	require.NoError(t, err)

	message := []byte("test message")
	signature := identity.Sign(message)

	assert.Len(t, signature, 64) // ED25519 signature is 64 bytes

	// Verify signature
	valid := identity.Verify(message, signature)
	assert.True(t, valid)

	// Invalid message should fail verification
	valid = identity.Verify([]byte("wrong message"), signature)
	assert.False(t, valid)
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

func TestVerifyUserSignature(t *testing.T) {
	mnemonic := "abandon ability able"
	identity, err := NewUserIdentity(mnemonic, "TestUser")
	require.NoError(t, err)

	message := identity.User.ID

	// Sign the message
	signature, err := identity.SignMessage(message)
	require.NoError(t, err)

	// Verify should succeed
	valid := VerifyUserSignature(identity.User.PublicKey, message, signature)
	assert.True(t, valid)

	// Wrong message should fail
	valid = VerifyUserSignature(identity.User.PublicKey, "wrong-message", signature)
	assert.False(t, valid)

	// Wrong signature should fail
	valid = VerifyUserSignature(identity.User.PublicKey, message, "invalidsignature")
	assert.False(t, valid)

	// Invalid public key should fail
	valid = VerifyUserSignature("invalidpubkey", message, signature)
	assert.False(t, valid)
}

func TestSignMessage(t *testing.T) {
	mnemonic := "abandon ability able"
	identity, err := NewUserIdentity(mnemonic, "TestUser")
	require.NoError(t, err)

	signature, err := identity.SignMessage("test message")
	require.NoError(t, err)

	// Signature should be valid base64
	decoded, err := base64.StdEncoding.DecodeString(signature)
	require.NoError(t, err)
	assert.Len(t, decoded, 64) // ED25519 signature is 64 bytes
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
		LastSeen: time.Now().Add(-31 * 24 * time.Hour), // Seen 31 days ago
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
	// Verify the constant value
	assert.Equal(t, 30, UserExpirationDays)
}
