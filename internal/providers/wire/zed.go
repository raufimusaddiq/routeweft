package wire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	geminiwire "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Zed hosted LLM aggregator (executors/zed.js, shared/zedAuth.js). POST
// {base}/completions takes a thread envelope around the request in the shape of
// the vendor that serves the model (Anthropic, OpenAI Responses, Google, xAI) and
// answers NDJSON: `{"event": <vendor chunk>}`, `{"status": ...}` and `[DONE]`
// (zed.js:5-11). It is authenticated with a short-lived LLM bearer exchanged for
// the account's "<user_id> <access_token>" (zedAuth.js:258-294), not with the
// access token itself.
//
// The pin overrides execute() whole (zed.js:13-15); Routeweft does the same with
// Hook.Do. The Chat body reaches it already finalized and forced to stream
// (Hook.Finalize, Hook.ForceStream), and Do answers Chat SSE, so the dispatcher's
// fold, accounting and client translation are the ordinary ones.

const (
	zedEndpoint = "completions"
	// zedVersion is the x-zed-version the pin sends when its config names none
	// (zed.js:267).
	zedVersion = "0.200.0"
	// zedTokenTTL and zedModelTTL are LLM_TOKEN_TTL_MS and MODEL_CACHE_TTL_MS
	// (zedAuth.js:31-32).
	zedTokenTTL = 50 * time.Minute
	zedModelTTL = time.Hour
	// zedCacheEntries bounds each cache. The pin's maps are unbounded.
	zedCacheEntries = 256
	// zedRequestLimit and zedReplyLimit bound what is held in memory: the request
	// being re-enveloped, and an exchange or catalog answer.
	zedRequestLimit = 64 << 20
	zedReplyLimit   = 4 << 20

	zedHeaderExpiredToken        = "x-zed-expired-token"
	zedHeaderOutdatedToken       = "x-zed-outdated-token"
	zedHeaderClientStatus        = "x-zed-client-supports-status-messages"
	zedHeaderClientStreamEnded   = "x-zed-client-supports-stream-ended-request-completion-status"
	zedHeaderClientXAI           = "x-zed-client-supports-x-ai"
	zedHeaderSystemID            = "x-zed-system-id"
	zedVendorAnthropic           = "anthropic"
	zedVendorOpenAI              = "open_ai"
	zedVendorGoogle              = "google"
	zedVendorXAI                 = "x_ai"
	zedClientUserAgent           = "Routeweft/zed"
	zedAcceptCompletions         = "application/x-ndjson, text/event-stream, */*"
	zedUnavailableCredentialText = "zed connection is missing its user id or access token; reconnect the account"
)

var zedHook = Hook{
	Target: func(routing.ProviderRef) (Target, error) { return Target{Endpoint: zedEndpoint}, nil },
	// A request without the account's user id cannot be exchanged for an LLM
	// token; it is refused here, before anything is sent, as a credential problem.
	RequestHeaders: zedRefuseWithoutAccount,
	// The access token is never the bearer: Do builds its own requests and sends
	// the LLM token.
	RequireCredential: true,
	ForceStream:       true,
	Finalize:          codebuddyStream,
	Do:                ZedDo,
	Exchange:          true,
	AcceptedFailure:   zedAcceptedFailure,
	// The session the client's own request resolves to (zed.js:251).
	ClientRequestHeaders: zedClientThread,
}

func zedRefuseWithoutAccount(provider routing.ProviderRef, _ []byte, _ string) (map[string]string, error) {
	if _, err := zedUserAuth(provider); err != nil {
		return nil, &CredentialError{Err: err}
	}
	return nil, nil
}

// zedUserAuth is buildZedUserAuthHeader (zedAuth.js:163-171): "<user_id> <token>".
func zedUserAuth(provider routing.ProviderRef) (string, error) {
	userID := strings.TrimSpace(provider.Connection.Settings.UserID)
	token := strings.TrimSpace(provider.APIToken)
	if userID == "" || token == "" {
		return "", errors.New(zedUnavailableCredentialText)
	}
	return userID + " " + token, nil
}

// zedAccount is what one request needs of the connection.
type zedAccount struct {
	provider routing.ProviderRef
	userAuth string
	base     url.URL
}

