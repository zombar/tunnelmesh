package udp

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/blake2s"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
)

// Noise protocol constants
var (
	// NoiseConstruction is the Noise protocol name
	NoiseConstruction = []byte("Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s")
	// NoiseIdentifier is the protocol identifier
	NoiseIdentifier = []byte("TunnelMesh v1")
)

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

// NewHandshakeState creates a new handshake state for initiating or responding.
func NewHandshakeState(staticPrivate, staticPublic, peerStaticPublic [32]byte) (*HandshakeState, error) {
	hs := &HandshakeState{
		staticPrivate:    staticPrivate,
		staticPublic:     staticPublic,
		peerStaticPublic: peerStaticPublic,
	}

	// Generate ephemeral key pair
	if _, err := rand.Read(hs.ephemeralPrivate[:]); err != nil {
		return nil, err
	}
	// Clamp
	hs.ephemeralPrivate[0] &= 248
	hs.ephemeralPrivate[31] &= 127
	hs.ephemeralPrivate[31] |= 64

	pubSlice, err := curve25519.X25519(hs.ephemeralPrivate[:], curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	copy(hs.ephemeralPublic[:], pubSlice)

	// Initialize handshake state
	hs.initializeState()

	return hs, nil
}

// initializeState initializes the Noise protocol state.
func (hs *HandshakeState) initializeState() {
	// h = HASH(PROTOCOL_NAME)
	hs.hash = blake2sHash(NoiseConstruction)

	// ck = h
	hs.chainingKey = hs.hash

	// h = HASH(h || IDENTIFIER)
	hs.hash = blake2sHash(append(hs.hash[:], NoiseIdentifier...))

	// h = HASH(h || responder_static_public)
	hs.hash = blake2sHash(append(hs.hash[:], hs.peerStaticPublic[:]...))
}

// CreateInitiation creates the first handshake message (initiator -> responder).
// Returns the message bytes and error.
func (hs *HandshakeState) CreateInitiation() ([]byte, error) {
	var msg [148]byte // Total size: 4 + 32 + 48 + 12 + 16 + 16 + 16 + 4

	hs.isInitiator = true

	// Generate session index
	idx, err := GenerateSessionIndex()
	if err != nil {
		return nil, err
	}
	hs.localIndex = idx
	binary.LittleEndian.PutUint32(msg[0:4], idx)

	// e (ephemeral public key)
	copy(msg[4:36], hs.ephemeralPublic[:])
	hs.hash = blake2sHash(append(hs.hash[:], hs.ephemeralPublic[:]...))

	// es = DH(e, rs)
	es, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return nil, err
	}
	hs.mixKey(es)

	// Encrypt static public key: AEAD(k, 0, s, h)
	aead, err := chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return nil, err
	}
	var nonce [12]byte
	ciphertext := aead.Seal(nil, nonce[:], hs.staticPublic[:], hs.hash[:])
	copy(msg[36:84], ciphertext)
	hs.hash = blake2sHash(append(hs.hash[:], ciphertext...))

	// ss = DH(s, rs)
	ss, err := curve25519.X25519(hs.staticPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return nil, err
	}
	hs.mixKey(ss)

	// Timestamp (replay protection)
	now := time.Now().UnixNano()
	binary.LittleEndian.PutUint64(hs.timestamp[:8], uint64(now))
	if _, err := rand.Read(hs.timestamp[8:]); err != nil {
		return nil, err
	}

	// Encrypt timestamp: AEAD(k, 0, timestamp, h)
	aead, err = chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return nil, err
	}
	ciphertext = aead.Seal(nil, nonce[:], hs.timestamp[:], hs.hash[:])
	copy(msg[84:112], ciphertext)
	hs.hash = blake2sHash(append(hs.hash[:], ciphertext...))

	// MAC (for additional integrity, optional in our simplified version)
	mac := blake2sMAC(hs.chainingKey[:], msg[:112])
	copy(msg[112:128], mac[:16])

	return msg[:128], nil
}

// ConsumeInitiation processes an incoming initiation message (responder side).
func (hs *HandshakeState) ConsumeInitiation(msg []byte) error {
	if len(msg) < 128 {
		return fmt.Errorf("initiation message too short")
	}

	// Parse sender index
	hs.remoteIndex = binary.LittleEndian.Uint32(msg[0:4])

	// Parse ephemeral public key
	copy(hs.peerEphemeralPublic[:], msg[4:36])
	hs.hash = blake2sHash(append(hs.hash[:], hs.peerEphemeralPublic[:]...))

	// es = DH(s, re)
	es, err := curve25519.X25519(hs.staticPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return err
	}
	hs.mixKey(es)

	// Decrypt peer static public key
	aead, err := chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return err
	}
	var nonce [12]byte
	plaintext, err := aead.Open(nil, nonce[:], msg[36:84], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt static key: %w", err)
	}
	copy(hs.peerStaticPublic[:], plaintext)
	hs.hash = blake2sHash(append(hs.hash[:], msg[36:84]...))

	// ss = DH(s, rs)
	ss, err := curve25519.X25519(hs.staticPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return err
	}
	hs.mixKey(ss)

	// Decrypt timestamp
	aead, err = chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return err
	}
	plaintext, err = aead.Open(nil, nonce[:], msg[84:112], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt timestamp: %w", err)
	}
	copy(hs.timestamp[:], plaintext)
	hs.hash = blake2sHash(append(hs.hash[:], msg[84:112]...))

	// Verify MAC
	expectedMAC := blake2sMAC(hs.chainingKey[:], msg[:112])
	if !hmac.Equal(expectedMAC[:16], msg[112:128]) {
		return fmt.Errorf("MAC verification failed")
	}

	return nil
}

