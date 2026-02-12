package mesh

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrGenerateCredentials(t *testing.T) {
	// Create temp directory for test keys
	tmpDir, err := os.MkdirTemp("", "mesh-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	keyPath := filepath.Join(tmpDir, "test_key")

	// First call should generate new key
	creds1, err := LoadOrGenerateCredentials(keyPath)
	require.NoError(t, err)
	require.NotNil(t, creds1)

	// Check credentials are populated
	assert.NotEmpty(t, creds1.PublicKey)
	assert.NotEmpty(t, creds1.AccessKey)
	assert.NotEmpty(t, creds1.SecretKey)
	assert.NotNil(t, creds1.PrivateKey)

	// Access key should be 20 chars
	assert.Len(t, creds1.AccessKey, 20)
	// Secret key should be 40 chars
	assert.Len(t, creds1.SecretKey, 40)

	// Second call should load existing key
	creds2, err := LoadOrGenerateCredentials(keyPath)
	require.NoError(t, err)
	require.NotNil(t, creds2)

	// Should be identical
	assert.Equal(t, creds1.PublicKey, creds2.PublicKey)
	assert.Equal(t, creds1.AccessKey, creds2.AccessKey)
	assert.Equal(t, creds1.SecretKey, creds2.SecretKey)
}

func TestLoadOrGenerateCredentials_DefaultPath(t *testing.T) {
	// Test with empty path - should use default
	// This will create ~/.tunnelmesh/s3bench_key, so we skip this test
	// if HOME is not set or if we don't want to pollute user's home dir
	if os.Getenv("CI") != "true" {
		t.Skip("Skipping default path test to avoid polluting home directory")
	}

	creds, err := LoadOrGenerateCredentials("")
	require.NoError(t, err)
	require.NotNil(t, creds)

	// Check credentials are populated
	assert.NotEmpty(t, creds.PublicKey)
	assert.NotEmpty(t, creds.AccessKey)
	assert.NotEmpty(t, creds.SecretKey)
}

func TestCredentials_S3CredentialDerivation(t *testing.T) {
	// Create temp directory for test keys
	tmpDir, err := os.MkdirTemp("", "mesh-test-*")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(tmpDir) }()

	keyPath := filepath.Join(tmpDir, "test_key")

	// Generate credentials
	creds, err := LoadOrGenerateCredentials(keyPath)
	require.NoError(t, err)

	// Verify S3 credentials are deterministically derived from public key
	// Re-load and verify they're the same
	creds2, err := LoadOrGenerateCredentials(keyPath)
	require.NoError(t, err)

	assert.Equal(t, creds.AccessKey, creds2.AccessKey, "Access key should be deterministic")
	assert.Equal(t, creds.SecretKey, creds2.SecretKey, "Secret key should be deterministic")
}