// cacheKey names the connection at the host it is dialled at. The credential is
// hashed, so no secret is kept as a map key.
func (a zedAccount) cacheKey(parts ...string) string {
	sum := sha256.New()
	for _, part := range append([]string{a.base.String(), a.userAuth}, parts...) {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (a zedAccount) organization() string {
	settings := a.provider.Connection.Settings
	return firstNonEmpty(settings.OrganizationID, settings.DefaultOrganizationID)
}

func (a zedAccount) headers(extra map[string]string) map[string]string {
	headers := map[string]string{"Accept": "application/json", "User-Agent": zedClientUserAgent}
	if systemID := strings.TrimSpace(a.provider.Connection.Settings.SystemID); systemID != "" {
		headers[zedHeaderSystemID] = systemID
	}
	for name, value := range extra {
		headers[name] = value
	}
	return headers
}

type zedToken struct {
	value, organization string
	expires             time.Time
}

type zedCatalog struct {
	vendors map[string]string // model id -> the catalog's provider value
	expires time.Time
}

// zedCaches are the pin's llmTokenCache and modelCache (zedAuth.js:34-36), bounded.
type zedCaches struct {
	mu     sync.Mutex
	tokens map[string]zedToken
	models map[string]zedCatalog
	now    func() time.Time
}

var zedState = &zedCaches{tokens: map[string]zedToken{}, models: map[string]zedCatalog{}, now: time.Now}

func (c *zedCaches) token(key string) (zedToken, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.tokens[key]
	return cached, ok && cached.expires.After(c.now())
}

func (c *zedCaches) putToken(key string, token zedToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tokens) >= zedCacheEntries {
		for k, v := range c.tokens {
			if !v.expires.After(c.now()) {
				delete(c.tokens, k)
			}
		}
		for k := range c.tokens {
			if len(c.tokens) < zedCacheEntries {
				break
			}
			delete(c.tokens, k)
		}
	}
	c.tokens[key] = token
}

func (c *zedCaches) catalog(key string) (zedCatalog, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.models[key]
	return cached, ok && cached.expires.After(c.now())
}

func (c *zedCaches) putCatalog(key string, catalog zedCatalog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.models) >= zedCacheEntries {
		for k := range c.models {
			if len(c.models) < zedCacheEntries {
				break
			}
			delete(c.models, k)
		}
	}
	c.models[key] = catalog
}

// zedReset empties both caches and the session stores, for tests.
func zedReset() {
	zedState.mu.Lock()
	defer zedState.mu.Unlock()
	zedState.tokens, zedState.models = map[string]zedToken{}, map[string]zedCatalog{}
	zedState.now = time.Now
	connectionSessions, assistantSessions = newSessionStore(connectionSessionCap), newSessionStore(assistantSessionCap)
	sessionNow = time.Now
}

// zedRejection is an exchange step the host answered with a non-2xx status. The
// answer is the account's, so it is handed to the dispatcher as the response.
type zedRejection struct{ response *http.Response }

func (r *zedRejection) Error() string { return "zed rejected the request: " + r.response.Status }

// zedJSON sends one request and reads its bounded JSON answer. A non-2xx answer
// is a *zedRejection carrying the answer; a malformed 2xx one is an error.
func zedJSON(ctx context.Context, client *http.Client, method, target string, headers map[string]string, body []byte, into any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(io.LimitReader(response.Body, zedReplyLimit+1))
	if err != nil {
		return err
	}
	if len(text) > zedReplyLimit {
		return errors.New("zed reply exceeds the limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		kept := *response
		kept.Header = response.Header.Clone()
		kept.Body = io.NopCloser(bytes.NewReader(text))
		kept.ContentLength = int64(len(text))
		return &zedRejection{response: &kept}
	}
	if err := json.Unmarshal(text, into); err != nil {
		return errors.New("zed reply is not valid JSON")
	}
	return nil
}

// zedID is normalizeOrganizationId / normalizeZedModelId (zedAuth.js:221-229,
// 326-334): a string, a one-element array, or an object with an id.
func zedID(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		if len(typed) > 0 {
			if id, ok := typed[0].(string); ok {
				return id
			}
		}
	case map[string]any:
		if id, ok := typed["id"].(string); ok {
			return id
		}
	}
	return ""
}

