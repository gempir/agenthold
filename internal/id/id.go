// Package id generates prefixed, random, URL-safe identifiers.
package id

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var enc = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// New returns prefix + "_" + 26 random base32 characters (130 bits).
func New(prefix string) string {
	return prefix + "_" + Random(16)
}

// Random returns n random bytes encoded as lowercase base32.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails, see crypto/rand docs
	return strings.ToLower(enc.EncodeToString(b))
}
