package promptcache

import (
	"bytes"
	"encoding/json"
	"testing"
)

func count(t *testing.T, body []byte) int {
	t.Helper()
	var v map[string]json.RawMessage
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	n := 0
	var blocks []map[string]json.RawMessage
	for _, key := range []string{"system", "tools"} {
		blocks = nil
		_ = json.Unmarshal(v[key], &blocks)
		for _, b := range blocks {
			if b["cache_control"] != nil {
				n++
			}
		}
	}
	var messages []struct {
		Content []map[string]json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(v["messages"], &messages)
	for _, m := range messages {
		for _, b := range m.Content {
			if b["cache_control"] != nil {
				n++
			}
		}
	}
	return n
}

func TestAnchorNPlusOneAndRetainsFields(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"stable"}],"tools":[{"name":"deferred","defer_loading":true}],"messages":[{"role":"user","content":[{"type":"text","text":"first"}]},{"role":"assistant","content":[{"type":"thinking","signature":"redacted"},{"type":"tool_use","name":"deferred"}]},{"role":"user","content":[{"type":"text","text":"next"}]}]}`)
	got, err := Anchor(body, 4)
	if err != nil || count(t, got) != 3 {
		t.Fatalf("N anchors=%d err=%v", count(t, got), err)
	}
	if !bytes.Contains(got, []byte("signature")) || !bytes.Contains(got, []byte("defer_loading")) {
		t.Fatalf("fields lost: %s", got)
	}
	next := bytes.Replace(body, []byte(`"text":"next"`), []byte(`"text":"next","future":true`), 1)
	got, err = Anchor(next, 4)
	if err != nil || count(t, got) != 3 {
		t.Fatalf("N+1 anchors=%d err=%v", count(t, got), err)
	}
	var fields struct {
		Messages []struct {
			Content []map[string]json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatal(err)
	}
	last := fields.Messages[len(fields.Messages)-1].Content
	if last[len(last)-1]["cache_control"] != nil {
		t.Fatal("active N+1 user turn was anchored")
	}
}

func TestAnchorClientMarkerBudgetAndIdempotence(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"t"}],"messages":[{"role":"user","content":[{"type":"text","text":"u","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"answer"}]}`)
	got, err := Anchor(body, 4)
	if err != nil || count(t, got) != 3 || !bytes.Contains(got, []byte(`"ttl":"1h"`)) {
		t.Fatalf("markers=%d err=%v body=%s", count(t, got), err, got)
	}
	got, err = Anchor(got, 4)
	if err != nil || count(t, got) != 3 {
		t.Fatalf("not idempotent: %s err=%v", got, err)
	}
	got, err = Anchor(body, 2)
	if err != nil || count(t, got) != 2 {
		t.Fatalf("budget: %s err=%v", got, err)
	}
}

func TestAnchorMalformedAndUnanchorable(t *testing.T) {
	for _, b := range [][]byte{[]byte(`[]`), []byte(`not json`)} {
		if _, err := Anchor(b, 4); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
	plainSystem := []byte(`{"system":"invalid"}`)
	if got, err := Anchor(plainSystem, 4); err != nil || !bytes.Equal(got, plainSystem) {
		t.Fatalf("plain system should remain unchanged: %s err=%v", got, err)
	}
	b := []byte(`{"future":{"keep":true},"messages":[{"role":"user","content":"plain"}]}`)
	got, err := Anchor(b, 4)
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("body changed: %s err=%v", got, err)
	}
}
