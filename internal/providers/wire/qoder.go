package wire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// COSY header constants (open-sse/shared/qoder/constants.js:67-74). The gateway
// matches these against the values used at signing time.
const (
	qoderIDEVersion   = "1.0.0"
	qoderClientType   = "5"
	qoderDataPolicy   = "disagree"
	qoderLoginVersion = "v2"
	qoderMachineOS    = "x86_64_windows"
	qoderMachineType  = "5"
)

// qoderRSAPublicKey wraps the per-request AES key (constants.js:98-105).
const qoderRSAPublicKey = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// Errors a caller can match; each is a refusal to build, never a degraded header set.
var (
	ErrCosyUserID    = errors.New("cosy: user id is empty")
	ErrCosyToken     = errors.New("cosy: auth token is empty")
	ErrCosyMachineID = errors.New("cosy: machine id is empty")
)

// CosyCredentials is what the builder needs from a qoder connection. MachineID
// is an input: the builder never generates or persists it, because a regenerated
// machine id invalidates every signature and a write from the request path is
// forbidden (AGENTS.md §3). Onboarding mints it (onboarding/providers.go:455).
type CosyCredentials struct {
	UserID    string
	AuthToken string // device access token (`dt-...`)
	Name      string
	Email     string
	MachineID string
}

// cosyEntropy is the per-request randomness and clock, injectable so a test can
// pin every byte. The production value is cosyLive.
type cosyEntropy struct {
	aesKey    func() string // 16 chars (cosy.js:33-35)
	requestID func() string // payload requestId (cosy.js:135)
	headerID  func() string // X-Request-Id (cosy.js:173), a separate uuid
	now       func() time.Time
	rsaRandom func() (rsaReader, error)
}

type rsaReader interface{ Read([]byte) (int, error) }

var cosyLive = cosyEntropy{
	aesKey:    func() string { return uuid.NewString()[:16] },
	requestID: uuid.NewString,
	headerID:  uuid.NewString,
	now:       time.Now,
	rsaRandom: func() (rsaReader, error) { return rand.Reader, nil },
}

// BuildCosyHeaders returns the full header set for one qoder request
// (cosy.js:116-175). body is the exact bytes that will be sent (empty for GET);
// requestURL is the full URL, used only for sigPath.
func BuildCosyHeaders(body []byte, requestURL string, creds CosyCredentials) (map[string]string, error) {
	return buildCosyHeaders(body, requestURL, creds, cosyLive)
}

func buildCosyHeaders(body []byte, requestURL string, creds CosyCredentials, e cosyEntropy) (map[string]string, error) {
	if creds.UserID == "" {
		return nil, ErrCosyUserID
	}
	if creds.AuthToken == "" {
		return nil, ErrCosyToken
	}
	if creds.MachineID == "" {
		return nil, ErrCosyMachineID
	}

	aesKey := e.aesKey()
	cosyKey, info, err := cosyEncryptUserInfo(cosyUserInfo{
		UID: creds.UserID, Token: creds.AuthToken, Name: creds.Name, AID: "", Email: creds.Email,
	}, aesKey, e)
	if err != nil {
		return nil, err
	}

	timestamp := strconv.FormatInt(e.now().Unix(), 10)
	payload, err := cosyJSON(cosyPayload{
		Version: "v1", RequestID: e.requestID(), Info: info, CosyVersion: qoderIDEVersion, IDEVersion: "",
	})
	if err != nil {
		return nil, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString([]byte(payload))

	sigPath := cosySigPath(requestURL)
	sig := cosySignature(payloadB64, cosyKey, timestamp, body, sigPath)

	return map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKey,
		"Cosy-User":              creds.UserID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           qoderIDEVersion,
		"Cosy-Machineid":         creds.MachineID,
		"Cosy-Machinetoken":      creds.MachineID,
		"Cosy-Machinetype":       qoderMachineType,
		"Cosy-Machineos":         qoderMachineOS,
		"Cosy-Clienttype":        qoderClientType,
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          md5Hex(body),
		"Cosy-Bodylength":        strconv.Itoa(len(body)),
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       qoderDataPolicy,
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          qoderLoginVersion,
		"X-Request-Id":           e.headerID(),
	}, nil
}

