package routing

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func ptrInt(v int) *int       { return &v }
func ptrInt64(v int64) *int64 { return &v }

// TestFusionDefaultsAreThePinnedV1Numbers pins the C06 decision: the default
// is v1's min 2 / grace 8000ms / hard 90000ms (combo.js:472-476), not the
// earlier 1 / 250ms / 30s.
func TestFusionDefaultsAreThePinnedV1Numbers(t *testing.T) {
	d := DefaultFusionConfig()
	if d.MinPanelQuorum != 2 || d.Grace != 8*time.Second || d.HardTimeout != 90*time.Second {
		t.Fatalf("defaults %+v", d)
	}
	var f Fusion
	f.Config = FusionConfig{}
	got, err := f.freezeConfig()
	if err != nil || got.MinPanelQuorum != 2 || got.HardTimeout != 90*time.Second {
		t.Fatalf("zero config resolved to %+v err=%v", got, err)
	}
}

func TestFusionTuningApplyMatchesV1Resolution(t *testing.T) {
	base := DefaultFusionConfig()
	cases := []struct {
		name   string
		tuning FusionTuning
		panels int
		min    int
		grace  time.Duration
		hard   time.Duration
	}{
		{"empty keeps defaults", FusionTuning{}, 3, 2, 8 * time.Second, 90 * time.Second},
		{"minPanel 1 is lifted to 2", FusionTuning{MinPanel: ptrInt(1)}, 3, 2, 8 * time.Second, 90 * time.Second},
		{"minPanel 0 is lifted to 2", FusionTuning{MinPanel: ptrInt(0)}, 3, 2, 8 * time.Second, 90 * time.Second},
		{"minPanel above the panel is clamped", FusionTuning{MinPanel: ptrInt(9)}, 3, 3, 8 * time.Second, 90 * time.Second},
		{"explicit zero grace is kept", FusionTuning{StragglerGraceMs: ptrInt64(0)}, 3, 2, 0, 90 * time.Second},
		{"grace and hard override", FusionTuning{StragglerGraceMs: ptrInt64(1500), PanelHardTimeoutMs: ptrInt64(20000)}, 4, 2, 1500 * time.Millisecond, 20 * time.Second},
	}
	for _, tc := range cases {
		got, err := tc.tuning.Apply(base, tc.panels)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.MinPanelQuorum != tc.min || got.Grace != tc.grace || got.HardTimeout != tc.hard {
			t.Fatalf("%s: got %+v", tc.name, got)
		}
		if got.MaxPanels != base.MaxPanels || got.MaxResponseBytes != base.MaxResponseBytes {
			t.Fatalf("%s: Routeweft caps changed: %+v", tc.name, got)
		}
	}
}

func TestFusionTuningRejectsValuesV1CannotMean(t *testing.T) {
	for name, tuning := range map[string]FusionTuning{
		"negative minPanel":     {MinPanel: ptrInt(-1)},
		"negative grace":        {StragglerGraceMs: ptrInt64(-1)},
		"zero hard timeout":     {PanelHardTimeoutMs: ptrInt64(0)},
		"hard timeout over cap": {PanelHardTimeoutMs: ptrInt64(MaxFusionTuningMs + 1)},
		"grace over cap":        {StragglerGraceMs: ptrInt64(MaxFusionTuningMs + 1)},
	} {
		if _, err := tuning.Apply(DefaultFusionConfig(), 3); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestFusionTuningCloneSharesNothingAndNormalizeLiftsMinPanel(t *testing.T) {
	orig := FusionTuning{MinPanel: ptrInt(0), StragglerGraceMs: ptrInt64(5)}
	clone := orig.Clone()
	*clone.MinPanel, *clone.StragglerGraceMs = 9, 9
	if *orig.MinPanel != 0 || *orig.StragglerGraceMs != 5 {
		t.Fatalf("clone aliases original: %+v", orig)
	}
	norm := orig.Normalize()
	if *norm.MinPanel != 2 || *orig.MinPanel != 0 || norm.PanelHardTimeoutMs != nil {
		t.Fatalf("normalize %+v orig %+v", norm, orig)
	}
}

// TestFusionGraceOpensOnlyAfterTwoAnswers is the behavioural half of the
// divergence: with the v1 gate, one fast answer does not start the grace clock,
// so a straggler slower than the grace still joins once it is the second
// answer. Under the former min=1 default the same panel returned "fast" alone.
func TestFusionGraceOpensOnlyAfterTwoAnswers(t *testing.T) {
	config, err := (FusionTuning{StragglerGraceMs: ptrInt64(10), PanelHardTimeoutMs: ptrInt64(2000)}).Apply(DefaultFusionConfig(), 2)
	if err != nil {
		t.Fatal(err)
	}
	var judged []string
	fusion := &Fusion{Config: config, Panels: func(_ context.Context, candidate PanelCandidate) (io.ReadCloser, error) {
		if candidate.ModelID == "a" {
			return io.NopCloser(strings.NewReader("fast")), nil
		}
		time.Sleep(60 * time.Millisecond)
		return io.NopCloser(strings.NewReader("slow")), nil
	}, Judge: func(_ context.Context, _ string, answers []string) (string, error) {
		judged = append([]string(nil), answers...)
		return "judged", nil
	}}
	answer, err := fusion.Run(context.Background(), candidates(2), "judge")
	if err != nil || answer != "judged" || len(judged) != 2 {
		t.Fatalf("answer=%q judged=%v err=%v", answer, judged, err)
	}
}

// TestFusionTunedHardTimeoutBoundsTheSlowPanel proves the resolved hard
// timeout, not the default, caps the panel phase.
func TestFusionTunedHardTimeoutBoundsTheSlowPanel(t *testing.T) {
	config, err := (FusionTuning{PanelHardTimeoutMs: ptrInt64(40)}).Apply(DefaultFusionConfig(), 2)
	if err != nil {
		t.Fatal(err)
	}
	fusion := &Fusion{Config: config, Panels: func(ctx context.Context, candidate PanelCandidate) (io.ReadCloser, error) {
		if candidate.ModelID == "a" {
			return io.NopCloser(strings.NewReader("fast")), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	start := time.Now()
	answer, err := fusion.Run(context.Background(), candidates(2), "judge")
	if err != nil || answer != "fast" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tuned 40ms hard timeout ran %v", elapsed)
	}
}
