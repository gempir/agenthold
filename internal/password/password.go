// Package password hashes and verifies the local admin password (argon2id,
// PHC string format: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>).
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters used for new hashes (OWASP's argon2id recommendation range).
const (
	memoryKiB  = 64 * 1024
	iterations = 3
	threads    = 4
	saltLen    = 16
	keyLen     = 32
)

// ErrInvalidHash is returned for strings that are not argon2id hashes.
var ErrInvalidHash = errors.New("not an argon2id hash (create one with `agenthold hash-password`)")

// Hash is a parsed argon2id hash.
type Hash struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

var b64 = base64.RawStdEncoding

// New hashes pw with fresh salt and returns the encoded hash.
func New(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, iterations, memoryKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, iterations, threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Parse decodes an encoded hash.
func Parse(encoded string) (*Hash, error) {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, ErrInvalidHash
	}
	h := &Hash{}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.memory, &h.time, &h.threads); err != nil {
		return nil, ErrInvalidHash
	}
	// Bounds keep a hostile config from exhausting memory or CPU.
	if h.memory < 8*1024 || h.memory > 4*1024*1024 || h.time < 1 || h.time > 100 || h.threads < 1 {
		return nil, ErrInvalidHash
	}
	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return nil, ErrInvalidHash
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 128 {
		return nil, ErrInvalidHash
	}
	return h, nil
}

// Verify reports whether pw matches, in constant time.
func (h *Hash) Verify(pw string) bool {
	key := argon2.IDKey([]byte(pw), h.salt, h.time, h.memory, h.threads, uint32(len(h.key))) //nolint:gosec // len is bounded by Parse
	return subtle.ConstantTimeCompare(key, h.key) == 1
}
