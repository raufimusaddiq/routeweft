package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func fusionApp(t *testing.T, upstream *upstreamLog, panel []string, settings string) (*App, string) {
	t.Helper()
	a, key := multiProviderApp(t, upstream, []string{"a", "b", "c", "j"})
	var members []runtime.ComboMember
	for position, ref := range panel {
		provider, model, _ := strings.Cut(ref, "/")
		members = append(members, runtime.ComboMember{ProviderID: provider, ModelID: model, Position: position, Selected: true})
	}
	if _, err := a.runtime.SetCombos(context.Background(), []runtime.Combo{{ID: "f", Name: "panel", Members: members}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"comboStrategies": settings}, nil); err != nil {
		t.Fatal(err)
	}
	return a, key
}

// pinnedFusion is the part of the recorded oracle runs these tests replay
// (compat/parity/generate-combo-routing.mjs runs the oracle's own handleFusionChat
// under a virtual clock).
type pinnedFusion struct {
	Name   string         `json:"name"`
	Panel  []string       `json:"panel"`
	Judge  *string        `json:"judge"`
	Tuning map[string]int `json:"tuning"`
	Body   map[string]any `json:"body"`
	Member map[string]struct {
		Delay  int    `json:"delay"`
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"members"`
	Calls []struct {
		Model string         `json:"model"`
		Panel bool           `json:"panel"`
		Body  map[string]any `json:"body"`
	} `json:"calls"`
	Final struct {
		Status int `json:"status"`
	} `json:"final"`
}

// timeScale stretches the recorded virtual milliseconds into real ones so a loaded
// machine does not reorder two answers the oracle saw 20 ms apart.
const timeScale = 3

func TestFusionMatchesTheOracle(t *testing.T) {
	raw, err := os.ReadFile("../ingress/testdata/combo_routing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Fusion []pinnedFusion `json:"fusion"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Fusion) < 20 {
		t.Fatalf("pin has %d fusion scenarios", len(pin.Fusion))
	}
	for _, scenario := range pin.Fusion {
		if hasKey(scenario.Body, "contents") {
			continue // Gemini needs a Gemini-capable test provider; its text helpers are tested in ingress
		}
		if !hasKey(scenario.Body, "messages") && !hasKey(scenario.Body, "input") {
			continue // Routeweft's Chat parser refuses a body with no messages before any Combo runs
		}
		t.Run(scenario.Name, func(t *testing.T) {
			single := len(scenario.Panel) == 1
			upstream := &upstreamLog{}
			upstream.script = func(model string, call int) (time.Duration, int, string) {
				ref := "p" + model + "/" + model
				if member, ok := scenario.Member[ref]; ok && call == 0 && !single {
					return time.Duration(member.Delay*timeScale) * time.Millisecond, member.Status, member.Body
				}
				return 0, 200, `{"choices":[{"message":{"role":"assistant","content":"FINAL"}}]}`
			}
			entry := map[string]any{"fallbackStrategy": "fusion"}
			if scenario.Judge != nil {
				entry["judgeModel"] = *scenario.Judge
			}
			tuning := map[string]int{}
			for key, value := range scenario.Tuning {
				if strings.HasSuffix(key, "Ms") {
					value *= timeScale
				}
				tuning[key] = value
			}
			if len(tuning) > 0 {
				entry["fusionTuning"] = tuning
			}
			settings, _ := json.Marshal(map[string]any{"panel": entry})
			a, key := fusionApp(t, upstream, scenario.Panel, string(settings))

			body := map[string]any{}
			for k, v := range scenario.Body {
				body[k] = v
			}
			body["model"] = "panel"
			path := "/v1/chat/completions"
			if hasKey(body, "input") {
				path = "/v1/responses"
			}
			encoded, _ := json.Marshal(body)
			w := serveRequest(a.Handler(), key, path, string(encoded))
			if w.Code != scenario.Final.Status {
				t.Fatalf("status %d, oracle %d (body %s)", w.Code, scenario.Final.Status, w.Body)
			}
			if w.Code == http.StatusServiceUnavailable && !strings.Contains(w.Body.String(), "All fusion panel models failed") {
				t.Fatalf("503 without the oracle's message: %s", w.Body)
			}
			upstream.mu.Lock()
			defer upstream.mu.Unlock()
			if len(upstream.calls) != len(scenario.Calls) {
				t.Fatalf("%d upstream calls %v, oracle made %d", len(upstream.calls), callNames(upstream.calls), len(scenario.Calls))
			}
			// Calls from the panel start together, so their arrival order is not an
			// order; a final call (the judge, or the one model that answered) is last.
			for _, want := range scenario.Calls {
				if want.Panel {
					continue
				}
				got := upstream.calls[len(upstream.calls)-1]
				if got.model != modelID(want.Model) {
					t.Fatalf("final call went to %s, oracle %s", got.model, modelID(want.Model))
				}
				compareBodies(t, "final", got.body, want.Body)
			}
			panelSeen := map[string]bool{}
			for _, call := range upstream.calls {
				if call.index == 0 && !single {
					panelSeen[call.model] = true
					for _, want := range scenario.Calls {
						if want.Panel && modelID(want.Model) == call.model {
							compareBodies(t, "panel "+call.model, call.body, want.Body)
						}
					}
				}
			}
			for _, want := range scenario.Calls {
				if want.Panel && !panelSeen[modelID(want.Model)] && !single {
					t.Fatalf("panel model %s was not called", want.Model)
				}
			}
		})
	}
}

