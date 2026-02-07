package auth

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateMnemonic(t *testing.T) {
	mnemonic, err := GenerateMnemonic()
	require.NoError(t, err)

	words := splitMnemonic(mnemonic)
	assert.Len(t, words, 3, "mnemonic should have exactly 3 words")

	// Each word should be in the wordlist
	for _, word := range words {
		assert.True(t, isValidWord(word), "word %q should be in wordlist", word)
	}
}

func TestGenerateMnemonicUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		mnemonic, err := GenerateMnemonic()
		require.NoError(t, err)
		assert.False(t, seen[mnemonic], "mnemonic should be unique")
		seen[mnemonic] = true
	}
}

func TestValidateMnemonic(t *testing.T) {
	tests := []struct {
		name      string
		mnemonic  string
		wantValid bool
	}{
		{"valid 3 words", "abandon ability able", true},
		{"valid different words", "zoo zebra zero", true},
		{"too few words", "abandon ability", false},
		{"too many words", "abandon ability able about", false},
		{"invalid word", "abandon ability notaword", false},
		{"empty", "", false},
		{"mixed case valid", "Abandon Ability Able", true}, // should normalize
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMnemonic(tt.mnemonic)
			if tt.wantValid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestDeriveKeypairFromMnemonic(t *testing.T) {
	mnemonic := "abandon ability able"

	pub, priv, err := DeriveKeypairFromMnemonic(mnemonic)
	require.NoError(t, err)
	assert.Len(t, pub, ed25519.PublicKeySize)
	assert.Len(t, priv, ed25519.PrivateKeySize)

	// Verify the keypair works for signing
	message := []byte("test message")
	sig := ed25519.Sign(priv, message)
	assert.True(t, ed25519.Verify(pub, message, sig))
}

func TestDeriveKeypairDeterministic(t *testing.T) {
	mnemonic := "abandon ability able"

	pub1, priv1, err := DeriveKeypairFromMnemonic(mnemonic)
	require.NoError(t, err)

	pub2, priv2, err := DeriveKeypairFromMnemonic(mnemonic)
	require.NoError(t, err)

	assert.Equal(t, pub1, pub2, "same mnemonic should produce same public key")
	assert.Equal(t, priv1, priv2, "same mnemonic should produce same private key")
}

func TestDeriveKeypairDifferentMnemonics(t *testing.T) {
	pub1, _, err := DeriveKeypairFromMnemonic("abandon ability able")
	require.NoError(t, err)

	pub2, _, err := DeriveKeypairFromMnemonic("zoo zebra zero")
	require.NoError(t, err)

	assert.NotEqual(t, pub1, pub2, "different mnemonics should produce different keys")
}

func TestDeriveKeypairCaseInsensitive(t *testing.T) {
	pub1, _, err := DeriveKeypairFromMnemonic("abandon ability able")
	require.NoError(t, err)

	pub2, _, err := DeriveKeypairFromMnemonic("ABANDON ABILITY ABLE")
	require.NoError(t, err)

	pub3, _, err := DeriveKeypairFromMnemonic("Abandon Ability Able")
	require.NoError(t, err)

	assert.Equal(t, pub1, pub2, "case should not matter")
	assert.Equal(t, pub1, pub3, "case should not matter")
}

func TestDeriveKeypairInvalidMnemonic(t *testing.T) {
	_, _, err := DeriveKeypairFromMnemonic("invalid mnemonic words")
	assert.Error(t, err)
}
