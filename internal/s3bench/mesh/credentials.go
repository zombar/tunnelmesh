package mesh

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/coord/s3"
	"golang.org/x/crypto/ssh"
)

// Credentials holds SSH keys and derived S3 access credentials.
type Credentials struct {
	// SSH keys
	PrivateKey ssh.Signer
	PublicKey  string // Base64-encoded SSH public key

	// Derived S3 credentials
	AccessKey string
	SecretKey string
}

// LoadOrGenerateCredentials loads an existing SSH key pair or generates a new one.
// If keyPath is empty, uses default path ~/.tunnelmesh/s3bench_key.
func LoadOrGenerateCredentials(keyPath string) (*Credentials, error) {
	// Use default key path if not specified
	if keyPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home directory: %w", err)
		}
		keyPath = filepath.Join(homeDir, ".tunnelmesh", "s3bench_key")
	}

	// Ensure parent directory exists
	keyDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}

	// Load or generate key pair
	signer, err := config.EnsureKeyPairExists(keyPath, "")
	if err != nil {
		return nil, fmt.Errorf("ensure key pair exists: %w", err)
	}

	// Encode public key for transmission
	publicKey := config.EncodePublicKey(signer.PublicKey())

	// Derive S3 credentials from public key
	accessKey, secretKey, err := s3.DeriveS3Credentials(publicKey)
	if err != nil {
		return nil, fmt.Errorf("derive S3 credentials: %w", err)
	}

	return &Credentials{
		PrivateKey: signer,
		PublicKey:  publicKey,
		AccessKey:  accessKey,
		SecretKey:  secretKey,
	}, nil
}
