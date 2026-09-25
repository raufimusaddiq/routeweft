// Package claude owns Claude Code OAuth/PKCE credential behavior. Credential
// persistence is caller-supplied, so this package never chooses token storage.
package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Default endpoint/identity values from the PROVIDER_BASELINE reference snapshot.
const (
	DefaultClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	DefaultAuthorizeURL = "https://claude.ai/oauth/authorize"
	DefaultTokenURL     = "https://api.anthropic.com/v1/oauth/token"
	DefaultRedirectURI  = "https://console.anthropic.com/oauth/code/callback"
	DefaultScope        = "org:create_api_key user:profile user:inference"
	CLIVersion          = "2.1.258"
	maxResponseBytes    = 1 << 20
	tokenTimeout        = 15 * time.Second
	refreshTimeout      = 30 * time.Second
	refreshLead         = 4 * time.Hour
)

// Tokens is one rotating Claude credential set. Tokens are secrets and must
// never be logged, rendered in diagnostics, or exposed to browser code.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// Config overrides Claude OAuth endpoints. Empty fields use defaults.
type Config struct {
	ClientID     string
	AuthorizeURL string
	TokenURL     string
	Scope        string
	RedirectURI  string
}

// HTTPClient is the minimal client contract used for token calls.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// ErrReauthRequired means the operator must reconnect the Claude account.
var ErrReauthRequired = errors.New("claude account requires reauthorization")

// ErrPersistenceFailed means rotated credentials were not durably committed.
var ErrPersistenceFailed = errors.New("claude credential persistence failed")

func (c Config) resolved() Config {
	if c.ClientID == "" {
		c.ClientID = DefaultClientID
	}
	if c.AuthorizeURL == "" {
		c.AuthorizeURL = DefaultAuthorizeURL
	}
	if c.TokenURL == "" {
		c.TokenURL = DefaultTokenURL
	}
	if c.Scope == "" {
		c.Scope = DefaultScope
	}
	if c.RedirectURI == "" {
		c.RedirectURI = DefaultRedirectURI
	}
	return c
}

// PKCE is one authorization-code verifier and its S256 challenge.
type PKCE struct {
	Verifier  string
	Challenge string
	Method    string
}

// NewPKCE generates an RFC 7636 S256 verifier/challenge pair.
func NewPKCE() (PKCE, error) {
	verifier, err := randomURLSafe(64)
	if err != nil {
		return PKCE{}, err
	}
	return ChallengeFor(verifier), nil
}

// ChallengeFor derives the S256 challenge for a known verifier.
func ChallengeFor(verifier string) PKCE {
	sum := sha256Sum(verifier)
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum), Method: "S256"}
}

// AuthorizeURL builds the Claude authorization URL for one PKCE attempt.
func AuthorizeURL(pkce PKCE) string {
	config := Config{}.resolved()
	values := url.Values{
		"client_id":             {config.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {config.RedirectURI},
		"scope":                 {config.Scope},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {pkce.Method},
	}
	return config.AuthorizeURL + "?" + values.Encode()
}

// ExchangeCode swaps one authorization code for tokens. Claude's token
// endpoint accepts JSON request bodies.
func ExchangeCode(ctx context.Context, pkce PKCE, code string, client HTTPClient) (Tokens, error) {
	config := Config{}.resolved()
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     config.ClientID,
		"code":          code,
		"redirect_uri":  config.RedirectURI,
		"code_verifier": pkce.Verifier,
	}
	return tokenRequest(ctx, config, body, client)
}

// Refresher performs one singleflight refresh and commits the rotated token set
// durably before a successful refresh is visible to callers.
type Refresher struct {
	Config  Config
	Client  HTTPClient
	Persist func(context.Context, Tokens) error
	Now     func() time.Time

	mu       sync.Mutex
	current  Tokens
	inflight *refreshCall
}

type refreshCall struct {
	done   chan struct{}
	tokens Tokens
	err    error
}

