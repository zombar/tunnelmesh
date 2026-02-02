package udp

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

// Noise protocol constants (matching WireGuard pattern)
const (
	NoiseConstruction = "Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s"
	NoiseIdentifier   = "TunnelMesh v1"
)

// Pre-computed initial state (computed once at startup)
var (
	InitialChainKey [blake2s.Size]byte
	InitialHash     [blake2s.Size]byte
	ZeroNonce       [chacha20poly1305.NonceSize]byte
)

func init() {
	// Initialize like WireGuard: chainKey = HASH(construction), hash = HASH(chainKey || identifier)
	InitialChainKey = blake2s.Sum256([]byte(NoiseConstruction))
	mixHashStatic(&InitialHash, &InitialChainKey, []byte(NoiseIdentifier))
}

// HandshakeState holds the state during a Noise IK handshake.
type HandshakeState struct {
	// Static keys (our identity)
	staticPrivate [32]byte
	staticPublic  [32]byte

	// Ephemeral keys (generated per handshake)
	ephemeralPrivate [32]byte
	ephemeralPublic  [32]byte

	// Peer's keys
	peerStaticPublic    [32]byte
	peerEphemeralPublic [32]byte

	// Precomputed static-static DH (for efficiency)
	precomputedStaticStatic [32]byte

	// Handshake state
	chainingKey [32]byte
	hash        [32]byte

	// Session indices
	localIndex  uint32
	remoteIndex uint32

	// Timestamp for replay protection
	timestamp [12]byte

	// Role tracking
	isInitiator bool
}

// NewInitiatorHandshake creates a new handshake state for the initiator.
// The initiator knows the responder's static public key in advance (Noise IK pattern).
func NewInitiatorHandshake(staticPrivate, staticPublic, responderPublic [32]byte) (*HandshakeState, error) {
	hs := &HandshakeState{
		staticPrivate:    staticPrivate,
		staticPublic:     staticPublic,
		peerStaticPublic: responderPublic,
		isInitiator:      true,
	}

	// Generate ephemeral key pair
	if err := hs.generateEphemeral(); err != nil {
		return nil, err
	}

	// Precompute static-static DH for efficiency
	ss, err := curve25519.X25519(staticPrivate[:], responderPublic[:])
	if err != nil {
		return nil, err
	}
	copy(hs.precomputedStaticStatic[:], ss)

	// Initialize: both parties start with hash including RESPONDER's public key
	hs.chainingKey = InitialChainKey
	hs.hash = InitialHash
	mixHashStatic(&hs.hash, &hs.hash, responderPublic[:])

	return hs, nil
}

// NewResponderHandshake creates a new handshake state for the responder.
// The responder doesn't know the initiator's identity until handshake message arrives.
func NewResponderHandshake(staticPrivate, staticPublic [32]byte) (*HandshakeState, error) {
	hs := &HandshakeState{
		staticPrivate: staticPrivate,
		staticPublic:  staticPublic,
		isInitiator:   false,
	}

	// Generate ephemeral key pair
	if err := hs.generateEphemeral(); err != nil {
		return nil, err
	}

	// Initialize: both parties start with hash including RESPONDER's public key (our own key)
	hs.chainingKey = InitialChainKey
	hs.hash = InitialHash
	mixHashStatic(&hs.hash, &hs.hash, staticPublic[:])

	return hs, nil
}

// generateEphemeral generates a new ephemeral key pair.
func (hs *HandshakeState) generateEphemeral() error {
	if _, err := rand.Read(hs.ephemeralPrivate[:]); err != nil {
		return err
	}
	// Clamp (X25519 requirement)
	hs.ephemeralPrivate[0] &= 248
	hs.ephemeralPrivate[31] &= 127
	hs.ephemeralPrivate[31] |= 64

	pubSlice, err := curve25519.X25519(hs.ephemeralPrivate[:], curve25519.Basepoint)
	if err != nil {
		return err
	}
	copy(hs.ephemeralPublic[:], pubSlice)
	return nil
}

