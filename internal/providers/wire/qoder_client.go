package wire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The network half of the Qoder serving path that is not the chat call itself: the
// PAT exchange and the live model catalog (services/qoderModels.js), and the assembly
// of one chat request (QoderExecutor.execute, executors/qoder.js:613-702).
//
// Nothing in this file is reachable from dispatch: qoder stays unroutable until the
// owner approves it (PARITY 6.19). A QoderClient holds process-local mutable state (the
// PAT cache, the catalog cache and the requests in flight), so the wiring change owns
// it in runtime.RuntimeState, never in the snapshot.

const (
	qoderOpenAPIBase      = "https://openapi.qoder.sh"                                  // constants.js:11
	qoderChatBase         = "https://api3.qoder.sh"                                     // :12
	qoderChatBaseAlt      = "https://api2.qoder.sh"                                     // :15; job tokens
	qoderJobTokenExchange = qoderOpenAPIBase + "/api/v1/jobToken/exchange"              // :26
	qoderUserinfo         = qoderOpenAPIBase + "/api/v1/userinfo"                       // :21
	qoderChatSigPath      = "/api/v2/service/pro/sse/agent_chat_generation"             // :29
	qoderChatQuery        = "?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1" // executors/qoder.js:605
	qoderCatalogTTL       = time.Hour                                                   // qoderModels.js:35
	qoderCatalogTimeout   = 15 * time.Second                                            // :34
	qoderPATRefreshBuffer = 5 * time.Minute                                             // :41
	qoderPATDefaultTTL    = 24 * time.Hour                                              // :42
	qoderMaxResponseBytes = 8 << 20
)

// QoderCredentials are the pin's `credentials` for a qoder connection.
type QoderCredentials struct {
	APIKey       string // a personal access token (pt-) is stored here
	AccessToken  string // a device (dt-) or job (jt-) token
	RefreshToken string
	UserID       string // providerSpecificData.userId
	MachineID    string // providerSpecificData.machineId
	DisplayName  string
	Email        string
}

func (c QoderCredentials) token() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return c.AccessToken
}

func (c QoderCredentials) cosy() CosyCredentials {
	return CosyCredentials{UserID: c.UserID, AuthToken: c.AccessToken, Name: c.DisplayName, Email: c.Email, MachineID: c.MachineID}
}

// IsQoderPAT is isQoderPat: a personal access token cannot sign COSY requests.
func IsQoderPAT(token string) bool { return strings.HasPrefix(token, "pt-") }

// qoderInferenceBase is constants.js:55-65: job tokens are served from api2, which
// api3 refuses with "Login expired".
func qoderInferenceBase(c QoderCredentials) string {
	raw := c.token()
	if raw != "" && !strings.HasPrefix(raw, "pt-") && (strings.HasPrefix(raw, "jt-") || strings.HasPrefix(c.AccessToken, "jt-")) {
		return qoderChatBaseAlt
	}
	return qoderChatBase
}

type cosySigner func(body []byte, url string, creds CosyCredentials) (map[string]string, error)

// QoderLocalError is a failure the pin answers itself, without calling Qoder, with
// the status it gives (executors/qoder.js:621-689). The wiring change decides how
// each maps to a Routeweft error; a 401 is a credential problem and must not cool
// an account (P02 audit).
type QoderLocalError struct {
	Status  int
	Message string
}

func (e *QoderLocalError) Error() string { return e.Message }

// QoderModel is one model the catalog offers to the IDE (qoderModels.js:259-269).
type QoderModel struct {
	ID              string
	Name            string
	ContextLength   float64
	IsVL            bool
	IsReasoning     bool
	MaxOutputTokens float64
	Description     string
}

// QoderCatalog is the live model list of one account.
type QoderCatalog struct {
	Models  []QoderModel
	keys    []string
	configs map[string]QoderModelConfig
	expires time.Time
}

// Config is the model_config for a key, hidden models (enable:false) included: the
// gateway still serves them.
func (c *QoderCatalog) Config(key string) (QoderModelConfig, bool) {
	if c == nil {
		return QoderModelConfig{}, false
	}
	config, ok := c.configs[key]
	return config, ok
}

// Keys are every key the catalog holds a config for, in the order the server listed them.
func (c *QoderCatalog) Keys() []string { return append([]string(nil), c.keys...) }

type qoderPAT struct {
	accessToken string
	userID      string
	expires     time.Time
}

type qoderFlight struct {
	done    chan struct{}
	catalog *QoderCatalog
	err     error
}

