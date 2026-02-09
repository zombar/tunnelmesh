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

// ServicePeerPrefix is the prefix for service peer IDs.
const ServicePeerPrefix = "svc:"

// peerExpirationDays is the number of days after LastSeen when a peer expires.
// Default is 270 (9 months). Set via SetPeerExpirationDays.
var peerExpirationDays = 270

// SetPeerExpirationDays sets the number of days after LastSeen when a peer expires.
func SetPeerExpirationDays(days int) {
	if days > 0 {
		peerExpirationDays = days
	}
}

// GetPeerExpirationDays returns the current peer expiration days setting.
func GetPeerExpirationDays() int {
	return peerExpirationDays
}

// Peer represents a TunnelMesh peer identified by an ED25519 public key.
// Peer identity is derived from the peer's SSH key - no separate peer registration needed.
type Peer struct {
	ID        string    `json:"id"`         // SHA256(pubkey)[:8] hex, or "svc:name" for services
	PublicKey string    `json:"public_key"` // Base64-encoded ED25519 public key
	Name      string    `json:"name"`       // Optional display name (defaults to peer name)
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Expired   bool      `json:"expired,omitempty"`    // True if account is expired
	ExpiredAt time.Time `json:"expired_at,omitempty"` // When the account was expired
}

// IsService returns true if this is a service peer (ID starts with "svc:").
func (p *Peer) IsService() bool {
	return strings.HasPrefix(p.ID, ServicePeerPrefix)
}

// IsExpired returns true if the peer account is expired.
// An account is expired if:
// - It has been explicitly marked as expired (Expired == true), OR
// - For human peers: LastSeen is more than PeerExpirationDays ago
// Service peers never expire based on time, only when explicitly marked.
func (p *Peer) IsExpired() bool {
	// If explicitly marked as expired, always return true
	if p.Expired {
		return true
	}

	// Service peers don't expire based on time
	if p.IsService() {
		return false
	}

	// Check if LastSeen is too old
	if p.LastSeen.IsZero() {
		return false // Never seen, not expired yet
	}

	expirationDuration := time.Duration(peerExpirationDays) * 24 * time.Hour
	return time.Since(p.LastSeen) > expirationDuration
}

// computePeerID generates a peer ID from a public key.
// The ID is the first 8 bytes of SHA256(pubkey) encoded as hex (16 chars).
func computePeerID(pubKey ed25519.PublicKey) string {
	hash := sha256.Sum256(pubKey)
	return hex.EncodeToString(hash[:8])
}

// ComputePeerID generates a peer ID from a raw ED25519 public key.
// This is the exported version of computePeerID for use by other packages.
func ComputePeerID(pubKey ed25519.PublicKey) string {
	return computePeerID(pubKey)
}

// ComputePeerIDFromBase64 generates a peer ID from a base64-encoded ED25519 public key.
func ComputePeerIDFromBase64(pubKeyB64 string) (string, error) {
	pubKey, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return "", err
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(pubKey))
	}
	return computePeerID(pubKey), nil
}
