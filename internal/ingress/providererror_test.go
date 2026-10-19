package ingress

import (
	"net/http"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestRecordUpstreamFailureMapsProviderErrToCooldown(t *testing.T) {
	state := runtime.NewState()
	handler := New(nil, Options{State: state})
	provider := routing.ProviderRef{ProviderID: "anthropic", Protocol: "anthropic-messages", ConnectionID: "acct-1"}
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	handler.recordUpstreamFailure(provider, http.StatusTooManyRequests, nil, body)
	until, cooling := state.CooldownUntil("acct-1", time.Now())
	if !cooling || !until.After(time.Now()) {
		t.Fatalf("expected cooldown for acct-1, got until=%s cooling=%v", until, cooling)
	}
}

func TestRecordUpstreamFailureAuthKindClassifies(t *testing.T) {
	// Keep the refresh classification while preventing repeated dead credentials.
	if got := classifyResponse(http.StatusUnauthorized, nil); got.Outcome != routing.OutcomeAuthRefreshRequired {
		t.Fatalf("status classification %+v", got)
	}
	if got := routing.ClassifyError("auth", http.StatusForbidden); got.Outcome != routing.OutcomeAuthRefreshRequired {
		t.Fatalf("kind classification %+v", got)
	}
	state := runtime.NewState()
	handler := New(nil, Options{State: state})
	handler.recordUpstreamFailure(routing.ProviderRef{ConnectionID: "auth", Protocol: "openai-chat"}, http.StatusUnauthorized, nil, nil)
	until, ok := state.CooldownUntil("auth", time.Now())
	if !ok || time.Until(until) < 119*time.Second {
		t.Fatal("auth failure has no pinned-v1 cooldown")
	}
}
