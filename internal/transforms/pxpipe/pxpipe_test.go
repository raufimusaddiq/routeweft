package pxpipe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

type fakeClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeClient) Do(request *http.Request) (*http.Response, error) { return f.do(request) }
func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type hostCase struct {
	Name string `json:"name"`
	Spec struct {
		Unit  string `json:"unit"`
		Times int    `json:"times"`
	} `json:"spec"`
	Options struct {
		Enabled   bool   `json:"enabled"`
		Format    string `json:"format"`
		Model     string `json:"model"`
		MinChars  int    `json:"minChars"`
		TimeoutMs int    `json:"timeoutMs"`
	} `json:"options"`
	Script string `json:"script"`
	Result *struct {
		Applied bool            `json:"applied"`
		Reason  string          `json:"reason"`
		Detail  json.RawMessage `json:"detail"`
		Body    json.RawMessage `json:"body"`
		Info    json.RawMessage `json:"info"`
		Cache   json.RawMessage `json:"cache"`
	} `json:"result"`
	Calls []struct {
		BodyChars int    `json:"bodyChars"`
		Model     string `json:"model"`
		Options   struct {
			MinCompressChars int `json:"minCompressChars"`
		} `json:"options"`
	} `json:"calls"`
	Body    json.RawMessage `json:"body"`
	Summary map[string]any  `json:"summary"`
	Log     string          `json:"log"`
}

func claudeBody(unit string, times int) []byte {
	text, _ := json.Marshal(strings.Repeat(unit, times))
	return []byte(`{"model":"claude-fable-5","max_tokens":100,"messages":[{"role":"user","content":` + string(text) + `}]}`)
}

// TestReplaysTheOracleHostContract runs the oracle's own compressWithPxpipe
// recordings (compat/parity/generate-pxpipe.mjs) against the same scripted helper
// answers: the same gates, what the helper is handed, the same summary arithmetic,
// the same refusals. The image rendering itself belongs to the helper.
func TestReplaysTheOracleHostContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/host_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct{ Cases []hostCase }
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	if len(pin.Cases) < 15 {
		t.Fatalf("pin has %d cases", len(pin.Cases))
	}
	for _, c := range pin.Cases {
		t.Run(c.Name, func(t *testing.T) {
			input := claudeBody(c.Spec.Unit, c.Spec.Times)
			protocol := anthropic.MessagesProtocol
			if c.Options.Format == "openai" {
				protocol = openai.ChatProtocol
			}
			var calls int
			transform := Transform{Enabled: c.Options.Enabled, URL: "https://pxpipe.example/transform", MinChars: c.Options.MinChars,
				Client: fakeClient{do: func(r *http.Request) (*http.Response, error) {
					calls++
					var in helperRequest
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Fatal(err)
					}
					sent, err := base64.StdEncoding.DecodeString(in.Body)
					if err != nil || !bytes.Equal(sent, input) {
						t.Errorf("the helper must be handed the whole body")
					}
					if len(c.Calls) == 1 {
						if in.Model != c.Calls[0].Model || in.Options.MinCompressChars != c.Calls[0].Options.MinCompressChars || jsLength(sent) != c.Calls[0].BodyChars {
							t.Errorf("handed model=%q min=%d chars=%d, oracle handed %+v", in.Model, in.Options.MinCompressChars, jsLength(sent), c.Calls[0])
						}
					}
					switch c.Script {
					case "throws":
						return httpResponse(http.StatusInternalServerError, "boom"), nil
					case "never":
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					answer := map[string]any{"applied": c.Result.Applied, "reason": c.Result.Reason}
					if len(c.Result.Detail) > 0 {
						answer["detail"] = c.Result.Detail
					}
					answer["body"] = base64.StdEncoding.EncodeToString(c.Result.Body)
					if len(c.Result.Info) > 0 {
						answer["info"] = c.Result.Info
					}
					if len(c.Result.Cache) > 0 {
						answer["cache"] = c.Result.Cache
					}
					encoded, _ := json.Marshal(answer)
					return httpResponse(http.StatusOK, string(encoded)), nil
				}}}
			if c.Script == "none" {
				transform.URL = ""
			}
			if c.Options.TimeoutMs > 0 {
				transform.Timeout = time.Duration(c.Options.TimeoutMs) * time.Millisecond
			}
			got, summary := transform.Compress(context.Background(), protocol, c.Options.Model, input)

			if wantCalls := len(c.Calls); calls != wantCalls {
				t.Fatalf("%d helper calls, oracle made %d", calls, wantCalls)
			}
			summary.DurationMs = 0
			encoded, _ := json.Marshal(summary)
			var have map[string]any
			_ = json.Unmarshal(encoded, &have)
			for key, want := range c.Summary {
				if key == "detail" && (c.Script == "throws" || c.Name == "only Claude-format bodies") {
					continue // the oracle names its exception or format; Routeweft names its HTTP status or protocol
				}
				got, present := have[key]
				if !present {
					got = zeroOf(want)
				}
				if got != want {
					t.Errorf("summary.%s = %v, oracle %v", key, got, want)
				}
			}
			for key, value := range have {
				if _, known := c.Summary[key]; !known && value != zeroOf(value) {
					t.Errorf("summary.%s = %v is not in the oracle's summary", key, value)
				}
			}
			if c.Summary["applied"] == true {
				if !bytes.Equal(compact(t, got), compact(t, c.Result.Body)) {
					t.Errorf("body\n got %.200s\nwant %.200s", got, c.Result.Body)
				}
			} else if !bytes.Equal(got, input) {
				t.Errorf("a refused attempt must return the body untouched")
			}
			summary.DurationMs = 7
			if line := FormatLog(summary); line != c.Log {
				t.Errorf("log %q, oracle %q", line, c.Log)
			}
		})
	}
}

