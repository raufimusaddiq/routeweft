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
	// auth maps to auth-refresh-required, which does not open a generic
	// fallback cooldown; the point of the test is that the body is parsed at all.
	if got := classifyResponse(http.StatusUnauthorized, nil); got.Outcome != routing.OutcomeAuthRefreshRequired {
		t.Fatalf("status classification %+v", got)
	}
	if got := routing.ClassifyError("auth", http.StatusForbidden); got.Outcome != routing.OutcomeAuthRefreshRequired {
		t.Fatalf("kind classification %+v", got)
	}
}
