// Package oauthrefresh adapts the Claude and Codex refresh engines to the
// credential registry's Exchanger, so rotating OAuth connections refresh and
// commit through the registry's single durable path (SPEC §19).
package oauthrefresh

import (
	"context"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
)

// Client is the HTTP client used for refresh calls. Nil uses the engines'
// SSRF-protected default.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Resolver returns the registry hook that picks an Exchanger per connection.
// Only OAuth connections with a refresh token get one; everything else stays a
// static credential.
func Resolver(client Client) func(credentials.Connection) credentials.Exchanger {
	return func(connection credentials.Connection) credentials.Exchanger {
		if connection.AuthKind != credentials.AuthOAuth || connection.Secret.RefreshToken == "" {
			return nil
		}
		switch connection.ProviderID {
		case "claude":
			return claudeExchanger(client)
		case "codex":
			return codexExchanger(client)
		}
		return nil
	}
}

func claudeExchanger(client Client) credentials.Exchanger {
	return func(ctx context.Context, current credentials.Secret) (credentials.Secret, error) {
		var rotated claude.Tokens
		refresher := &claude.Refresher{Client: client, Persist: func(_ context.Context, tokens claude.Tokens) error {
			rotated = tokens
			return nil
		}}
		// Seeding only the refresh token forces one refresh; the registry owns
		// the durable commit, so Persist just captures the result.
		refresher.Seed(claude.Tokens{RefreshToken: current.RefreshToken})
		if _, err := refresher.Token(ctx); err != nil {
			return credentials.Secret{}, err
		}
		return credentials.Secret{AccessToken: rotated.AccessToken, RefreshToken: rotated.RefreshToken, IDToken: current.IDToken, Expiry: rotated.Expiry}, nil
	}
}

func codexExchanger(client Client) credentials.Exchanger {
	return func(ctx context.Context, current credentials.Secret) (credentials.Secret, error) {
		var rotated codex.Tokens
		refresher := &codex.Refresher{Client: client, Persist: func(_ context.Context, tokens codex.Tokens) error {
			rotated = tokens
			return nil
		}}
		refresher.Seed(codex.Tokens{RefreshToken: current.RefreshToken})
		if _, err := refresher.Token(ctx); err != nil {
			return credentials.Secret{}, err
		}
		idToken := rotated.IDToken
		if idToken == "" {
			idToken = current.IDToken
		}
		return credentials.Secret{AccessToken: rotated.AccessToken, RefreshToken: rotated.RefreshToken, IDToken: idToken, Expiry: rotated.Expiry}, nil
	}
}