func zeroOf(value any) any {
	switch value.(type) {
	case string:
		return ""
	case bool:
		return false
	default:
		return float64(0)
	}
}

func compact(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAnUnusableEndpointNeverReceivesTheBody(t *testing.T) {
	input := claudeBody("x", 30000)
	for _, raw := range []string{"http://127.0.0.1:8787", "http://169.254.169.254/pipe", "file:///etc/passwd", "https://user:pass@pipe.example", "https://pipe.example/?token=1"} {
		contacted := false
		transform := Transform{Enabled: true, URL: raw, Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
			contacted = true
			return nil, errors.New("must not be called")
		}}}
		got, summary := transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", input)
		if contacted || summary.Applied || summary.Reason != "transform_error" || !bytes.Equal(got, input) {
			t.Errorf("%s: contacted=%v summary=%+v", raw, contacted, summary)
		}
	}
}

func TestHelperAnswersThatCannotBeTrustedFailOpen(t *testing.T) {
	input := claudeBody("x", 30000)
	cases := map[string]struct {
		status int
		body   string
		max    int64
	}{
		"status":        {status: 502, body: "{}"},
		"not JSON":      {status: 200, body: "<html>"},
		"too large":     {status: 200, body: strings.Repeat("x", 100), max: 10},
		"bad base64":    {status: 200, body: `{"applied":true,"body":"!!!"}`},
		"body not JSON": {status: 200, body: `{"applied":true,"body":"` + base64.StdEncoding.EncodeToString([]byte("[1]")) + `"}`},
		"empty body":    {status: 200, body: `{"applied":true,"body":""}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			transform := Transform{Enabled: true, URL: "https://pxpipe.example", MaxResponseBytes: c.max, Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
				return httpResponse(c.status, c.body), nil
			}}}
			got, summary := transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", input)
			if summary.Applied || summary.Reason != "transform_error" || !bytes.Equal(got, input) {
				t.Fatalf("summary=%+v", summary)
			}
			if strings.Contains(summary.Detail, "xxxx") {
				t.Fatalf("detail repeats the body: %q", summary.Detail)
			}
		})
	}
}

func TestACancelledRequestStopsTheHelperCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	transform := Transform{Enabled: true, URL: "https://pxpipe.example", Timeout: time.Minute, Client: fakeClient{do: func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}}
	done := make(chan Summary, 1)
	go func() {
		_, summary := transform.Compress(ctx, anthropic.MessagesProtocol, "m", claudeBody("x", 30000))
		done <- summary
	}()
	<-started
	cancel()
	select {
	case summary := <-done:
		if summary.Applied || summary.Reason == "timeout" {
			t.Fatalf("summary=%+v: a client that left is not a helper timeout", summary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the helper call outlived the client")
	}
}

func TestLengthsAreUTF16UnitsOfCompactJSON(t *testing.T) {
	for raw, want := range map[string]int{
		`{"a": 1,  "b": [1, 2]}`:         17,
		`{"t":"héllo"}`:                  13,
		`{"t":"😀"}`:                      10, // two units
		`{"t":"<&>"}`:                    11, // JSON.stringify does not escape them
		"{\"t\":\" \"}":                  9,  // nor U+2028
		`{"t":"a\\u2028b"}`:              17, // an escaped backslash stays two characters
		`{"n":1.50,"m":10}`:              16, // 1.5
		`{"n":1e2}`:                      9,  // 100
		`{"n":1E-7}`:                     10, // 1e-7
		`{"t":"line\nbreak","u":"q\"x"}`: 30,
	} {
		if got := jsLength([]byte(raw)); got != want {
			t.Errorf("%s: %d units, want %d", raw, got, want)
		}
	}
}

func TestToFixedRoundsLikeJavaScript(t *testing.T) {
	for value, want := range map[float64]float64{0.125: 0.13, 0.375: 0.38, 2.675: 2.67, 56.5100001: 56.51, 0: 0, 100: 100, 45.3099: 45.31, 1.005: 1} {
		if got := toFixed2(value); got != want {
			t.Errorf("toFixed(%v) = %v, want %v", value, got, want)
		}
	}
}

func TestStatsCountAppliedSkippedAndFailedAttempts(t *testing.T) {
	status := http.StatusOK
	answer := `{"applied":false,"reason":"not_profitable"}`
	service := NewService(Transform{Enabled: true, URL: "https://pxpipe.example", MinChars: 100, Client: fakeClient{do: func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet {
			return httpResponse(http.StatusOK, "ok"), nil
		}
		return httpResponse(status, answer), nil
	}}})
	transform := service.Transform
	transform.Diagnostics = service.Observe
	input := claudeBody("x", 500)
	transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", claudeBody("x", 5)) // below threshold: skipped
	transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", input)              // the helper declines: skipped
	answer = `{"applied":true,"body":"` + base64.StdEncoding.EncodeToString([]byte(`{"model":"m"}`)) + `","info":{"imageCount":1}}`
	transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", input) // applied
	status = http.StatusBadGateway
	transform.Compress(context.Background(), anthropic.MessagesProtocol, "m", input) // failed
	stats := service.Stats()
	if stats.Skipped != 2 || stats.Succeeded != 1 || stats.Failed != 1 || stats.Attempts != 2 || len(service.Logs()) != 4 {
		t.Fatalf("stats=%+v logs=%d", stats, len(service.Logs()))
	}
	if last := service.Logs()[2]; last.Summary == nil || !last.Summary.Applied || last.Summary.ImageCount != 1 {
		t.Fatalf("applied log=%+v", last)
	}
	if err := service.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (Transform{URL: "file:///tmp/x"}).Health(context.Background()); err == nil {
		t.Fatal("non-HTTP health URL accepted")
	}
}

func TestHealthHonorsTrustedLocalOptionAndTypesStatusErrors(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	if err := (Transform{URL: server.URL}).Health(context.Background()); err == nil {
		t.Fatal("loopback helper must be refused without the trusted-local option")
	}
	local := Transform{URL: server.URL, AllowPrivate: true, Client: transport.NewTrustedLocalSSRFProtectedClient()}
	if err := local.Health(context.Background()); err != nil {
		t.Fatalf("trusted-local health: %v", err)
	}
	status = http.StatusBadGateway
	var failure *transforms.HealthStatusError
	if err := local.Health(context.Background()); !errors.As(err, &failure) || failure.StatusCode != http.StatusBadGateway {
		t.Fatalf("err=%v want HealthStatusError 502", err)
	}
}
