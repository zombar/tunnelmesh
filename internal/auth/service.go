package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// ServiceNames for built-in services
const (
	ServiceCoordinator = "coordinator"
	ServiceBackupAgent = "backup-agent"
)

// DeriveServiceKeypair derives an ED25519 keypair for a service using HKDF.
// The derivation is deterministic: same CA key + service name = same keypair.
func DeriveServiceKeypair(caPrivateKey []byte, serviceName string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	// Use HKDF to derive a seed from the CA private key and service name
	info := []byte("service:" + serviceName)
	reader := hkdf.New(sha256.New, caPrivateKey, nil, info)

	seed := make([]byte, ed25519.SeedSize)
	if _, err := reader.Read(seed); err != nil {
		return nil, nil, err
	}

	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return publicKey, privateKey, nil
}

// NewServicePeer creates a Peer for a service.
func NewServicePeer(caPrivateKey []byte, serviceName string) (*Peer, error) {
	pubKey, _, err := DeriveServiceKeypair(caPrivateKey, serviceName)
	if err != nil {
		return nil, err
	}

	return &Peer{
		ID:        ServicePeerID(serviceName),
		PublicKey: base64.StdEncoding.EncodeToString(pubKey),
		Name:      serviceDisplayName(serviceName),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// ServicePeerID returns the peer ID for a service.
func ServicePeerID(serviceName string) string {
	return ServicePeerPrefix + serviceName
}

// IsServicePeer checks if a peer ID belongs to a service peer.
func IsServicePeer(peerID string) bool {
	return strings.HasPrefix(peerID, ServicePeerPrefix)
}

// serviceDisplayName returns a human-readable name for a service.
func serviceDisplayName(serviceName string) string {
	switch serviceName {
	case ServiceCoordinator:
		return "Coordinator Service"
	case ServiceBackupAgent:
		return "Backup Agent Service"
	default:
		// Convert kebab-case to Title Case
		parts := strings.Split(serviceName, "-")
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return strings.Join(parts, " ") + " Service"
	}
}