// organization is resolveZedOrganizationId (zedAuth.js:231-242): the connection's
// own, else the account's default, else its personal organization, else its first.
func (a zedAccount) fetchOrganization(ctx context.Context, client *http.Client) (string, error) {
	var user struct {
		Default      any `json:"default_organization_id"`
		DefaultCamel any `json:"defaultOrganizationId"`
		Orgs         []struct {
			ID       any  `json:"id"`
			Personal bool `json:"is_personal"`
		} `json:"organizations"`
	}
	if err := zedJSON(ctx, client, http.MethodGet, a.url("/client/users/me"), a.headers(map[string]string{"Authorization": a.userAuth}), nil, &user); err != nil {
		return "", err
	}
	if id := firstNonEmpty(zedID(user.Default), zedID(user.DefaultCamel)); id != "" {
		return id, nil
	}
	for _, org := range user.Orgs {
		if org.Personal {
			if id := zedID(org.ID); id != "" {
				return id, nil
			}
		}
	}
	if len(user.Orgs) > 0 {
		return zedID(user.Orgs[0].ID), nil
	}
	return "", nil
}

func (a zedAccount) url(path string) string {
	base := a.base
	base.Path = strings.TrimRight(base.Path, "/") + path
	return base.String()
}

// llmToken is fetchZedLlmToken (zedAuth.js:258-294). A missing token in a 2xx
// answer is an error: the request is not sent with anything else.
func (a zedAccount) llmToken(ctx context.Context, client *http.Client, force bool) (string, error) {
	key := a.cacheKey("llm-token", a.organization())
	cached, ok := zedState.token(key)
	if ok && !force {
		return cached.value, nil
	}
	organization := firstNonEmpty(a.organization(), cached.organization)
	if organization == "" {
		var err error
		if organization, err = a.fetchOrganization(ctx, client); err != nil {
			return "", err
		}
	}
	if organization == "" {
		return "", errors.New("no Zed organization selected")
	}
	body, _ := json.Marshal(map[string]string{"organization_id": organization})
	var reply struct {
		Token any `json:"token"`
	}
	headers := a.headers(map[string]string{"Authorization": a.userAuth})
	if err := zedJSON(ctx, client, http.MethodPost, a.url("/client/llm_tokens"), headers, body, &reply); err != nil {
		return "", err
	}
	token := ""
	switch typed := reply.Token.(type) {
	case string:
		token = typed
	case []any:
		token = zedID(typed)
	case map[string]any:
		token, _ = typed["value"].(string)
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \r\n\t") {
		return "", errors.New("zed did not return an LLM token")
	}
	zedState.putToken(key, zedToken{value: token, organization: organization, expires: zedState.now().Add(zedTokenTTL)})
	return token, nil
}

// fetch is zedLlmFetch (zedAuth.js:304-324): the request carries the LLM bearer,
// and is sent once more with a fresh token when the host calls the first expired
// or outdated.
func (a zedAccount) fetch(ctx context.Context, client *http.Client, method, path string, headers map[string]string, body []byte) (*http.Response, error) {
	send := func(force bool) (*http.Response, error) {
		token, err := a.llmToken(ctx, client, force)
		if err != nil {
			return nil, err
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, a.url(path), reader)
		if err != nil {
			return nil, err
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		return client.Do(request)
	}
	response, err := send(false)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized || response.Header.Get(zedHeaderExpiredToken) != "" || response.Header.Get(zedHeaderOutdatedToken) != "" {
		response.Body.Close()
		return send(true)
	}
	return response, nil
}

// zedVendorOf is normalizeZedProvider (zed.js:46-58): the catalog's value, else
// the model's name. The wire values are the catalog's own spellings; an
// unrecognized one fails the whole request upstream (zed.js:32-38).
func zedVendorOf(catalogValue, model string) string {
	switch strings.ToLower(catalogValue) {
	case "anthropic":
		return zedVendorAnthropic
	case "openai", "open_ai":
		return zedVendorOpenAI
	case "google", "gemini":
		return zedVendorGoogle
	case "xai", "x_ai", "x-ai":
		return zedVendorXAI
	}
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "claude"):
		return zedVendorAnthropic
	case strings.Contains(lower, "gemini"):
		return zedVendorGoogle
	case strings.Contains(lower, "grok"), strings.Contains(lower, "xai"):
		return zedVendorXAI
	}
	return zedVendorOpenAI
}

