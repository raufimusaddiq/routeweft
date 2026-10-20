// Package oauthrefresh adapts the onboarding package's refresh table to the
// credential registry's Exchanger, so every provider LiteRouter can refresh is
// refreshed through the registry's single durable commit path (SPEC §19).
//
// The provider set is not enumerated here: onboarding.RefreshResolver owns it,
// so a provider added there refreshes here without a second list to keep in step.
package oauthrefresh

import (
	"context"
	"errors"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
)

// Client is the HTTP client used for refresh calls. Nil uses the engines'
// SSRF-protected default.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// ErrNoClient means the caller supplied no HTTP client. It is returned rather than
// panicking: a wiring mistake in a library is the caller's to report, not a crash
// in whatever imports the package.
var ErrNoClient = errors.New("an HTTP client is required; without one every refresh fails at request time")

// Resolver returns the registry hook that picks an Exchanger per connection.
// Connections whose credential does not rotate keep no exchanger and stay static.
func Resolver(client Client) (func(credentials.Connection) credentials.Exchanger, error) {
	if client == nil {
		return nil, ErrNoClient
	}
	resolve, err := onboarding.RefreshResolver(client, nil)
	if err != nil {
		return nil, err
	}
	return func(connection credentials.Connection) credentials.Exchanger {
		if connection.AuthKind != credentials.AuthOAuth && connection.AuthKind != credentials.AuthCookie {
			return nil
		}
		refresher := resolve(connection)
		if refresher == nil {
			return nil
		}
		return func(ctx context.Context, current credentials.Secret) (credentials.Secret, error) {
			next, err := refresher(ctx, current)
			if errors.Is(err, credentials.ErrNoExchanger) {
				return credentials.Secret{}, credentials.ErrNoExchanger
			}
			return next, err
		}
	}, nil
}
