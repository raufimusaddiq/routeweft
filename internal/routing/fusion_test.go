package routing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func candidates(count int) []PanelCandidate {
	list := make([]PanelCandidate, 0, count)
	for i := 0; i < count; i++ {
		list = append(list, PanelCandidate{ProviderID: "p", ModelID: string(rune('a' + i))})
	}
	return list
}

func TestFusionZeroPanelsErrors(t *testing.T) {
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 1}, Panels: func(context.Context, PanelCandidate) (string, error) {
		return "", errors.New("boom")
	}}
	if _, err := fusion.Run(context.Background(), candidates(2), "judge"); !errors.Is(err, ErrFusionNoPanels) {
		t.Fatalf("err=%v", err)
	}
}

func TestFusionSinglePanelReturnsDirectlyWithoutJudge(t *testing.T) {
	judgeCalled := false
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 1}, Panels: func(_ context.Context, candidate PanelCandidate) (string, error) {
		if candidate.ModelID == "a" {
			return "only", nil
		}
		return "", errors.New("boom")
	}, Judge: func(context.Context, string, []string) (string, error) {
		judgeCalled = true
		return "", nil
	}}
	answer, err := fusion.Run(context.Background(), candidates(2), "judge")
	if err != nil || answer != "only" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if judgeCalled {
		t.Fatal("judge must not run for a single success")
	}
}

func TestFusionMultiPanelUsesJudgeWithAnonymousAnswers(t *testing.T) {
	var got []string
	var gotJudge string
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 2, Grace: 10 * time.Millisecond}, Panels: func(_ context.Context, candidate PanelCandidate) (string, error) {
		return "answer-" + candidate.ModelID, nil
	}, Judge: func(_ context.Context, judge string, answers []string) (string, error) {
		gotJudge = judge
		got = append([]string(nil), answers...)
		return "synthesis", nil
	}}
	answer, err := fusion.Run(context.Background(), candidates(2), "configured-judge")
	if err != nil || answer != "synthesis" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if gotJudge != "configured-judge" || len(got) < 2 {
		t.Fatalf("judge=%q answers=%v", gotJudge, got)
	}
	for _, candidate := range got {
		if candidate == "a" || candidate == "b" {
			t.Fatalf("panel answers must be anonymous: %v", got)
		}
	}
}

func TestFusionDefaultJudgeFallsBackToFirstComboModel(t *testing.T) {
	var gotJudge string
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 2}, DefaultJudge: "first-combo-model", Panels: func(_ context.Context, candidate PanelCandidate) (string, error) {
		return "answer", nil
	}, Judge: func(_ context.Context, judge string, _ []string) (string, error) {
		gotJudge = judge
		return "ok", nil
	}}
	if _, err := fusion.Run(context.Background(), candidates(2), ""); err != nil {
		t.Fatal(err)
	}
	if gotJudge != "first-combo-model" {
		t.Fatalf("judge=%q", gotJudge)
	}
}

func TestFusionHardTimeoutAndQuorum(t *testing.T) {
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 2, HardTimeout: 30 * time.Millisecond, Grace: 5 * time.Millisecond}, Panels: func(ctx context.Context, candidate PanelCandidate) (string, error) {
		if candidate.ModelID == "a" {
			return "fast", nil
		}
		select {
		case <-time.After(time.Second):
			return "slow", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}, Judge: func(context.Context, string, []string) (string, error) { return "judged", nil }}
	if _, err := fusion.Run(context.Background(), candidates(2), "judge"); !errors.Is(err, ErrFusionQuorum) {
		t.Fatalf("err=%v", err)
	}
}

func TestFusionConcurrencyCap(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 1, MaxConcurrent: 1, HardTimeout: time.Second}, Panels: func(ctx context.Context, _ PanelCandidate) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = fusion.Run(context.Background(), candidates(1), "")
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request never started")
	}
	if _, err := fusion.Run(context.Background(), candidates(1), ""); !errors.Is(err, ErrFusionBusy) {
		t.Fatalf("err=%v", err)
	}
	close(release)
	wg.Wait()
}

func TestFusionResponseByteCapRejectsOversizedPanel(t *testing.T) {
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 1, MaxResponseBytes: 4}, Panels: func(_ context.Context, _ PanelCandidate) (string, error) {
		return "way too long", nil
	}}
	if _, err := fusion.Run(context.Background(), candidates(1), ""); !errors.Is(err, ErrFusionNoPanels) {
		t.Fatalf("err=%v", err)
	}
}

func TestFusionGraceCollectsStragglerAfterQuorum(t *testing.T) {
	var answers []string
	// Quorum 2 with 3 panels: two fast answers satisfy quorum and the slow third
	// must still be collected inside the grace window.
	fusion := &Fusion{Config: FusionConfig{MinPanelQuorum: 2, Grace: 80 * time.Millisecond, HardTimeout: time.Second}, Panels: func(_ context.Context, candidate PanelCandidate) (string, error) {
		if candidate.ModelID == "a" {
			return "quick", nil
		}
		if candidate.ModelID == "b" {
			return "quick2", nil
		}
		time.Sleep(20 * time.Millisecond)
		return "straggler", nil
	}, Judge: func(_ context.Context, _ string, got []string) (string, error) {
		answers = append([]string(nil), got...)
		return "judged", nil
	}}
	if _, err := fusion.Run(context.Background(), candidates(3), "judge"); err != nil {
		t.Fatal(err)
	}
	if len(answers) != 3 {
		t.Fatalf("grace omitted straggler: %v", answers)
	}
}

func TestFusionPanelCountIsBounded(t *testing.T) {
	fusion := &Fusion{Config: FusionConfig{MaxPanels: 1}, Panels: func(context.Context, PanelCandidate) (string, error) { return "ok", nil }}
	if _, err := fusion.Run(context.Background(), candidates(2), ""); !errors.Is(err, ErrFusionPanelLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestStripToolsAndFlattenToolHistory(t *testing.T) {
	body := StripTools(map[string]any{"tools": []any{1}, "tool_choice": "auto", "model": "m"})
	if _, ok := body["tools"]; ok {
		t.Fatal("tools not stripped")
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatal("tool_choice not stripped")
	}
	if body["model"] != "m" {
		t.Fatal("unrelated field removed")
	}
	if got := FlattenToolHistory([]string{"call add(1,2)", "result 3"}); got != "call add(1,2)\nresult 3" {
		t.Fatalf("flatten=%q", got)
	}
}