func hasKey(object map[string]any, key string) bool { _, ok := object[key]; return ok }

func modelID(ref string) string { _, model, _ := strings.Cut(ref, "/"); return model }

func callNames(calls []upstreamCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		names = append(names, fmt.Sprintf("%s#%d", call.model, call.index))
	}
	return names
}

// compareBodies holds the request the oracle sent to the one Routeweft sent for
// everything the oracle decides: the conversation, and whether tools and
// streaming survive. The model field is the combo name in the oracle and the
// provider's own model id here, and Routeweft's native path may add its own
// defaults, so neither is compared.
func compareBodies(t *testing.T, what, got string, want map[string]any) {
	t.Helper()
	var sent map[string]any
	if err := json.Unmarshal([]byte(got), &sent); err != nil {
		t.Fatalf("%s: upstream body is not JSON: %v", what, err)
	}
	for _, key := range []string{"messages", "input"} {
		if wantValue, ok := want[key]; ok {
			if !reflect.DeepEqual(roundTrip(sent[key]), roundTrip(wantValue)) {
				t.Errorf("%s %s:\n got  %s\n want %s", what, key, mustJSON(sent[key]), mustJSON(wantValue))
			}
		}
	}
	for _, key := range []string{"tools", "tool_choice", "stream_options"} {
		_, wantHas := want[key]
		_, gotHas := sent[key]
		if wantHas != gotHas {
			t.Errorf("%s: %s present=%v, oracle %v", what, key, gotHas, wantHas)
		}
	}
	wantStream, _ := want["stream"].(bool)
	gotStream, _ := sent["stream"].(bool)
	if wantStream != gotStream {
		t.Errorf("%s: stream=%v, oracle %v", what, gotStream, wantStream)
	}
}

func roundTrip(value any) any {
	var out any
	_ = json.Unmarshal([]byte(mustJSON(value)), &out)
	return out
}

func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }

func fusionBody(model string, extra string) string {
	return fmt.Sprintf(`{"model":%q,%s"messages":[{"role":"user","content":"question"}]}`, model, extra)
}

const finalAnswer = `{"choices":[{"message":{"role":"assistant","content":"FINAL"}}]}`

// answers scripts a panel whose models answer after delay and a judge, model j,
// that gives the final answer at once.
func answers(delay time.Duration) func(string, int) (time.Duration, int, string) {
	return func(model string, call int) (time.Duration, int, string) {
		if model == "j" {
			return 0, 200, finalAnswer
		}
		return delay, 200, fmt.Sprintf(`{"choices":[{"message":{"content":"answer from %s"}}]}`, model)
	}
}