// vendor is ZedExecutor.resolveModel (zed.js:226-244): the live catalog names the
// model's vendor; a model it does not list is looked up once more on a fresh
// catalog, and a catalog that cannot be read falls back to the model's name.
func (a zedAccount) vendor(ctx context.Context, client *http.Client, model string) string {
	key := a.cacheKey("models", a.organization())
	load := func(force bool) (string, bool, error) {
		catalog, ok := zedState.catalog(key)
		if !ok || force {
			headers := a.headers(map[string]string{zedHeaderClientXAI: "true"})
			response, err := a.fetch(ctx, client, http.MethodGet, "/models", headers, nil)
			if err != nil {
				return "", false, err
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return "", false, fmt.Errorf("zed models failed: %d", response.StatusCode)
			}
			var reply struct {
				Models []struct {
					ID       any    `json:"id"`
					Provider string `json:"provider"`
				} `json:"models"`
			}
			if err := json.NewDecoder(io.LimitReader(response.Body, zedReplyLimit)).Decode(&reply); err != nil {
				return "", false, errors.New("zed models reply is not valid JSON")
			}
			catalog = zedCatalog{vendors: map[string]string{}, expires: zedState.now().Add(zedModelTTL)}
			for _, entry := range reply.Models {
				if id := zedID(entry.ID); id != "" {
					catalog.vendors[id] = entry.Provider
				}
			}
			zedState.putCatalog(key, catalog)
		}
		vendor, found := catalog.vendors[model]
		return vendor, found, nil
	}
	vendor, found, err := load(false)
	if err == nil && !found {
		vendor, found, err = load(true)
	}
	if err != nil {
		slog.Warn("zed model catalog unavailable; inferring the vendor from the model name", "model", model, "error", err.Error())
		return zedVendorOf("", model)
	}
	if !found {
		vendor = ""
	}
	return zedVendorOf(vendor, model)
}

// zedThreadHeader carries the session the client's request resolves to from
// Hook.ClientRequestHeaders, which sees the client's headers and original body, to
// ZedDo. It exists only on the in-process request: ZedDo builds every upstream
// request itself and never copies it.
const zedThreadHeader = "X-Routeweft-Zed-Thread"

// zedSessionScope is the scope the pin passes to captureSessionId for Zed: the
// target format (translator/index.js:97; registry/zed.js:26 format "openai").
const zedSessionScope = "openai"

// zedClientThread is credentials._clientSessionId (translator/index.js:96-99): the
// session resolved from the client's headers and the body as the client sent it,
// before conversion, with the pin's precedence, scope and lifetime (resolveSession,
// opencodego_session.go; utils/sessionManager.js:138-221,246-248).
func zedClientThread(provider routing.ProviderRef, client http.Header, body []byte) map[string]string {
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	return map[string]string{zedThreadHeader: resolveSession(client, parsed, provider, zedSessionScope)}
}

// zedTruthy is JavaScript truthiness for a decoded JSON value, for `a || b`.
func zedTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case json.Number:
		f, err := typed.Float64()
		return err != nil || f != 0
	}
	return true
}

// zedThreadID is `bodyRecord.thread_id || credentials._clientSessionId`
// (zed.js:251): the body's own thread id when it is truthy, whatever its type,
// else the session the client's request resolved to. A request that did not come
// through a client (a probe) resolves it from the body it has, as the pin does for
// a request with no raw headers.
func zedThreadID(chat map[string]any, resolved string, provider routing.ProviderRef) json.RawMessage {
	if value := chat["thread_id"]; zedTruthy(value) {
		if encoded, err := json.Marshal(value); err == nil {
			return encoded
		}
	}
	if resolved == "" {
		resolved = resolveSession(nil, chat, provider, zedSessionScope)
	}
	encoded, _ := json.Marshal(resolved)
	return encoded
}

// zedLeg converts a vendor's SSE to Chat SSE; nil for a vendor that already
// answers in Chat chunks (xAI).
type zedLeg = func(context.Context, io.Reader, io.Writer) error

