package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
)

const PingHistorySetting = "quotaAutoPingHistory"

type PingRecord struct {
	Reset string    `json:"reset"`
	At    time.Time `json:"at"`
}

type AutoPinger struct {
	Service  *Service
	Settings func() map[string]string
	Save     func(context.Context, map[string]PingRecord) error
	Send     func(context.Context, string, Credential) error
	mu       sync.Mutex
	resets   map[string]time.Time
	failures map[string]time.Time
	history  map[string]PingRecord
}

func (p *AutoPinger) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = p.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *AutoPinger) Tick(ctx context.Context) error {
	if !p.mu.TryLock() {
		return nil
	}
	defer p.mu.Unlock()
	if p.Service == nil || p.Service.Connections == nil || p.Service.Credentials == nil || p.Settings == nil || p.Save == nil || p.Send == nil {
		return errors.New("quota auto-ping is not configured")
	}
	settings := p.Settings()
	if p.history == nil {
		p.history = map[string]PingRecord{}
		if raw := settings[PingHistorySetting]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &p.history); err != nil || p.history == nil {
				p.history = nil
				return errors.New("invalid quota auto-ping history")
			}
		}
	}
	history := p.history
	if p.resets == nil {
		p.resets = map[string]time.Time{}
		p.failures = map[string]time.Time{}
	}
	var firstErr error
	active := map[string]bool{}
	for _, provider := range []string{"claude", "codex"} {
		var optIn struct {
			Connections map[string]bool `json:"connections"`
		}
		if json.Unmarshal([]byte(settings[provider+"AutoPing"]), &optIn) != nil || len(optIn.Connections) == 0 {
			continue
		}
		connections, err := p.Service.Connections.ListConnections(ctx, provider)
		if err != nil {
			return err
		}
		if p.Service.Seeder != nil {
			if _, err := p.Service.Seeder.Load(ctx, provider); err != nil {
				return err
			}
		}
		for _, connection := range connections {
			if !connection.Enabled || connection.AuthKind != credentials.AuthOAuth || !optIn.Connections[connection.ID] {
				continue
			}
			active[connection.ID] = true
			if err := p.ping(ctx, provider, connection.ID, history); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	for id := range p.resets {
		if !active[id] {
			delete(p.resets, id)
			delete(p.failures, id)
		}
	}
	return firstErr
}

func (p *AutoPinger) ping(ctx context.Context, provider, id string, history map[string]PingRecord) error {
	now := p.Service.Observer.now()
	if failed, ok := p.failures[id]; ok && now.Sub(failed) < 15*time.Minute {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := p.attempt(ctx, provider, id, history, now)
	if err != nil {
		p.failures[id] = now
	} else {
		delete(p.failures, id)
	}
	return err
}

func (p *AutoPinger) attempt(ctx context.Context, provider, id string, history map[string]PingRecord, now time.Time) error {
	secret, err := p.Service.Credentials.Secret(ctx, id)
	if err != nil {
		return errors.New("quota auto-ping credential unavailable")
	}
	credential := Credential{ConnectionID: id, AccessToken: secret.AccessToken, Cookie: secret.Cookie, ProviderData: secret.ProviderData}
	state, err := p.Service.observe(ctx, provider, id, credential)
	if err != nil {
		return errors.New("quota auto-ping usage unavailable")
	}
	sessionName := "session"
	if provider == "claude" {
		sessionName = "session (5h)"
	}
	var session *Window
	for i := range state.Windows {
		if state.Windows[i].Name == sessionName {
			session = &state.Windows[i]
			break
		}
	}
	if session == nil || session.ResetAt == nil {
		return nil
	}
	previous := p.resets[id]
	reset := *session.ResetAt
	p.resets[id] = reset
	if session.Unlimited || session.Remaining == nil || *session.Remaining <= 0 {
		return nil
	}
	record := history[id]
	key := reset.UTC().Truncate(time.Minute).Format(time.RFC3339)
	if record.Reset == key {
		return nil
	}
	if provider == "codex" {
		if previous.IsZero() || reset.Sub(previous) < 30*time.Second || !record.At.IsZero() && now.Sub(record.At) < 10*time.Minute {
			return nil
		}
		for _, window := range state.Windows {
			if !strings.Contains(strings.ToLower(window.Name), "session") && !window.Unlimited && window.Remaining != nil && *window.Remaining <= 0 {
				return nil
			}
		}
	} else if now.Before(reset.Add(-5 * time.Second)) {
		return nil
	}
	if err := p.Send(ctx, provider, credential); err != nil {
		return err
	}
	// Keep successful sends suppressed in this process even if persistence fails.
	history[id] = PingRecord{Reset: key, At: now}
	p.resets[id] = reset
	if err := p.Save(ctx, history); err != nil {
		return errors.New("quota auto-ping history could not be saved")
	}
	return nil
}

type PingHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func SendPing(ctx context.Context, client PingHTTPClient, provider string, credential Credential) error {
	if client == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return errors.New("quota auto-ping requires a client and credential")
	}
	endpoint := "https://api.anthropic.com/v1/messages?beta=true"
	body := `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	headers := claude.Headers()
	if provider == "codex" {
		endpoint = codex.ResponseEndpoint
		body = `{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"instructions":"Reply with OK.","reasoning":{"effort":"none","summary":"auto"},"store":false,"stream":true}`
		headers = codex.Headers()
		account := credential.ProviderData["workspaceId"]
		if account == "" {
			account = credential.ProviderData["chatgptAccountId"]
		}
		if account == "" {
			account = credential.ProviderData["accountId"]
		}
		if account != "" {
			headers["ChatGPT-Account-ID"] = account
		}
	} else if provider != "claude" {
		return ErrNotConfigured
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	if provider == "codex" {
		request.Header.Set("Accept", "text/event-stream")
		sessionID := credential.ConnectionID
		if sessionID == "" {
			sessionID = "default"
		}
		request.Header.Set("session_id", sessionID)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("quota auto-ping request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("quota auto-ping returned HTTP %d", response.StatusCode)
	}
	// Codex starts its window only after the stream finishes.
	size, err := io.Copy(io.Discard, io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || size > 1<<20 {
		return errors.New("quota auto-ping response did not complete")
	}
	return nil
}
