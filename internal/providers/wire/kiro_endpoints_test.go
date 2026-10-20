package wire

import (
	"errors"
	"net/http"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

func TestKiroEndpointOrderAndRegions(t *testing.T) {
	for _, auth := range []string{"api_key", "external_idp", "idc", "builder-id", "google", "github"} {
		for _, region := range []string{"", "us-east-1", "eu-west-1", "us-west-2", "eu-central-1", "ap-southeast-1"} {
			p := kiroRef()
			p.BaseURL = "https://runtime.us-east-1.kiro.dev"
			p.Connection.Settings.AuthMethod, p.Connection.Settings.Region = auth, region
			effective := region
			if effective == "" {
				effective = "us-east-1"
			}
			want := []string{"https://q." + effective + ".amazonaws.com", "https://codewhisperer." + effective + ".amazonaws.com", "https://runtime.us-east-1.kiro.dev"}
			for i, base := range want {
				target, err := ResolveTarget(p, "ignored", i)
				if err != nil || target.BaseURL != base || target.Endpoint != kiroOperation {
					t.Fatalf("%s %s attempt %d: %+v %v", auth, region, i, target, err)
				}
				u, err := target.URL(transport.ValidatePublicURL)
				if err != nil || u.String() != base+"/generateAssistantResponse" {
					t.Fatalf("URL %v: %v", u, err)
				}
				req, _ := http.NewRequest(http.MethodPost, u.String(), nil)
				req.Header = OpenAIHeaders(p)
				req.Header.Set("X-Amz-Target", "stale")
				if err := ApplyRequestHeaders(p, req, nil); err != nil {
					t.Fatal(err)
				}
				expectedTarget := ""
				if i == 1 {
					expectedTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
				}
				if req.Header.Get("X-Amz-Target") != expectedTarget {
					t.Fatalf("%s: stale target header", u)
				}
			}
			for _, i := range []int{-1, 3, 100} {
				target, err := ResolveTarget(p, "ignored", i)
				if !errors.Is(err, ErrKiroEndpointsExhausted) || target != (Target{}) {
					t.Fatalf("attempt %d returned %+v, %v", i, target, err)
				}
			}
		}
	}
}

func TestKiroConfiguredEndpointFirstAndBounded(t *testing.T) {
	for _, configured := range []string{"https://proxy.example/kiro", "https://proxy.example/kiro/generateAssistantResponse", "https://q.us-east-1.amazonaws.com", "https://runtime.us-east-1.kiro.dev"} {
		p := kiroRef()
		p.BaseURL = "https://runtime.us-east-1.kiro.dev"
		p.Connection.Settings.BaseURL = configured
		count := 0
		seen := map[string]bool{}
		for ; count < 5; count++ {
			target, err := ResolveTarget(p, "ignored", count)
			if errors.Is(err, ErrKiroEndpointsExhausted) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if seen[target.BaseURL] {
				t.Fatalf("repeated host: %s", target.BaseURL)
			}
			seen[target.BaseURL] = true
			if count == 0 && target.BaseURL != configured && target.BaseURL+"/"+kiroOperation != configured {
				t.Fatalf("configured first: %+v", target)
			}
		}
		want := 4
		if configured == "https://q.us-east-1.amazonaws.com" || configured == "https://runtime.us-east-1.kiro.dev" {
			want = 3
		}
		if count != want {
			t.Fatalf("attempt count %d, want %d", count, want)
		}
	}
	p := kiroRef()
	p.Connection.Settings.Region = "eu-west-1"
	target, err := ResolveTarget(p, "ignored")
	if err != nil || target.BaseURL != "https://codewhisperer.eu-west-1.amazonaws.com" {
		t.Fatalf("configured Amazon region: %+v %v", target, err)
	}
	for _, region := range []string{"../bad", "eu-west-1.evil.test", "us-east-1:443", "eu-west-\n1"} {
		p.Connection.Settings.Region = region
		_, err := ResolveTarget(p, "ignored")
		var refused *TargetError
		if !errors.As(err, &refused) {
			t.Fatalf("unsafe region %q accepted: %v", region, err)
		}
	}
}

func BenchmarkKiroResolveTarget(b *testing.B) {
	p := kiroRef()
	p.BaseURL = "https://runtime.us-east-1.kiro.dev"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ResolveTarget(p, "ignored"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestKiroConfiguredEndpointUsesExistingURLPolicy(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1", "http://169.254.169.254", "https://user:password@proxy.example", "https://proxy.example?target=other"} {
		p := kiroRef()
		p.Connection.Settings.BaseURL = base
		target, err := ResolveTarget(p, "ignored")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.URL(transport.ValidatePublicURL); err == nil {
			t.Fatalf("unsafe configured base accepted: %s", base)
		}
	}
}
