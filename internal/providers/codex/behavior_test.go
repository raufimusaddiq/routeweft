package codex

import (
	"reflect"
	"testing"
)

func TestCodexHeadersAndReviewModels(t *testing.T) {
	want := map[string]string{"originator": "codex_cli_rs", "User-Agent": "codex_cli_rs/0.154.0"}
	if got := Headers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("headers=%v want=%v", got, want)
	}
	for _, tc := range []struct {
		model, upstream string
		review          bool
	}{{"gpt-5.5", "gpt-5.5", false}, {"gpt-5.5-review", "gpt-5.5", true}, {AutoReviewModel, AutoReviewModel, true}, {"model-review-review", "model-review", true}} {
		if got := UpstreamModel(tc.model); got != tc.upstream {
			t.Errorf("UpstreamModel(%q)=%q want %q", tc.model, got, tc.upstream)
		}
		if got := IsReviewModel(tc.model); got != tc.review {
			t.Errorf("IsReviewModel(%q)=%v want %v", tc.model, got, tc.review)
		}
	}
	if got := Headers(); got["User-Agent"] == "" {
		t.Fatal("missing User-Agent")
	}
	g := Headers()
	g["originator"] = "changed"
	if Headers()["originator"] != Originator {
		t.Fatal("Headers returned shared mutable state")
	}
}
