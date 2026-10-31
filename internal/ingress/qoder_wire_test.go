package ingress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Qoder's dispatch seam is QoderDo + the codec: the Chat body is converted and
// signed by PrepareChat, not by the old RequestHeaders-on-Chat seam
// (executors/qoder.js:613-702). The wire-level TestQoderDoPreparesAndSignsTheEnvelope
// proves the envelope; this pins that dispatch uses it and stays fenced.
func TestQoderDispatchUsesThePreparedEnvelope(t *testing.T) {
	if wire.For("qoder").Do == nil {
		t.Fatal("qoder has no dispatch Do")
	}
	if !wire.HasCodec("qoder") {
		t.Fatal("qoder has no codec")
	}
	for _, source := range clientWires {
		if CanTranslate(source, "qoder") {
			t.Fatalf("%s reaches qoder while fenced", source)
		}
	}
}

func TestQoderMissingIdentityFailsBeforeNetwork(t *testing.T) {
	for _, missing := range []string{"machineId", "userId", "token"} {
		t.Run(missing, func(t *testing.T) {
			up := newRecorder(t)
			ref := routing.ProviderRef{ProviderID: "qoder", Protocol: "qoder", UpstreamModel: "auto", BaseURL: up.srv.URL, APIToken: "fixture-token", ConnectionID: "qoder-account",
				Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{UserID: "fixture-user", MachineID: "stored-machine"}}}
			switch missing {
			case "machineId":
				ref.Connection.Settings.MachineID = " "
			case "userId":
				ref.Connection.Settings.UserID = ""
			case "token":
				ref.APIToken = ""
			}
			state := runtime.NewState()
			mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, State: state,
				ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) { return ref, true },
			})
			before := time.Now()
			out := httptest.NewRecorder()
			bearer(mux, key).ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody)))
			want := http.StatusServiceUnavailable
			wantText := "credentials_unavailable"
			switch missing {
			case "machineId":
				// Without qoderHeaders the request reaches QoderDo, which answers the
				// pin's local error before any network call (qoder.js:621-689).
				want, wantText = http.StatusBadRequest, "machineId"
			case "userId":
				want, wantText = http.StatusUnauthorized, "userId"
			}
			if out.Code != want || !strings.Contains(out.Body.String(), wantText) {
				t.Fatalf("missing %s: status %d, body %s", missing, out.Code, out.Body)
			}
			until, cooling := state.CooldownUntil(ref.ConnectionID, time.Now())
			if missing == "machineId" {
				if cooling {
					t.Fatalf("a 400 cooled the account: %s", until)
				}
			} else if !cooling || until.Before(before.Add(2*time.Minute)) || until.After(time.Now().Add(2*time.Minute)) {
				t.Fatalf("missing %s did not get the auth-refresh cooldown: %s", missing, until)
			}

			up.mu.Lock()
			defer up.mu.Unlock()
			if len(up.seen) != 0 {
				t.Fatal("unsigned request reached upstream")
			}
		})
	}
}

// A non-stream client is served by unwrapping Qoder's envelope before the forced
// stream is folded; without the codec-aware step FoldChatStream refused the
// statusCodeValue field (executors/qoder.js:362-597).
func TestQoderNonStreamFoldUnwrapsBeforeFolding(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "qoder", Protocol: "qoder", UpstreamModel: "auto"}
	ok := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"pong\\\"}}]}\"}\n\ndata: [DONE]\n\n"))}
	if err := foldForcedStream(context.Background(), ok, provider, false); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(ok.Body)
	ok.Body.Close()
	if !strings.Contains(string(body), `"content":"pong"`) || strings.Contains(string(body), "statusCodeValue") {
		t.Fatalf("folded body %s", body)
	}
	failed := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: {\"statusCodeValue\":403,\"body\":\"{\\\"code\\\":\\\"112\\\",\\\"pricingUrl\\\":\\\"x\\\"}\"}\n\n"))}
	err := foldForcedStream(context.Background(), failed, provider, false)
	var dispatch *dispatchError
	if !errors.As(err, &dispatch) || dispatch.status != http.StatusForbidden {
		t.Fatalf("first-frame failure: %v", err)
	}
}
