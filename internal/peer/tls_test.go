package peer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTLSManager_CAPath(t *testing.T) {
	mgr := NewTLSManager("/tmp/test")
	expected := filepath.Join("/tmp/test", "tls", "ca.pem")
	if got := mgr.CAPath(); got != expected {
		t.Errorf("CAPath() = %q, want %q", got, expected)
	}
}

func TestTLSManager_StoreCA(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	mgr := NewTLSManager(tmpDir)
	caPEM := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")

	// Store CA
	if err := mgr.StoreCA(caPEM); err != nil {
		t.Fatalf("StoreCA() error = %v", err)
	}

	// Verify file exists
	caPath := mgr.CAPath()
	if _, err := os.Stat(caPath); os.IsNotExist(err) {
		t.Errorf("CA file not created at %s", caPath)
	}

	// Verify contents
	got, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("failed to read CA file: %v", err)
	}
	if string(got) != string(caPEM) {
		t.Errorf("CA content = %q, want %q", got, caPEM)
	}

	// Verify TLS directory was created
	tlsDir := filepath.Join(tmpDir, "tls")
	if _, err := os.Stat(tlsDir); os.IsNotExist(err) {
		t.Errorf("TLS directory not created at %s", tlsDir)
	}
}

func TestTLSManager_StoreCA_CreatesDirectory(t *testing.T) {
	// Create temp directory and immediately remove it to test creation
	tmpDir, err := os.MkdirTemp("", "tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	_ = os.RemoveAll(tmpDir) // Remove so StoreCA has to create it

	mgr := NewTLSManager(tmpDir)
	caPEM := []byte("test-ca")

	if err := mgr.StoreCA(caPEM); err != nil {
		t.Fatalf("StoreCA() error = %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Verify directory was created
	if _, err := os.Stat(mgr.CAPath()); os.IsNotExist(err) {
		t.Errorf("CA file not created")
	}
}
