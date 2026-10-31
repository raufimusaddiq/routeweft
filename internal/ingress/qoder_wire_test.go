package ingress

import (
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
			ref := routing.ProviderRef{ProviderID: "qoder", Protocol: "openai-chat", BaseURL: up.srv.URL, APIToken: "fixture-token", ConnectionID: "qoder-account",
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
			if out.Code != http.StatusServiceUnavailable || !strings.Contains(out.Body.String(), "credentials_unavailable") {
				t.Fatalf("missing %s: status %d, body %s", missing, out.Code, out.Body)
			}
			until, cooling := state.CooldownUntil(ref.ConnectionID, time.Now())
			if !cooling || until.Before(before.Add(2*time.Minute)) || until.After(time.Now().Add(2*time.Minute)) {
				t.Fatalf("missing %s did not get the auth-refresh cooldown: %s", missing, until)
			}
			if missing == "machineId" && (!strings.Contains(out.Body.String(), "machineId") || !strings.Contains(out.Body.String(), "re-run Qoder sign-in")) {
				t.Fatalf("missing recovery instruction: %s", out.Body)
			}
			up.mu.Lock()
			defer up.mu.Unlock()
			if len(up.seen) != 0 {
				t.Fatal("unsigned request reached upstream")
			}
		})
	}
}
