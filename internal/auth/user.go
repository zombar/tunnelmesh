package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ServiceUserPrefix is the prefix for service user IDs.
const ServiceUserPrefix = "svc:"

// userExpirationDays is the number of days after LastSeen when a user expires.
// Default is 270 (9 months). Set via SetUserExpirationDays.
var userExpirationDays = 270

// SetUserExpirationDays sets the number of days after LastSeen when a user expires.
func SetUserExpirationDays(days int) {
	if days > 0 {
		userExpirationDays = days
	}
}

// GetUserExpirationDays returns the current user expiration days setting.
func GetUserExpirationDays() int {
	return userExpirationDays
}

// User represents a TunnelMesh user identified by an ED25519 public key.
type User struct {
	ID        string    `json:"id"`         // SHA256(pubkey)[:8] hex, or "svc:name" for services
	PublicKey string    `json:"public_key"` // Base64-encoded ED25519 public key
	Name      string    `json:"name"`       // Optional display name
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Expired   bool      `json:"expired,omitempty"`    // True if account is expired
	ExpiredAt time.Time `json:"expired_at,omitempty"` // When the account was expired
}

// IsService returns true if this is a service user (ID starts with "svc:").
func (u *User) IsService() bool {
	return strings.HasPrefix(u.ID, ServiceUserPrefix)
}

// IsExpired returns true if the user account is expired.
// An account is expired if:
// - It has been explicitly marked as expired (Expired == true), OR
// - For human users: LastSeen is more than UserExpirationDays ago
// Service users never expire based on time, only when explicitly marked.
func (u *User) IsExpired() bool {
	// If explicitly marked as expired, always return true
	if u.Expired {
		return true
	}

	// Service users don't expire based on time
	if u.IsService() {
		return false
	}

	// Check if LastSeen is too old
	if u.LastSeen.IsZero() {
		return false // Never seen, not expired yet
	}

	expirationDuration := time.Duration(userExpirationDays) * 24 * time.Hour
	return time.Since(u.LastSeen) > expirationDuration
}

// UserIdentity holds a user's identity including their private key.
// This is stored locally on the user's machine, never sent to the coordinator.
type UserIdentity struct {
	User       User   `json:"user"`
	PrivateKey string `json:"private_key"` // Base64-encoded ED25519 private key
}

// NewUserFromMnemonic creates a new User from a mnemonic phrase.
func NewUserFromMnemonic(mnemonic, name string) (*User, error) {
	pubKey, _, err := DeriveKeypairFromMnemonic(mnemonic)
	if err != nil {
		return nil, err
	}

	return &User{
		ID:        computeUserID(pubKey),
		PublicKey: base64.StdEncoding.EncodeToString(pubKey),
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// NewUserIdentity creates a new UserIdentity from a mnemonic phrase.
// The identity includes the private key for signing operations.
func NewUserIdentity(mnemonic, name string) (*UserIdentity, error) {
	pubKey, privKey, err := DeriveKeypairFromMnemonic(mnemonic)
	if err != nil {
		return nil, err
	}

	return &UserIdentity{
		User: User{
			ID:        computeUserID(pubKey),
			PublicKey: base64.StdEncoding.EncodeToString(pubKey),
			Name:      name,
			CreatedAt: time.Now().UTC(),
		},
		PrivateKey: base64.StdEncoding.EncodeToString(privKey),
	}, nil
}

// Save writes the user identity to a file with restricted permissions.
func (ui *UserIdentity) Save(path string) error {
	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(ui, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

// LoadUserIdentity reads a user identity from a file.
func LoadUserIdentity(path string) (*UserIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var identity UserIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, err
	}

	return &identity, nil
}

// Sign signs a message with the user's private key.
func (ui *UserIdentity) Sign(message []byte) []byte {
	privKey, err := base64.StdEncoding.DecodeString(ui.PrivateKey)
	if err != nil {
		return nil
	}
	return ed25519.Sign(privKey, message)
}

// Verify verifies a signature against a message using the user's public key.
func (ui *UserIdentity) Verify(message, signature []byte) bool {
	pubKey, err := base64.StdEncoding.DecodeString(ui.User.PublicKey)
	if err != nil {
		return false
	}
	return ed25519.Verify(pubKey, message, signature)
}

// GetPrivateKey returns the decoded private key.
func (ui *UserIdentity) GetPrivateKey() (ed25519.PrivateKey, error) {
	return base64.StdEncoding.DecodeString(ui.PrivateKey)
}

// GetPublicKey returns the decoded public key.
func (ui *UserIdentity) GetPublicKey() (ed25519.PublicKey, error) {
	return base64.StdEncoding.DecodeString(ui.User.PublicKey)
}

// computeUserID generates a user ID from a public key.
// The ID is the first 8 bytes of SHA256(pubkey) encoded as hex (16 chars).
func computeUserID(pubKey ed25519.PublicKey) string {
	hash := sha256.Sum256(pubKey)
	return hex.EncodeToString(hash[:8])
}

// ComputeUserID generates a user ID from a raw ED25519 public key.
// This is the exported version of computeUserID for use by other packages.
func ComputeUserID(pubKey ed25519.PublicKey) string {
	return computeUserID(pubKey)
}

// ComputeUserIDFromBase64 generates a user ID from a base64-encoded ED25519 public key.
func ComputeUserIDFromBase64(pubKeyB64 string) (string, error) {
	pubKey, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", err
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(pubKey))
	}
	return computeUserID(pubKey), nil
}

// VerifyUserSignature verifies a signature of a message using a base64-encoded public key.
// This is used to verify user registration requests.
func VerifyUserSignature(publicKeyB64, message, signatureB64 string) bool {
	pubKey, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return false
	}

	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}

	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}

	return ed25519.Verify(pubKey, []byte(message), signature)
}

// SignMessage signs a message with the identity's private key and returns base64-encoded signature.
func (ui *UserIdentity) SignMessage(message string) (string, error) {
	privKey, err := ui.GetPrivateKey()
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privKey, []byte(message))
	return base64.StdEncoding.EncodeToString(signature), nil
}
