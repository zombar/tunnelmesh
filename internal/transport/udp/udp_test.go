package udp

import (
	"testing"
)

func TestPacketHeader(t *testing.T) {
	header := PacketHeader{
		Type:     PacketTypeData,
		Receiver: 0x12345678,
		Counter:  0xABCDEF0123456789,
	}

	data := header.Marshal()
	if len(data) != HeaderSize {
		t.Errorf("expected header size %d, got %d", HeaderSize, len(data))
	}

	parsed, err := UnmarshalHeader(data)
	if err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.Type != header.Type {
		t.Errorf("type mismatch: expected %d, got %d", header.Type, parsed.Type)
	}
	if parsed.Receiver != header.Receiver {
		t.Errorf("receiver mismatch: expected %x, got %x", header.Receiver, parsed.Receiver)
	}
	if parsed.Counter != header.Counter {
		t.Errorf("counter mismatch: expected %x, got %x", header.Counter, parsed.Counter)
	}
}

func TestReplayWindow(t *testing.T) {
	w := NewReplayWindow(64)

	// First packet should be accepted
	if !w.Check(1) {
		t.Error("first packet should be accepted")
	}

	// Duplicate should be rejected
	if w.Check(1) {
		t.Error("duplicate packet should be rejected")
	}

	// Sequential packets should be accepted
	for i := uint64(2); i <= 10; i++ {
		if !w.Check(i) {
			t.Errorf("packet %d should be accepted", i)
		}
	}

	// Out of order within window should be accepted
	// (already received 1-10, so 11 should work)
	if !w.Check(11) {
		t.Error("packet 11 should be accepted")
	}

	// Packet 0 is always invalid
	if w.Check(0) {
		t.Error("packet 0 should be rejected")
	}

	// Far future packet should be accepted and advance window
	if !w.Check(100) {
		t.Error("far future packet should be accepted")
	}

	// Now very old packets should be rejected
	if w.Check(1) {
		t.Error("very old packet should be rejected after window advance")
	}
}

func TestCryptoRoundtrip(t *testing.T) {
	// Generate keys
	priv1, pub1, err := X25519KeyPair()
	if err != nil {
		t.Fatalf("generate key pair 1: %v", err)
	}

	priv2, pub2, err := X25519KeyPair()
	if err != nil {
		t.Fatalf("generate key pair 2: %v", err)
	}

	// Compute shared secrets
	shared1, err := X25519SharedSecret(priv1, pub2)
	if err != nil {
		t.Fatalf("shared secret 1: %v", err)
	}

	shared2, err := X25519SharedSecret(priv2, pub1)
	if err != nil {
		t.Fatalf("shared secret 2: %v", err)
	}

	// Shared secrets should match
	if shared1 != shared2 {
		t.Error("shared secrets don't match")
	}

	// Derive keys
	sendKey1, recvKey1, err := DeriveKeys(shared1, pub1, pub2, true)
	if err != nil {
		t.Fatalf("derive keys 1: %v", err)
	}

	sendKey2, recvKey2, err := DeriveKeys(shared2, pub1, pub2, false)
	if err != nil {
		t.Fatalf("derive keys 2: %v", err)
	}

	// Send key of one side should be receive key of other
	if sendKey1 != recvKey2 {
		t.Error("key derivation mismatch: sendKey1 != recvKey2")
	}
	if recvKey1 != sendKey2 {
		t.Error("key derivation mismatch: recvKey1 != sendKey2")
	}

	// Create crypto states
	crypto1, err := NewCryptoState(sendKey1, recvKey1)
	if err != nil {
		t.Fatalf("create crypto 1: %v", err)
	}

	crypto2, err := NewCryptoState(sendKey2, recvKey2)
	if err != nil {
		t.Fatalf("create crypto 2: %v", err)
	}

	// Test encryption/decryption
	plaintext := []byte("Hello, World!")
	var counter uint64 = 1
	var additionalData []byte

	ciphertext := crypto1.Encrypt(counter, plaintext, additionalData)
	decrypted, err := crypto2.Decrypt(counter, ciphertext, additionalData)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted mismatch: expected %q, got %q", plaintext, decrypted)
	}

	// Test wrong counter
	_, err = crypto2.Decrypt(counter+1, ciphertext, additionalData)
	if err == nil {
		t.Error("decrypt with wrong counter should fail")
	}
}