// QoderClient reaches Qoder for the PAT exchange, the model catalog and a prepared chat request.
type QoderClient struct {
	HTTP QoderHTTPDoer
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Logf receives the pin's log lines; nil is silent.
	Logf func(format string, args ...any)

	sign cosySigner
	env  qoderEnv

	mu       sync.Mutex
	pats     map[string]qoderPAT
	catalogs map[string]*QoderCatalog
	flights  map[string]*qoderFlight
}

// NewQoderClient returns a client that sends through doer.
func NewQoderClient(doer QoderHTTPDoer) *QoderClient {
	return &QoderClient{HTTP: doer, sign: BuildCosyHeaders, env: qoderLive}
}

func (c *QoderClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *QoderClient) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// do sends one request and reads at most qoderMaxResponseBytes of the answer.
func (c *QoderClient) do(ctx context.Context, method, url string, headers map[string]string, body io.Reader) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, qoderMaxResponseBytes))
	return response.StatusCode, data, err
}

func okStatus(status int) bool { return status >= 200 && status <= 299 }

// exchangeJobToken is :66-98. The endpoint is a plain JSON POST, not COSY-signed.
func (c *QoderClient) exchangeJobToken(ctx context.Context, pat string) (token string, expires time.Time, err error) {
	status, data, err := c.do(ctx, http.MethodPost, qoderJobTokenExchange, map[string]string{
		"Content-Type": "application/json", "Accept": "application/json", "User-Agent": "qodercli/1.0.0",
		"Cosy-Version": qoderIDEVersion, "Cosy-ClientType": qoderClientType,
	}, strings.NewReader(stringifyJS(newJSObject().set("personal_token", pat))))
	if err != nil {
		return "", time.Time{}, err
	}
	if !okStatus(status) {
		detail, _ := sliceUTF16(string(data), 200)
		return "", time.Time{}, fmt.Errorf("qoder PAT exchange failed: %d %s", status, detail)
	}
	value, err := parseJS(data)
	if err != nil {
		return "", time.Time{}, err
	}
	if value == nil {
		return "", time.Time{}, errors.New("Cannot read properties of null (reading 'token')")
	}
	reply, _ := value.(*jsObject)
	if !jsIsTruthy(reply.get("token")) {
		return "", time.Time{}, errors.New("qoder PAT exchange returned no job token")
	}
	expires = c.now().Add(qoderPATDefaultTTL)
	if at := reply.get("expires_at"); jsIsTruthy(at) {
		if ms, ok := jsDateParse(jsToString(at)); ok {
			expires = time.UnixMilli(int64(ms))
		}
	} else if in, ok := jsNumeric(reply.get("expires_in")); ok && in > 0 {
		// The pin adds expires_in to Date.now() as milliseconds.
		expires = c.now().Add(time.Duration(in * float64(time.Millisecond)))
	}
	return jsToString(reply.get("token")), expires, nil
}

// userIDForJobToken is :104-125: "" on any failure, and the caller keeps the stored id.
func (c *QoderClient) userIDForJobToken(ctx context.Context, jobToken string) string {
	status, data, err := c.do(ctx, http.MethodGet, qoderUserinfo, map[string]string{
		"Authorization": "Bearer " + jobToken, "Accept": "application/json", "User-Agent": "qodercli/1.0.0",
	}, nil)
	if err != nil || !okStatus(status) {
		return ""
	}
	value, err := parseJS(data)
	reply, ok := value.(*jsObject)
	if err != nil || !ok {
		return ""
	}
	for _, key := range []string{"id", "userId", "user_id"} {
		if v := reply.get(key); jsIsTruthy(v) {
			return jsToString(v)
		}
	}
	return ""
}

// ResolveCredentials makes a connection signable (:146-163): a personal access token
// (pt-) is exchanged for a short-lived job token and the user id behind it, cached per
// token until five minutes before it expires; anything else is returned unchanged.
func (c *QoderClient) ResolveCredentials(ctx context.Context, creds QoderCredentials) (QoderCredentials, error) {
	pat := creds.token()
	if !IsQoderPAT(pat) {
		return creds, nil
	}
	c.mu.Lock()
	cached, hit := c.pats[pat]
	c.mu.Unlock()
	if !hit || cached.expires.Sub(c.now()) <= qoderPATRefreshBuffer {
		jobToken, expires, err := c.exchangeJobToken(ctx, pat)
		if err != nil {
			return QoderCredentials{}, err
		}
		cached = qoderPAT{accessToken: jobToken, userID: c.userIDForJobToken(ctx, jobToken), expires: expires}
		c.mu.Lock()
		if c.pats == nil {
			c.pats = map[string]qoderPAT{}
		}
		c.pats[pat] = cached
		c.mu.Unlock()
	}
	creds.AccessToken, creds.APIKey = cached.accessToken, ""
	if cached.userID != "" {
		creds.UserID = cached.userID
	}
	return creds, nil
}

