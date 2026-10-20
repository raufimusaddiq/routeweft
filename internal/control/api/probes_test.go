package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestProbeWireVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, typ, body string
		ok, invalid               bool
	}{
		{"chat", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\r\n\r\n", true, false},
		{"reasoning", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\r\r", true, false},
		{"multi-data", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\r\ndata: \"OK\"}}]}\r\n\r\n", true, false},
		{"tool", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\n", true, false},
		{"length", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n", true, false},
		{"trailing", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}", true, false},
		{"empty", "openai-chat", "text/event-stream", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", false, false},
		{"done", "openai-chat", "text/event-stream", "data: [DONE]\n\n", false, false},
		{"error", "openai-chat", "text/event-stream", "data: {\"error\":{\"message\":\"secret completion\"}}\n\n", false, true},
		{"malformed", "openai-chat", "text/event-stream", "data: {bad}\n\n", false, true},
		{"json", "openai-chat", "application/json", `{"choices":[{"message":{"content":"OK"}}]}`, true, false},
		{"json-empty", "openai-chat", "application/json", `{}`, false, false},
		{"json-invalid", "openai-chat", "application/json", `null`, false, true},
		{"responses", "openai-responses", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n", true, false},
		{"responses-tool", "openai-responses", "text/event-stream", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\"}}\n\n", true, false},
		{"responses-empty", "openai-responses", "text/event-stream", "data: {\"type\":\"response.completed\"}\n\n", false, false},
		{"responses-failed", "openai-responses", "text/event-stream", "data: {\"type\":\"response.failed\"}\n\n", false, true},
		{"responses-metadata", "openai-responses", "text/event-stream", "data: {\"type\":\"response.metadata.delta\",\"delta\":\"not output\"}\n\ndata: [DONE]\n\n", false, false},
		{"responses-terminal-output", "openai-responses", "text/event-stream", "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"text\":\"OK\"}]}]}}\n\n", true, false},
		{"responses-json", "openai-responses", "application/json", `{"output":[{"content":[{"text":"OK"}]}]}`, true, false},
		{"responses-json-failed", "openai-responses", "application/json", `{"status":"failed","output":[{"content":[{"text":"partial"}]}]}`, false, true},
		{"responses-json-length", "openai-responses", "application/json", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, true, false},
		{"messages", "anthropic-messages", "text/event-stream", "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"OK\"}}\n\n", true, false},
		{"messages-tool", "anthropic-messages", "text/event-stream", "data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\"}}\n\n", true, false},
		{"messages-empty", "anthropic-messages", "text/event-stream", "data: {\"type\":\"message_stop\"}\n\n", false, false},
		{"messages-json", "anthropic-messages", "application/json", `{"content":[{"type":"text","text":"OK"}]}`, true, false},
		{"messages-json-length", "anthropic-messages", "application/json", `{"content":[],"stop_reason":"max_tokens"}`, true, false},
		{"gemini", "gemini", "text/event-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\n", true, false},
		{"gemini-json-array", "gemini", "application/json", `[{"candidates":[{"content":{"parts":[]}}]},{"candidates":[{"content":{"parts":[{"text":"OK"}]}}]}]`, true, false},
		{"gemini-empty", "gemini", "text/event-stream", "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n", false, false},
		{"ollama", "ollama", "application/x-ndjson", "{\"message\":{\"content\":\"\"}}\n{\"message\":{\"thinking\":\"thinking\"},\"done\":false}\n", true, false},
		{"ollama-empty", "ollama", "application/x-ndjson", "{\"done\":true}\n", false, false},
		{"systemone", "systemone", "text/event-stream", "data: {\"answers\":{\"probe\":false}}\n\n", true, false},
		{"cloudflare", "systemone", "text/event-stream", "data: {\"result\":{\"answers\":{\"probe\":{\"noul\":0.9}}}}\n\n", true, false},
		{"systemone-json", "systemone", "application/json", `{"answers":{"probe":true}}`, true, false},
		{"oversize", "openai-chat", "application/json", strings.Repeat(" ", maxProbeBytes+1), false, true},
		{"oversize-stream", "openai-chat", "text/event-stream", "data: " + strings.Repeat(" ", maxProbeBytes+1), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(tc.body)
			if tc.name == "chat" || tc.name == "reasoning" || tc.name == "multi-data" {
				reader = iotest.OneByteReader(reader)
			}
			response := &http.Response{Header: http.Header{"Content-Type": []string{tc.typ}}, Body: io.NopCloser(reader)}
			var served string
			ok, err := readProbe(response, tc.protocol, &served)
			if ok != tc.ok || (err != nil) != tc.invalid {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestProbeBudgetContract(t *testing.T) {
	if modelProbeTimeout != 200*time.Second || modelBatchTimeout != 10*time.Minute || modelProbeTokens != 32 {
		t.Fatalf("probe budgets: per-model=%s aggregate=%s tokens=%d", modelProbeTimeout, modelBatchTimeout, modelProbeTokens)
	}
	body, err := json.Marshal(modelTestResult{ModelID: "unreached", Classification: "failed"})
	if err != nil || strings.Contains(string(body), "latencyMs") {
		t.Fatalf("unreached failure latency: %s %v", body, err)
	}
}

type probeHTTPFunc func(*http.Request) (*http.Response, error)

func (f probeHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestProbeBudgetAndConnectionReuse(t *testing.T) {
	var connections atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.Start()
	defer upstream.Close()
	h := New(Options{AllowPrivateUpstreams: true})
	discoveryClient := h.opts.DiscoveryClient.HTTP.(*http.Client)
	discoveryTransport := discoveryClient.Transport.(*http.Transport)
	discoveryClient.Timeout = time.Millisecond
	discoveryTransport.ResponseHeaderTimeout = time.Millisecond
	for i := 0; i < 2; i++ {
		result := h.testProviderModel(context.Background(), credentials.Connection{AuthKind: credentials.AuthNone}, credentials.Node{BaseURL: upstream.URL}, registry.Spec{}, "openai-chat", "manual", time.Second)
		if !result.OK || discoveryClient.Timeout != time.Millisecond || discoveryTransport.ResponseHeaderTimeout != time.Millisecond {
			t.Fatalf("probe inherited or mutated discovery limits: %+v", result)
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("probe transport did not reuse the connection: %d", connections.Load())
	}
}

func TestProbeIndependentBudgetsAndFailureEnvelopes(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "manual", "http://127.0.0.1:1234", "synthetic")
	var deadlines []time.Time
	h.opts.DiscoveryClient = &discovery.Client{HTTP: probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) < 199*time.Second {
			t.Error("model did not get its own 200-second budget")
		}
		deadlines = append(deadlines, deadline)
		if len(deadlines) == 1 {
			time.Sleep(20 * time.Millisecond)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n"))}, nil
	})}
	w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test-models", `{"modelIds":["first","second"]}`)
	if w.Code != 200 || len(deadlines) != 2 || deadlines[1].Sub(deadlines[0]) < 20*time.Millisecond {
		t.Fatalf("shared model budget: %d %s %v", w.Code, w.Body, deadlines)
	}
	workflowAccount(t, creds, "manual", "http://127.0.0.1:1234", "second-account")
	deadlines = nil
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/providers/test-batch", `{"mode":"all","modelId":"manual"}`)
	if w.Code != 200 || len(deadlines) != 2 || deadlines[1].Sub(deadlines[0]) < 20*time.Millisecond || !strings.Contains(w.Body.String(), `"passed":2`) {
		t.Fatalf("shared batch budget: %d %s %v", w.Code, w.Body, deadlines)
	}
	for _, failure := range []struct {
		err            error
		status         int
		classification string
		code           string
	}{
		{context.DeadlineExceeded, 504, "slow", "connection_test_timeout"},
		{errors.New("synthetic credential-bearing network failure"), 502, "failed", "connection_test_failed"},
	} {
		h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == "GET" {
				return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			return nil, failure.err
		})
		w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test", `{"modelId":"manual"}`)
		if w.Code != failure.status || !strings.Contains(w.Body.String(), `"code":"`+failure.code+`"`) || !strings.Contains(w.Body.String(), `"classification":"`+failure.classification+`"`) || strings.Contains(w.Body.String(), "credential-bearing") {
			t.Fatalf("failure envelope: %d %s", w.Code, w.Body)
		}
	}
	for _, workflow := range []struct{ path, body string }{
		{"/admin/v1/connections/" + c.ID + "/test-models", `{"modelIds":["completed","interrupted","never-started"]}`},
		{"/admin/v1/providers/test-batch", `{"mode":"all","modelId":"manual"}`},
	} {
		for _, stop := range []string{"cancel", "deadline"} {
			ctx, cancel := context.WithCancel(context.Background())
			if stop == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			calls := 0
			h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"OK"}}]}`))}, nil
				}
				if stop == "cancel" {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			request := httptest.NewRequest("POST", workflow.path, strings.NewReader(workflow.body)).WithContext(ctx)
			request.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, request)
			cancel()
			var partial struct {
				Results []modelTestResult `json:"results"`
			}
			want := 408
			if stop == "deadline" {
				want = 504
			}
			if w.Code != want || json.Unmarshal(w.Body.Bytes(), &partial) != nil || len(partial.Results) != 2 || !partial.Results[0].OK || partial.Results[1].OK || partial.Results[1].Classification != "canceled" || partial.Results[1].Status != 408 || calls != 2 {
				t.Fatalf("lost partial %s results: %d %s calls=%d", stop, w.Code, w.Body, calls)
			}
		}
	}
	for _, body := range []string{
		"data: {\"model\":\"not-served\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: {\"model\":\"not-served\",\"error\":{\"message\":\"secret\"}}\n\n",
		"data: {\"model\":\"not-served\",\"choices\":[{\"delta\":{}}]}\n\ndata: {\"error\":{\"message\":\"secret\"}}\n\n",
	} {
		h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		r := h.testProviderModel(context.Background(), c, credentials.Node{ProviderID: "manual", BaseURL: "http://127.0.0.1:1234"}, registry.Spec{}, "openai-chat", "manual", time.Second)
		if r.OK || r.Status != 502 || r.UpstreamStatus != 200 || r.ServedModelID != "" || strings.Contains(r.Error, "secret") {
			t.Fatalf("error metadata claimed successful serving: %+v", r)
		}
		w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test", `{"modelId":"manual"}`)
		var failure struct {
			Error struct {
				UpstreamStatus int `json:"upstreamStatus"`
			} `json:"error"`
			Result modelTestResult `json:"result"`
		}
		if w.Code != 502 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.UpstreamStatus != 200 || failure.Result.Status != 502 || failure.Result.ServedModelID != "" {
			t.Fatalf("legacy upstream status was replaced by the probe verdict: %d %s", w.Code, w.Body)
		}
	}
}

func waitProbeSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not observe cancellation")
	}
}

func TestProbeFirstTokenCancelsEveryNativeTransport(t *testing.T) {
	for _, tc := range []struct{ name, protocol, path, typ, token string }{
		{"chat", "openai-chat", "/chat/completions", "text/event-stream", "data: {\"model\":\"served-version\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\r\n\r\n"},
		{"chat-json-mime", "openai-chat", "/chat/completions", "application/json", "data: {\"model\":\"served-version\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n"},
		{"chat-text-mime", "openai-chat", "/chat/completions", "text/plain", "data: {\"model\":\"served-version\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n"},
		{"responses", "openai-responses", "/responses", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"},
		{"messages", "anthropic-messages", "/messages", "text/event-stream", "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"OK\"}}\n\n"},
		{"gemini", "gemini", "/models/manual:7b:streamGenerateContent", "text/event-stream", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\n"},
		{"ollama", "ollama", "/api/chat", "application/x-ndjson", "{\"message\":{\"content\":\"OK\"},\"done\":false}\n"},
		{"systemone", "systemone", "/", "text/event-stream", "data: {\"answers\":{\"probe\":true}}\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canceled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(canceled)
				if r.URL.Path != tc.path || tc.protocol == "gemini" && r.URL.Query().Get("alt") != "sse" {
					t.Errorf("target=%s", r.URL)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if tc.protocol != "gemini" && body["stream"] != true {
					t.Error("probe did not request streaming")
				}
				w.Header().Set("Content-Type", tc.typ)
				// Split a CRLF boundary across writes, without ending generation.
				for _, part := range []string{tc.token[:len(tc.token)-3], tc.token[len(tc.token)-3:]} {
					io.WriteString(w, part)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			h := New(Options{AllowPrivateUpstreams: true, DiscoveryClient: &discovery.Client{HTTP: upstream.Client()}})
			c := credentials.Connection{Enabled: true, AuthKind: credentials.AuthNone}
			node := credentials.Node{Kind: credentials.NodeGeneric, ProviderID: "synthetic"}
			result := h.testProviderModel(context.Background(), c, withBase(node, upstream.URL), registry.Spec{}, tc.protocol, "manual:7b", time.Second)
			if !result.OK || result.Classification != "reachable" || result.ModelID != "manual:7b" || result.UpstreamModelID != "manual:7b" {
				t.Fatalf("first token: %+v", result)
			}
			if tc.protocol == "openai-chat" && result.ServedModelID != "served-version" {
				t.Fatalf("lost provider attribution: %+v", result)
			}
			waitProbeSignal(t, canceled)
		})
	}
}

func TestProbeClientDisconnectAbortsUpstream(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	_, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "manual", upstream.URL, "synthetic")
	admin := httptest.NewServer(mux)
	defer admin.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", admin.URL+"/admin/v1/connections/"+c.ID+"/test-models", strings.NewReader(`{"modelIds":["manual:7b"]}`))
	request.AddCookie(cookie)
	done := make(chan error, 1)
	go func() {
		response, err := admin.Client().Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	waitProbeSignal(t, started)
	cancel()
	waitProbeSignal(t, canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("client disconnect: %v", err)
	}
}

func TestProbeSlowVersusFailedAndCanceled(t *testing.T) {
	for _, mode := range []string{"headers", "body", "hard-failure", "upstream-504", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			aborted := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if mode == "hard-failure" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				}
				if mode == "upstream-504" {
					w.WriteHeader(504)
					return
				}
				if mode == "body" {
					w.Header().Set("Content-Type", "text/event-stream")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(aborted)
			}))
			defer upstream.Close()
			h := New(Options{AllowPrivateUpstreams: true})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			result := h.testProviderModel(ctx, credentials.Connection{AuthKind: credentials.AuthNone}, credentials.Node{BaseURL: upstream.URL}, registry.Spec{}, "openai-chat", "manual", 100*time.Millisecond)
			wantClass, wantStatus := "slow", 504
			if mode == "hard-failure" {
				wantClass, wantStatus = "failed", 502
			}
			if mode == "upstream-504" {
				wantClass, wantStatus = "failed", 504
			}
			if mode == "canceled" {
				wantClass, wantStatus = "canceled", 408
			}
			if result.OK || result.Classification != wantClass || result.Status != wantStatus || result.Error == "" {
				t.Fatalf("classification: %+v", result)
			}
			if mode == "headers" || mode == "body" {
				waitProbeSignal(t, aborted)
			}
		})
	}
}

func TestProbeAliasAttributionAcrossWorkflows(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "actual-model" && body.Model != "provider-native" && body.Model != "openai/gpt-5.4" {
			t.Errorf("alias not resolved: model=%q err=%v", body.Model, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"model\":\"actual-version\",\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "manual", upstream.URL, "synthetic")
	ctx := context.Background()
	if err := h.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "manual", ID: "actual-model"}); err != nil {
		t.Fatal(err)
	}
	if err := h.opts.ProviderCatalog.PutAlias(ctx, "friendly", runtime.ModelRef{ProviderID: "manual", ModelID: "actual-model"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, body string }{
		{"/admin/v1/connections/" + c.ID + "/test", `{"modelId":"friendly"}`},
		{"/admin/v1/connections/" + c.ID + "/test-models", `{"modelIds":["friendly","manual/actual-model"]}`},
		{"/admin/v1/providers/test-batch", `{"mode":"all","modelId":"friendly"}`},
	} {
		w := doJSON(t, mux, cookie, "POST", tc.path, tc.body)
		var result struct {
			modelTestResult
			Results []modelTestResult `json:"results"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("attribution: %d %s", w.Code, w.Body)
		}
		results := result.Results
		if len(results) == 0 {
			results = []modelTestResult{result.modelTestResult}
		}
		for _, r := range results {
			if !r.OK || r.ModelID != "friendly" && r.ModelID != "manual/actual-model" || r.UpstreamModelID != "actual-model" || r.ServedModelID != "actual-version" {
				t.Fatalf("lost attribution: %+v", r)
			}
		}
	}
	if err := h.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "other", ID: "other-model"}); err != nil {
		t.Fatal(err)
	}
	if err := h.opts.ProviderCatalog.PutAlias(ctx, "wrong-provider", runtime.ModelRef{ProviderID: "other", ModelID: "other-model"}); err != nil {
		t.Fatal(err)
	}
	w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test-models", `{"modelIds":["wrong-provider"]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "different provider") {
		t.Fatalf("cross-provider alias was sent with this account's credentials: %d %s", w.Code, w.Body)
	}
	if err := h.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "other", ID: "provider-native"}); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test-models", `{"modelIds":["provider-native"]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), `"upstreamModelId":"provider-native"`) {
		t.Fatalf("another provider's catalog blocked a raw manual ID: %d %s", w.Code, w.Body)
	}
	for _, model := range []runtime.Model{
		{ProviderID: "openai", ID: "gpt-5.4"},
		{ProviderID: "manual", ID: "openai/gpt-5.4"},
		{ProviderID: "manual", ID: "provider-native"},
	} {
		if err := h.opts.ProviderCatalog.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.opts.ProviderCatalog.PutAlias(ctx, "provider-native", runtime.ModelRef{ProviderID: "other", ModelID: "other-model"}); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test-models", `{"modelIds":["openai/gpt-5.4","provider-native"]}`)
	if w.Code != 200 || strings.Count(w.Body.String(), `"ok":true`) != 2 || !strings.Contains(w.Body.String(), `"upstreamModelId":"openai/gpt-5.4"`) {
		t.Fatalf("native passthrough/alias collision lost: %d %s", w.Code, w.Body)
	}
	if err := h.opts.ProviderCatalog.SetModelDisabled(ctx, "manual", "actual-model", true); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+c.ID+"/test-models", `{"modelIds":["friendly"]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "disabled") || strings.Contains(w.Body.String(), "servedModelId") {
		t.Fatalf("disabled alias was sent literally: %d %s", w.Code, w.Body)
	}
}

func withBase(node credentials.Node, base string) credentials.Node {
	node.BaseURL = base
	return node
}