// The judge call keeps the client's own streaming; the panel is never streamed
// (combo.js:566-571,621-624).
func TestFusionStreamsTheJudgeAnswer(t *testing.T) {
	upstream := &upstreamLog{}
	upstream.script = func(model string, call int) (time.Duration, int, string) {
		if model != "j" {
			return 0, 200, fmt.Sprintf(`{"choices":[{"message":{"content":"answer from %s"}}]}`, model)
		}
		return 0, 200, "data: {\"choices\":[{\"delta\":{\"content\":\"FINAL\"}}]}\n\ndata: [DONE]\n\n"
	}
	a, key := fusionApp(t, upstream, []string{"pa/a", "pb/b"}, `{"panel":{"fallbackStrategy":"fusion","judgeModel":"pj/j"}}`)
	w := post(a, key, fmusion(`"stream":true,"stream_options":{"include_usage":true},`))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(w.Body.String(), "FINAL") {
		t.Fatalf("status=%d type=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	for _, call := range upstream.calls {
		var sent map[string]any
		_ = json.Unmarshal([]byte(call.body), &sent)
		panelCall := call.model != "j"
		if streamed, _ := sent["stream"].(bool); streamed == panelCall {
			t.Errorf("%s stream=%v: panel calls are not streamed, the judge call is", call.model, streamed)
		}
		if _, has := sent["stream_options"]; has == panelCall {
			t.Errorf("%s stream_options present=%v", call.model, has)
		}
	}
}

func fmusion(extra string) string { return fusionBody("panel", extra) }

// A restricted key that holds the Fusion Combo reaches its members and its judge,
// which it was not granted, and nothing else; one that does not hold it is refused
// (keyAccess.js:92-97; the Combo's grant admits what it routes to).
func TestFusionHonoursTheKeysComboGrant(t *testing.T) {
	upstream := &upstreamLog{script: answers(0)}
	a, _ := fusionApp(t, upstream, []string{"pa/a", "pb/b"}, `{"panel":{"fallbackStrategy":"fusion","judgeModel":"pj/j"}}`)
	ctx := context.Background()
	_, granted, err := a.runtime.CreateAPIKeyWithAccess(ctx, "granted", true, []string{"panel"})
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := a.runtime.CreateAPIKeyWithAccess(ctx, "other", true, []string{"pa/a"})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(a, granted, fmusion("")); w.Code != 200 || !strings.Contains(w.Body.String(), "FINAL") || upstream.seen() != "a b j" && upstream.seen() != "b a j" {
		t.Fatalf("granted key: status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
	upstream.reset()
	if w := post(a, other, fmusion("")); w.Code != 403 || upstream.seen() != "" {
		t.Fatalf("a key without the Combo must be refused before any panel call: status=%d seen=%q", w.Code, upstream.seen())
	}
	// A direct request for a member is still decided by the key's own list.
	upstream.reset()
	if w := post(a, granted, fusionBody("pb/b", "")); w.Code != 403 || upstream.seen() != "" {
		t.Fatalf("the Combo's grant must not open its members to direct use: status=%d", w.Code)
	}
	// And no key at all is no fusion.
	if w := serveRequest(a.Handler(), "wrong", "/v1/chat/completions", fmusion("")); w.Code != 401 || upstream.seen() != "" {
		t.Fatalf("unauthenticated: status=%d seen=%q", w.Code, upstream.seen())
	}
}

// The client leaving ends the panel: every call still waiting at the upstream is
// cancelled, so the gateway does not keep paying for answers nobody will read.
func TestFusionCancelsThePanelWhenTheClientLeaves(t *testing.T) {
	upstream := &upstreamLog{script: answers(5 * time.Second)}
	a, key := fusionApp(t, upstream, []string{"pa/a", "pb/b", "pc/c"}, `{"panel":{"fallbackStrategy":"fusion"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmusion(""))).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+key)
	started := time.Now()
	a.Handler().ServeHTTP(httptest.NewRecorder(), request)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the request outlived its client by %v", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		upstream.mu.Lock()
		cancelled, begun := upstream.cancelled, upstream.started
		upstream.mu.Unlock()
		if begun == 3 && cancelled == 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d panel calls were cancelled at the upstream", cancelled, begun)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A panel call that outlives the grace window is cancelled, not left running.
func TestFusionCancelsStragglers(t *testing.T) {
	upstream := &upstreamLog{}
	upstream.script = func(model string, call int) (time.Duration, int, string) {
		switch {
		case call > 0:
			return 0, 200, finalAnswer
		case model == "c":
			return 5 * time.Second, 200, `{"choices":[{"message":{"content":"late"}}]}`
		}
		return 20 * time.Millisecond, 200, fmt.Sprintf(`{"choices":[{"message":{"content":"answer from %s"}}]}`, model)
	}
	a, key := fusionApp(t, upstream, []string{"pa/a", "pb/b", "pc/c"}, `{"panel":{"fallbackStrategy":"fusion","fusionTuning":{"stragglerGraceMs":100}}}`)
	started := time.Now()
	w := post(a, key, fmusion(""))
	if w.Code != 200 || time.Since(started) > 2*time.Second {
		t.Fatalf("status=%d after %v", w.Code, time.Since(started))
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		upstream.mu.Lock()
		cancelled := upstream.cancelled
		upstream.mu.Unlock()
		if cancelled == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the straggler was not cancelled (%d cancelled)", cancelled)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SPEC §16 caps: fan-out, and Fusion requests in flight.
func TestFusionLimits(t *testing.T) {
	t.Run("too many panel models", func(t *testing.T) {
		upstream := &upstreamLog{script: answers(0)}
		ids := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		a, key := multiProviderApp(t, upstream, ids)
		var members []runtime.ComboMember
		for position, id := range ids {
			members = append(members, runtime.ComboMember{ProviderID: "p" + id, ModelID: id, Position: position, Selected: true})
		}
		if _, err := a.runtime.SetCombos(context.Background(), []runtime.Combo{{ID: "f", Name: "panel", Members: members}}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"comboStrategies": `{"panel":{"fallbackStrategy":"fusion"}}`}, nil); err != nil {
			t.Fatal(err)
		}
		if w := post(a, key, fmusion("")); w.Code != http.StatusUnprocessableEntity || upstream.seen() != "" {
			t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
		}
	})
	t.Run("concurrent requests", func(t *testing.T) {
		upstream := &upstreamLog{script: answers(1500 * time.Millisecond)}
		a, key := fusionApp(t, upstream, []string{"pa/a", "pb/b"}, `{"panel":{"fallbackStrategy":"fusion","judgeModel":"pj/j"}}`)
		const inFlight = 8
		var wg sync.WaitGroup
		for i := 0; i < inFlight; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); post(a, key, fmusion("")) }()
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			upstream.mu.Lock()
			begun := upstream.started
			upstream.mu.Unlock()
			if begun >= inFlight*2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d panel calls started", begun)
			}
			time.Sleep(10 * time.Millisecond)
		}
		w := post(a, key, fmusion(""))
		wg.Wait()
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "fusion_busy") || w.Header().Get("Retry-After") == "" {
			t.Fatalf("the ninth request: status=%d body=%s", w.Code, w.Body)
		}
	})
}

// A Fusion Combo may be a panel member of another: the inner one fuses its own
// panel and answers as one model (chat.js:196-213 reaches handleFusionChat from
// handleSingleModelChat).
func TestFusionNestsInsideFusion(t *testing.T) {
	upstream := &upstreamLog{}
	upstream.script = func(model string, call int) (time.Duration, int, string) {
		if call == 0 && model != "j" {
			return 0, 200, fmt.Sprintf(`{"choices":[{"message":{"content":"answer from %s"}}]}`, model)
		}
		return 0, 200, finalAnswer
	}
	a, key := multiProviderApp(t, upstream, []string{"a", "b", "c", "j"})
	ctx := context.Background()
	if _, err := a.runtime.SetCombos(ctx, []runtime.Combo{
		{ID: "o", Name: "outer", Members: []runtime.ComboMember{{ModelID: "inner", Position: 0, Selected: true}, {ProviderID: "pc", ModelID: "c", Position: 1, Selected: true}}},
		{ID: "i", Name: "inner", Members: []runtime.ComboMember{{ProviderID: "pa", ModelID: "a", Position: 0, Selected: true}, {ProviderID: "pb", ModelID: "b", Position: 1, Selected: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"comboStrategies": `{"outer":{"fallbackStrategy":"fusion","judgeModel":"pj/j"},"inner":{"fallbackStrategy":"fusion","judgeModel":"pj/j"}}`}, nil); err != nil {
		t.Fatal(err)
	}
	w := post(a, key, fusionBody("outer", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "FINAL") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	// inner: panel a, b, then its judge j; outer: panel (inner, c), then its judge j.
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	counts := map[string]int{}
	for _, call := range upstream.calls {
		counts[call.model]++
	}
	if counts["a"] != 1 || counts["b"] != 1 || counts["c"] != 1 || counts["j"] != 2 {
		t.Fatalf("calls %v", callNames(upstream.calls))
	}
}

// A Fusion Combo that is a member of an ordered Combo is not served from inside the
// attempt loop; it answers 501 rather than being treated as an ordered Combo.
func TestFusionNestedInAnOrderedComboIsRefusedExplicitly(t *testing.T) {
	upstream := &upstreamLog{script: answers(0)}
	a, key := multiProviderApp(t, upstream, []string{"a", "b", "c"})
	ctx := context.Background()
	if _, err := a.runtime.SetCombos(ctx, []runtime.Combo{
		{ID: "o", Name: "ordered", Members: []runtime.ComboMember{{ModelID: "panel", Position: 0, Selected: true}}},
		{ID: "p", Name: "panel", Members: []runtime.ComboMember{{ProviderID: "pa", ModelID: "a", Position: 0, Selected: true}, {ProviderID: "pb", ModelID: "b", Position: 1, Selected: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"comboStrategies": `{"panel":{"fallbackStrategy":"fusion"}}`}, nil); err != nil {
		t.Fatal(err)
	}
	if w := post(a, key, fusionBody("ordered", "")); w.Code != http.StatusNotImplemented || upstream.seen() != "" {
		t.Fatalf("status=%d seen=%q body=%s", w.Code, upstream.seen(), w.Body)
	}
}

// Turning the strategy back off returns the Combo to ordered fallback; the stored
// Combo was never changed by the setting.
func TestFusionSettingDoesNotRewriteTheStoredCombo(t *testing.T) {
	upstream := &upstreamLog{script: answers(0)}
	a, key := fusionApp(t, upstream, []string{"pa/a", "pb/b"}, `{"panel":{"fallbackStrategy":"fusion"}}`)
	ctx := context.Background()
	// An unrelated configuration change writes every Combo back; it must not
	// store the fusion overlay.
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"comboStickyRoundRobinLimit": "3"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"comboStrategies": `{}`}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := a.runtime.Load()
	if combo, _ := snapshot.ComboByName("panel"); combo.FusionEnabled {
		t.Fatal("the setting was written into the stored Combo")
	}
	if w := post(a, key, fmusion("")); w.Code != 200 || upstream.seen() != "a" {
		t.Fatalf("an ordinary Combo again: status=%d seen=%q", w.Code, upstream.seen())
	}
}

// Fusion is also reached through the Combo's own stored flag (the admin Combos API),
// and the Responses and Messages entry points go through the same gate.
func TestFusionOnEveryProtocol(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"panel","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"panel","input":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"panel","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			upstream := &upstreamLog{script: answers(0)}
			a, key := multiProviderApp(t, upstream, []string{"a", "b", "j"})
			if _, err := a.runtime.SetCombos(context.Background(), []runtime.Combo{{ID: "f", Name: "panel", FusionEnabled: true, JudgeModel: "pj/j", Members: []runtime.ComboMember{{ProviderID: "pa", ModelID: "a", Position: 0, Selected: true}, {ProviderID: "pb", ModelID: "b", Position: 1, Selected: true}}}}); err != nil {
				t.Fatal(err)
			}
			w := serveRequest(a.Handler(), key, tc.path, tc.body)
			upstream.mu.Lock()
			defer upstream.mu.Unlock()
			last := upstream.calls[len(upstream.calls)-1]
			if w.Code != 200 || len(upstream.calls) != 3 || last.model != "j" {
				t.Fatalf("status=%d calls=%v body=%s", w.Code, callNames(upstream.calls), w.Body)
			}
		})
	}
}
