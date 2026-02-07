// Package auth provides user identity and authentication for TunnelMesh.
package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"strings"

	"github.com/tyler-smith/go-bip39"
)

// MnemonicWordCount is the number of words in a TunnelMesh mnemonic.
const MnemonicWordCount = 3

// GenerateMnemonic generates a new 3-word mnemonic phrase.
// The mnemonic can be used to derive a deterministic ED25519 keypair.
func GenerateMnemonic() (string, error) {
	// Generate 32 bits of entropy (enough for 3 words from 2048 word list)
	// BIP39 requires minimum 128 bits, so we generate that and take first 3 words
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		return "", err
	}

	fullMnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return "", err
	}

	// Take only the first 3 words
	words := strings.Fields(fullMnemonic)
	return strings.Join(words[:MnemonicWordCount], " "), nil
}

// ValidateMnemonic checks if a mnemonic phrase is valid.
// It must have exactly 3 words, all from the BIP39 wordlist.
func ValidateMnemonic(mnemonic string) error {
	mnemonic = normalizeMnemonic(mnemonic)
	words := splitMnemonic(mnemonic)

	if len(words) != MnemonicWordCount {
		return errors.New("mnemonic must have exactly 3 words")
	}

	for _, word := range words {
		if !isValidWord(word) {
			return errors.New("invalid word in mnemonic: " + word)
		}
	}

	return nil
}

// DeriveKeypairFromMnemonic derives an ED25519 keypair from a mnemonic phrase.
// The derivation is deterministic: the same mnemonic always produces the same keypair.
func DeriveKeypairFromMnemonic(mnemonic string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, nil, err
	}

	mnemonic = normalizeMnemonic(mnemonic)

	// Hash the mnemonic to get a 32-byte seed for ED25519
	hash := sha256.Sum256([]byte(mnemonic))
	seed := hash[:]

	// Generate ED25519 keypair from seed
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return publicKey, privateKey, nil
}

// normalizeMnemonic converts to lowercase and trims whitespace.
func normalizeMnemonic(mnemonic string) string {
	return strings.ToLower(strings.TrimSpace(mnemonic))
}

// splitMnemonic splits a mnemonic into individual words.
func splitMnemonic(mnemonic string) []string {
	return strings.Fields(mnemonic)
}

// isValidWord checks if a word is in the BIP39 wordlist.
func isValidWord(word string) bool {
	wordList := bip39.GetWordList()
	for _, w := range wordList {
		if w == word {
			return true
		}
	}
	return false
}
