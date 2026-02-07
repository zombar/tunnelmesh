package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRegistration(t *testing.T) {
	reg := NewRegistration("abc123", "work-mesh")

	assert.Equal(t, "abc123", reg.UserID)
	assert.Equal(t, "work-mesh", reg.MeshDomain)
	assert.False(t, reg.RegisteredAt.IsZero())
	assert.Empty(t, reg.Roles)
}

func TestRegistrationSaveLoad(t *testing.T) {
	tempDir := t.TempDir()
	regPath := filepath.Join(tempDir, "registration.json")

	reg := &Registration{
		UserID:       "abc123def456",
		MeshDomain:   "work.tunnelmesh",
		RegisteredAt: time.Now().UTC(),
		Certificate:  "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		Roles:        []string{"admin", "bucket-read"},
		S3AccessKey:  "AKIAIOSFODNN7EXAMPLE",
		S3SecretKey:  "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}

	err := reg.Save(regPath)
	require.NoError(t, err)

	loaded, err := LoadRegistration(regPath)
	require.NoError(t, err)

	assert.Equal(t, reg.UserID, loaded.UserID)
	assert.Equal(t, reg.MeshDomain, loaded.MeshDomain)
	assert.Equal(t, reg.Certificate, loaded.Certificate)
	assert.Equal(t, reg.Roles, loaded.Roles)
	assert.Equal(t, reg.S3AccessKey, loaded.S3AccessKey)
	assert.Equal(t, reg.S3SecretKey, loaded.S3SecretKey)
}

func TestRegistrationLoadNotExist(t *testing.T) {
	_, err := LoadRegistration("/nonexistent/path/registration.json")
	assert.True(t, os.IsNotExist(err))
}

func TestRegistrationHasRole(t *testing.T) {
	reg := &Registration{
		Roles: []string{"admin", "bucket-read"},
	}

	assert.True(t, reg.HasRole("admin"))
	assert.True(t, reg.HasRole("bucket-read"))
	assert.False(t, reg.HasRole("bucket-write"))
}

func TestRegistrationIsAdmin(t *testing.T) {
	reg := &Registration{
		Roles: []string{"bucket-read"},
	}
	assert.False(t, reg.IsAdmin())

	reg.Roles = []string{"admin", "bucket-read"}
	assert.True(t, reg.IsAdmin())
}

func TestDefaultRegistrationPath(t *testing.T) {
	path := DefaultRegistrationPath("work")
	assert.Contains(t, path, ".tunnelmesh")
	assert.Contains(t, path, "contexts")
	assert.Contains(t, path, "work")
	assert.Contains(t, path, "registration.json")
}
