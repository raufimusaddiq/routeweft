package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// upstreamLog is an injected upstream that records the model of every request
// and answers per model: a status for a model that should fail, 200 otherwise.
type upstreamLog struct {
	mu     sync.Mutex
	models []string
	bodies map[string][]string // model -> raw request bodies, in order
	fail   map[string]int
	// failBody, when set for a model, is the error body its failure carries.
	failBody map[string]string
}

func (u *upstreamLog) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.models = append(u.models, body.Model)
		if u.bodies == nil {
			u.bodies = map[string][]string{}
		}
		u.bodies[body.Model] = append(u.bodies[body.Model], string(raw))
		status := u.fail[body.Model]
		failBody := u.failBody[body.Model]
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			if failBody == "" {
				failBody = `{"error":{"message":"scripted failure"}}`
			}
			fmt.Fprint(w, failBody)
			return
		}
		fmt.Fprintf(w, `{"served_by":%q}`, body.Model)
	})
}

func (u *upstreamLog) seen() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.models, " ")
}

func (u *upstreamLog) reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.models = nil
}

const (
	textRequest  = `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`
	imageRequest = `{"model":%q,"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`
)

func comboServingApp(t *testing.T, upstream *upstreamLog) (*App, string) {
	t.Helper()
	server := httptest.NewServer(upstream.handler())
	t.Cleanup(server.Close)
	a, key := servingApp(t, server.URL+"/v1", 1)
	ctx := context.Background()
	for _, model := range []runtime.Model{
		{ProviderID: "test", ID: "text-a"},
		{ProviderID: "test", ID: "text-b"},
		{ProviderID: "test", ID: "sees", Capabilities: []string{"vision"}},
		{ProviderID: "test", ID: "also-sees", Capabilities: []string{"vision"}},
	} {
		if err := a.runtime.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	return a, key
}

// A failed attempt cools its account for the provider; the tests share one
// account, so a follow-up request starts from a clean slate.
func clearCooldowns(a *App) { a.runtime.State().ClearCooldown("account-00") }

func setCombos(t *testing.T, a *App, combos ...runtime.Combo) {
	t.Helper()
	if _, err := a.runtime.SetCombos(context.Background(), combos); err != nil {
		t.Fatal(err)
	}
}

func members(models ...string) []runtime.ComboMember {
	out := make([]runtime.ComboMember, 0, len(models))
	for index, model := range models {
		out = append(out, runtime.ComboMember{ProviderID: "test", ModelID: model, Position: index, Selected: true})
	}
	return out
}

func post(a *App, key, body string) *httptest.ResponseRecorder {
	return serveRequest(a.Handler(), key, "/v1/chat/completions", body)
}

// A named Combo is served as its ordered members: a fallback-classified failure
// moves to the next member, a success stops, and the client sees the answer of
// the member that served (combo.js:300-361).
func TestComboServingFallsThroughMembers(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"text-a": 503}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	w := post(a, key, fmt.Sprintf(textRequest, "duo"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"served_by":"text-b"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if got := upstream.seen(); got != "text-a text-b" {
		t.Fatalf("upstream order %q", got)
	}
}

// A terminal (non-fallback) failure of the first member is the answer; the next
// member is not tried (combo.js:337-340).
func TestComboServingStopsOnTerminalFailure(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"text-a": 400}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	w := post(a, key, fmt.Sprintf(textRequest, "duo"))
	if w.Code != 400 || upstream.seen() != "text-a" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
}

// When every member fails the client gets an error rather than a hang or a 200.
func TestComboServingAllMembersFail(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"text-a": 503, "text-b": 503}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	w := post(a, key, fmt.Sprintf(textRequest, "duo"))
	if w.Code < 500 || upstream.seen() != "text-a text-b" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
}

// Round-robin rotates the starting member per request and sticky holds it for
// the configured number of requests (combo.js:208-237).
func TestComboServingRotatesAndSticks(t *testing.T) {
	upstream := &upstreamLog{}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a,
		runtime.Combo{ID: "r", Name: "rotating", Strategy: "round-robin", Members: members("text-a", "text-b")},
		runtime.Combo{ID: "s", Name: "sticky", Strategy: "round-robin", StickyLimit: 2, Members: members("text-a", "text-b")})
	for i := 0; i < 4; i++ {
		post(a, key, fmt.Sprintf(textRequest, "rotating"))
	}
	if got := upstream.seen(); got != "text-a text-b text-a text-b" {
		t.Fatalf("round-robin order %q", got)
	}
	upstream.reset()
	for i := 0; i < 4; i++ {
		post(a, key, fmt.Sprintf(textRequest, "sticky"))
	}
	if got := upstream.seen(); got != "text-a text-a text-b text-b" {
		t.Fatalf("sticky order %q", got)
	}
}

