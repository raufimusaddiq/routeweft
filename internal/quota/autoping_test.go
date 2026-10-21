package quota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func TestAutoPingOptInResetAndRestart(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			reset := now
			name := "session"
			if provider == "claude" {
				name = "session (5h)"
			}
			usage := &fakeClient{state: State{Windows: []Window{{Name: name, Remaining: ptr(1), ResetAt: &reset}}}}
			service := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{provider: {{ID: "a", ProviderID: provider, Enabled: true, AuthKind: credentials.AuthOAuth}, {ID: "b", ProviderID: provider, Enabled: true, AuthKind: credentials.AuthAPIKey}}}}, fakeCredentials{values: map[string]string{"a": "fixture"}}, Observer{Clients: map[string]UsageClient{provider: usage}, Now: func() time.Time { return now }})
			settings := map[string]string{provider + "AutoPing": `{"connections":{}}`}
			calls := 0
			p := &AutoPinger{Service: service, Settings: func() map[string]string { return settings }, Send: func(context.Context, string, Credential) error { calls++; return nil }, Save: func(_ context.Context, h map[string]PingRecord) error {
				b, _ := json.Marshal(h)
				settings[PingHistorySetting] = string(b)
				return nil
			}}
			tick := func() {
				t.Helper()
				if err := p.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			tick()
			if usage.calls != 0 {
				t.Fatal("read without opt-in")
			}
			settings[provider+"AutoPing"] = `{"connections":{"a":true,"b":true}}`
			if provider == "codex" {
				tick()
				if calls != 0 {
					t.Fatal("first sample warmed Codex")
				}
				reset = reset.Add(time.Minute)
			}
			tick()
			tick()
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			restarted := &AutoPinger{Service: service, Settings: p.Settings, Save: p.Save, Send: p.Send}
			if err := restarted.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("repeated after restart")
			}
			if provider == "codex" {
				now = now.Add(time.Minute)
				reset = reset.Add(time.Minute)
				tick()
				if calls != 1 {
					t.Fatal("minimum interval ignored")
				}
			}
			now = now.Add(11 * time.Minute)
			reset = now
			p.history = nil
			if provider == "codex" {
				reset = reset.Add(time.Minute)
			}
			tick()
			if calls != 2 {
				t.Fatalf("new reset calls=%d", calls)
			}
		})
	}
}

func TestAutoPingFailureCooldownAndBlockingQuota(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	reset := now
	client := &fakeClient{state: State{Windows: []Window{{Name: "session", Remaining: ptr(1), ResetAt: &reset}, {Name: "weekly", Remaining: ptr(0)}}}}
	s := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: "a", ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}}}}, fakeCredentials{values: map[string]string{"a": "fixture"}}, Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }})
	calls := 0
	p := &AutoPinger{Service: s, Settings: func() map[string]string { return map[string]string{"codexAutoPing": `{"connections":{"a":true}}`} }, Save: func(context.Context, map[string]PingRecord) error { return nil }, Send: func(context.Context, string, Credential) error { calls++; return errors.New("failed") }}
	p.Tick(context.Background())
	reset = reset.Add(time.Minute)
	p.Tick(context.Background())
	if calls != 0 {
		t.Fatal("pinged exhausted weekly quota")
	}
	client.state.Windows[1].Remaining = ptr(1)
	reset = reset.Add(time.Minute)
	if p.Tick(context.Background()) == nil {
		t.Fatal("missing send error")
	}
	reset = reset.Add(time.Minute)
	p.Tick(context.Background())
	if calls != 1 {
		t.Fatal("failure cooldown ignored")
	}
	now = now.Add(16 * time.Minute)
	reset = now.Add(time.Hour)
	p.Send = func(context.Context, string, Credential) error { calls++; return nil }
	p.Save = func(context.Context, map[string]PingRecord) error { return errors.New("disk") }
	if p.Tick(context.Background()) == nil {
		t.Fatal("missing save error")
	}
	now = now.Add(16 * time.Minute)
	p.Tick(context.Background())
	if calls != 2 {
		t.Fatal("repeated successful send after failed persistence")
	}
}

func TestAutoPingSerializesConcurrentTicks(t *testing.T) {
	now := time.Now()
	reset := now
	c := &fakeClient{state: State{Windows: []Window{{Name: "session (5h)", Remaining: ptr(1), ResetAt: &reset}}}}
	s := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{"claude": {{ID: "a", Enabled: true, AuthKind: credentials.AuthOAuth}}}}, fakeCredentials{values: map[string]string{"a": "fixture"}}, Observer{Clients: map[string]UsageClient{"claude": c}, Now: func() time.Time { return now }})
	calls := 0
	p := &AutoPinger{Service: s, Settings: func() map[string]string { return map[string]string{"claudeAutoPing": `{"connections":{"a":true}}`} }, Save: func(context.Context, map[string]PingRecord) error { return nil }, Send: func(context.Context, string, Credential) error { calls++; return nil }}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.Tick(context.Background()) }()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

