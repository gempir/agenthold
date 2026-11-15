package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
)

const tokenPrefix = "ahnode1_"

// ErrInvalidToken is returned for tokens that cannot be parsed.
var ErrInvalidToken = errors.New("invalid join token")

// Token is what an admin hands to a new node. It is a bearer credential until
// redeemed once; the controller stores only HashSecret(Secret).
type Token struct {
	// Endpoint is the controller's node listener, host:port.
	Endpoint string `json:"e"`
	// Fingerprint pins the controller's certificate.
	Fingerprint string `json:"f"`
	Secret      string `json:"s"`
}

// NewToken creates a token with a fresh random secret.
func NewToken(endpoint, fingerprint string) (Token, error) {
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return Token{}, errors.New("node endpoint must be host:port")
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Token{}, err
	}
	return Token{Endpoint: endpoint, Fingerprint: fingerprint, Secret: base64.RawURLEncoding.EncodeToString(b[:])}, nil
}

// String encodes the token for `agenthold node --join`.
func (t Token) String() string {
	data, _ := json.Marshal(t)
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseToken decodes a token produced by String.
func ParseToken(s string) (Token, error) {
	var t Token
	body, ok := strings.CutPrefix(strings.TrimSpace(s), tokenPrefix)
	if !ok || len(body) > 4096 {
		return t, ErrInvalidToken
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(data, &t) != nil {
		return t, ErrInvalidToken
	}
	if _, _, err := net.SplitHostPort(t.Endpoint); err != nil || len(t.Fingerprint) != 64 || len(t.Secret) < 32 {
		return t, ErrInvalidToken
	}
	if _, err := hex.DecodeString(t.Fingerprint); err != nil {
		return t, ErrInvalidToken
	}
	return t, nil
}

// HashSecret is the value stored by the controller for a token secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
