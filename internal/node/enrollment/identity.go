// Package enrollment authenticates edge nodes to their controller.
//
// Both sides own a self-signed ed25519 TLS certificate. A join token carries
// the controller's endpoint, the SHA-256 fingerprint of its certificate and a
// single-use secret; the node pins the controller by that fingerprint and the
// controller records the fingerprint of the node's certificate when the token
// is redeemed. Every later connection is mutually authenticated by those two
// pinned fingerprints. No certificate authority is involved.
package enrollment

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Identity is a self-signed TLS identity recognized by its fingerprint.
type Identity struct {
	Certificate tls.Certificate
	Fingerprint string
}

// Fingerprint is the lowercase hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// NewIdentity generates a fresh key pair and a long-lived self-signed certificate.
// Validity is irrelevant for trust (fingerprints are pinned) but TLS needs one.
func NewIdentity(commonName string) (*Identity, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(100, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, err
	}
	return identityFrom(der, key)
}

func identityFrom(der []byte, key ed25519.PrivateKey) (*Identity, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	if pub, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !pub.Equal(key.Public()) {
		return nil, errors.New("certificate does not match its key")
	}
	return &Identity{
		Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		Fingerprint: Fingerprint(der),
	}, nil
}

// MarshalPEM encodes the certificate and private key.
func (id *Identity) MarshalPEM() ([]byte, error) {
	keyDER, err := x509.MarshalPKCS8PrivateKey(id.Certificate.PrivateKey)
	if err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Certificate.Certificate[0]})
	return append(out, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})...), nil
}

// ParseIdentity decodes what MarshalPEM produced.
func ParseIdentity(data []byte) (*Identity, error) {
	var der []byte
	var key ed25519.PrivateKey
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			der = block.Bytes
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, err
			}
			var ok bool
			if key, ok = k.(ed25519.PrivateKey); !ok {
				return nil, errors.New("identity key must be ed25519")
			}
		}
	}
	if der == nil || key == nil {
		return nil, errors.New("identity needs a certificate and a private key")
	}
	return identityFrom(der, key)
}

// LoadOrCreateIdentity reads a PEM identity from path, creating it (mode 0600)
// on first use. The controller keeps its identity in its data directory.
func LoadOrCreateIdentity(path, commonName string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		id, err := ParseIdentity(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	id, err := NewIdentity(commonName)
	if err != nil {
		return nil, err
	}
	data, err = id.MarshalPEM()
	if err != nil {
		return nil, err
	}
	if err := writePrivate(path, data); err != nil {
		return nil, err
	}
	return id, nil
}

// writePrivate atomically replaces path with a 0600 file.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
