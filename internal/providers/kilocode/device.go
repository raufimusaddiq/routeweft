// Package kilocode implements Kilo Code's device-authorization sign-in. The
// pinned reference flow (open-sse/providers/registry/kilocode.js oauth block and
// src/lib/oauth/providers/kilocode.js) posts to the initiate endpoint, shows the
// operator a verification URL, then polls the code until the operator approves.
// The approved token is a long-lived bearer with no refresh token or expiry.
package kilocode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	DefaultInitiateURL = "https://api.kilo.ai/api/device-auth/codes"
	// DefaultPollInterval is the reference flow's fixed polling interval.
	DefaultPollInterval = 3 * time.Second
	defaultExpiresIn    = 300 * time.Second
	maxResponseBytes    = 1 << 20
	requestTimeout      = 15 * time.Second
)

// HTTPClient is the minimal client contract used for device calls.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Config overrides endpoints for tests. Empty fields use defaults.
type Config struct {
	InitiateURL string
}

// Device is one pending authorization. Code is both the device code and the
// user code in this provider's protocol.
type Device struct {
	Code            string
	VerificationURL string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

// Outcome is the state of one poll.
type Outcome string

const (
	OutcomePending  Outcome = "pending"
	OutcomeApproved Outcome = "approved"
	OutcomeDenied   Outcome = "denied"
	OutcomeExpired  Outcome = "expired"
)

// Poll is the result of one poll. Token is set only when Outcome is approved and
// is a secret: never log it or return it to browser code.
type Poll struct {
	Outcome Outcome
	Token   string
}

// ErrRateLimited means the provider has too many pending authorizations.
var ErrRateLimited = errors.New("kilocode device authorization is rate limited; try again later")

func (c Config) initiateURL() string {
	if c.InitiateURL != "" {
		return c.InitiateURL
	}
	return DefaultInitiateURL
}

// RequestDevice starts a device authorization.
func RequestDevice(ctx context.Context, config Config, client HTTPClient) (Device, error) {
	body, status, err := do(ctx, client, http.MethodPost, config.initiateURL())
	if err != nil {
		return Device{}, err
	}
	if status == http.StatusTooManyRequests {
		return Device{}, ErrRateLimited
	}
	if status < 200 || status >= 300 {
		return Device{}, fmt.Errorf("kilocode device authorization returned HTTP %d", status)
	}
	var decoded struct {
		Code            string `json:"code"`
		VerificationURL string `json:"verificationUrl"`
		ExpiresIn       int64  `json:"expiresIn"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Device{}, errors.New("kilocode device authorization returned invalid JSON")
	}
	code := strings.TrimSpace(decoded.Code)
	if code == "" || !safeSegment(code) {
		return Device{}, errors.New("kilocode device authorization response is missing a usable code")
	}
	verification, err := url.Parse(decoded.VerificationURL)
	if err != nil || verification.Scheme != "https" || verification.Host == "" {
		return Device{}, errors.New("kilocode device authorization response is missing an https verification URL")
	}
	expires := defaultExpiresIn
	if decoded.ExpiresIn > 0 {
		expires = time.Duration(decoded.ExpiresIn) * time.Second
	}
	return Device{Code: code, VerificationURL: verification.String(), ExpiresIn: expires, Interval: DefaultPollInterval}, nil
}

// PollDevice checks one device code once. Pending, denied and expired are
// outcomes, not errors; only transport and protocol faults return an error.
func PollDevice(ctx context.Context, config Config, code string, client HTTPClient) (Poll, error) {
	if !safeSegment(code) {
		return Poll{}, errors.New("kilocode device code is invalid")
	}
	body, status, err := do(ctx, client, http.MethodGet, strings.TrimRight(config.initiateURL(), "/")+"/"+code)
	if err != nil {
		return Poll{}, err
	}
	switch status {
	case http.StatusAccepted:
		return Poll{Outcome: OutcomePending}, nil
	case http.StatusForbidden:
		return Poll{Outcome: OutcomeDenied}, nil
	case http.StatusGone:
		return Poll{Outcome: OutcomeExpired}, nil
	}
	if status < 200 || status >= 300 {
		return Poll{}, fmt.Errorf("kilocode device poll returned HTTP %d", status)
	}
	var decoded struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Poll{}, errors.New("kilocode device poll returned invalid JSON")
	}
	if decoded.Status == "approved" && strings.TrimSpace(decoded.Token) != "" {
		return Poll{Outcome: OutcomeApproved, Token: decoded.Token}, nil
	}
	return Poll{Outcome: OutcomePending}, nil
}

// safeSegment keeps a provider-supplied code usable as exactly one URL path
// segment, so it cannot redirect the poll request elsewhere.
func safeSegment(code string) bool {
	if len(code) > 256 {
		return false
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return code != ""
}

func do(ctx context.Context, client HTTPClient, method, target string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if client == nil {
		client = transport.NewSSRFProtectedClient()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("kilocode device request failed")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, errors.New("kilocode device response could not be read")
	}
	if len(data) > maxResponseBytes {
		return nil, 0, errors.New("kilocode device response exceeds size limit")
	}
	return data, response.StatusCode, nil
}