// zedProviderRequest is buildProviderRequest (zed.js:60-79), through the
// conversion legs the Chat hub already has. The request is always a stream.
func zedProviderRequest(vendor string, chat map[string]any, raw []byte, model string) (json.RawMessage, zedLeg, error) {
	switch vendor {
	case zedVendorAnthropic:
		body, names, err := anthropic.ChatRequestToMessages(chat, model, true, true, zedEndsOnAssistant(chat["messages"]))
		if err != nil {
			return nil, nil, err
		}
		return body, func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return anthropic.MessagesStreamToChat(ctx, src, dst, names)
		}, nil
	case zedVendorGoogle:
		body, names, err := geminiwire.ChatRequestToGemini(chat, geminiwire.RequestOptions{Model: model, Repair: true})
		if err != nil {
			return nil, nil, err
		}
		// Zed's Gemini backend speaks the Vertex safety vocabulary, not the public
		// enum the shared leg emits, so the client's safetySettings are dropped
		// and Zed applies its own (zed.js:66-71).
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, nil, err
		}
		delete(fields, "safetySettings")
		if body, err = json.Marshal(fields); err != nil {
			return nil, nil, err
		}
		options := geminiwire.ResponseOptions{Model: model, Names: names, Wire: geminiwire.Protocol}
		return body, func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return geminiwire.GeminiStreamToChat(ctx, src, dst, options)
		}, nil
	case zedVendorOpenAI:
		request, err := openai.ParseChatRequest(raw)
		if err != nil {
			return nil, nil, err
		}
		body, err := openai.ChatToResponses(request)
		if err != nil {
			return nil, nil, err
		}
		return body, func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return openai.ResponsesStreamToChat(ctx, src, dst, model)
		}, nil
	}
	// xAI is OpenAI-shaped: forwarded as it is (zed.js:77-78).
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, err
	}
	fields["model"], _ = json.Marshal(model)
	fields["stream"] = json.RawMessage("true")
	body, err := json.Marshal(fields)
	return body, nil, err
}

// zedEndsOnAssistant is translator/index.js:60-66: a conversation that ends on an
// assistant turn is prefill, not an emptied turn to restore.
func zedEndsOnAssistant(value any) bool {
	list := concerns.Items(value)
	return len(list) > 0 && concerns.Text(concerns.Object(list[len(list)-1])["role"]) == "assistant"
}

type zedEnvelope struct {
	ThreadID        json.RawMessage `json:"thread_id"`
	PromptID        json.RawMessage `json:"prompt_id,omitempty"` // as the body has it, null included; absent is omitted (JSON.stringify drops undefined)
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	ProviderRequest json.RawMessage `json:"provider_request"`
}

