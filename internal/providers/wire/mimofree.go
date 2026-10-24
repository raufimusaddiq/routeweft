package wire

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"time"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// MiMo Code Free (executors/mimo-free.js). The free chat endpoint is not
// anonymous: each request carries a JWT bootstrapped for the device
// (mimo-free.js:79-105), a Chrome-like User-Agent and the MiMoCode system marker,
// or the gateway answers 403 "Illegal access" (:15-25). `mmf` is the same executor
// under its alias (executors/index.js:68), so both ids share this hook, and the
// JWT and the session id are shared too, as the pin's module state is.
//
// The JWT is a fail-closed input: when it cannot be minted the request is
// refused with an error and nothing is sent, never with an empty or placeholder
// bearer that would put the traffic on no one's account.

const (
	// MimoSystemMarker is MIMO_SYSTEM_MARKER (mimo-free.js:24-25).
	MimoSystemMarker = "You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."
	// mimoBootstrapPath is BOOTSTRAP_URL's path (mimo-free.js:7); the host is the
	// one the chat request is dialled at, which the SSRF policy has validated.
	mimoBootstrapPath = "/api/free-ai/bootstrap"
	mimoSource        = "mimocode-cli-free"
	// mimoFallbackTTL and mimoExpiryBuffer are JWT_FALLBACK_TTL_SEC and
	// JWT_EXPIRY_BUFFER_MS (:11-12).
	mimoFallbackTTL  = 3000 * time.Second
	mimoExpiryBuffer = 300 * time.Second
	mimoSessionChars = "abcdefghijklmnopqrstuvwxyz0123456789"
	mimoSessionLen   = 24
	mimoReplyLimit   = 1 << 20
	mimoRequestLimit = 64 << 20
	// mimoJWTLimit bounds a minted token: a real one is a few hundred bytes.
	mimoJWTLimit = 8 << 10
)

// mimoUserAgents are USER_AGENTS (:16-20).
var mimoUserAgents = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
}

var mimoFreeHook = Hook{
	Finalize: mimoFreeMarker,
	// The credential is the minted JWT, set by Do; the connection has no account.
	Do: MimoFreeDo,
}

// mimoFreeMarker is injectSystemMarker (:63-72): a system message carrying the
// marker is put first unless one already holds it. As in the pin, only a system
// message whose content is a string counts.
func mimoFreeMarker(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil || messages == nil {
		return false
	}
	for _, message := range messages {
		var parsed struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		var text string
		if json.Unmarshal(message, &parsed) == nil && parsed.Role == "system" &&
			json.Unmarshal(parsed.Content, &text) == nil && strings.Contains(text, MimoSystemMarker) {
			return false
		}
	}
	marker, _ := json.Marshal(map[string]string{"role": "system", "content": MimoSystemMarker})
	encoded, err := json.Marshal(append([]json.RawMessage{marker}, messages...))
	if err != nil {
		return false
	}
	body["messages"] = encoded
	return true
}

type mimoJWT struct {
	value   string
	expires time.Time
}

// mimoJWTs is the pin's cachedJwt/jwtExpiresAt (:27-29), per bootstrap host.
var mimoJWTs = struct {
	mu    sync.Mutex
	byURL map[string]mimoJWT
	now   func() time.Time
}{byURL: map[string]mimoJWT{}, now: time.Now}

// mimoReset forgets every JWT and the session id, for tests.
func mimoReset() {
	mimoJWTs.mu.Lock()
	mimoJWTs.byURL, mimoJWTs.now = map[string]mimoJWT{}, time.Now
	mimoJWTs.mu.Unlock()
}

var mimoSession struct {
	once sync.Once
	id   string
}

// mimoSessionID is generateSessionId (:44-50), made once for the process.
func mimoSessionID() string {
	mimoSession.once.Do(func() {
		id := []byte("ses_")
		for i := 0; i < mimoSessionLen; i++ {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(mimoSessionChars))))
			if err != nil {
				n = big.NewInt(int64(i))
			}
			id = append(id, mimoSessionChars[n.Int64()])
		}
		mimoSession.id = string(id)
	})
	return mimoSession.id
}

func mimoUserAgent() string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(mimoUserAgents))))
	if err != nil {
		return mimoUserAgents[0]
	}
	return mimoUserAgents[n.Int64()]
}

var mimoFingerprint struct {
	once  sync.Once
	value string
}

// mimoDeviceFingerprint is generateFingerprint (:31-42): the hash of the host
// name, platform, architecture, CPU model and user that the bootstrap names as
// its client. Stable for the machine; only the digest leaves it.
func mimoDeviceFingerprint() string {
	mimoFingerprint.once.Do(func() {
		username := "unknown-user"
		if current, err := user.Current(); err == nil && current.Username != "" {
			username = current.Username
		}
		host, err := os.Hostname()
		if err != nil {
			host = "unknown-host"
		}
		seed := strings.Join([]string{host, runtime.GOOS, runtime.GOARCH, mimoCPUModel(), username}, "|")
		sum := sha256.Sum256([]byte(seed))
		mimoFingerprint.value = hex.EncodeToString(sum[:])
	})
	return mimoFingerprint.value
}

