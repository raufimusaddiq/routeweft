package routing

import "testing"

func TestBuildPlanValidatesAndDefaultsTarget(t *testing.T) {
	provider := ProviderRef{ProviderID: "openai", Protocol: "openai-chat", BaseURL: "https://api.openai.com/v1"}
	plan, err := BuildPlan(PlanOptions{SourceProtocol: "openai-chat", Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NativePath() || plan.TargetProtocol != "openai-chat" {
		t.Fatalf("plan %+v", plan)
	}

	cross := provider
	cross.Protocol = "anthropic-messages"
	plan, err = BuildPlan(PlanOptions{SourceProtocol: "openai-chat", Provider: cross})
	if err != nil {
		t.Fatal(err)
	}
	if plan.NativePath() {
		t.Fatal("cross-protocol plan reported native")
	}
}

func TestBuildPlanRejectsIncompleteProvider(t *testing.T) {
	for _, provider := range []ProviderRef{{}, {ProviderID: "p"}, {ProviderID: "p", BaseURL: "https://x"}} {
		if provider.BaseURL == "https://x" {
			continue
		}
		if _, err := BuildPlan(PlanOptions{SourceProtocol: "openai-chat", Provider: provider}); err == nil {
			t.Fatalf("accepted provider %+v", provider)
		}
	}
}

func TestAttemptBudgetBoundsRetries(t *testing.T) {
	budget := DefaultAttemptBudget()
	if !budget.Allow(0) || budget.Allow(1) {
		t.Fatalf("default budget wrong: %+v", budget)
	}
	if (AttemptBudget{}).Allow(0) {
		t.Fatal("zero budget must not allow attempts")
	}
}
