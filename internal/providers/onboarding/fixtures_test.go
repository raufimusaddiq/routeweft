package onboarding

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"testing"
)

// urlSafeEncode is the base64url alphabet without padding, as JWTs use.
func urlSafeEncode(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// base64Std encodes a fixture the way the Cline extension does.
func base64Std(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }

// rsaKeyPair mints a throwaway RSA keypair for the Zed handshake test, returning
// the URL-safe base64 private key and the matching public key.
func rsaKeyPair(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return base64.RawURLEncoding.EncodeToString(der), &key.PublicKey
}

// encryptZedToken produces the RSA-encrypted callback payload the Zed app sends.
func encryptZedToken(t *testing.T, publicKey *rsa.PublicKey, token string) string {
	t.Helper()
	encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, publicKey, []byte(token), nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(encrypted)
}

// encryptXiaomiPayload builds the ECDH + AES-256-GCM callback payload the Xiaomi
// platform sends: [12-byte nonce][32-byte ephemeral pubkey][ciphertext][16-byte tag].
func encryptXiaomiPayload(t *testing.T, publicKeyB64, plaintext string) string {
	t.Helper()
	spki, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		t.Fatal(err)
	}
	recipient, ok := clientPublic.(interface{ Bytes() []byte })
	if !ok {
		t.Fatal("fixture public key has no raw bytes")
	}
	recipientKey, err := ecdh.X25519().NewPublicKey(recipient.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ephemeral.ECDH(recipientKey)
	if err != nil {
		t.Fatal(err)
	}
	derived := sha256.Sum256(shared)
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append([]byte(nil), nonce...)
	payload = append(payload, ephemeral.PublicKey().Bytes()...)
	payload = append(payload, sealed...)
	return base64.StdEncoding.EncodeToString(payload)
}
