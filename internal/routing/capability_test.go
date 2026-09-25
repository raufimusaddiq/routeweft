package routing

import (
	"reflect"
	"testing"
)

func TestOrderComboKeepsFallbackCandidates(t *testing.T) {
	members := []Member{
		{ModelID: "text", Capabilities: nil},
		{ModelID: "vision", Capabilities: []string{"vision"}},
		{ModelID: "text2"},
	}
	ordered := OrderCombo(members, []CapabilityRequirement{{Name: "vision"}})
	if ordered[0].ModelID != "vision" || len(ordered) != 3 {
		t.Fatalf("order=%+v", ordered)
	}
	if ordered[1].ModelID != "text" || ordered[2].ModelID != "text2" {
		t.Fatalf("non-capable order not stable: %+v", ordered)
	}
}

func TestAdapterCandidatesNoopWhenDisabledOrEmpty(t *testing.T) {
	pool := []Member{{ModelID: "vision", Capabilities: []string{"vision"}}}
	if got := AdapterCandidates([]CapabilityRequirement{{Name: "vision", AdapterEnabled: false, AdapterPool: pool}}, nil); len(got) != 0 {
		t.Fatalf("disabled adapter produced %+v", got)
	}
	if got := AdapterCandidates([]CapabilityRequirement{{Name: "vision", AdapterEnabled: true, AdapterPool: nil}}, nil); len(got) != 0 {
		t.Fatalf("empty pool produced %+v", got)
	}
	got := AdapterCandidates([]CapabilityRequirement{{Name: "vision", AdapterEnabled: true, AdapterPool: pool}}, nil)
	if len(got) != 1 || got[0].ModelID != "vision" {
		t.Fatalf("adapter=%+v", got)
	}
}

func TestTrimHistoryForContextPreservesHeadAndTail(t *testing.T) {
	messages := []string{"sys", "old1", "old2", "old3", "user"}
	trimmed := TrimHistoryForContext(messages, 1, 1, 3)
	if !reflect.DeepEqual(trimmed, []string{"sys", "old1", "user"}) {
		t.Fatalf("trim=%v", trimmed)
	}
	// Tail wins when both protected regions cannot fit.
	if got := TrimHistoryForContext(messages, 3, 3, 3); !reflect.DeepEqual(got, []string{"sys", "old3", "user"}) {
		t.Fatalf("tight trim=%v", got)
	}
	// No trimming needed returns a copy.
	short := []string{"a", "b"}
	kept := TrimHistoryForContext(short, 1, 1, 5)
	if !reflect.DeepEqual(kept, short) {
		t.Fatalf("kept=%v", kept)
	}
}
