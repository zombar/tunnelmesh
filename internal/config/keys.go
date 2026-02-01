package config

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// GenerateKeyPair generates a new ED25519 SSH key pair and saves it to disk.
// The private key is saved to privPath and public key to privPath.pub
func GenerateKeyPair(privPath string) error {
	// Generate ED25519 key pair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key pair: %w", err)
	}

	// Marshal private key to OpenSSH format
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("create SSH public key: %w", err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	// Encode PEM block to bytes
	pemBytes := pem.EncodeToMemory(pemBlock)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(privPath), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}

	// Write private key with restricted permissions
	if err := os.WriteFile(privPath, pemBytes, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	// Write public key
	pubPath := privPath + ".pub"
	pubBytes := ssh.MarshalAuthorizedKey(sshPubKey)
	if err := os.WriteFile(pubPath, pubBytes, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return nil
}

// LoadPrivateKey loads an SSH private key from disk.
func LoadPrivateKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	return signer, nil
}

// LoadAuthorizedKeys loads a list of public keys from an authorized_keys file.
func LoadAuthorizedKeys(path string) ([]ssh.PublicKey, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open authorized_keys: %w", err)
	}
	defer file.Close()

	var keys []ssh.PublicKey
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			// Skip invalid lines
			continue
		}
		keys = append(keys, pubKey)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read authorized_keys: %w", err)
	}

	return keys, nil
}

// EnsureKeyPairExists loads an existing key pair or generates a new one.
func EnsureKeyPairExists(privPath string) (ssh.Signer, error) {
	// Try to load existing key
	signer, err := LoadPrivateKey(privPath)
	if err == nil {
		return signer, nil
	}

	// Generate new key pair if it doesn't exist
	if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
		if err := GenerateKeyPair(privPath); err != nil {
			return nil, fmt.Errorf("generate key pair: %w", err)
		}
		return LoadPrivateKey(privPath)
	}

	return nil, err
}

// GetPublicKeyFingerprint returns the SHA256 fingerprint of an SSH public key.
func GetPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.StdEncoding.EncodeToString(hash[:])
}

// EncodePublicKey encodes an SSH public key to base64 wire format for transmission.
func EncodePublicKey(key ssh.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key.Marshal())
}

// DecodePublicKey decodes a base64-encoded SSH public key.
func DecodePublicKey(encoded string) (ssh.PublicKey, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	key, err := ssh.ParsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	return key, nil
}
