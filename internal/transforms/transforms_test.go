package transforms_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/caveman"
	"github.com/raufimusaddiq/routeweft/internal/transforms/ponytail"
	"github.com/raufimusaddiq/routeweft/internal/transforms/rtk"
)

func TestPipelineRunsInDeclaredNormativeOrderAndFailOpen(t *testing.T) {
	var order []string
	step := func(name string, fail bool) transforms.Transform {
		return testTransform{name: name, order: &order, fail: fail}
	}
	input := transforms.Request{System: []string{"base"}, Messages: []transforms.Message{{Role: "tool", Content: "keep"}}}
	got := (transforms.Pipeline{Steps: []transforms.Transform{step("rtk", false), step("headroom", true), step("caveman", false), step("ponytail", false), step("pxpipe", false)}}).Run(input)
	if !reflect.DeepEqual(order, []string{"rtk", "headroom", "caveman", "ponytail", "pxpipe"}) {
		t.Fatalf("order=%v", order)
	}
	if !reflect.DeepEqual(input, transforms.Request{System: []string{"base"}, Messages: []transforms.Message{{Role: "tool", Content: "keep"}}}) {
		t.Fatal("pipeline mutated its input")
	}
	if !reflect.DeepEqual(got.System, []string{"base", "rtk", "caveman", "ponytail", "pxpipe"}) {
		t.Fatalf("failed step output leaked or later steps skipped: %+v", got)
	}
}

type testTransform struct {
	name  string
	order *[]string
	fail  bool
}

func (t testTransform) Name() string { return t.name }
func (t testTransform) Apply(request transforms.Request) (transforms.Request, error) {
	*t.order = append(*t.order, t.name)
	request.System = append(request.System, t.name)
	if t.fail {
		return request, errors.New("optional transform failed")
	}
	return request, nil
}

func TestClientBypassSkipsAllTokenSaverTransformsByCaller(t *testing.T) {
	request := transforms.Request{Bypass: true, System: []string{"base"}, Messages: []transforms.Message{{Role: "tool", Content: "a\nb\nc\nd", ToolResult: true}}}
	var order []string
	got := (transforms.Pipeline{Steps: []transforms.Transform{testTransform{name: "rtk", order: &order}}}).Run(request)
	if len(order) != 0 || !reflect.DeepEqual(got, request) {
		t.Fatalf("bypass ran a transform: order=%v request=%+v", order, got)
	}
}

func TestTokenSaverTransformsAreIdempotentAndPreserveOtherMessages(t *testing.T) {
	request := transforms.Request{System: []string{"base"}, Messages: []transforms.Message{
		{Role: "user", Content: "user text"},
		{Role: "tool", Content: "1\n2\n3\n4\n5\n6", ToolResult: true},
	}}
	pipeline := transforms.Pipeline{Steps: []transforms.Transform{
		rtk.Transform{Enabled: true},
		caveman.Transform{Enabled: true, Level: "full"},
		ponytail.Transform{Enabled: true, Level: "lite"},
	}}
	first := pipeline.Run(request)
	second := pipeline.Run(first)
	if first.Messages[0].Content != "user text" || !first.Messages[1].ToolResult {
		t.Fatalf("non-tool content changed: %+v", first.Messages)
	}
	if !reflect.DeepEqual(first.System, second.System) {
		t.Fatalf("policy blocks duplicated: %v / %v", first.System, second.System)
	}
	if len(first.System) != 3 || first.System[0][:len(caveman.Marker)] != caveman.Marker || first.System[1][:len(ponytail.Marker)] != ponytail.Marker {
		t.Fatalf("policy block order/content=%v", first.System)
	}
	if first.Messages[1].Content != "1\n2\n3\n4\n5\n6" {
		t.Fatalf("unique tool result content changed: %q", first.Messages[1].Content)
	}
}

func TestRTKMinifiesJSONAndPreservesNonJSONToolResults(t *testing.T) {
	input := transforms.Request{Messages: []transforms.Message{
		{Role: "tool", Content: "{ \"x\" : 1, \"x\" : 2, \"rows\" : [ 1, 1, 2 ] }", ToolResult: true},
		{Role: "tool", Content: "a\na\na\nb", ToolResult: true},
	}}
	got := (transforms.Pipeline{Steps: []transforms.Transform{rtk.Transform{Enabled: true}}}).Run(input)
	if got.Messages[0].Content != `{"x":1,"x":2,"rows":[1,1,2]}` {
		t.Fatalf("JSON output=%q", got.Messages[0].Content)
	}
	if got.Messages[1].Content != input.Messages[1].Content {
		t.Fatalf("non-JSON content changed: %q", got.Messages[1].Content)
	}
}

func TestPromptPoliciesIndividually(t *testing.T) {
	for _, tc := range []struct {
		name string
		step transforms.Transform
		want string
	}{
		{"caveman-lite", caveman.Transform{Enabled: true, Level: "lite"}, caveman.Marker + " Respond concisely. Prefer short sentences and omit pleasantries."},
		{"caveman-full", caveman.Transform{Enabled: true, Level: "full"}, caveman.Marker + " Respond tersely. No filler, no restating the question, no pleasantries."},
		{"caveman-ultra", caveman.Transform{Enabled: true, Level: "ultra"}, caveman.Marker + " Respond in minimal telegraphic fragments. Drop all non-essential words."},
		{"ponytail-lite", ponytail.Transform{Enabled: true, Level: "lite"}, ponytail.Marker + " Prefer shorter answers; skip preamble and summary unless asked."},
		{"ponytail-full", ponytail.Transform{Enabled: true, Level: "full"}, ponytail.Marker + " Prefer the smallest correct answer. No preamble, no recap, no repetition."},
		{"ponytail-ultra", ponytail.Transform{Enabled: true, Level: "ultra"}, ponytail.Marker + " Answer with the minimum viable output. Code or facts only when sufficient."},
		{"caveman-disabled", caveman.Transform{}, ""},
		{"caveman-unknown", caveman.Transform{Enabled: true, Level: "unknown"}, ""},
		{"ponytail-disabled", ponytail.Transform{}, ""},
		{"ponytail-unknown", ponytail.Transform{Enabled: true, Level: "unknown"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := transforms.Request{System: []string{"base"}, Messages: []transforms.Message{{Role: "user", Content: "keep"}}}
			pipeline := transforms.Pipeline{Steps: []transforms.Transform{tc.step}}
			got := pipeline.Run(input)
			want := input.Clone()
			if tc.want != "" {
				want.System = []string{tc.want, "base"}
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(pipeline.Run(got), got) {
				t.Fatalf("got %+v, want %+v; not idempotent or wrong policy", got, want)
			}
		})
	}
}
