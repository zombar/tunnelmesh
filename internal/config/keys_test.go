package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tunnelmesh/tunnelmesh/testutil"
	"golang.org/x/crypto/ssh"
)

func TestGenerateKeyPair(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	privPath := filepath.Join(dir, "id_ed25519")
	pubPath := filepath.Join(dir, "id_ed25519.pub")

	err := GenerateKeyPair(privPath)
	require.NoError(t, err)

	// Check files exist
	_, err = os.Stat(privPath)
	assert.NoError(t, err, "private key should exist")

	_, err = os.Stat(pubPath)
	assert.NoError(t, err, "public key should exist")

	// Check private key permissions (skip on Windows as it handles permissions differently)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(privPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm(), "private key should have 0600 permissions")
	}

	// Verify we can load the generated keys
	_, err = LoadPrivateKey(privPath)
	assert.NoError(t, err, "should be able to load generated private key")
}

func TestLoadPrivateKey(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	privPath, _ := testutil.WriteSSHKeyPair(t, dir)

	signer, err := LoadPrivateKey(privPath)
	require.NoError(t, err)
	assert.NotNil(t, signer)

	// Verify it's an ED25519 key
	assert.Equal(t, ssh.KeyAlgoED25519, signer.PublicKey().Type())
}

func TestLoadPrivateKey_NotFound(t *testing.T) {
	_, err := LoadPrivateKey("/nonexistent/path/key")
	assert.Error(t, err)
}

func TestLoadPrivateKey_InvalidFormat(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	// Write invalid key data
	invalidPath := testutil.TempFile(t, dir, "invalid_key", "not a valid key")

	_, err := LoadPrivateKey(invalidPath)
	assert.Error(t, err)
}

func TestLoadAuthorizedKeys(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	// Generate two key pairs
	_, pub1 := testutil.GenerateSSHKeyPair(t)
	_, pub2 := testutil.GenerateSSHKeyPair(t)

	// Write authorized_keys file
	authKeysContent := string(ssh.MarshalAuthorizedKey(pub1)) + string(ssh.MarshalAuthorizedKey(pub2))
	authKeysPath := testutil.TempFile(t, dir, "authorized_keys", authKeysContent)

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestLoadAuthorizedKeys_Empty(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	authKeysPath := testutil.TempFile(t, dir, "authorized_keys", "")

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 0)
}

func TestLoadAuthorizedKeys_WithComments(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	_, pub1 := testutil.GenerateSSHKeyPair(t)

	// Write with comment line
	content := "# This is a comment\n" + string(ssh.MarshalAuthorizedKey(pub1))
	authKeysPath := testutil.TempFile(t, dir, "authorized_keys", content)

	keys, err := LoadAuthorizedKeys(authKeysPath)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
}

func TestEnsureKeyPairExists(t *testing.T) {
	dir, cleanup := testutil.TempDir(t)
	defer cleanup()

	privPath := filepath.Join(dir, "id_ed25519")

	// First call should generate keys
	signer1, err := EnsureKeyPairExists(privPath)
	require.NoError(t, err)
	assert.NotNil(t, signer1)

	// Second call should load existing keys
	signer2, err := EnsureKeyPairExists(privPath)
	require.NoError(t, err)
	assert.NotNil(t, signer2)

	// Keys should be the same
	assert.Equal(t,
		ssh.MarshalAuthorizedKey(signer1.PublicKey()),
		ssh.MarshalAuthorizedKey(signer2.PublicKey()),
	)
}

func TestGetPublicKeyFingerprint(t *testing.T) {
	_, pub := testutil.GenerateSSHKeyPair(t)

	fp := GetPublicKeyFingerprint(pub)
	assert.NotEmpty(t, fp)
	assert.Contains(t, fp, "SHA256:")
}
