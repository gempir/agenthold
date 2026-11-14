// Package secrets stores secret values encrypted at rest and renders the
// headers the egress proxy injects. Secret values never enter a sandbox.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the master key length.
const KeySize = chacha20poly1305.KeySize

// Box encrypts and decrypts with the master key (XChaCha20-Poly1305).
type Box struct{ key []byte }

// NewBox returns a box for a 32-byte key.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", KeySize, len(key))
	}
	return &Box{key: append([]byte(nil), key...)}, nil
}

// Seal encrypts plaintext. aad binds the ciphertext to its context (the
// secret id), so ciphertexts cannot be swapped between rows.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open decrypts what Seal produced.
func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(b.key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, errors.New("decryption failed (wrong master key?)")
	}
	return pt, nil
}

// LoadMasterKey reads the master key from AGENTHOLD_MASTER_KEY (base64), from
// keyFile, or generates keyFile if it does not exist yet. generated reports
// whether a new key was created, so the caller can warn about backing it up.
func LoadMasterKey(keyFile string) (key []byte, generated bool, err error) {
	if v := strings.TrimSpace(os.Getenv("AGENTHOLD_MASTER_KEY")); v != "" {
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, false, fmt.Errorf("AGENTHOLD_MASTER_KEY: %w", err)
		}
		return key, false, nil
	}
	data, err := os.ReadFile(keyFile)
	if err == nil {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", keyFile, err)
		}
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key = make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return key, true, nil
}
