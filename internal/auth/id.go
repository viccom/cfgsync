package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a 32-character hex string from 16 random bytes.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
