package headroom_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/body"
	"github.com/raufimusaddiq/routeweft/internal/transforms/headroom"
)

type recorder func(*http.Request) (*http.Response, error)

func (f recorder) Do(request *http.Request) (*http.Response, error) { return f(request) }

type pinCase struct {
	Name                 string          `json:"name"`
	Format               string          `json:"format"`
	URL                  string          `json:"url"`
	Model                string          `json:"model"`
	CompressUserMessages bool            `json:"compressUserMessages"`
	Replay               bool            `json:"replay"`
	Body                 json.RawMessage `json:"body"`
	Reply                struct {
		Status int             `json:"status"`
		JSON   json.RawMessage `json:"json"`
		Throws string          `json:"throws"`
	} `json:"reply"`
	Calls []struct {
		URL     string          `json:"url"`
		Payload json.RawMessage `json:"payload"`
	} `json:"calls"`
	Stats *struct {
		Before float64 `json:"tokens_before"`
		After  float64 `json:"tokens_after"`
		Saved  float64 `json:"tokens_saved"`
	} `json:"stats"`
	After       json.RawMessage `json:"after"`
	Diagnostics struct {
		Reason string `json:"reason"`
	} `json:"diagnostics"`
	Phantom bool `json:"phantom"`
}

var protocols = map[string]string{"openai": openai.ChatProtocol, "claude": anthropic.MessagesProtocol, "openai-responses": openai.ResponsesProtocol, "gemini": gemini.Protocol}

// texts lists the conversation text of a body in order: for a Claude body only the
// turns (the oracle's round trip through its translators adds a system prompt and
// cache markers of its own, which Routeweft's anchoring step owns, PARITY 6.37).
type turn struct{ Role, Text string }

func texts(t *testing.T, format string, raw []byte) []turn {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []turn
	one := func(role string, content any) {
		switch value := content.(type) {
		case string:
			out = append(out, turn{role, value})
		case []any:
			var parts []string
			for _, part := range value {
				if object, ok := part.(map[string]any); ok {
					if text, ok := object["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
			out = append(out, turn{role, strings.Join(parts, "\n")})
		}
	}
	key := "messages"
	if format == "openai-responses" {
		key = "input"
	}
	items, _ := doc[key].([]any)
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			role, _ := object["role"].(string)
			one(role, object["content"])
		}
	}
	return out
}

// TestReplaysTheOracleCompressWire runs the oracle's own compressWithHeadroom
// recordings (compat/parity/generate-headroom.mjs) through the serving pipeline:
// the helper must be called at the same path with the same payload, a reply must
// land in the same text, and every refusal must be the oracle's reason.
func TestReplaysTheOracleCompressWire(t *testing.T) {
	raw, err := os.ReadFile("testdata/compress_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct{ Cases []pinCase }
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	replayed := 0
	for _, c := range pin.Cases {
		if !c.Replay {
			continue
		}
		replayed++
		t.Run(c.Name, func(t *testing.T) {
			enabled := !strings.Contains(c.Name, ": disabled")
			url := strings.Replace(c.URL, "http://headroom:8787", "https://headroom.example", 1)
			var calls []map[string]json.RawMessage
			var paths []string
			var diagnostic headroom.Diagnostic
			transform := headroom.Transform{URL: url, CompressUserMessages: c.CompressUserMessages, Diagnostics: func(d headroom.Diagnostic) { diagnostic = d },
				Client: recorder(func(r *http.Request) (*http.Response, error) {
					body, _ := io.ReadAll(r.Body)
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Fatal(err)
					}
					calls = append(calls, payload)
					paths = append(paths, r.URL.String())
					if c.Reply.Throws != "" {
						return nil, &net{c.Reply.Throws}
					}
					return &http.Response{StatusCode: c.Reply.Status, Body: io.NopCloser(bytes.NewReader(c.Reply.JSON)), Header: http.Header{}}, nil
				})}
			steps := []transforms.Transform{}
			if enabled {
				steps = append(steps, transform)
			}
			got, _ := body.Run(context.Background(), protocols[c.Format], c.Body, transforms.Pipeline{Steps: steps})

			if len(c.Calls) != len(calls) {
				t.Fatalf("%d helper calls, oracle made %d", len(calls), len(c.Calls))
			}
			for i, want := range c.Calls {
				wantURL := strings.Replace(want.URL, "http://headroom:8787", "https://headroom.example", 1)
				if paths[i] != wantURL {
					t.Errorf("called %s, oracle called %s", paths[i], wantURL)
				}
				var expected map[string]json.RawMessage
				_ = json.Unmarshal(want.Payload, &expected)
				if !jsonEqual(expected, calls[i]) {
					t.Errorf("payload\n got %s\nwant %s", mustJSON(calls[i]), mustJSON(expected))
				}
			}
			wantReason := c.Diagnostics.Reason
			if enabled && !strings.HasPrefix(wantReason, "disabled") {
				// the oracle spells a connection failure with the error code in front
				if strings.HasPrefix(wantReason, "request failed: ECONNREFUSED: ") {
					wantReason = "request failed: " + strings.TrimPrefix(wantReason, "request failed: ECONNREFUSED: ")
				}
				if diagnostic.Error != wantReason {
					t.Errorf("reason %q, oracle %q", diagnostic.Error, wantReason)
				}
			}
			if diagnostic.Phantom != c.Phantom {
				t.Errorf("phantom=%v, oracle %v", diagnostic.Phantom, c.Phantom)
			}
			if c.Stats != nil && diagnostic.Succeeded {
				if diagnostic.TokensBefore != c.Stats.Before || diagnostic.TokensAfter != c.Stats.After || diagnostic.TokensSaved != c.Stats.Saved {
					t.Errorf("stats %v/%v/%v, oracle %+v", diagnostic.TokensBefore, diagnostic.TokensAfter, diagnostic.TokensSaved, *c.Stats)
				}
			}
			// The text that goes on to the provider: the oracle's after-body, or the
			// untouched original when it declined.
			if c.Format == "gemini" {
				if !bytes.Equal(got, c.Body) {
					t.Errorf("a gemini body must pass untouched")
				}
				return
			}
			want, have := texts(t, c.Format, c.After), texts(t, c.Format, got)
			if !c.CompressUserMessages {
				// Routeweft discards what the helper does to a user turn unless
				// headroomCompressUserMessages is on (PARITY 6.37, D21); the oracle
				// applies the reply as it comes.
				for i, original := range texts(t, c.Format, c.Body) {
					if original.Role == "user" && i < len(want) {
						want[i].Text = original.Text
					}
				}
			}
			if !reflect.DeepEqual(want, have) {
				t.Errorf("text\n got %q\nwant %q", have, want)
			}
		})
	}
	if replayed < 10 {
		t.Fatalf("only %d cases replayed", replayed)
	}
}

type net struct{ text string }

func (e *net) Error() string { return e.text }

func jsonEqual(a, b any) bool {
	return bytes.Equal(mustJSON(a), mustJSON(b))
}

func mustJSON(v any) []byte {
	var decoded any
	encoded, _ := json.Marshal(v)
	_ = json.Unmarshal(encoded, &decoded)
	out, _ := json.Marshal(decoded)
	return out
}