type pingClient func(*http.Request) (*http.Response, error)

func (f pingClient) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestPingWireAndCancellation(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			read := false
			client := pingClient(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Fatal("bad auth/method")
				}
				var payload map[string]any
				if json.Unmarshal(body, &payload) != nil {
					t.Fatal("bad JSON")
				}
				if provider == "codex" {
					if r.URL.String() != "https://chatgpt.com/backend-api/codex/responses" || r.Header.Get("Accept") != "text/event-stream" || r.Header.Get("session_id") != "connection-fixture" || r.Header.Get("ChatGPT-Account-ID") != "workspace" || payload["stream"] != true || payload["store"] != false || payload["model"] != "gpt-5.6-terra" {
						t.Fatalf("request=%v body=%s", r, body)
					}
				} else if r.URL.String() != "https://api.anthropic.com/v1/messages?beta=true" || payload["max_tokens"] != float64(1) {
					t.Fatal("bad Claude request")
				}
				return &http.Response{StatusCode: 200, Body: &pingBody{Reader: strings.NewReader("data: {}\n\n"), read: &read}}, nil
			})
			if err := SendPing(context.Background(), client, provider, Credential{ConnectionID: "connection-fixture", AccessToken: "fixture", ProviderData: map[string]string{"workspaceId": "workspace"}}); err != nil {
				t.Fatal(err)
			}
			if !read {
				t.Fatal("did not drain")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := SendPing(ctx, pingClient(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() }), provider, Credential{AccessToken: "fixture"}); err == nil {
				t.Fatal("lost cancellation")
			}
		})
	}
}

type pingBody struct {
	*strings.Reader
	read *bool
}

func (b *pingBody) Read(p []byte) (int, error) { *b.read = true; return b.Reader.Read(p) }
func (b *pingBody) Close() error               { return nil }

// Production holds ResetAt constant between ticks once a window has slid; the
// retry after a failed send must work with that sequence.
func TestAutoPingCodexRetriesFailedSendWithConstantReset(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	client := &fakeClient{state: State{Windows: []Window{{Name: "session", Remaining: ptr(1), ResetAt: &reset}}}}
	s := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: "a", ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}}}}, fakeCredentials{values: map[string]string{"a": "fixture"}}, Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }})
	calls, fail := 0, true
	p := &AutoPinger{Service: s, Settings: func() map[string]string { return map[string]string{"codexAutoPing": `{"connections":{"a":true}}`} }, Save: func(context.Context, map[string]PingRecord) error { return nil }, Send: func(context.Context, string, Credential) error {
		calls++
		if fail {
			return errors.New("failed")
		}
		return nil
	}}
	p.Tick(context.Background()) // first sample
	reset = reset.Add(time.Minute)
	if p.Tick(context.Background()) == nil || calls != 1 {
		t.Fatalf("slide did not attempt a ping: calls=%d", calls)
	}
	now = now.Add(time.Minute) // reset unchanged from here on
	p.Tick(context.Background())
	if calls != 1 {
		t.Fatal("retried inside the 15 minute cooldown")
	}
	fail = false
	now = now.Add(16 * time.Minute)
	if err := p.Tick(context.Background()); err != nil || calls != 2 {
		t.Fatalf("no retry after cooldown: calls=%d err=%v", calls, err)
	}
}

func TestAutoPingPersistsHistoryPruneWithoutPing(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: "a", ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}}}}, fakeCredentials{values: map[string]string{"a": "x"}}, Observer{Clients: map[string]UsageClient{"codex": &fakeClient{}}, Now: func() time.Time { return now }})
	var saved map[string]PingRecord
	p := &AutoPinger{Service: s, Save: func(_ context.Context, h map[string]PingRecord) error { saved = h; return nil }, Send: func(context.Context, string, Credential) error { return nil }, Settings: func() map[string]string {
		return map[string]string{"codexAutoPing": `{"connections":{"a":true}}`, PingHistorySetting: `{"gone":{"reset":"k"}}`}
	}}
	if err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if saved == nil || len(saved) != 0 {
		t.Fatalf("prune not persisted: %v", saved)
	}
}