func TestHandshakeState(t *testing.T) {
	// Generate key pairs for initiator and responder
	initPriv, initPub, err := X25519KeyPair()
	if err != nil {
		t.Fatalf("generate initiator keys: %v", err)
	}

	respPriv, respPub, err := X25519KeyPair()
	if err != nil {
		t.Fatalf("generate responder keys: %v", err)
	}

	// Create handshake states
	initiator, err := NewHandshakeState(initPriv, initPub, respPub)
	if err != nil {
		t.Fatalf("create initiator state: %v", err)
	}

	// For responder, create a fresh state for consuming initiation
	responder, err := NewHandshakeState(respPriv, respPub, initPub)
	if err != nil {
		t.Fatalf("create responder state: %v", err)
	}
	// Re-init for responder mode
	responder.hash = blake2sHash(NoiseConstruction)
	responder.chainingKey = responder.hash
	responder.hash = blake2sHash(append(responder.hash[:], NoiseIdentifier...))
	responder.hash = blake2sHash(append(responder.hash[:], respPub[:]...))

	// Create initiation
	initMsg, err := initiator.CreateInitiation()
	if err != nil {
		t.Fatalf("create initiation: %v", err)
	}

	// Consume initiation
	if err := responder.ConsumeInitiation(initMsg); err != nil {
		t.Fatalf("consume initiation: %v", err)
	}

	// Create response
	respMsg, err := responder.CreateResponse()
	if err != nil {
		t.Fatalf("create response: %v", err)
	}

	// Consume response
	if err := initiator.ConsumeResponse(respMsg); err != nil {
		t.Fatalf("consume response: %v", err)
	}

	// Derive keys
	initSend, initRecv, err := initiator.DeriveKeys()
	if err != nil {
		t.Fatalf("initiator derive keys: %v", err)
	}

	respSend, respRecv, err := responder.DeriveKeys()
	if err != nil {
		t.Fatalf("responder derive keys: %v", err)
	}

	// Verify keys match (initiator send = responder recv, etc.)
	if initSend != respRecv {
		t.Error("initiator send key != responder recv key")
	}
	if initRecv != respSend {
		t.Error("initiator recv key != responder send key")
	}
}

func BenchmarkEncrypt(b *testing.B) {
	priv1, _, _ := X25519KeyPair()
	_, pub2, _ := X25519KeyPair()
	shared, _ := X25519SharedSecret(priv1, pub2)

	var sendKey, recvKey [32]byte
	copy(sendKey[:], shared[:])
	copy(recvKey[:], shared[:])

	crypto, _ := NewCryptoState(sendKey, recvKey)
	plaintext := make([]byte, 1400) // Typical MTU-sized packet

	b.ResetTimer()
	b.SetBytes(int64(len(plaintext)))

	for i := 0; i < b.N; i++ {
		crypto.Encrypt(uint64(i), plaintext, nil)
	}
}

func BenchmarkDecrypt(b *testing.B) {
	priv1, _, _ := X25519KeyPair()
	_, pub2, _ := X25519KeyPair()
	shared, _ := X25519SharedSecret(priv1, pub2)

	var sendKey, recvKey [32]byte
	copy(sendKey[:], shared[:])
	copy(recvKey[:], shared[:])

	crypto, _ := NewCryptoState(sendKey, recvKey)
	plaintext := make([]byte, 1400)
	ciphertext := crypto.Encrypt(1, plaintext, nil)

	b.ResetTimer()
	b.SetBytes(int64(len(plaintext)))

	for i := 0; i < b.N; i++ {
		_, _ = crypto.Decrypt(1, ciphertext, nil)
	}
}