// CreateInitiation creates the first handshake message (initiator -> responder).
// Returns the message bytes and error.
// Message format: [4:index][32:ephemeral][48:encrypted_static][28:encrypted_timestamp][16:mac]
func (hs *HandshakeState) CreateInitiation() ([]byte, error) {
	var msg [128]byte

	// Generate session index
	idx, err := GenerateSessionIndex()
	if err != nil {
		return nil, err
	}
	hs.localIndex = idx
	binary.LittleEndian.PutUint32(msg[0:4], idx)

	// e (ephemeral public key)
	copy(msg[4:36], hs.ephemeralPublic[:])

	// mixKey(e) and mixHash(e) - like WireGuard
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, hs.ephemeralPublic[:])
	mixHashStatic(&hs.hash, &hs.hash, hs.ephemeralPublic[:])

	log.Debug().
		Hex("ephemeral_pub", hs.ephemeralPublic[:8]).
		Hex("hash_after_eph", hs.hash[:8]).
		Hex("peer_static_pub", hs.peerStaticPublic[:8]).
		Msg("initiator: after ephemeral mix")

	// es = DH(e, rs) - ephemeral-static DH
	es, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return nil, fmt.Errorf("ephemeral-static DH: %w", err)
	}

	// KDF2 to get new chain key and encryption key
	var key [32]byte
	KDF2(&hs.chainingKey, &key, hs.chainingKey[:], es)

	log.Debug().
		Hex("es_dh", es[:8]).
		Hex("chaining_key_after", hs.chainingKey[:8]).
		Msg("initiator: after es DH")

	// Encrypt static public key: AEAD(key, 0, static_pub, hash)
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	encryptedStatic := aead.Seal(nil, ZeroNonce[:], hs.staticPublic[:], hs.hash[:])
	copy(msg[36:84], encryptedStatic)
	mixHashStatic(&hs.hash, &hs.hash, encryptedStatic)

	// ss = DH(s, rs) - static-static DH (use precomputed)
	KDF2(&hs.chainingKey, &key, hs.chainingKey[:], hs.precomputedStaticStatic[:])

	// Timestamp (replay protection) - TAI64N format
	hs.timestamp = TAI64N()

	// Encrypt timestamp: AEAD(key, 0, timestamp, hash)
	aead, err = chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	encryptedTimestamp := aead.Seal(nil, ZeroNonce[:], hs.timestamp[:], hs.hash[:])
	copy(msg[84:112], encryptedTimestamp)
	mixHashStatic(&hs.hash, &hs.hash, encryptedTimestamp)

	// MAC for integrity (using BLAKE2s keyed hash)
	mac := blake2sMAC(hs.chainingKey[:], msg[:112])
	copy(msg[112:128], mac[:16])

	return msg[:], nil
}

// ConsumeInitiation processes an incoming initiation message (responder side).
func (hs *HandshakeState) ConsumeInitiation(msg []byte) error {
	if len(msg) < 128 {
		return fmt.Errorf("initiation message too short: %d", len(msg))
	}

	// Parse sender index
	hs.remoteIndex = binary.LittleEndian.Uint32(msg[0:4])

	// Parse ephemeral public key
	copy(hs.peerEphemeralPublic[:], msg[4:36])

	// mixKey(e) and mixHash(e) - same as initiator did
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, hs.peerEphemeralPublic[:])
	mixHashStatic(&hs.hash, &hs.hash, hs.peerEphemeralPublic[:])

	log.Debug().
		Hex("ephemeral_pub", hs.peerEphemeralPublic[:8]).
		Hex("hash_after_eph", hs.hash[:8]).
		Msg("responder: after ephemeral mix")

	// es = DH(s, re) - our static with their ephemeral
	es, err := curve25519.X25519(hs.staticPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return fmt.Errorf("static-ephemeral DH: %w", err)
	}

	// KDF2 to get new chain key and decryption key
	var key [32]byte
	KDF2(&hs.chainingKey, &key, hs.chainingKey[:], es)

	log.Debug().
		Hex("es_dh", es[:8]).
		Hex("chaining_key_after", hs.chainingKey[:8]).
		Msg("responder: after es DH")

	// Decrypt peer's static public key
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return err
	}
	plaintext, err := aead.Open(nil, ZeroNonce[:], msg[36:84], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt static key: %w", err)
	}
	copy(hs.peerStaticPublic[:], plaintext)
	mixHashStatic(&hs.hash, &hs.hash, msg[36:84]) // Mix ciphertext, not plaintext

	// ss = DH(s, rs) - static-static DH
	ss, err := curve25519.X25519(hs.staticPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return fmt.Errorf("static-static DH: %w", err)
	}
	// Store for later use
	copy(hs.precomputedStaticStatic[:], ss)
	KDF2(&hs.chainingKey, &key, hs.chainingKey[:], ss)

	// Decrypt timestamp
	aead, err = chacha20poly1305.New(key[:])
	if err != nil {
		return err
	}
	plaintext, err = aead.Open(nil, ZeroNonce[:], msg[84:112], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt timestamp: %w", err)
	}
	copy(hs.timestamp[:], plaintext)
	mixHashStatic(&hs.hash, &hs.hash, msg[84:112]) // Mix ciphertext

	// Verify MAC
	expectedMAC := blake2sMAC(hs.chainingKey[:], msg[:112])
	if !hmac.Equal(expectedMAC[:16], msg[112:128]) {
		return fmt.Errorf("MAC verification failed")
	}

	return nil
}

