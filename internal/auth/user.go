package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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
// User identity is derived from the peer's SSH key - no separate user registration needed.
type User struct {
	ID        string    `json:"id"`         // SHA256(pubkey)[:8] hex, or "svc:name" for services
	PublicKey string    `json:"public_key"` // Base64-encoded ED25519 public key
	Name      string    `json:"name"`       // Optional display name (defaults to peer name)
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