func qoderCatalogKey(c QoderCredentials) string {
	seed := c.UserID
	for _, fallback := range []string{c.RefreshToken, c.AccessToken, "anonymous"} {
		if seed != "" {
			break
		}
		seed = fallback
	}
	sum := sha256.Sum256([]byte("qoder:" + seed))
	return hex.EncodeToString(sum[:])
}

// fetchCatalog is fetchQoderCatalogRaw (:195-273); nil when Qoder gives no list.
func (c *QoderClient) fetchCatalog(ctx context.Context, creds QoderCredentials) (*QoderCatalog, error) {
	signer := creds.cosy()
	if signer.UserID == "" || signer.AuthToken == "" {
		return nil, nil
	}
	url := qoderChatBase + "/algo/api/v2/model/list"
	if strings.HasPrefix(signer.AuthToken, "jt-") {
		url = qoderChatBaseAlt + "/algo/api/v2/model/list"
	}
	sign := c.sign
	if sign == nil {
		sign = BuildCosyHeaders
	}
	headers, err := sign(nil, url, signer)
	if err != nil {
		return nil, err
	}
	headers["Accept"], headers["Accept-Encoding"] = "application/json", "identity"
	ctx, cancel := context.WithTimeout(ctx, qoderCatalogTimeout)
	defer cancel()
	status, data, err := c.do(ctx, http.MethodGet, url, headers, nil)
	if err != nil {
		return nil, err
	}
	if !okStatus(status) {
		return nil, nil
	}
	value, err := parseJS(data)
	body, _ := value.(*jsObject)
	list, isList := body.get("chat").([]any)
	if err != nil || !isList {
		return nil, nil
	}
	catalog := &QoderCatalog{configs: map[string]QoderModelConfig{}}
	for _, raw := range list {
		entry, ok := raw.(*jsObject)
		key, isString := entry.get("key").(string)
		if !ok || !isString || key == "" {
			continue
		}
		if _, seen := catalog.configs[key]; !seen {
			catalog.keys = append(catalog.keys, key)
		}
		// The config is cached even for a model the IDE hides (enable:false):
		// the gateway still accepts chat for it.
		catalog.configs[key] = QoderModelConfig{obj: entry}
		if entry.get("enable") == false {
			continue
		}
		display := key
		if jsIsTruthy(entry.get("display_name")) {
			display = jsToString(entry.get("display_name"))
		}
		window := jsToNumber(entry.get("max_input_tokens"))
		if window == 0 || math.IsNaN(window) {
			window = 131_072
		}
		output := jsToNumber(entry.get("max_output_tokens"))
		if math.IsNaN(output) {
			output = 0
		}
		description := ""
		if jsIsTruthy(entry.get("description")) {
			description = jsToString(entry.get("description"))
		}
		catalog.Models = append(catalog.Models, QoderModel{
			ID: key, Name: display, ContextLength: window, IsVL: jsIsTruthy(entry.get("is_vl")),
			IsReasoning: jsIsTruthy(entry.get("is_reasoning")), MaxOutputTokens: output, Description: description,
		})
	}
	return catalog, nil
}

