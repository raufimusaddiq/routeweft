package registry

import "testing"

// A new provider cannot ship without a readiness value: every row of the served
// catalog carries one of the three statuses, and a non-ready one says why.
func TestEveryBuiltinDeclaresReadiness(t *testing.T) {
	catalog, err := NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range Builtins() {
		got, _ := catalog.Lookup(spec.ID)
		if !got.Readiness.Status.valid() {
			t.Errorf("%s has no readiness (status %q)", spec.ID, got.Readiness.Status)
		}
		if got.Readiness.Status != ProviderReady && got.Readiness.Reason == "" {
			t.Errorf("%s is %s without a reason", spec.ID, got.Readiness.Status)
		}
		if got.Readiness.Status == ProviderReady && got.Readiness.Reason != "" {
			t.Errorf("%s is ready but carries reason %q", spec.ID, got.Readiness.Reason)
		}
	}
}

// notReady must name real providers, so a rename cannot orphan an entry and
// silently turn an unavailable provider ready.
func TestNotReadyNamesOnlyBuiltins(t *testing.T) {
	ids := map[string]bool{}
	for _, spec := range Builtins() {
		ids[spec.ID] = true
	}
	for id := range notReady {
		if !ids[id] {
			t.Errorf("notReady names %q, which is not a built-in provider", id)
		}
	}
}

// Readiness is a property of the build: neither credentials nor enablement are
// inputs to it, so a ready provider is ready without any account.
func TestReadinessIsIndependentOfAccountState(t *testing.T) {
	catalog, err := NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	groq, _ := catalog.Lookup("groq")
	if groq.Readiness.Status != ProviderReady || !groq.Readiness.Served() {
		t.Errorf("groq = %+v, want ready with no connection involved", groq.Readiness)
	}
	cursor, _ := catalog.Lookup("cursor")
	if cursor.Readiness.Status != ProviderUnavailable || cursor.Readiness.Served() {
		t.Errorf("cursor = %+v, want unavailable", cursor.Readiness)
	}
}

func TestReadinessValidation(t *testing.T) {
	base := Spec{ID: "x", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, ModelCatalog: CatalogStatic}
	for name, readiness := range map[string]Readiness{
		"unknown status":         {Status: "beta"},
		"unavailable no reason":  {Status: ProviderUnavailable},
		"experimental no reason": {Status: ProviderExperimental},
		"reason without status":  {Reason: "why"},
	} {
		spec := base
		spec.Readiness = readiness
		if spec.Validate() == nil {
			t.Errorf("%s: Validate accepted %+v", name, readiness)
		}
	}
	spec := base
	spec.Readiness = Readiness{Status: ProviderReady}
	if err := spec.Validate(); err != nil {
		t.Errorf("ready rejected: %v", err)
	}
}