func mimoCPUModel() string {
	text, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		for _, line := range strings.Split(string(text), "\n") {
			if name, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "model name" {
				return strings.TrimSpace(value)
			}
		}
	}
	return "unknown-cpu"
}

// mimoJWTExpiry is parseJwtExp (:52-61): the token's exp claim, else a fixed TTL
// from now.
func mimoJWTExpiry(jwt string, now time.Time) time.Time {
	if parts := strings.Split(jwt, "."); len(parts) > 1 {
		segment := strings.NewReplacer("+", "-", "/", "_").Replace(strings.TrimRight(parts[1], "="))
		if payload, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
			var claims struct {
				Exp float64 `json:"exp"`
			}
			if json.Unmarshal(payload, &claims) == nil && claims.Exp > 0 {
				return time.Unix(int64(claims.Exp), 0)
			}
		}
	}
	return now.Add(mimoFallbackTTL)
}

// mimoBootstrap is bootstrapJwt (:79-105). A bootstrap that fails, or returns no token
// or one that cannot travel as a header value, is an error.
func mimoBootstrap(client *http.Client, request *http.Request, endpoint string) (string, error) {
	mimoJWTs.mu.Lock()
	cached, ok := mimoJWTs.byURL[endpoint]
	now := mimoJWTs.now()
	mimoJWTs.mu.Unlock()
	if ok && now.Before(cached.expires.Add(-mimoExpiryBuffer)) {
		return cached.value, nil
	}
	payload, _ := json.Marshal(map[string]string{"client": mimoDeviceFingerprint()})
	bootstrap, err := http.NewRequestWithContext(request.Context(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	bootstrap.Header.Set("Content-Type", "application/json")
	bootstrap.Header.Set("User-Agent", mimoUserAgent())
	response, err := client.Do(bootstrap)
	if err != nil {
		return "", fmt.Errorf("MiMo bootstrap failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("MiMo bootstrap failed: %d", response.StatusCode)
	}
	var reply struct {
		JWT string `json:"jwt"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, mimoReplyLimit)).Decode(&reply); err != nil {
		return "", errors.New("MiMo bootstrap returned an unreadable reply")
	}
	jwt := strings.TrimSpace(reply.JWT)
	if jwt == "" {
		return "", errors.New("MiMo bootstrap returned no JWT")
	}
	if len(jwt) > mimoJWTLimit || strings.ContainsAny(jwt, " \t\r\n\x00") {
		return "", errors.New("MiMo bootstrap returned a JWT that is not a valid bearer")
	}
	mimoJWTs.mu.Lock()
	mimoJWTs.byURL[endpoint] = mimoJWT{value: jwt, expires: mimoJWTExpiry(jwt, mimoJWTs.now())}
	mimoJWTs.mu.Unlock()
	return jwt, nil
}

func mimoForget(endpoint string) {
	mimoJWTs.mu.Lock()
	delete(mimoJWTs.byURL, endpoint)
	mimoJWTs.mu.Unlock()
}

// MimoFreeDo is MimoFreeExecutor.execute (:131-159): the request goes out with a
// bootstrapped JWT, and a 401 or 403 forgets the token and sends once more with a
// fresh one. The body is Chat, already carrying the marker (Hook.Finalize).
func MimoFreeDo(_ routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, mimoRequestLimit+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > mimoRequestLimit {
		return nil, errors.New("request exceeds the limit")
	}
	endpoint := (&url.URL{Scheme: request.URL.Scheme, Host: request.URL.Host, Path: mimoBootstrapPath}).String()
	var stream struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &stream)
	accept := "application/json"
	if stream.Stream {
		accept = "text/event-stream"
	}
	send := func(jwt string) (*http.Response, error) {
		attempt := request.Clone(request.Context())
		attempt.Body = io.NopCloser(bytes.NewReader(raw))
		attempt.ContentLength = int64(len(raw))
		attempt.GetBody = nil
		attempt.Header = request.Header.Clone()
		attempt.Header.Set("Content-Type", "application/json")
		attempt.Header.Set("X-Mimo-Source", mimoSource)
		attempt.Header.Set("User-Agent", mimoUserAgent())
		attempt.Header.Set("x-session-affinity", mimoSessionID())
		attempt.Header.Set("Accept", accept)
		attempt.Header.Set("Authorization", "Bearer "+jwt)
		return client.Do(attempt)
	}
	jwt, err := mimoBootstrap(client, request, endpoint)
	if err != nil {
		return nil, err
	}
	response, err := send(jwt)
	if err != nil || (response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden) {
		return response, err
	}
	response.Body.Close()
	mimoForget(endpoint)
	if jwt, err = mimoBootstrap(client, request, endpoint); err != nil {
		return nil, err
	}
	return send(jwt)
}