// CreateResponse creates the response message (responder -> initiator).
// Message format: [4:sender_idx][4:receiver_idx][32:ephemeral][16:encrypted_empty][16:mac]
func (hs *HandshakeState) CreateResponse() ([]byte, error) {
	var msg [72]byte

	// Generate session index
	idx, err := GenerateSessionIndex()
	if err != nil {
		return nil, err
	}
	hs.localIndex = idx
	binary.LittleEndian.PutUint32(msg[0:4], idx)
	binary.LittleEndian.PutUint32(msg[4:8], hs.remoteIndex)

	// e (ephemeral public key)
	copy(msg[8:40], hs.ephemeralPublic[:])
	mixHashStatic(&hs.hash, &hs.hash, hs.ephemeralPublic[:])
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, hs.ephemeralPublic[:])

	// ee = DH(e, re) - both ephemerals
	ee, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return nil, fmt.Errorf("ephemeral-ephemeral DH: %w", err)
	}
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, ee)

	// se = DH(s, re) - our static with their ephemeral
	se, err := curve25519.X25519(hs.staticPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return nil, fmt.Errorf("static-ephemeral DH: %w", err)
	}
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, se)

	// KDF3 for PSK mixing (we use empty PSK) - get tau, key
	var tau, key [32]byte
	var psk [32]byte // Zero PSK
	KDF3(&hs.chainingKey, &tau, &key, hs.chainingKey[:], psk[:])
	mixHashStatic(&hs.hash, &hs.hash, tau[:])

	// Encrypt empty payload
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, err
	}
	encryptedEmpty := aead.Seal(nil, ZeroNonce[:], nil, hs.hash[:])
	copy(msg[40:56], encryptedEmpty)
	mixHashStatic(&hs.hash, &hs.hash, encryptedEmpty)

	// MAC
	mac := blake2sMAC(hs.chainingKey[:], msg[:56])
	copy(msg[56:72], mac[:16])

	return msg[:], nil
}

// ConsumeResponse processes an incoming response message (initiator side).
func (hs *HandshakeState) ConsumeResponse(msg []byte) error {
	if len(msg) < 72 {
		return fmt.Errorf("response message too short: %d", len(msg))
	}

	// Parse sender and receiver indices
	hs.remoteIndex = binary.LittleEndian.Uint32(msg[0:4])
	receiverIndex := binary.LittleEndian.Uint32(msg[4:8])
	if receiverIndex != hs.localIndex {
		return fmt.Errorf("receiver index mismatch: got %d, want %d", receiverIndex, hs.localIndex)
	}

	// Parse ephemeral public key
	copy(hs.peerEphemeralPublic[:], msg[8:40])
	mixHashStatic(&hs.hash, &hs.hash, hs.peerEphemeralPublic[:])
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, hs.peerEphemeralPublic[:])

	// ee = DH(e, re) - both ephemerals
	ee, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return fmt.Errorf("ephemeral-ephemeral DH: %w", err)
	}
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, ee)

	// es = DH(e, rs) - our ephemeral with their static
	es, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return fmt.Errorf("ephemeral-static DH: %w", err)
	}
	mixKeyStatic(&hs.chainingKey, &hs.chainingKey, es)

	// KDF3 for PSK mixing (we use empty PSK) - get tau, key
	var tau, key [32]byte
	var psk [32]byte // Zero PSK
	KDF3(&hs.chainingKey, &tau, &key, hs.chainingKey[:], psk[:])
	mixHashStatic(&hs.hash, &hs.hash, tau[:])

	// Decrypt empty payload (verify)
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return err
	}
	_, err = aead.Open(nil, ZeroNonce[:], msg[40:56], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt response: %w", err)
	}
	mixHashStatic(&hs.hash, &hs.hash, msg[40:56])

	// Verify MAC
	expectedMAC := blake2sMAC(hs.chainingKey[:], msg[:56])
	if !hmac.Equal(expectedMAC[:16], msg[56:72]) {
		return fmt.Errorf("MAC verification failed")
	}

	return nil
}