// Seed installs an already-persisted token set, e.g. after import.
func (r *Refresher) Seed(tokens Tokens) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = tokens
}

// Token returns a valid access token, refreshing once under singleflight.
func (r *Refresher) Token(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.validLocked() {
		token := r.current.AccessToken
		r.mu.Unlock()
		return token, nil
	}
	if call := r.inflight; call != nil {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.tokens.AccessToken, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	r.inflight = call
	refreshToken := r.current.RefreshToken
	r.mu.Unlock()

	// The refresh obeys caller cancellation so an abandoned request does not
	// commit rotated credentials for work nobody is waiting on. Only the durable
	// commit window is detached, and it is bounded, so already-rotated tokens are
	// not silently lost (SPEC §19 / PRD-AUTH-003).
	operationCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	tokens, err := r.refresh(operationCtx, refreshToken)
	if err == nil && strings.TrimSpace(tokens.RefreshToken) == "" {
		tokens.RefreshToken = refreshToken
	}
	if err == nil {
		// Persist under a bounded detached context: the refresh completed and the
		// upstream refresh token is now rotated, so the commit must not be dropped
		// just because the caller went away between refresh and commit.
		commitCtx, commitCancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		err = r.persist(commitCtx, tokens)
		commitCancel()
	}
	if err != nil {
		tokens = Tokens{}
	}

	r.mu.Lock()
	if err == nil {
		r.current = tokens
	}
	r.inflight = nil
	call.tokens, call.err = tokens, err
	r.mu.Unlock()
	close(call.done)
	if err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func (r *Refresher) validLocked() bool {
	if r.current.AccessToken == "" || r.current.Expiry.IsZero() {
		return false
	}
	return r.nowLocked().Add(refreshLead).Before(r.current.Expiry)
}

func (r *Refresher) nowLocked() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Refresher) persist(ctx context.Context, tokens Tokens) error {
	if r.Persist == nil {
		return ErrPersistenceFailed
	}
	if err := r.Persist(ctx, tokens); err != nil {
		return fmt.Errorf("persist rotated claude credentials: %w", ErrPersistenceFailed)
	}
	return nil
}

func (r *Refresher) refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("claude refresh token is required")
	}
	config := r.Config.resolved()
	return tokenRequest(ctx, config, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     config.ClientID,
		"refresh_token": refreshToken,
	}, r.Client)
}

func tokenRequest(ctx context.Context, config Config, body map[string]string, client HTTPClient) (Tokens, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	if client == nil {
		client = transport.NewSSRFProtectedClient()
	}
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenURL, strings.NewReader(string(payload)))
	if err != nil {
		return Tokens{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Tokens{}, ctx.Err()
		}
		return Tokens{}, errors.New("claude token request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Tokens{}, errors.New("claude token response could not be read")
	}
	if len(data) > maxResponseBytes {
		return Tokens{}, errors.New("claude token response exceeds size limit")
	}
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return Tokens{}, ErrReauthRequired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Tokens{}, fmt.Errorf("claude token endpoint returned HTTP %d", response.StatusCode)
	}
	var decoded struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    any    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Tokens{}, errors.New("claude token endpoint returned invalid JSON")
	}
	if strings.TrimSpace(decoded.AccessToken) == "" {
		return Tokens{}, errors.New("claude token response is missing access_token")
	}
	seconds, ok := expiresIn(decoded.ExpiresIn)
	if !ok {
		return Tokens{}, errors.New("claude token response is missing a valid expires_in")
	}
	return Tokens{AccessToken: decoded.AccessToken, RefreshToken: decoded.RefreshToken, Expiry: time.Now().Add(time.Duration(seconds) * time.Second)}, nil
}

func expiresIn(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	case string:
		seconds, err := parseSeconds(typed)
		if err != nil || seconds <= 0 {
			return 0, false
		}
		return seconds, true
	default:
		return 0, false
	}
}
