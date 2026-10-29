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

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// upstreamLog is an injected upstream that records the model of every request
// and answers per model: a status for a model that should fail, 200 otherwise.
type upstreamLog struct {
	mu     sync.Mutex
	models []string
	fail   map[string]int
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
		status := u.fail[body.Model]
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"scripted failure"}}`)
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
