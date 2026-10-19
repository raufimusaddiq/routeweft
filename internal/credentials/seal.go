package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// MasterKeySize is the AES-256 key size required by the sealed-blob format.
const MasterKeySize = 32

// blobPrefix versions the sealed-blob envelope so a future key/algorithm change
// can be detected instead of silently misread.
const blobPrefix = "rwc1."

// ErrKeyRequired means no master key was supplied where sealing is mandatory.
var ErrKeyRequired = errors.New("credential master key is required")

// Sealer encrypts and decrypts connection secret blobs with AES-256-GCM. It is
// safe for concurrent use because it holds no mutable state after construction.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer validates the operator-supplied master key. A wrong length is a
// configuration error, not a per-request failure, so it fails fast.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != MasterKeySize {
		return nil, fmt.Errorf("credential master key must be %d bytes", MasterKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credential master key is unusable: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential cipher is unusable: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext and returns a versioned, base64 envelope. The random
// nonce is prepended so no nonce reuse can occur across seals.
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	if s == nil || s.aead == nil {
		return "", ErrKeyRequired
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	sealed := s.aead.Seal(nonce, nonce, plaintext, nil)
	return blobPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open reverses Seal. A malformed or truncated envelope is rejected without
// attempting to decipher it, so corrupt rows cannot be mistaken for credentials.
func (s *Sealer) Open(envelope string) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, ErrKeyRequired
	}
	trimmed := strings.TrimSpace(envelope)
	if trimmed == "" {
		return nil, errors.New("credential blob is empty")
	}
	if !strings.HasPrefix(trimmed, blobPrefix) {
		return nil, errors.New("credential blob has an unknown envelope version")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(trimmed, blobPrefix))
	if err != nil {
		return nil, errors.New("credential blob is not valid base64")
	}
	nonceSize := s.aead.NonceSize()
	if len(raw) < nonceSize+s.aead.Overhead() {
		return nil, errors.New("credential blob is truncated")
	}
	plaintext, err := s.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		return nil, errors.New("credential blob failed authentication")
	}
	return plaintext, nil
}