func zedPromptID(chat map[string]any) json.RawMessage {
	value, present := chat["prompt_id"]
	if !present {
		return nil
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// zedRefusal answers a request Zed's wire cannot carry as the client's error,
// not as the account's failure.
func zedRefusal(request *http.Request, status int, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
	return &http.Response{
		Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), StatusCode: status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}

// ZedDo is ZedExecutor.execute (zed.js:246-282). request carries the finalized
// Chat body aimed at {base}/completions; its credential is the connection's.
// The reply is Chat SSE, or the upstream's own non-2xx answer unconverted, for
// the dispatcher to classify. An exchange step the host rejected is returned as
// that rejection; one that cannot be completed is an error and nothing is sent.
func ZedDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, zedRequestLimit+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > zedRequestLimit {
		return zedRefusal(request, http.StatusRequestEntityTooLarge, "request exceeds the limit"), nil
	}
	userAuth, err := zedUserAuth(provider)
	if err != nil {
		return nil, &CredentialError{Err: err}
	}
	base := *request.URL
	base.Path, base.RawPath, base.RawQuery, base.Fragment = strings.TrimSuffix(base.Path, "/"+zedEndpoint), "", "", ""
	account := zedAccount{provider: provider, userAuth: userAuth, base: base}

	ctx, cancel := context.WithCancel(request.Context())
	fail := func(err error) (*http.Response, error) {
		cancel()
		var rejected *zedRejection
		if errors.As(err, &rejected) {
			return rejected.response, nil
		}
		return nil, err
	}

	chat, err := concerns.WireObject(raw)
	if err != nil {
		cancel()
		return zedRefusal(request, http.StatusBadRequest, "request body is not a JSON object"), nil
	}
	model := strings.TrimSpace(concerns.Text(chat["model"]))
	if model == "" {
		cancel()
		return zedRefusal(request, http.StatusBadRequest, "request names no model"), nil
	}
	vendor := account.vendor(ctx, client, model)
	providerRequest, leg, err := zedProviderRequest(vendor, chat, raw, model)
	if err != nil {
		cancel()
		return zedRefusal(request, http.StatusBadRequest, "request cannot be expressed for Zed's "+vendor+" backend: "+err.Error()), nil
	}
	envelope, err := json.Marshal(zedEnvelope{
		ThreadID:        zedThreadID(chat, request.Header.Get(zedThreadHeader), provider),
		PromptID:        zedPromptID(chat),
		Provider:        vendor,
		Model:           model,
		ProviderRequest: providerRequest,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	response, err := account.fetch(ctx, client, http.MethodPost, "/"+zedEndpoint, account.headers(map[string]string{
		"Content-Type":             "application/json",
		"Accept":                   zedAcceptCompletions,
		"x-zed-version":            zedVersion,
		zedHeaderClientStatus:      "true",
		zedHeaderClientStreamEnded: "true",
	}), envelope)
	if err != nil {
		return fail(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body = &zedBody{ReadCloser: response.Body, cancel: cancel}
		return response, nil
	}
	// A failed status ahead of any output is read before the reply is handed on, so
	// the dispatcher can cool the account and try the next candidate; the client is
	// still shown the pin's text (zedRelay). Past the first output a failure can no
	// longer move to another account and is only relayed.
	upstream, headFailure := zedPeekHead(response.Body)
	response.Header = response.Header.Clone()
	response.Header.Del(zedFailureHeader)
	if headFailure != nil {
		response.Header.Set(zedFailureHeader, base64.StdEncoding.EncodeToString([]byte(*headFailure)))
	}
	reader, writer := io.Pipe()
	go func() {
		_ = zedRelay(ctx, upstream, vendor, leg, model, writer)
		upstream.Close()
		writer.Close()
	}()
	response.Body = &zedBody{ReadCloser: reader, cancel: cancel}
	response.Header.Set("Content-Type", "text/event-stream")
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.ContentLength, response.Uncompressed = -1, true
	return response, nil
}

// zedBody ties the reply to the upstream request: closing it stops both.
type zedBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *zedBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

// zedStatus is the pin's normalizeStatus and the two statuses it acts on
// (zed.js:137-146,173-182): a string, or an object keyed by the status name or
// carrying it as "type". Any other status is progress and is ignored.
func zedStatus(status any) (ended bool, failure string, failed bool) {
	name, detail := "", map[string]any{}
	switch typed := status.(type) {
	case string:
		name = typed
	case map[string]any:
		detail = typed
		name, _ = typed["type"].(string)
		for key, value := range typed {
			if key == "failed" || key == "stream_ended" {
				name = key
				if nested, ok := value.(map[string]any); ok {
					detail = nested
				}
			}
		}
	}
	switch name {
	case "stream_ended":
		return true, "", false
	case "failed":
		message := firstNonEmpty(concerns.Text(detail["message"]), concerns.Text(detail["error"]), concerns.Text(detail["code"]))
		if message == "" {
			message = "request failed"
		}
		return false, message, true
	}
	return false, "", false
}

// zedOut sits between a vendor leg and the client. The legs end a stream that
// stopped short with an `upstream_incomplete` error object and `[DONE]`
// (concerns.FailChatStream); the pin's wrapper does neither, so both are held back
// here and the relay writes the one `[DONE]` itself. Every other frame, a real
// in-band upstream error included, passes through.
type zedOut struct {
	dst io.Writer
	// sawError is set only by an error frame that reached the client: the held-back
	// incomplete-stream frame is not one, and must not stop the relay from saying
	// why the stream broke.
	sawError bool
}

func (o *zedOut) Write(p []byte) (int, error) {
	text := string(p)
	switch {
	case text == "data: [DONE]\n\n":
		return len(p), nil
	case strings.HasPrefix(text, `data: {"error":`):
		if strings.Contains(text, `"code":"upstream_incomplete"`) {
			return len(p), nil
		}
		o.sawError = true
	}
	return o.dst.Write(p)
}

// zedResponsesTerminals are the Responses events after which the pin's translator
// has sent its finish (finishReasonSent) and has nothing left to flush.
var zedResponsesTerminals = map[string]bool{"response.completed": true, "response.incomplete": true, "response.failed": true, "error": true}

// zedRelay is wrapZedCompletionStream (zed.js:148-219): Zed's NDJSON is unwrapped
// into the vendor's own SSE, the vendor's leg turns it into Chat SSE, and the
// stream ends as the pin ends it.
//
//   - A `failed` status writes "[Zed error] <message>" as the assistant's text with
//     finish_reason "stop" (createErrorChunk, :97-107,173-179), then finishes.
//   - `stream_ended`, `[DONE]` and the end of the body all finish (:180-182,200-208);
//     a stream cut off is not an error. Finishing is the translator's flush
//     (convertProviderEvent(provider, null, state), :159): the OpenAI Responses
//     translator closes a started turn with its finish chunk, the others have
//     nothing to send. The Go legs end a stream that stopped short with an error
//     object, so the Responses flush is a synthesized `response.completed` and the
//     legs' error tail is dropped (zedOut).
//   - A line that is not JSON is skipped (:132-134).
//   - `[DONE]` is written once, last (:161).
//
// Not replicated, being accidents of the pin: a `null` line or `{"event":null}`
// is handed to the translator as its flush call, ending the turn early, and a
// stream that has produced nothing is not given a finish. A line over 4 MiB, or a
// body over the translated-response bound, is an error, as for every other leg.
func zedRelay(ctx context.Context, upstream io.Reader, vendorName string, leg zedLeg, model string, dst io.Writer) error {
	out := &zedOut{dst: dst}
	chat := &concerns.SSEWriter{Ctx: ctx, Dst: out}
	var vendorWriter *io.PipeWriter
	var vendor *concerns.SSEWriter
	legDone := make(chan error, 1)
	if leg != nil {
		var vendorReader *io.PipeReader
		vendorReader, vendorWriter = io.Pipe()
		vendor = &concerns.SSEWriter{Ctx: ctx, Dst: vendorWriter}
		go func() {
			err := leg(ctx, vendorReader, out)
			_ = vendorReader.CloseWithError(err)
			legDone <- err
		}()
	}
	var failure *string
	relayed, terminal := false, false
	readErr := concerns.ReadLinesPin(ctx, upstream, func(value any) (bool, error) {
		event, status, isStatus := zedFrame(value)
		if isStatus {
			ended, message, failed := zedStatus(status)
			if failed {
				failure = &message
				return true, nil
			}
			return ended, nil
		}
		if event == nil {
			return false, nil
		}
		relayed = true
		if leg == nil {
			return false, zedPassThrough(chat, event)
		}
		if object, ok := event.(map[string]any); ok {
			if kind, _ := object["type"].(string); zedResponsesTerminals[kind] {
				terminal = true
			}
		}
		return false, vendor.Event("", event)
	})
	// Reading ended: the body is exhausted, Zed said so, or it reported a failure.
	// Anything else (the client left, a line over the bound) is not a finish.
	hard := readErr != nil && !errors.Is(readErr, concerns.ErrLinesEnded)
	var legErr error
	if leg != nil {
		if !hard && failure != nil {
			// The leg has written everything it read once it takes the next byte;
			// the error chunk goes after that and before the flush.
			_, _ = vendorWriter.Write([]byte("\n"))
		}
		if !hard {
			if failure != nil {
				if err := zedErrorChunk(chat, model, *failure); err != nil {
					hard, readErr = true, err
				}
			}
			if !hard && relayed && !terminal && vendorName == zedVendorOpenAI {
				_ = vendor.Event("", map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{}}})
			}
		}
		// A body that merely stopped is a clean end to the leg, which reports it as
		// short (and is answered below); only a read that failed is passed on.
		if hard {
			_ = vendorWriter.CloseWithError(readErr)
		} else {
			_ = vendorWriter.Close()
		}
		legErr = <-legDone
	} else if !hard && failure != nil {
		if err := zedErrorChunk(chat, model, *failure); err != nil {
			hard, readErr = true, err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// The legs report a stream that stopped short; the pin finishes it.
	if errors.Is(legErr, io.ErrUnexpectedEOF) {
		legErr = nil
	}
	err := readErr
	if !hard {
		err = legErr
	}
	if err != nil && !out.sawError {
		_ = chat.Event("", map[string]any{"error": map[string]any{"message": err.Error(), "type": "api_error", "code": "zed_stream_error"}})
	}
	// The legs' [DONE] was held back (zedOut); this is the one, written past it.
	_ = (&concerns.SSEWriter{Ctx: ctx, Dst: dst}).Done()
	return err
}

// zedFrame is unwrapZedLine (zed.js:118-135): a line with an "event" key is that
// event, one with a "status" key is that status, and any other JSON value is itself
// an event. A `null` event is dropped (see zedRelay).
func zedFrame(value any) (event, status any, isStatus bool) {
	if object, ok := value.(map[string]any); ok {
		if event, ok := object["event"]; ok {
			return event, nil, false
		}
		if status, ok := object["status"]; ok {
			return nil, status, true
		}
	}
	return value, nil, false
}

// zedPassThrough is enqueueSseObject (zed.js:109-116) for the xAI path, where the
// event already is a Chat chunk: an array is relayed item by item, and an item
// that is falsy is dropped.
func zedPassThrough(chat *concerns.SSEWriter, event any) error {
	items, ok := event.([]any)
	if !ok {
		items = []any{event}
	}
	for _, item := range items {
		if !zedTruthy(item) {
			continue
		}
		if err := chat.Event("", item); err != nil {
			return err
		}
	}
	return nil
}

// zedErrorChunk is createErrorChunk (zed.js:97-107): the failure as the
// assistant's text, finishing the turn, under the model the request named.
func zedErrorChunk(chat *concerns.SSEWriter, model, message string) error {
	return chat.Event("", map[string]any{
		"id":      fmt.Sprintf("chatcmpl-zed-error-%d", time.Now().UnixMilli()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "[Zed error] " + message}, "finish_reason": "stop"}},
	})
}

// zedFailureHeader carries a failed status found ahead of any output from ZedDo to
// the dispatcher on the in-process response (Hook.AcceptedFailure removes it).
const zedFailureHeader = "X-Routeweft-Zed-Failure"

// zedHeadLimit bounds what is read looking for the first decisive line.
const zedHeadLimit = 1 << 20

// zedHeadLine reads one line as zedRelay will: decisive when it is an event, the end
// of the stream, or a failed status; a `queued`-style status, an empty line, a line
// that is not JSON and a null event decide nothing yet.
func zedHeadLine(raw []byte) (decisive bool, failure *string) {
	line := strings.TrimSpace(string(raw))
	line = strings.TrimLeft(strings.TrimPrefix(line, "data:"), " \t")
	if line == "" {
		return false, nil
	}
	if line == "[DONE]" {
		return true, nil
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return false, nil
	}
	event, status, isStatus := zedFrame(value)
	if !isStatus {
		return event != nil, nil
	}
	ended, message, failed := zedStatus(status)
	if failed {
		return true, &message
	}
	return ended, nil
}

// zedPeekHead reads up to the first decisive line of body and returns a reader that
// replays everything read followed by the rest. failure is the message when that
// line is a failed status.
func zedPeekHead(body io.ReadCloser) (io.ReadCloser, *string) {
	reader := bufio.NewReaderSize(body, 64<<10)
	var head, pending []byte
	var failure *string
	for len(head) < zedHeadLimit {
		chunk, err := reader.ReadSlice('\n')
		head = append(head, chunk...)
		pending = append(pending, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		decisive, failed := zedHeadLine(pending)
		pending = pending[:0]
		if decisive {
			failure = failed
			break
		}
		if err != nil {
			break
		}
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), reader), body}, failure
}

// zedAcceptedFailure is Hook.AcceptedFailure: how a failed status the client is
// shown as text counts against the account. The pin has no account accounting for
// it; Routeweft reads the message the way it reads an upstream error body: quota,
// billing and rate words lock the account, authentication words ask for a refresh,
// anything else is a failed attempt that falls back and cools for the default
// backoff.
func zedAcceptedFailure(response *http.Response) (routing.Classification, bool) {
	encoded := response.Header.Get(zedFailureHeader)
	response.Header.Del(zedFailureHeader)
	if encoded == "" {
		return routing.Classification{}, false
	}
	message, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return routing.Classification{}, false
	}
	lower := strings.ToLower(string(message))
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(lower, word) {
				return true
			}
		}
		return false
	}
	switch {
	case has("quota", "billing", "credit", "limit", "exhaust", "rate"):
		return routing.Classification{Outcome: routing.OutcomeQuotaLock}, true
	case has("unauthorized", "authentication", "permission", "forbidden", "expired token", "invalid token"):
		return routing.Classification{Outcome: routing.OutcomeAuthRefreshRequired}, true
	}
	return routing.Classification{Outcome: routing.OutcomeFallbackAccount}, true
}