// CreateResponse creates the response message (responder -> initiator).
func (hs *HandshakeState) CreateResponse() ([]byte, error) {
	var msg [92]byte // 4 + 4 + 32 + 16 + 16 + 16 + 4

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
	hs.hash = blake2sHash(append(hs.hash[:], hs.ephemeralPublic[:]...))

	// ee = DH(e, re)
	ee, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return nil, err
	}
	hs.mixKey(ee)

	// se = DH(s, re)
	se, err := curve25519.X25519(hs.staticPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return nil, err
	}
	hs.mixKey(se)

	// Encrypt empty payload
	aead, err := chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return nil, err
	}
	var nonce [12]byte
	ciphertext := aead.Seal(nil, nonce[:], nil, hs.hash[:])
	copy(msg[40:56], ciphertext)
	hs.hash = blake2sHash(append(hs.hash[:], ciphertext...))

	// MAC
	mac := blake2sMAC(hs.chainingKey[:], msg[:56])
	copy(msg[56:72], mac[:16])

	return msg[:72], nil
}

// ConsumeResponse processes an incoming response message (initiator side).
func (hs *HandshakeState) ConsumeResponse(msg []byte) error {
	if len(msg) < 72 {
		return fmt.Errorf("response message too short")
	}

	// Parse sender and receiver indices
	hs.remoteIndex = binary.LittleEndian.Uint32(msg[0:4])
	receiverIndex := binary.LittleEndian.Uint32(msg[4:8])
	if receiverIndex != hs.localIndex {
		return fmt.Errorf("receiver index mismatch")
	}

	// Parse ephemeral public key
	copy(hs.peerEphemeralPublic[:], msg[8:40])
	hs.hash = blake2sHash(append(hs.hash[:], hs.peerEphemeralPublic[:]...))

	// ee = DH(e, re)
	ee, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerEphemeralPublic[:])
	if err != nil {
		return err
	}
	hs.mixKey(ee)

	// es = DH(e, rs)
	es, err := curve25519.X25519(hs.ephemeralPrivate[:], hs.peerStaticPublic[:])
	if err != nil {
		return err
	}
	hs.mixKey(es)

	// Decrypt empty payload (verify)
	aead, err := chacha20poly1305.New(hs.chainingKey[:])
	if err != nil {
		return err
	}
	var nonce [12]byte
	_, err = aead.Open(nil, nonce[:], msg[40:56], hs.hash[:])
	if err != nil {
		return fmt.Errorf("decrypt response: %w", err)
	}
	hs.hash = blake2sHash(append(hs.hash[:], msg[40:56]...))

	// Verify MAC
	expectedMAC := blake2sMAC(hs.chainingKey[:], msg[:56])
	if !hmac.Equal(expectedMAC[:16], msg[56:72]) {
		return fmt.Errorf("MAC verification failed")
	}

	return nil
}

// DeriveKeys derives the final transport keys after handshake completion.
func (hs *HandshakeState) DeriveKeys() (sendKey, recvKey [32]byte, err error) {
	// Final key derivation using HKDF
	reader := hkdfExpand(hs.chainingKey[:], nil)

	key1 := make([]byte, 32)
	key2 := make([]byte, 32)

	if _, err = io.ReadFull(reader, key1); err != nil {
		return
	}
	if _, err = io.ReadFull(reader, key2); err != nil {
		return
	}

	// Initiator sends with key1, receives with key2
	// Responder sends with key2, receives with key1
	if hs.isInitiator {
		copy(sendKey[:], key1)
		copy(recvKey[:], key2)
	} else {
		copy(sendKey[:], key2)
		copy(recvKey[:], key1)
	}

	return
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

// mixKey mixes new key material into the chaining key.
func (hs *HandshakeState) mixKey(input []byte) {
	reader := hkdfExpand(hs.chainingKey[:], input)
	_, _ = io.ReadFull(reader, hs.chainingKey[:])
}

// blake2sHash computes BLAKE2s-256 hash.
func blake2sHash(data []byte) [32]byte {
	return blake2s.Sum256(data)
}

// blake2sMAC computes BLAKE2s MAC with key.
func blake2sMAC(key, data []byte) [32]byte {
	h, _ := blake2s.New256(key[:32])
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// hkdfExpand performs HKDF-Expand using BLAKE2s.
func hkdfExpand(key, info []byte) io.Reader {
	return &hkdfReader{
		key:  key,
		info: info,
	}
}

type hkdfReader struct {
	key     []byte
	info    []byte
	counter byte
	buffer  []byte
	prev    []byte
}

func (r *hkdfReader) Read(p []byte) (n int, err error) {
	for len(p) > 0 {
		if len(r.buffer) == 0 {
			r.counter++
			h, _ := blake2s.New256(r.key)
			h.Write(r.prev)
			h.Write(r.info)
			h.Write([]byte{r.counter})
			r.prev = h.Sum(nil)
			r.buffer = r.prev
		}

		copied := copy(p, r.buffer)
		p = p[copied:]
		r.buffer = r.buffer[copied:]
		n += copied
	}
	return
}

// MixHash mixes data into the hash state.
func (hs *HandshakeState) MixHash(data []byte) {
	combined := make([]byte, len(hs.hash)+len(data))
	copy(combined, hs.hash[:])
	copy(combined[len(hs.hash):], data)
	hs.hash = blake2sHash(combined)
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
