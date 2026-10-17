package ingress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestSaverHookFailureCannotMutateFailOpenBody(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		raw := []byte(`{"messages":[{"role":"user","content":"keep"}]}`)
		h := New(nil, Options{TransformFinalBody: func(_ context.Context, _ *runtime.RuntimeSnapshot, _ string, body []byte) ([]byte, error) {
			body[0] = '!'
			if invalid {
				return body, nil
			}
			return body, errors.New("broken saver")
		}})
		got := h.finalBody(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), nil, openai.ChatProtocol, raw)
		if string(got) != `{"messages":[{"role":"user","content":"keep"}]}` {
			t.Fatalf("broken saver mutated input: %s", got)
		}
	}
	for _, malformed := range []string{"[]", "null", `"text"`} {
		h := New(nil, Options{TransformFinalBody: func(context.Context, *runtime.RuntimeSnapshot, string, []byte) ([]byte, error) {
			return []byte(malformed), nil
		}})
		raw := []byte(`{"messages":[{"role":"user","content":"keep"}]}`)
		got := h.finalBody(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), nil, openai.ChatProtocol, raw)
		if string(got) != string(raw) {
			t.Fatalf("non-object saver accepted: %s", got)
		}
	}
}

func TestFinalBodyAnchorsOnlyAfterSaverAndBypassKeepsAnchors(t *testing.T) {
	var calls int
	h := New(nil, Options{TransformFinalBody: func(_ context.Context, _ *runtime.RuntimeSnapshot, protocol string, body []byte) ([]byte, error) {
		calls++
		if protocol != anthropic.MessagesProtocol || strings.Contains(string(body), "cache_control") {
			t.Fatalf("anchored before saver: %s %s", protocol, body)
		}
		return []byte(`{"system":[{"type":"text","text":"last saver"}],"messages":[{"role":"user","content":"hi"}]}`), nil
	}})
	raw := []byte(`{"system":"base","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	got := h.finalBody(r, nil, anthropic.MessagesProtocol, raw)
	if !strings.Contains(string(got), `"text":"last saver"`) || !strings.Contains(string(got), "cache_control") || calls != 1 {
		t.Fatalf("bad final body: %s calls=%d", got, calls)
	}
	r.Header.Set("X-9router-Token-Saver", "on")
	r.Header.Set("X-Literouter-Token-Saver", "off")
	got = h.finalBody(r, nil, anthropic.MessagesProtocol, raw)
	if !strings.Contains(string(got), `"text":"base"`) || !strings.Contains(string(got), "cache_control") || calls != 1 {
		t.Fatalf("bad bypass: %s calls=%d", got, calls)
	}
	w := httptest.NewRecorder()
	h.handlePreflight(w, r)
	for _, header := range []string{"x-9router-token-saver", "x-literouter-token-saver"} {
		if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), header) {
			t.Fatalf("bypass header omitted from preflight: %s", header)
		}
	}
}

func TestCountTokensExcludesMarkersNotDataNamedCacheControl(t *testing.T) {
	if got := countValueChars(map[string]any{"cache_control": "real"}); got != 17 {
		t.Fatalf("data field omitted: %d", got)
	}
	tool := map[string]any{"name": "tool", "input_schema": map[string]any{"cache_control": "real"}}
	payload := map[string]any{"system": "stable", "tools": []any{tool}}
	before := estimateAnthropicInputTokens(payload)
	tool["cache_control"] = map[string]any{"type": "ephemeral"}
	payload["system"] = []any{map[string]any{"type": "text", "text": "stable", "cache_control": map[string]any{"type": "ephemeral"}}}
	if got := estimateAnthropicInputTokens(payload); got != before {
		t.Fatalf("cache metadata counted as text: %d / %d", before, got)
	}
}

func TestCountTokensRejectsMalformedInputBeforeSavers(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "count")
	if err != nil {
		t.Fatal(err)
	}
	h := New(manager, Options{TransformFinalBody: func(context.Context, *runtime.RuntimeSnapshot, string, []byte) ([]byte, error) {
		t.Fatal("malformed input reached savers")
		return nil, nil
	}})
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"messages":[]} trailing`))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	h.handleCountTokens(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed request accepted: %d %s", w.Code, w.Body)
	}
}