// cosySigInput is the canonical string the signature covers
// (cosy.js:147): payloadB64 \n cosyKey \n timestamp \n body \n sigPath.
//
// The pin renders the body with toString("latin1") and re-encodes the whole
// string with Buffer.from(..., "latin1") (:147-148). Both steps map one byte to
// one code point and back, so the signed bytes are the body bytes verbatim; it
// is not UTF-8 decoding. The other parts are ASCII (base64, decimal, an escaped
// URL path), so plain byte concatenation is exact.
func cosySigInput(payloadB64, cosyKey, timestamp string, body []byte, sigPath string) []byte {
	var b bytes.Buffer
	b.WriteString(payloadB64)
	b.WriteByte('\n')
	b.WriteString(cosyKey)
	b.WriteByte('\n')
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.Write(body)
	b.WriteByte('\n')
	b.WriteString(sigPath)
	return b.Bytes()
}

// cosySignature is md5hex of cosySigInput (cosy.js:148).
func cosySignature(payloadB64, cosyKey, timestamp string, body []byte, sigPath string) string {
	return md5Hex(cosySigInput(payloadB64, cosyKey, timestamp, body, sigPath))
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// cosySigPath strips the leading "/algo" from the URL pathname (cosy.js:81-92).
// An unparseable URL gives "". A URL with no path has pathname "/" under WHATWG
// URL, which the pin returns unchanged.
func cosySigPath(requestURL string) string {
	u, err := url.Parse(requestURL)
	if err != nil || u.Host == "" {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return strings.TrimPrefix(path, "/algo")
}

// cosyUserInfo and cosyPayload keep the pin's JSON key order
// (cosy.js:126-132, 137-143); the order is part of the signed bytes.
type cosyUserInfo struct {
	UID   string `json:"uid"`
	Token string `json:"security_oauth_token"`
	Name  string `json:"name"`
	AID   string `json:"aid"`
	Email string `json:"email"`
}

type cosyPayload struct {
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	Info        string `json:"info"`
	CosyVersion string `json:"cosyVersion"`
	IDEVersion  string `json:"ideVersion"`
}

// cosyJSON marshals like JSON.stringify: no HTML escaping, no trailing newline,
// and U+2028/2029 left literal.
func cosyJSON(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(b.String(), "\n")
	out = strings.ReplaceAll(out, ` `, " ")
	out = strings.ReplaceAll(out, ` `, " ")
	return out, nil
}

// cosyEncryptUserInfo returns the RSA-wrapped AES key and the AES-128-CBC
// encrypted user info, both base64 (cosy.js:44-71). The IV is the key bytes
// (:49); padding is PKCS#7 (:37-42); the RSA step is PKCS#1 v1.5 (:59).
func cosyEncryptUserInfo(info cosyUserInfo, aesKey string, e cosyEntropy) (cosyKey, infoB64 string, err error) {
	key := []byte(aesKey)
	if len(key) != 16 {
		return "", "", fmt.Errorf("cosy: aes key must be 16 bytes, got %d", len(key))
	}
	plaintext, err := cosyJSON(info)
	if err != nil {
		return "", "", err
	}
	pad := 16 - len(plaintext)%16
	padded := append([]byte(plaintext), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", err
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key).CryptBlocks(out, padded)

	pub, err := cosyPublicKey()
	if err != nil {
		return "", "", err
	}
	random, err := e.rsaRandom()
	if err != nil {
		return "", "", err
	}
	wrapped, err := rsa.EncryptPKCS1v15(random, pub, key)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(wrapped), base64.StdEncoding.EncodeToString(out), nil
}

func cosyPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(qoderRSAPublicKey))
	if block == nil {
		return nil, errors.New("cosy: public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("cosy: public key is not RSA")
	}
	return pub, nil
}

func qoderHeaders(provider routing.ProviderRef, body []byte, requestURL string) (map[string]string, error) {
	// Serving signs the agent envelope in QoderDo; this hook is the listing
	// signer (the listing route calls RequestHeaders over the final URL/body,
	// with no model). Returning nil for a model keeps QoderDo's local refusals.
	if strings.TrimSpace(provider.UpstreamModel) != "" {
		return nil, nil
	}
	settings := provider.Connection.Settings
	machineID := strings.TrimSpace(settings.MachineID)
	if machineID == "" {
		return nil, &CredentialError{Err: fmt.Errorf("qoder: %w; re-run Qoder sign-in to create a connection with machineId", ErrCosyMachineID)}
	}
	headers, err := BuildCosyHeaders(body, requestURL, CosyCredentials{
		UserID: strings.TrimSpace(settings.UserID), AuthToken: strings.TrimSpace(provider.APIToken),
		Name: provider.Connection.Name, Email: settings.Email, MachineID: machineID,
	})
	if errors.Is(err, ErrCosyUserID) || errors.Is(err, ErrCosyToken) {
		return nil, &CredentialError{Err: err}
	}
	return headers, err
}
