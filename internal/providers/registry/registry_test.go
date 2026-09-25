package registry

import "testing"

func TestProviderIdentityBindsNativeProtocolsAndCopiesTransports(t *testing.T) {
	catalog, err := NewCatalog([]Spec{{ID: "deepseek", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.deepseek.com"}})
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog.Lookup("deepseek")
	if !ok || catalog.Len() != 1 {
		t.Fatalf("catalog lookup %v %d", ok, catalog.Len())
	}
	if transport, ok := spec.NativeBinding("anthropic-messages"); !ok || transport != TransportAnthropic {
		t.Fatalf("native binding %q %v", transport, ok)
	}
	spec.Transports[0] = "mutated"
	again, _ := catalog.Lookup("deepseek")
	if again.Transports[0] != TransportOpenAIChat {
		t.Fatal("catalog transport slice leaked")
	}
}

func TestProviderCatalogRejectsInvalidAndDuplicateEntries(t *testing.T) {
	if _, err := NewCatalog([]Spec{{ID: "bad", Auth: AuthAPIKey}}); err == nil {
		t.Fatal("accepted provider without transport")
	}
	spec := Spec{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone}
	if _, err := NewCatalog([]Spec{spec, spec}); err == nil {
		t.Fatal("accepted duplicate provider")
	}
}