// Capability-aware auto-switch: a request carrying an image floats a
// vision-capable member to the front; a text request keeps the order.
func TestComboServingAutoSwitchesForTheRequest(t *testing.T) {
	upstream := &upstreamLog{}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "mixed", Members: members("text-a", "sees")})
	post(a, key, fmt.Sprintf(imageRequest, "mixed"))
	if got := upstream.seen(); got != "sees" {
		t.Fatalf("image request tried %q", got)
	}
	upstream.reset()
	post(a, key, fmt.Sprintf(textRequest, "mixed"))
	if got := upstream.seen(); got != "text-a" {
		t.Fatalf("text request tried %q", got)
	}
}

// An image in an older turn does not pin the Combo to a vision model (combo.js:90-100).
func TestComboServingIgnoresMediaInHistory(t *testing.T) {
	upstream := &upstreamLog{}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "mixed", Members: members("text-a", "sees")})
	body := `{"model":"mixed","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"assistant","content":"seen"},{"role":"user","content":"and now?"}]}`
	post(a, key, body)
	if got := upstream.seen(); got != "text-a" {
		t.Fatalf("history media pinned the combo to %q", got)
	}
}

// Capacity adapter: when no member can take the request a pool model is tried
// first; the Combo's own members remain as fallback.
func TestComboServingCapacityAdapterRescuesCombo(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"also-sees": 503}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "texty", Members: members("text-a", "text-b")})
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{
		"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"test","modelId":"also-sees"},{"providerId":"test","modelId":"sees"}]}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	w := post(a, key, fmt.Sprintf(imageRequest, "texty"))
	if w.Code != 200 || upstream.seen() != "also-sees sees" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
	// A text request needs no rescue.
	upstream.reset()
	clearCooldowns(a)
	post(a, key, fmt.Sprintf(textRequest, "texty"))
	if got := upstream.seen(); got != "text-a" {
		t.Fatalf("text request tried %q", got)
	}
}

// A single model that lacks a capability the request needs is rescued by the
// adapter pool too, and the requested model stays as the last fallback
// (chat.js:152-169).
func TestComboServingCapacityAdapterRescuesSingleModel(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"sees": 503}}
	a, key := comboServingApp(t, upstream)
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{
		"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"test","modelId":"sees"}]}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	w := post(a, key, fmt.Sprintf(imageRequest, "test/text-a"))
	if w.Code != 200 || upstream.seen() != "sees text-a" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
	upstream.reset()
	clearCooldowns(a)
	post(a, key, fmt.Sprintf(textRequest, "test/text-a"))
	if got := upstream.seen(); got != "text-a" {
		t.Fatalf("text request tried %q", got)
	}
}

