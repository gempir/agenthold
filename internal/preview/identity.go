package preview

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IdentityTTL is the lifetime of an X-Agenthold-Identity token.
const IdentityTTL = 5 * time.Minute

// Signer issues the identity tokens (EdDSA JWTs) apps receive.
type Signer struct {
	key ed25519.PrivateKey
	kid string
}

// LoadOrCreateSigner reads an Ed25519 key (PKCS#8 PEM) from path, creating it
// on first use, like the egress CA and the node listener identity next to it.
func LoadOrCreateSigner(path string) (*Signer, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		_, key, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		der, merr := x509.MarshalPKCS8PrivateKey(key)
		if merr != nil {
			return nil, merr
		}
		data = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return NewSigner(key), nil
}

// NewSigner wraps a key.
func NewSigner(key ed25519.PrivateKey) *Signer {
	pub := key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(pub)
	return &Signer{key: key, kid: b64(sum[:12])}
}

// Claims are the identity token's claims.
type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience string `json:"aud"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Sign returns a compact JWS of c.
func (s *Signer) Sign(c Claims) (string, error) {
	h, err := json.Marshal(header{Alg: "EdDSA", Typ: "JWT", Kid: s.kid})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	input := b64(h) + "." + b64(p)
	return input + "." + b64(ed25519.Sign(s.key, []byte(input))), nil
}

// JWK is one public key in a JWKS.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// JWKS is a JSON Web Key Set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// JWKS returns the public keys apps verify identity tokens with.
func (s *Signer) JWKS() JWKS {
	pub := s.key.Public().(ed25519.PublicKey)
	return JWKS{Keys: []JWK{{Kty: "OKP", Crv: "Ed25519", X: b64(pub), Kid: s.kid, Alg: "EdDSA", Use: "sig"}}}
}

// Verify checks a token against a key set, audience, issuer and time, the
// way an app should. It is here for tests and as a reference.
func Verify(token string, set JWKS, audience, issuer string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("malformed token")
	}
	hb, err := unb64(parts[0])
	if err != nil {
		return c, err
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return c, err
	}
	if h.Alg != "EdDSA" {
		return c, fmt.Errorf("unexpected alg %q", h.Alg)
	}
	var pub ed25519.PublicKey
	for _, k := range set.Keys {
		if k.Kid == h.Kid && k.Kty == "OKP" && k.Crv == "Ed25519" {
			if pub, err = unb64(k.X); err != nil {
				return c, err
			}
		}
	}
	if len(pub) != ed25519.PublicKeySize {
		return c, errors.New("unknown key")
	}
	sig, err := unb64(parts[2])
	if err != nil {
		return c, err
	}
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return c, errors.New("bad signature")
	}
	pb, err := unb64(parts[1])
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(pb, &c); err != nil {
		return c, err
	}
	switch {
	case c.Audience != audience:
		return c, fmt.Errorf("token is for %q", c.Audience)
	case c.Issuer != issuer:
		return c, fmt.Errorf("token issued by %q", c.Issuer)
	case now.Unix() >= c.Expires:
		return c, errors.New("token expired")
	}
	return c, nil
}

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
