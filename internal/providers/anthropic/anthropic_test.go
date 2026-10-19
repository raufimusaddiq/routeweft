package anthropic

import "testing"

func TestFirstPartyIdentityAndHeaders(t *testing.T) {
	if ID != "anthropic" || DefaultBaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("identity=%s %s", ID, DefaultBaseURL)
	}
	models := Models()
	if len(models) != 3 {
		t.Fatalf("models=%v", models)
	}
	models[0] = "mutated"
	if Models()[0] == "mutated" {
		t.Fatal("Models leaked slice")
	}
	got := Headers()
	if got["anthropic-version"] != Version || got["Anthropic-Beta"] != Beta {
		t.Fatalf("headers=%v", got)
	}
	got["Anthropic-Beta"] = "mutated"
	if Headers()["Anthropic-Beta"] != Beta {
		t.Fatal("Headers leaked mutable state")
	}
}
