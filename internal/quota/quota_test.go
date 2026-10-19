package quota

import (
	"context"
	"errors"
	"testing"
	"time"
)

func ptr(value float64) *float64 { return &value }

func TestDeriveStatusDistinguishesAllFiveStates(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	for _, tc := range []struct {
		name  string
		state State
		want  Status
	}{
		{"available", State{Windows: []Window{{Name: "session", Remaining: ptr(0.5)}}}, StatusAvailable},
		{"exhausted", State{Windows: []Window{{Name: "session", Remaining: ptr(0), ResetAt: &future}}}, StatusExhausted},
		{"reset passed is available", State{Windows: []Window{{Name: "session", Remaining: ptr(0), ResetAt: &past}}}, StatusAvailable},
		{"unlimited window is available", State{Windows: []Window{{Name: "session", Unlimited: true}}}, StatusAvailable},
		{"no windows is unknown", State{}, StatusUnknown},
		{"error stays error", State{Status: StatusError, Err: "boom"}, StatusError},
	} {
		if got := tc.state.DeriveStatus(now); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestExhaustedSkipsUnboundedAndPastResetWindows(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	state := State{Windows: []Window{
		{Name: "a", Remaining: ptr(0), ResetAt: &past},
		{Name: "b", Remaining: nil},
		{Name: "c", Unlimited: true},
	}}
	if state.Exhausted(now) {
		t.Fatal("unbounded/past-reset windows must not mark exhaustion")
	}
	state.Windows = append(state.Windows, Window{Name: "d", Remaining: ptr(0), ResetAt: &future})
	if !state.Exhausted(now) {
		t.Fatal("a zero window with a future reset must mark exhaustion")
	}
}

func TestClampAndPercentHelpers(t *testing.T) {
	if got := *PercentFromUsed(150); got != 0 {
		t.Fatalf("used 150 -> %v", got)
	}
	if got := *PercentFromUsed(-10); got != 1 {
		t.Fatalf("used -10 -> %v", got)
	}
	if got := *PercentFromUsed(40); got != 0.6 {
		t.Fatalf("used 40 -> %v", got)
	}
	if got := *RemainingPercent(87); got != 0.87 {
		t.Fatalf("remaining 87 -> %v", got)
	}
	if PercentFromUsed(mathNaN()) != nil {
		t.Fatal("NaN must be unbounded, not a number")
	}
}

type fakeClient struct {
	state State
	err   error
	calls int
}

func (f *fakeClient) Usage(context.Context, string) (State, error) {
	f.calls++
	return f.state, f.err
}

type recorder struct {
	remaining *float64
	resetAt   time.Time
	observed  time.Time
	errText   string
	calls     int
}

func (r *recorder) ObserveQuotaResult(account string, remaining *float64, resetAt time.Time, observedAt time.Time, errText string) {
	r.calls++
	r.remaining, r.resetAt, r.observed, r.errText = remaining, resetAt, observedAt, errText
}

func TestObservePublishesMostConstrainedWindow(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	client := &fakeClient{state: State{Plan: "plus", Windows: []Window{
		{Name: "weekly", Remaining: ptr(0.8), ResetAt: &reset},
		{Name: "session", Remaining: ptr(0.1), ResetAt: &reset},
		{Name: "unbounded", Remaining: nil},
	}}}
	recorder := &recorder{}
	observer := Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }, Publisher: recorder}
	state, err := observer.Observe(context.Background(), "codex", "acct-1", "token")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusAvailable || state.Plan != "plus" {
		t.Fatalf("state=%+v", state)
	}
	if recorder.calls != 1 || recorder.remaining == nil || *recorder.remaining != 0.1 || !recorder.resetAt.Equal(reset) || !recorder.observed.Equal(now) {
		t.Fatalf("published=%+v", recorder)
	}
}

func TestObserveReadFailureIsErrorNotExhaustion(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{err: errors.New("usage endpoint returned HTTP 503")}
	recorder := &recorder{}
	observer := Observer{Clients: map[string]UsageClient{"claude": client}, Now: func() time.Time { return now }, Publisher: recorder}
	state, err := observer.Observe(context.Background(), "claude", "acct-1", "token")
	if err == nil {
		t.Fatal("expected the read failure to surface")
	}
	if state.Status != StatusError || state.Exhausted(now) {
		t.Fatalf("a failed read must not be exhaustion: %+v", state)
	}
	if recorder.remaining != nil || recorder.errText == "" {
		t.Fatalf("failure must publish no remaining bound plus an error: %+v", recorder)
	}
}

func TestObserveUnknownProviderIsNotConfigured(t *testing.T) {
	observer := Observer{Clients: map[string]UsageClient{}}
	if _, err := observer.Observe(context.Background(), "blackbox", "acct", "token"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}

func TestObserveExhaustedPublishesZeroRemaining(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	client := &fakeClient{state: State{Windows: []Window{{Name: "weekly", Remaining: ptr(0), ResetAt: &reset}}}}
	recorder := &recorder{}
	observer := Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }, Publisher: recorder}
	state, err := observer.Observe(context.Background(), "codex", "acct-1", "token")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusExhausted {
		t.Fatalf("status=%q", state.Status)
	}
	if recorder.remaining == nil || *recorder.remaining != 0 {
		t.Fatalf("published remaining=%v", recorder.remaining)
	}
}

func mathNaN() float64 {
	var zero float64
	return zero / zero
}
