package wire

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestOpenCodeGoForcedStreamModels(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "grok-4.6", "muse-spark-1.2-contributor", "muse-spark-1.3-contributor", "deepseek-v4-pro", "glm-5.3", "unknown"} {
		for _, protocol := range []string{chat, responses, "anthropic-messages"} {
			p := routing.ProviderRef{ProviderID: "opencode-go", Protocol: protocol, UpstreamModel: model}
			want := protocol == responses && (model == "gpt-5.6-luna" || model == "grok-4.6" || model == "muse-spark-1.2-contributor" || model == "muse-spark-1.3-contributor")
			if ForcesStream(p) != want {
				t.Fatalf("%s/%s: wrong stream flag", model, protocol)
			}
			body := []byte(`{"model":"` + model + `","stream":false,"future_field":"keep"}`)
			got := FinalizeBodyAt(p, "responses", body)
			fields := decodeBody(t, string(got))
			if fields["stream"] != want || fields["future_field"] != "keep" {
				t.Fatalf("%s/%s: %s", model, protocol, got)
			}
			if again := FinalizeBodyAt(p, "responses", got); string(again) != string(got) {
				t.Fatalf("not idempotent: %s", again)
			}
		}
	}
}

func TestZedForcesOnlyItsDeclaredChatBody(t *testing.T) {
	for _, protocol := range []string{chat, responses, "anthropic-messages"} {
		p := routing.ProviderRef{ProviderID: "zed", Protocol: protocol}
		got := decodeBody(t, string(FinalizeBodyAt(p, "chat/completions", []byte(`{"stream":false,"future_field":"keep"}`))))
		if got["stream"] != (protocol == chat) || got["future_field"] != "keep" {
			t.Fatalf("%s: %v", protocol, got)
		}
	}
}

func BenchmarkNewForcedStreamBodies(b *testing.B) {
	for _, p := range []routing.ProviderRef{
		{ProviderID: "zed", Protocol: chat},
		{ProviderID: "opencode-go", Protocol: responses, UpstreamModel: "gpt-5.6-luna"},
	} {
		b.Run(p.ProviderID, func(b *testing.B) {
			body := []byte(`{"model":"gpt-5.6-luna","input":"ping","stream":false}`)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				FinalizeBodyAt(p, "responses", body)
			}
		})
	}
}

func TestForcedTransportIsScopedToProviderAndModel(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"opencode-go", "gpt-5.6-luna", responses},
		{"opencode-go", "grok-4.6", responses},
		{"opencode-go", "muse-spark-1.2-contributor", responses},
		{"opencode-go", "muse-spark-1.3-contributor", responses},
		{"opencode-go", "glm-5.3", ""},
		{"opencode-go", "unknown", ""},
		{"openai", "gpt-5.6-luna", ""},
		{"unknown", "gpt-5.6-luna", ""},
	} {
		if got := ForcedTransport(tc.provider, tc.model); got != tc.want {
			t.Errorf("%s/%s: got %q want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}