// DeriveKeys derives the final transport keys after handshake completion.
func (hs *HandshakeState) DeriveKeys() (sendKey, recvKey [32]byte, err error) {
	// Final key derivation using KDF2 (like WireGuard)
	var key1, key2 [32]byte
	KDF2(&key1, &key2, hs.chainingKey[:], nil)

	// Initiator sends with key1, receives with key2
	// Responder sends with key2, receives with key1
	if hs.isInitiator {
		sendKey = key1
		recvKey = key2
	} else {
		sendKey = key2
		recvKey = key1
	}

	// Zero out sensitive state
	setZero(hs.chainingKey[:])
	setZero(hs.hash[:])
	setZero(hs.ephemeralPrivate[:])

	return sendKey, recvKey, nil
}

// LocalIndex returns the local session index.
func (hs *HandshakeState) LocalIndex() uint32 {
	return hs.localIndex
}

// RemoteIndex returns the remote session index.
func (hs *HandshakeState) RemoteIndex() uint32 {
	return hs.remoteIndex
}

// PeerStaticPublic returns the peer's static public key.
func (hs *HandshakeState) PeerStaticPublic() [32]byte {
	return hs.peerStaticPublic
}

// WireGuard-style KDF and helper functions

// mixKeyStatic mixes data into the chain key using KDF1.
func mixKeyStatic(dst, c *[blake2s.Size]byte, data []byte) {
	KDF1(dst, c[:], data)
}

// mixHashStatic mixes data into the hash.
func mixHashStatic(dst, h *[blake2s.Size]byte, data []byte) {
	hash, _ := blake2s.New256(nil)
	hash.Write(h[:])
	hash.Write(data)
	hash.Sum(dst[:0])
}

// HMAC1 computes HMAC-BLAKE2s with one input.
func HMAC1(sum *[blake2s.Size]byte, key, in0 []byte) {
	mac := hmac.New(func() hash.Hash {
		h, _ := blake2s.New256(nil)
		return h
	}, key)
	mac.Write(in0)
	mac.Sum(sum[:0])
}

// HMAC2 computes HMAC-BLAKE2s with two inputs.
func HMAC2(sum *[blake2s.Size]byte, key, in0, in1 []byte) {
	mac := hmac.New(func() hash.Hash {
		h, _ := blake2s.New256(nil)
		return h
	}, key)
	mac.Write(in0)
	mac.Write(in1)
	mac.Sum(sum[:0])
}

// KDF1 derives one key from input.
func KDF1(t0 *[blake2s.Size]byte, key, input []byte) {
	HMAC1(t0, key, input)
	HMAC1(t0, t0[:], []byte{0x1})
}

// KDF2 derives two keys from input.
func KDF2(t0, t1 *[blake2s.Size]byte, key, input []byte) {
	var prk [blake2s.Size]byte
	HMAC1(&prk, key, input)
	HMAC1(t0, prk[:], []byte{0x1})
	HMAC2(t1, prk[:], t0[:], []byte{0x2})
	setZero(prk[:])
}

// KDF3 derives three keys from input.
func KDF3(t0, t1, t2 *[blake2s.Size]byte, key, input []byte) {
	var prk [blake2s.Size]byte
	HMAC1(&prk, key, input)
	HMAC1(t0, prk[:], []byte{0x1})
	HMAC2(t1, prk[:], t0[:], []byte{0x2})
	HMAC2(t2, prk[:], t1[:], []byte{0x3})
	setZero(prk[:])
}

// blake2sMAC computes BLAKE2s MAC with key.
func blake2sMAC(key, data []byte) [32]byte {
	h, _ := blake2s.New256(key[:32])
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// setZero securely zeroes a byte slice.
func setZero(arr []byte) {
	for i := range arr {
		arr[i] = 0
	}
}

// TAI64N returns a TAI64N timestamp for the current time.
func TAI64N() [12]byte {
	var timestamp [12]byte
	now := time.Now()
	secs := uint64(now.Unix()) + 4611686018427387914 // TAI64 epoch offset
	binary.BigEndian.PutUint64(timestamp[:8], secs)
	binary.BigEndian.PutUint32(timestamp[8:], uint32(now.Nanosecond()))
	return timestamp
}
