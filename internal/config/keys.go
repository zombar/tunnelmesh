package config

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/edwards25519"
	"golang.org/x/crypto/curve25519"
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
	defer func() { _ = file.Close() }()

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

	// Check if file doesn't exist (handle both Unix and Windows error messages)
	errMsg := err.Error()
	isNotExist := os.IsNotExist(err) ||
		strings.Contains(errMsg, "no such file") ||
		strings.Contains(errMsg, "cannot find the file")

	// Generate new key pair if it doesn't exist
	if isNotExist {
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

// ExtractED25519FromSSHKey extracts the raw ED25519 public key from an SSH public key.
// The SSH key must be an ED25519 key; returns an error for other key types.
func ExtractED25519FromSSHKey(sshPubKey ssh.PublicKey) (ed25519.PublicKey, error) {
	cryptoPubKey, ok := sshPubKey.(ssh.CryptoPublicKey)
	if !ok {
		return nil, fmt.Errorf("SSH key does not support CryptoPublicKey interface")
	}

	edPubKey, ok := cryptoPubKey.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("SSH key is not an ED25519 key")
	}

	return edPubKey, nil
}

// DecodeED25519PublicKey decodes a base64-encoded SSH ED25519 public key to raw ED25519 bytes.
func DecodeED25519PublicKey(encoded string) (ed25519.PublicKey, error) {
	sshPubKey, err := DecodePublicKey(encoded)
	if err != nil {
		return nil, err
	}

	return ExtractED25519FromSSHKey(sshPubKey)
}

// ED25519PrivateToX25519 converts an ED25519 private key to X25519 format.
// This uses the standard conversion: hash the ED25519 seed with SHA-512
// and use the first 32 bytes as the X25519 scalar.
func ED25519PrivateToX25519(edPriv ed25519.PrivateKey) []byte {
	// The ED25519 private key seed is the first 32 bytes
	seed := edPriv.Seed()

	// Hash the seed with SHA-512 and take first 32 bytes
	h := sha512.Sum512(seed)

	// Apply clamping as per X25519 spec (first 32 bytes)
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64

	return h[:32]
}

// ED25519PublicToX25519 converts an ED25519 public key to X25519 format.
// This performs the birational map from the Ed25519 curve to Curve25519.
// The formula is: u = (1 + y) / (1 - y) mod p
func ED25519PublicToX25519(edPub ed25519.PublicKey) ([]byte, error) {
	if len(edPub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ED25519 public key size")
	}

	// Parse the ED25519 public key as an edwards25519 point
	point, err := new(edwards25519.Point).SetBytes(edPub)
	if err != nil {
		return nil, fmt.Errorf("parse ED25519 public key: %w", err)
	}

	// Convert to Montgomery u-coordinate (X25519 format)
	return point.BytesMontgomery(), nil
}

// DeriveX25519KeyPair derives an X25519 key pair from an ED25519 private key.
func DeriveX25519KeyPair(edPriv ed25519.PrivateKey) (x25519Priv, x25519Pub []byte, err error) {
	x25519Priv = ED25519PrivateToX25519(edPriv)

	// Derive public key from private key
	x25519Pub, err = curve25519.X25519(x25519Priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, fmt.Errorf("derive X25519 public key: %w", err)
	}

	return x25519Priv, x25519Pub, nil
}

// GetED25519PrivateKey extracts the ED25519 private key from an SSH signer.
func GetED25519PrivateKey(signer ssh.Signer) (ed25519.PrivateKey, error) {
	// The ssh.Signer from ParsePrivateKey wraps the crypto key
	pubKey := signer.PublicKey()
	if pubKey.Type() != ssh.KeyAlgoED25519 {
		return nil, fmt.Errorf("key type %s is not ED25519", pubKey.Type())
	}

	// The ssh library doesn't expose the private key directly
	// We need to re-parse the key data
	return nil, fmt.Errorf("cannot extract ED25519 private key from ssh.Signer; use LoadED25519PrivateKey instead")
}

// LoadED25519PrivateKey loads an ED25519 private key directly from a PEM file.
func LoadED25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	// Parse the PEM block
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}

	// Parse as OpenSSH private key
	key, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}

	// Type assert to ED25519
	edKey, ok := key.(*ed25519.PrivateKey)
	if !ok {
		// Try pointer to key
		if edKeyPtr, ok := key.(ed25519.PrivateKey); ok {
			return edKeyPtr, nil
		}
		return nil, fmt.Errorf("key is not ED25519 (got %T)", key)
	}

	return *edKey, nil
}
