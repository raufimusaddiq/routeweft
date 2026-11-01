package wire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// QoderDo is the dispatch binding: it turns the Chat body into the signed agent
// envelope and sends it once (executors/qoder.js:613-702), while QoderUnwrapSSE
// turns the answer back into Chat.
func TestQoderDoPreparesAndSignsTheEnvelope(t *testing.T) {
	doer := &scriptedDoer{steps: []*jsObject{fxObj(map[string]any{"status": 200, "body": "data: {\"statusCodeValue\":200}\n\n"})}}
	var clock time.Duration
	qc := clientAt(doer, BuildCosyHeaders, &clock)
	creds := QoderCredentials{AccessToken: "dt-synthetic", UserID: "user-1", MachineID: "machine-1", DisplayName: "n", Email: "e@example.invalid"}
	config, err := ParseQoderModelConfig([]byte(`{"key":"auto","max_output_tokens":1024}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog := &QoderCatalog{configs: map[string]QoderModelConfig{}, expires: qoderNow.Add(time.Hour)}
	catalog.keys, catalog.configs["auto"] = []string{"auto"}, config
	qc.catalogs = map[string]*QoderCatalog{qoderCatalogKey(creds): catalog}

	previous := qoderClientFor
	qoderClientFor = func(*http.Client) *QoderClient { return qc }
	t.Cleanup(func() { qoderClientFor = previous })

	provider := routing.ProviderRef{
		ProviderID: "qoder", Protocol: "qoder", UpstreamModel: "auto", APIToken: "dt-synthetic",
		Connection: routing.ConnectionMetadata{Name: "n", Settings: routing.ProviderSettings{UserID: "user-1", MachineID: "machine-1", Email: "e@example.invalid"}},
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://ignored.invalid", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// Send is the probe's path too (Exchanges("qoder") is true), so this test
	// exercises what dispatch and the model probe actually take.
	response, err := Send(provider, &http.Client{Transport: roundTripFunc(doer.Do)}, request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response %v err %v", response, err)
	}
	response.Body.Close()
	if len(doer.calls) != 1 {
		t.Fatalf("%d requests", len(doer.calls))
	}
	sent := doer.calls[0]
	if !strings.Contains(sent.URL, "agent_chat_generation") || !strings.Contains(sent.URL, "Encode=1") {
		t.Fatalf("url %s", sent.URL)
	}
	if sent.Header.Get("Authorization") == "" || sent.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("headers %v", sent.Header)
	}
	var envelope map[string]any
	plain := qoderDecodeBody(sent.Body)
	if err := json.Unmarshal(plain, &envelope); err != nil {
		t.Fatalf("sent body is not the encoded envelope: %v", err)
	}
	if envelope["request_id"] == nil || envelope["chat_context"] == nil {
		t.Fatalf("envelope %s", plain)
	}

	// A missing userId is the pin's local 401, answered before any network call.
	provider.Connection.Settings.UserID = ""
	before := len(doer.calls)
	refused, err := Send(provider, &http.Client{Transport: roundTripFunc(doer.Do)}, request.Clone(context.Background()))
	if err != nil || refused.StatusCode != http.StatusUnauthorized || len(doer.calls) != before {
		t.Fatalf("local refusal: %v %v calls=%d", refused, err, len(doer.calls)-before)
	}
	body, _ := io.ReadAll(refused.Body)
	refused.Body.Close()
	if !strings.Contains(string(body), "userId") {
		t.Fatalf("refusal body %s", body)
	}
}

// The codec hands QoderDo the Chat body and QoderUnwrapSSE the answer.
func TestQoderCodecBuildsChatAndStream(t *testing.T) {
	var chat map[string]any
	if err := json.Unmarshal([]byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`), &chat); err != nil {
		t.Fatal(err)
	}
	codec, ok, err := CodecFor(routing.ProviderRef{ProviderID: "qoder"}, nil, chat, "auto")
	if !ok || err != nil || codec.Endpoint != qoderCodecEndpoint || codec.Stream == nil {
		t.Fatalf("codec ok=%v err=%v %+v", ok, err, codec)
	}
	var out strings.Builder
	if err := codec.Stream(context.Background(), strings.NewReader("data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"pong\\\"}}]}\"}\n\ndata: [DONE]\n\n"), &out); err != nil || !strings.Contains(out.String(), "pong") {
		t.Fatalf("stream err=%v out=%s", err, out.String())
	}
	// A non-stream client: the dispatcher folds the forced stream, then applies
	// Response to the folded Chat document. Identity, never a refusal.
	folded := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"pong"},"finish_reason":"stop"}]}`)
	if got, err := codec.Response(folded); err != nil || string(got) != string(folded) {
		t.Fatalf("response after fold: %q %v", got, err)
	}
	if !Exchanges("qoder") {
		t.Fatal("the model probe must go through QoderDo to be signed")
	}
}