// An enabled empty pool and a disabled pool are no-ops.
func TestComboServingEmptyAndDisabledPoolsAreNoOps(t *testing.T) {
	for name, pool := range map[string]string{
		"empty":    `{"enabled":true,"roundRobin":false,"pool":[]}`,
		"disabled": `{"enabled":false,"roundRobin":false,"pool":[{"providerId":"test","modelId":"sees"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &upstreamLog{}
			a, key := comboServingApp(t, upstream)
			if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"capacityAdapterVision": pool}, nil); err != nil {
				t.Fatal(err)
			}
			post(a, key, fmt.Sprintf(imageRequest, "test/text-a"))
			if got := upstream.seen(); got != "text-a" {
				t.Fatalf("tried %q", got)
			}
		})
	}
}

// A restricted key is never routed to an adapter pool model outside its allowlist,
// and the Combo it holds keeps working (keyAccess.js:162-177).
func TestComboServingRestrictedKeyNeverReachesAnUnlistedPoolModel(t *testing.T) {
	upstream := &upstreamLog{}
	a, _ := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "texty", Members: members("text-a")})
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{
		"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"test","modelId":"sees"},{"providerId":"test","modelId":"also-sees"}]}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, noPool, err := a.runtime.CreateAPIKeyWithAccess(ctx, "combo-only", true, []string{"texty"})
	if err != nil {
		t.Fatal(err)
	}
	_, onePool, err := a.runtime.CreateAPIKeyWithAccess(ctx, "combo-and-one", true, []string{"texty", "test/also-sees"})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(a, noPool, fmt.Sprintf(imageRequest, "texty")); w.Code != 200 || upstream.seen() != "text-a" {
		t.Fatalf("key with no pool grant: status=%d seen=%q", w.Code, upstream.seen())
	}
	upstream.reset()
	if w := post(a, onePool, fmt.Sprintf(imageRequest, "texty")); w.Code != 200 || upstream.seen() != "also-sees" {
		t.Fatalf("key with one pool grant: status=%d seen=%q", w.Code, upstream.seen())
	}
	// A single model reached by a restricted key is rescued only by granted pool models.
	upstream.reset()
	_, direct, err := a.runtime.CreateAPIKeyWithAccess(ctx, "direct", true, []string{"test/text-a"})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(a, direct, fmt.Sprintf(imageRequest, "test/text-a")); w.Code != 200 || upstream.seen() != "text-a" {
		t.Fatalf("direct key: status=%d seen=%q", w.Code, upstream.seen())
	}
	// And a Combo it was not granted is refused before anything is planned.
	if w := post(a, direct, fmt.Sprintf(textRequest, "texty")); w.Code != 403 {
		t.Fatalf("ungranted combo: status=%d", w.Code)
	}
}

// A Combo whose strategy is fusion is not silently served as a fallback list.
func TestComboServingFusionIsNotServedAsFallback(t *testing.T) {
	upstream := &upstreamLog{}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "f", Name: "panel", FusionEnabled: true, Members: members("text-a", "text-b")})
	w := post(a, key, fmt.Sprintf(textRequest, "panel"))
	if w.Code != http.StatusNotImplemented || upstream.seen() != "" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
}

// Every protocol entry point dispatches the Combo.
func TestComboServingEveryProtocol(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"text-a": 503}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"duo","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"duo","input":"hi"}`},
		{"/v1/messages", `{"model":"duo","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		upstream.reset()
		clearCooldowns(a)
		w := serveRequest(a.Handler(), key, tc.path, tc.body)
		if w.Code != 200 || upstream.seen() != "text-a text-b" {
			t.Fatalf("%s: status=%d seen=%q body=%s", tc.path, w.Code, upstream.seen(), w.Body)
		}
	}
}

// messageCount is how many messages the upstream received for a model's nth call.
func (u *upstreamLog) messageCount(t *testing.T, model string, call int) int {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if call >= len(u.bodies[model]) {
		t.Fatalf("no call %d to %s (have %d)", call, model, len(u.bodies[model]))
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
		Input    []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(u.bodies[model][call]), &body); err != nil {
		t.Fatal(err)
	}
	return len(body.Messages) + len(body.Input)
}

func longHistory(turns int, last string) string {
	var messages []string
	for i := 0; i < turns; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, fmt.Sprintf(`{"role":%q,"content":"turn %d"}`, role, i))
	}
	messages = append(messages, last)
	return `{"model":"%s","messages":[` + strings.Join(messages, ",") + `]}`
}

// A model the gateway added has its history fitted to its context window; the
// Combo's own members, called later as fallback, get the whole history
// (capacityAdapter.js:98-169,154-168).
func TestComboServingFitsHistoryOnlyForAdapterModels(t *testing.T) {
	upstream := &upstreamLog{fail: map[string]int{"also-sees": 503}}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "texty", Members: members("text-a")})
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{
		"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"test","modelId":"also-sees"}]}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	image := `{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`
	// Ten older turns, an image on the current one: six older turns are kept (the
	// oracle's HEAD_KEEP), the other four dropped, then the current turn.
	w := post(a, key, fmt.Sprintf(longHistory(10, image), "texty"))
	if w.Code != 200 || upstream.seen() != "also-sees text-a" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
	if got := upstream.messageCount(t, "also-sees", 0); got != 7 {
		t.Fatalf("adapter model received %d messages, want 7 (six older turns and the current one)", got)
	}
	if got := upstream.messageCount(t, "text-a", 0); got != 11 {
		t.Fatalf("the Combo's own member received %d messages, want all 11", got)
	}
	// Rescuing a single model fits history the same way, on every protocol that
	// carries messages.
	upstream.reset()
	clearCooldowns(a)
	upstream.mu.Lock()
	upstream.fail = map[string]int{}
	upstream.bodies = nil
	upstream.mu.Unlock()
	if w := post(a, key, fmt.Sprintf(longHistory(10, image), "test/text-a")); w.Code != 200 || upstream.seen() != "also-sees" {
		t.Fatalf("single model: status=%d seen=%q", w.Code, upstream.seen())
	}
	if got := upstream.messageCount(t, "also-sees", 0); got != 7 {
		t.Fatalf("single model rescue received %d messages, want 7", got)
	}
}

// When every member fails the answer is the first failed member's status and the
// last failure's message, not the last upstream body (combo.js:363-381).
func TestComboServingAnswersAnExhaustedComboLikeTheOracle(t *testing.T) {
	upstream := &upstreamLog{
		fail:     map[string]int{"text-a": 502, "text-b": 500},
		failBody: map[string]string{"text-a": `{"error":{"message":"bad gateway"}}`, "text-b": `{"error":"boom"}`},
	}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	w := post(a, key, fmt.Sprintf(textRequest, "duo"))
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	if w.Code != 502 || body.Error.Message != "boom" || upstream.seen() != "text-a text-b" {
		t.Fatalf("status=%d message=%q seen=%q body=%s", w.Code, body.Error.Message, upstream.seen(), w.Body)
	}
}

// A message that names a transient condition on a 502/503/504 is waited out
// before the Combo falls through, within five seconds (combo.js:342-349).
func TestComboServingWaitsOutATransientFailureBeforeFallingThrough(t *testing.T) {
	upstream := &upstreamLog{
		fail:     map[string]int{"text-a": 503},
		failBody: map[string]string{"text-a": `{"error":{"message":"The model is overloaded"}}`},
	}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	started := time.Now()
	w := post(a, key, fmt.Sprintf(textRequest, "duo"))
	elapsed := time.Since(started)
	if w.Code != 200 || upstream.seen() != "text-a text-b" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
	if elapsed < 1900*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("waited %v, want about 2s", elapsed)
	}
	// A bare 503 is not waited on.
	upstream.mu.Lock()
	upstream.failBody = nil
	upstream.mu.Unlock()
	upstream.reset()
	clearCooldowns(a)
	started = time.Now()
	post(a, key, fmt.Sprintf(textRequest, "duo"))
	if elapsed = time.Since(started); elapsed > time.Second {
		t.Fatalf("a bare 503 waited %v", elapsed)
	}
}

// The client leaving during the wait ends the request.
func TestComboServingTransientWaitStopsWhenTheClientLeaves(t *testing.T) {
	upstream := &upstreamLog{
		fail:     map[string]int{"text-a": 503},
		failBody: map[string]string{"text-a": `{"error":{"message":"overloaded"}}`},
	}
	a, key := comboServingApp(t, upstream)
	setCombos(t, a, runtime.Combo{ID: "c", Name: "duo", Members: members("text-a", "text-b")})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(textRequest, "duo"))).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+key)
	started := time.Now()
	a.Handler().ServeHTTP(httptest.NewRecorder(), request)
	if elapsed := time.Since(started); elapsed > time.Second || upstream.seen() != "text-a" {
		t.Fatalf("request lasted %v and tried %q; the wait must end with the client", elapsed, upstream.seen())
	}
}

// Every protocol that carries a history fits it for an adapter model: the handler
// swaps the request it parsed for the fitted one for that model's attempt.
func TestComboServingFitsHistoryOnEveryProtocol(t *testing.T) {
	older := func(key string) string {
		var turns []string
		for i := 0; i < 10; i++ {
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			turns = append(turns, fmt.Sprintf(`{"role":%q,"content":"turn %d"}`, role, i))
		}
		return strings.Join(turns, ",")
	}
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"texty","messages":[` + older("messages") + `,{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`},
		{"/v1/responses", `{"model":"texty","input":[` + older("input") + `,{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`},
		{"/v1/messages", `{"model":"texty","max_tokens":8,"messages":[` + older("messages") + `,{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			upstream := &upstreamLog{}
			a, key := comboServingApp(t, upstream)
			setCombos(t, a, runtime.Combo{ID: "c", Name: "texty", Members: members("text-a")})
			if _, err := a.runtime.SetSettings(context.Background(), map[string]string{
				"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"test","modelId":"also-sees"}]}`,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if w := serveRequest(a.Handler(), key, tc.path, tc.body); w.Code != 200 || upstream.seen() != "also-sees" {
				t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
			}
			if got := upstream.messageCount(t, "also-sees", 0); got != 7 {
				t.Fatalf("adapter model received %d history items, want 7", got)
			}
		})
	}
}
