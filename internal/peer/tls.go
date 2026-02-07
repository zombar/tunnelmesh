package peer

import (
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
)

// TLSManager handles TLS certificate storage and loading for a peer.
type TLSManager struct {
	dataDir string
}

// NewTLSManager creates a new TLS manager with the given data directory.
func NewTLSManager(dataDir string) *TLSManager {
	return &TLSManager{
		dataDir: dataDir,
	}
}

// tlsDir returns the directory for TLS files.
func (m *TLSManager) tlsDir() string {
	return filepath.Join(m.dataDir, "tls")
}

// CertPath returns the path to the certificate file.
func (m *TLSManager) CertPath() string {
	return filepath.Join(m.tlsDir(), "cert.pem")
}

// KeyPath returns the path to the private key file.
func (m *TLSManager) KeyPath() string {
	return filepath.Join(m.tlsDir(), "key.pem")
}

// CAPath returns the path to the CA certificate file.
func (m *TLSManager) CAPath() string {
	return filepath.Join(m.tlsDir(), "ca.pem")
}

// StoreCert saves the certificate and private key to disk.
func (m *TLSManager) StoreCert(certPEM, keyPEM []byte) error {
	// Ensure directory exists
	if err := os.MkdirAll(m.tlsDir(), 0755); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	// Save certificate
	if err := os.WriteFile(m.CertPath(), certPEM, 0644); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}

	// Save private key with restricted permissions
	if err := os.WriteFile(m.KeyPath(), keyPEM, 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	log.Debug().
		Str("cert", m.CertPath()).
		Str("key", m.KeyPath()).
		Msg("stored TLS certificate")

	return nil
}

// StoreCA saves the CA certificate to disk.
func (m *TLSManager) StoreCA(caPEM []byte) error {
	// Ensure directory exists
	if err := os.MkdirAll(m.tlsDir(), 0755); err != nil {
		return fmt.Errorf("create tls dir: %w", err)
	}

	// Save CA certificate
	if err := os.WriteFile(m.CAPath(), caPEM, 0644); err != nil {
		return fmt.Errorf("write ca: %w", err)
	}

	log.Debug().Str("ca", m.CAPath()).Msg("stored CA certificate")
	return nil
}

// LoadCert loads the certificate and private key from disk.
func (m *TLSManager) LoadCert() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(m.CertPath(), m.KeyPath())
	if err != nil {
		return nil, fmt.Errorf("load cert: %w", err)
	}
	return &cert, nil
}

// HasCert returns true if a certificate exists on disk.
func (m *TLSManager) HasCert() bool {
	_, certErr := os.Stat(m.CertPath())
	_, keyErr := os.Stat(m.KeyPath())
	return certErr == nil && keyErr == nil
}
