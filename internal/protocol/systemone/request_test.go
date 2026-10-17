package systemone

import (
	"strings"
	"testing"
)

func TestQuestionsAreExactlyANonEmptyObject(t *testing.T) {
	for _, questions := range []string{"null", "[]", `["question"]`, `{}`, `"question"`, "0", "false"} {
		_, err := ParseRequest([]byte(`{"model":"jev-latest","state":null,"questions":` + questions + `}`))
		if err == nil || err.Error() != "questions must be a non-empty object" {
			t.Errorf("questions=%s error=%v", questions, err)
		}
	}
	for _, raw := range []string{
		`{"model":"jev-latest","state":null}`,
		`{"model":"jev-latest","questions":{"q":null}}`,
		`{"state":{},"questions":{"q":true}}`,
		`{"model":" ","state":{},"questions":{"q":true}}`,
		`{"model":"jev-latest","state":{},"questions":{"q":true}} {}`,
		`[]`,
	} {
		if _, err := ParseRequest([]byte(raw)); err == nil {
			t.Errorf("invalid envelope accepted: %s", raw)
		}
	}
	// Only the questions container is ours to validate; values belong to the provider.
	raw := ` {"model":"jev-latest","state":null,"questions":{"q":null,"future":[1,true]},"future":9007199254740993} `
	request, err := ParseRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := request.MarshalBody("jev-latest")
	if err != nil || string(got) != raw {
		t.Fatalf("native body changed: %s %v", got, err)
	}
	got, err = request.MarshalBody("mapped")
	if err != nil || !strings.Contains(string(got), `"model":"mapped"`) || !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("model mapping lost typed data: %s %v", got, err)
	}
}
