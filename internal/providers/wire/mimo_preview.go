package wire

// Xiaomi MiMo Preview inference (executors/xiaomi-mimo.js:4-7,39-101;
// shared/mimoAccount.js:116-218). The two Preview models are served by the
// account service at {MIMO_API_BASE}/api/route/chat/completions and authorize
// with a Cookie session, not the connection's sk- key. The session comes from
// the sealed passToken already stored on the connection; a 401 drops the cached
// cookie and retries once with a fresh handshake.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/providers/mimoaccount"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const mimoPreviewPath = "api/route/chat/completions"
const mimoPreviewMissingSession = "Xiaomi MiMo account session unavailable. Sign in to MiMo Desktop once so its passToken is present, then retry."

var mimoPreviewModels = map[string]bool{"mimo-x-pro-preview": true, "mimo-x-flash-preview": true}

// mimoSessions keeps the service cookie per passToken across requests.
var mimoSessions = mimoaccount.NewSessions()

func mimoPreviewModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	model = strings.TrimSpace(model)
	if mimoPreviewModels[model] {
		return model
	}
	return ""
}

// mimoPreviewNeedsData is Hook.NeedsAccountData: only Preview models read the
// passToken; every cloud model keeps the ordinary bearer path.
func mimoPreviewNeedsData(provider routing.ProviderRef) bool {
	return mimoPreviewModel(provider.UpstreamModel) != ""
}

// mimoTarget sends a Preview model to the account route; every other model
// keeps the catalog's cloud transport.
func mimoTarget(provider routing.ProviderRef) (Target, error) {
	if mimoPreviewModel(provider.UpstreamModel) == "" {
		return Target{}, nil
	}
	return Target{BaseURL: mimoaccount.Base, Endpoint: mimoPreviewPath}, nil
}

// accountDataKey carries the allowlisted sealed fields a wire asked for.
type accountDataKey struct{}

// WithAccountData attaches provider account fields to a request context.
func WithAccountData(ctx context.Context, data map[string]string) context.Context {
	return context.WithValue(ctx, accountDataKey{}, data)
}

// AccountData returns the fields the dispatcher attached, or nil.
func AccountData(ctx context.Context) map[string]string {
	data, _ := ctx.Value(accountDataKey{}).(map[string]string)
	return data
}

// NeedsAccountData reports whether the provider's exchange reads sealed account
// fields beyond the bearer token.
func NeedsAccountData(provider routing.ProviderRef) bool {
	needs := For(provider.ProviderID).NeedsAccountData
	return needs != nil && needs(provider)
}

func mimoRefusal(request *http.Request, status int, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "api_error"}})
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

// MimoDo is the Preview exchange. Cloud models keep the dispatcher's request.
func MimoDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	if mimoPreviewModel(provider.UpstreamModel) == "" {
		return client.Do(request)
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, cursorRequestLimit+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > cursorRequestLimit {
		return mimoRefusal(request, http.StatusRequestEntityTooLarge, "request exceeds the limit"), nil
	}
	body, err := mimoPreviewBody(raw)
	if err != nil {
		return mimoRefusal(request, http.StatusBadRequest, "request cannot be expressed for Xiaomi MiMo Preview: "+err.Error()), nil
	}
	data := AccountData(request.Context())
	if strings.TrimSpace(data["mimoPassToken"]) == "" {
		return mimoRefusal(request, http.StatusBadGateway, mimoPreviewMissingSession), nil
	}
	response, err := mimoPreviewSend(provider, client, request, data, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	// One retry with a fresh session (xiaomi-mimo.js:86-101).
	response.Body.Close()
	mimoSessions.Invalidate()
	return mimoPreviewSend(provider, client, request, data, body)
}

// mimoPreviewBody applies the defaults the pin sets when the caller did not
// (xiaomi-mimo.js:76-81).
func mimoPreviewBody(raw []byte) ([]byte, error) {
	body, err := concerns.WireObject(raw)
	if err != nil {
		return nil, fmt.Errorf("request is not a JSON object")
	}
	if body["thinking"] == nil {
		body["thinking"] = map[string]any{"type": "enabled"}
	}
	if body["temperature"] == nil {
		body["temperature"] = 1.0
	}
	if body["top_p"] == nil {
		body["top_p"] = 0.95
	}
	if max, ok := body["max_tokens"].(float64); !ok || max == 0 {
		body["max_tokens"] = 4096
	}
	return json.Marshal(body)
}

func mimoPreviewSend(provider routing.ProviderRef, client *http.Client, request *http.Request, data map[string]string, body []byte) (*http.Response, error) {
	cookie, err := mimoSessions.Cookie(request.Context(), data["mimoPassToken"], timeNow, func() (string, error) {
		return mimoaccount.ServiceCookie(request.Context(), client, data)
	})
	if err != nil || cookie == "" {
		return mimoRefusal(request, http.StatusBadGateway, mimoPreviewMissingSession), nil
	}
	sent, err := http.NewRequestWithContext(request.Context(), http.MethodPost, request.URL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	sent.Header = request.Header.Clone()
	sent.Header.Del("Authorization")
	sent.Header.Set("Content-Type", "application/json")
	sent.Header.Set("Cookie", cookie)
	sent.Header.Set("User-Agent", mimoaccount.UserAgent)
	sent.Header.Set("Accept", "text/event-stream, application/json")
	return client.Do(sent)
}

// timeNow is a variable so a test can pin the session TTL clock.
var timeNow = func() time.Time { return time.Now() }
