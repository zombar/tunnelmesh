package udp

import (
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Key sizes
const (
	KeySize   = 32 // ChaCha20-Poly1305 key size
	NonceSize = 12 // ChaCha20-Poly1305 nonce size
)

// CryptoState holds the encryption keys for a session.
type CryptoState struct {
	sendKey    [KeySize]byte
	recvKey    [KeySize]byte
	sendCipher cipher.AEAD
	recvCipher cipher.AEAD
}

// NewCryptoState creates a new crypto state from derived keys.
func NewCryptoState(sendKey, recvKey [KeySize]byte) (*CryptoState, error) {
	sendCipher, err := chacha20poly1305.New(sendKey[:])
	if err != nil {
		return nil, fmt.Errorf("create send cipher: %w", err)
	}

	recvCipher, err := chacha20poly1305.New(recvKey[:])
	if err != nil {
		return nil, fmt.Errorf("create recv cipher: %w", err)
	}

	return &CryptoState{
		sendKey:    sendKey,
		recvKey:    recvKey,
		sendCipher: sendCipher,
		recvCipher: recvCipher,
	}, nil
}

// Encrypt encrypts plaintext and returns ciphertext with auth tag.
func (c *CryptoState) Encrypt(counter uint64, plaintext, additionalData []byte) []byte {
	nonce := counterToNonce(counter)
	return c.sendCipher.Seal(nil, nonce[:], plaintext, additionalData)
}

// Decrypt decrypts ciphertext and verifies the auth tag.
func (c *CryptoState) Decrypt(counter uint64, ciphertext, additionalData []byte) ([]byte, error) {
	nonce := counterToNonce(counter)
	return c.recvCipher.Open(nil, nonce[:], ciphertext, additionalData)
}

// counterToNonce converts a 64-bit counter to a 12-byte nonce.
// Format: [4 zero bytes][8 counter bytes big-endian]
func counterToNonce(counter uint64) [NonceSize]byte {
	var nonce [NonceSize]byte
	// Leave first 4 bytes as zero
	nonce[4] = byte(counter >> 56)
	nonce[5] = byte(counter >> 48)
	nonce[6] = byte(counter >> 40)
	nonce[7] = byte(counter >> 32)
	nonce[8] = byte(counter >> 24)
	nonce[9] = byte(counter >> 16)
	nonce[10] = byte(counter >> 8)
	nonce[11] = byte(counter)
	return nonce
}

// X25519KeyPair generates a new X25519 key pair.
func X25519KeyPair() (privateKey, publicKey [32]byte, err error) {
	_, err = rand.Read(privateKey[:])
	if err != nil {
		return
	}

	// Clamp private key as per X25519 spec
	privateKey[0] &= 248
	privateKey[31] &= 127
	privateKey[31] |= 64

	pubSlice, err := curve25519.X25519(privateKey[:], curve25519.Basepoint)
	if err != nil {
		return
	}
	copy(publicKey[:], pubSlice)

	return
}

// X25519SharedSecret computes the shared secret from a private key and peer's public key.
func X25519SharedSecret(privateKey, peerPublicKey [32]byte) ([32]byte, error) {
	var shared [32]byte
	result, err := curve25519.X25519(privateKey[:], peerPublicKey[:])
	if err != nil {
		return shared, err
	}
	copy(shared[:], result)
	return shared, nil
}

// DeriveKeys derives encryption keys from a shared secret using HKDF.
func DeriveKeys(sharedSecret [32]byte, initiatorPub, responderPub [32]byte, isInitiator bool) (sendKey, recvKey [KeySize]byte, err error) {
	// Combine shared secret with public keys for key derivation
	info := make([]byte, 64)
	copy(info[:32], initiatorPub[:])
	copy(info[32:], responderPub[:])

	// Use HKDF-SHA256
	hkdfReader := hkdf.New(sha256.New, sharedSecret[:], nil, info)

	// Derive two keys
	key1 := make([]byte, KeySize)
	key2 := make([]byte, KeySize)

	if _, err = io.ReadFull(hkdfReader, key1); err != nil {
		return
	}
	if _, err = io.ReadFull(hkdfReader, key2); err != nil {
		return
	}

	// Initiator sends with key1, receives with key2
	// Responder sends with key2, receives with key1
	if isInitiator {
		copy(sendKey[:], key1)
		copy(recvKey[:], key2)
	} else {
		copy(sendKey[:], key2)
		copy(recvKey[:], key1)
	}

	return
}

// GenerateSessionIndex generates a random 32-bit session index.
func GenerateSessionIndex() (uint32, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3]), nil
}
