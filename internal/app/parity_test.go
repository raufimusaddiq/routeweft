package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPinnedSystemOneDifferential(t *testing.T) {
	if os.Getenv("ORACLE_DIR") == "" {
		t.Skip("pinned differential gate sets ORACLE_DIR; normal unit tests need no oracle")
	}
	type probe struct {
		Name           string `json:"name"`
		Body           string `json:"body"`
		Authorized     bool   `json:"authorized"`
		UpstreamStatus int    `json:"upstreamStatus"`
		UpstreamBody   string `json:"upstreamBody"`
		ContentType    string `json:"contentType"`
		Cancel         bool   `json:"cancel"`
	}
	fixture := systemOneFixture(t, "systemone-nonstreaming")
	stream := systemOneFixture(t, "systemone-streaming")
	valid := fixture.Request.Body
	cases := []probe{
		{"typed", valid, true, 200, fixture.Response.Body, "application/json", false},
		{"stream", stream.Request.Body, true, 200, stream.Response.Body, "text/event-stream", false},
		{"unauthenticated", valid, false, 200, fixture.Response.Body, "application/json", false},
		{"invalid-json", "{", true, 200, "{}", "application/json", false},
		{"null", "null", true, 200, "{}", "application/json", false},
		{"empty-questions", `{"model":"jev-latest","state":null,"questions":{}}`, true, 200, "{}", "application/json", false},
		{"array-questions", `{"model":"jev-latest","state":null,"questions":[]}`, true, 200, "{}", "application/json", false},
		{"missing-state", `{"model":"jev-latest","questions":{"ok":{}}}`, true, 200, "{}", "application/json", false},
		{"upstream-error", valid, true, 400, `{"error":{"message":"synthetic failure"}}`, "application/json", false},
		{"cancel", stream.Request.Body, true, 200, stream.Response.Body, "text/event-stream", true},
	}
	input, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	command := exec.CommandContext(ctx, "node", "../../compat/parity/oracle.mjs", "systemone")
	command.Stdin = bytes.NewReader(input)
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	if err != nil {
		t.Fatalf("pinned oracle: %v: %s", err, &diagnostics)
	}
	var oracle []struct {
		Status   int
		Body     string
		Outbound map[string]any
		Aborted  bool
	}
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle) != len(cases) {
		t.Fatal("oracle dropped cases")
	}
	for i, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			var outbound map[string]any
			aborted := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&outbound); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", test.ContentType)
				w.WriteHeader(test.UpstreamStatus)
				if test.Cancel {
					io.WriteString(w, "data: {}\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					close(aborted)
					return
				}
				io.WriteString(w, test.UpstreamBody)
			}))
			defer upstream.Close()
			a, key, _ := systemOneApp(t, "typesafe", upstream.URL+"/typed", 1)
			if !test.Authorized {
				key = "invalid"
			}
			if test.Cancel {
				server := httptest.NewServer(a.Handler())
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/systemone", strings.NewReader(test.Body))
				r.Header.Set("Authorization", "Bearer "+key)
				response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				response.Body.Close()
				select {
				case <-aborted:
				case <-time.After(3 * time.Second):
					t.Fatal("upstream did not abort")
				}
				if !oracle[i].Aborted {
					t.Fatal("oracle did not propagate cancellation")
				}
				return
			}
			response := serveRequest(a.Handler(), key, "/v1/systemone", test.Body)
			if response.Code != oracle[i].Status {
				t.Fatalf("status=%d v1=%d body=%s", response.Code, oracle[i].Status, response.Body)
			}
			if !reflect.DeepEqual(outbound, oracle[i].Outbound) {
				t.Fatalf("outbound mismatch: %v vs %v", outbound, oracle[i].Outbound)
			}
			if outbound != nil {
				if response.Body.String() != oracle[i].Body {
					t.Fatal("native response bytes differ")
				}
			} else {
				var body map[string]json.RawMessage
				if json.Unmarshal(response.Body.Bytes(), &body) != nil || body["error"] == nil {
					t.Fatal("missing JSON error envelope")
				}
			}
		})
	}
}

func TestConfiguredH2CUpgradeFallsBackToNativeHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" || r.Header.Get("HTTP2-Settings") != "" {
			t.Error("client hop headers reached upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"synthetic"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Connection", "Upgrade, HTTP2-Settings")
	r.Header.Set("Upgrade", "h2c")
	r.Header.Set("HTTP2-Settings", "AAMAAABkAARAAAAAAAIAAAAA")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != `{"choices":[]}` {
		t.Fatalf("h2c attempt: status=%d err=%v body=%s", response.StatusCode, err, body)
	}
}
