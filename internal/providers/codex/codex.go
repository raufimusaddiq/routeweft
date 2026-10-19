// Package codex owns the OpenAI Codex OAuth/PKCE and rotating-credential
// behavior required by PROVIDER_BASELINE. Credential persistence is supplied by
// the caller, so this package never decides at-rest storage for tokens.
package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Default endpoint values come from the PROVIDER_BASELINE reference snapshot.
const (
	DefaultClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	DefaultTokenURL     = "https://auth.openai.com/oauth/token"
	DefaultScope        = "openid profile email offline_access"
	DefaultRedirectURI  = "http://localhost:1455/auth/callback"
	ResponseEndpoint    = "https://chatgpt.com/backend-api/codex/responses"
	UsageEndpoint       = "https://chatgpt.com/backend-api/wham/usage"
	ResetCreditsURL     = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	maxResponseBytes    = 1 << 20
	accessRefreshLead   = 60 * time.Second
	tokenTimeout        = 15 * time.Second
	refreshTimeout      = 30 * time.Second
)

// Tokens is one rotating credential set. RefreshToken is a secret and must
// never be logged, returned to browser code, or rendered in diagnostics.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	Expiry       time.Time
}

// Config overrides the built-in Codex OAuth endpoints. Empty fields use defaults.
type Config struct {
	ClientID     string
	AuthorizeURL string
	TokenURL     string
	Scope        string
	RedirectURI  string
}

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

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// PKCE holds one authorization-code verifier and its S256 challenge.
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

// ChallengeFor derives the S256 challenge for a known verifier (import/test use).
func ChallengeFor(verifier string) PKCE {
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:]), Method: "S256"}
}

func randomURLSafe(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// AuthorizeURL builds the Codex authorization URL for one PKCE attempt.
func AuthorizeURL(pkce PKCE) string {
	config := Config{}.resolved()
	values := url.Values{
		"client_id":                  {config.ClientID},
		"response_type":              {"code"},
		"redirect_uri":               {config.RedirectURI},
		"scope":                      {config.Scope},
		"code_challenge":             {pkce.Challenge},
		"code_challenge_method":      {pkce.Method},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"codex_cli_rs"},
	}
	return config.AuthorizeURL + "?" + values.Encode()
}

// ExchangeCode swaps one authorization code for tokens.
func ExchangeCode(ctx context.Context, pkce PKCE, code string, client HTTPClient) (Tokens, error) {
	config := Config{}.resolved()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {config.ClientID},
		"code":          {code},
		"redirect_uri":  {config.RedirectURI},
		"code_verifier": {pkce.Verifier},
	}
	return tokenRequest(ctx, config, form, client)
}

// Refresher performs one refresh and commits the rotated token set durably
// before a successful refresh is visible to callers (SPEC §19).
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

// Seed installs an already-persisted token set, for example after import.
func (r *Refresher) Seed(tokens Tokens) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = tokens
}

// Token returns a valid access token, refreshing once under singleflight when
// the current access token is missing or near expiry.
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

	operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	tokens, err := r.refresh(operationCtx, refreshToken)
	if err == nil && strings.TrimSpace(tokens.RefreshToken) == "" {
		tokens.RefreshToken = refreshToken
	}
	if err == nil {
		err = r.persist(operationCtx, tokens)
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
	return r.nowLocked().Add(accessRefreshLead).Before(r.current.Expiry)
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
		return fmt.Errorf("persist rotated codex credentials: %w", ErrPersistenceFailed)
	}
	return nil
}

// ErrPersistenceFailed means rotated credentials were not durably committed,
// so the refresh is not exposed as successful.
var ErrPersistenceFailed = errors.New("codex credential persistence failed")

func (r *Refresher) refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("codex refresh token is required")
	}
	config := r.Config.resolved()
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {config.ClientID},
		"refresh_token": {refreshToken},
		"scope":         {config.Scope},
	}
	return tokenRequest(ctx, config, form, r.Client)
}

func tokenRequest(ctx context.Context, config Config, form url.Values, client HTTPClient) (Tokens, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	if client == nil {
		client = transport.NewSSRFProtectedClient()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Tokens{}, errors.New("codex token request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Tokens{}, errors.New("codex token response could not be read")
	}
	if len(body) > maxResponseBytes {
		return Tokens{}, errors.New("codex token response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return Tokens{}, ErrReauthRequired
		}
		return Tokens{}, fmt.Errorf("codex token endpoint returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    any    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Tokens{}, errors.New("codex token endpoint returned invalid JSON")
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return Tokens{}, errors.New("codex token response is missing access_token")
	}
	tokens := Tokens{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, IDToken: payload.IDToken}
	seconds, ok := expiresIn(payload.ExpiresIn)
	if !ok {
		return Tokens{}, errors.New("codex token response is missing a valid expires_in")
	}
	tokens.Expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	return tokens, nil
}

// ErrReauthRequired means the refresh token or authorization code is no longer
// valid and the operator must reconnect the account.
var ErrReauthRequired = errors.New("codex account requires reauthorization")

func expiresIn(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	case string:
		seconds, err := strconv.ParseInt(typed, 10, 64)
		if err != nil || seconds <= 0 {
			return 0, false
		}
		return seconds, true
	default:
		return 0, false
	}
}