// Catalog returns the live catalog for an account, cached for an hour (:295-344).
// Concurrent misses share one request; forceRefresh always makes its own. A nil
// catalog with a nil error is "no list" (no identity, a failed exchange, a refusal);
// a transport failure is an error, as the pin lets it escape.
func (c *QoderClient) Catalog(ctx context.Context, creds QoderCredentials, forceRefresh bool) (*QoderCatalog, error) {
	resolved, err := c.ResolveCredentials(ctx, creds)
	if err != nil {
		c.logf("QODER PAT exchange failed: %s", err)
		return nil, nil
	}
	if resolved.AccessToken == "" || resolved.UserID == "" {
		return nil, nil
	}
	key := qoderCatalogKey(resolved)
	c.mu.Lock()
	if !forceRefresh {
		if cached := c.catalogs[key]; cached != nil && cached.expires.After(c.now()) {
			c.mu.Unlock()
			return cached, nil
		}
		if flight := c.flights[key]; flight != nil {
			c.mu.Unlock()
			select {
			case <-flight.done:
				return flight.catalog, flight.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	flight := &qoderFlight{done: make(chan struct{})}
	if c.flights == nil {
		c.flights = map[string]*qoderFlight{}
	}
	c.flights[key] = flight
	c.mu.Unlock()

	flight.catalog, flight.err = c.fetchCatalog(ctx, resolved)
	c.mu.Lock()
	if flight.catalog != nil {
		flight.catalog.expires = c.now().Add(qoderCatalogTTL)
		if c.catalogs == nil {
			c.catalogs = map[string]*QoderCatalog{}
		}
		c.catalogs[key] = flight.catalog
	}
	// Only the flight that is still current clears the slot; a later forced refresh may have replaced it.
	if c.flights[key] == flight {
		delete(c.flights, key)
	}
	c.mu.Unlock()
	close(flight.done)
	return flight.catalog, flight.err
}

// ModelConfig is the model_config for a key (getQoderModelConfig plus the one forced
// refresh buildQoderRequestBody makes, :213-227). Sending the wrong block silently
// serves another model, so a key the catalog does not know is an error.
func (c *QoderClient) ModelConfig(ctx context.Context, creds QoderCredentials, key string) (QoderModelConfig, error) {
	catalog, err := c.Catalog(ctx, creds, false)
	if err != nil {
		return QoderModelConfig{}, err
	}
	if config, ok := catalog.Config(key); ok {
		return config.withKey(key), nil
	}
	catalog, err = c.Catalog(ctx, creds, true)
	if err != nil {
		return QoderModelConfig{}, err
	}
	if config, ok := catalog.Config(key); ok {
		return config.withKey(key), nil
	}
	return QoderModelConfig{}, fmt.Errorf("qoder: model_config for %q not yet known (run a model list fetch or check upstream connectivity)", key)
}

// QoderChatInput is one chat request.
type QoderChatInput struct {
	Model       string // routed model, with or without "qoder/"
	Body        []byte // Chat Completions body
	Credentials QoderCredentials
	ContextTier string
}

// QoderChatRequest is the signed request to send, and nothing else about it needs the
// pin's state: the body is the encoded bytes the signature covers.
type QoderChatRequest struct {
	URL     string
	Headers map[string]string
	Body    []byte
	Key     string
}

// PrepareChat is the request half of QoderExecutor.execute. The caller sends it once:
// a COSY signature carries its request id, so a replay of the same bytes is refused
// (code 103) and a retry must come back here for a fresh signature.
func (c *QoderClient) PrepareChat(ctx context.Context, in QoderChatInput) (QoderChatRequest, error) {
	creds := in.Credentials
	if IsQoderPAT(creds.token()) {
		resolved, err := c.ResolveCredentials(ctx, creds)
		if err != nil {
			return QoderChatRequest{}, &QoderLocalError{Status: 401, Message: "qoder PAT exchange failed: " + err.Error()}
		}
		creds = resolved
	}
	url := qoderInferenceBase(creds) + "/algo" + qoderChatSigPath + qoderChatQuery
	if creds.UserID == "" {
		return QoderChatRequest{}, &QoderLocalError{Status: 401, Message: "qoder credential is missing userId; reconnect the account"}
	}
	if creds.AccessToken == "" {
		return QoderChatRequest{}, &QoderLocalError{Status: 401, Message: "qoder credential is missing accessToken; reconnect the account"}
	}
	config, err := c.ModelConfig(ctx, creds, strings.TrimPrefix(in.Model, "qoder/"))
	if err != nil {
		return QoderChatRequest{}, &QoderLocalError{Status: 400, Message: err.Error()}
	}
	envelope, err := buildQoderEnvelope(ctx, QoderEnvelopeInput{
		Model: in.Model, Body: in.Body, UserID: creds.UserID, Config: config, ContextTier: in.ContextTier,
		Upload: qoderImageUploader(c.HTTP, creds, c.env, c.signer(), qoderNonce(c.now)),
	}, c.env)
	if err != nil {
		return QoderChatRequest{}, &QoderLocalError{Status: 400, Message: err.Error()}
	}
	encoded := QoderEncodeBody(envelope.Payload)
	cosy, err := c.signer()(encoded, url, creds.cosy())
	if err != nil {
		return QoderChatRequest{}, &QoderLocalError{Status: 401, Message: "qoder cosy signing failed: " + err.Error()}
	}
	headers := map[string]string{
		"Content-Type": "application/json", "Accept": "text/event-stream", "Cache-Control": "no-cache",
		"X-Model-Key": envelope.Key, "X-Model-Source": envelope.ModelSource,
		// gzip triggers signature validation on Qoder's CDN; force identity.
		"Accept-Encoding": "identity",
	}
	for k, v := range cosy {
		headers[k] = v
	}
	return QoderChatRequest{URL: url, Headers: headers, Body: encoded, Key: envelope.Key}, nil
}

func (c *QoderClient) signer() cosySigner {
	if c.sign != nil {
		return c.sign
	}
	return BuildCosyHeaders
}

func qoderNonce(now func() time.Time) func() string {
	return func() string {
		t := now()
		return fmt.Sprintf("%x%x", t.UnixMilli(), t.UnixNano()&0xfffffffffffff)
	}
}
