package cline

import (
	"strings"
	"testing"
)

func TestAccessTokenPrefixFollowsTokenShape(t *testing.T) {
	const jwt = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.sanitized"
	for _, tc := range []struct{ name, token, want string }{
		{"workos jwt gets the prefix", jwt, "workos:" + jwt},
		{"already prefixed is kept", "workos:abc", "workos:abc"},
		{"prefix match ignores case and keeps it", "WorkOS:abc", "WorkOS:abc"},
		{"clinepass key is verbatim", "clp_sanitized", "clp_sanitized"},
		{"jwt-like without a payload is verbatim", "eyJhbGciOi", "eyJhbGciOi"},
		{"surrounding space is trimmed", "  " + jwt + "\n", "workos:" + jwt},
		{"blank is empty", "   ", ""},
		{"empty is empty", "", ""},
	} {
		if got := AccessToken(tc.token); got != tc.want {
			t.Errorf("%s: AccessToken(%q)=%q want %q", tc.name, tc.token, got, tc.want)
		}
	}
	if Authorization("clp_x") != "Bearer clp_x" || Authorization("") != "" {
		t.Fatal("authorization value wrong")
	}
}

func TestHeadersCarryTheFullClineSet(t *testing.T) {
	headers := Headers("clp_sanitized")
	for name, want := range map[string]string{
		"HTTP-Referer": "https://cline.bot", "X-Title": "Cline", "X-CLIENT-TYPE": "routeweft",
		"X-IS-MULTIROOT": "false", "Authorization": "Bearer clp_sanitized",
	} {
		if headers[name] != want {
			t.Errorf("%s=%q want %q", name, headers[name], want)
		}
	}
	if !strings.HasPrefix(headers["User-Agent"], "Routeweft/") {
		t.Errorf("User-Agent=%q", headers["User-Agent"])
	}
	for _, name := range []string{"X-PLATFORM", "X-PLATFORM-VERSION", "X-CLIENT-VERSION", "X-CORE-VERSION"} {
		if headers[name] == "" {
			t.Errorf("%s missing", name)
		}
	}
	if _, ok := Headers("")["Authorization"]; ok {
		t.Error("a blank token must send no Authorization")
	}
}

func TestUnwrapEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		unwrapped        bool
	}{
		{"success", `{"success":true,"data":{"id":"x","z":1,"a":2}}`, `{"id":"x","z":1,"a":2}`, true},
		{"success with whitespace", ` { "success" : true , "data" : { "id" : "x" } } `, `{ "id" : "x" }`, true},
		{"error envelope", `{"success":false,"data":{"id":"x"}}`, `{"success":false,"data":{"id":"x"}}`, false},
		{"success is a string", `{"success":"true","data":{"id":"x"}}`, `{"success":"true","data":{"id":"x"}}`, false},
		{"data null", `{"success":true,"data":null}`, `{"success":true,"data":null}`, false},
		{"data array", `{"success":true,"data":[1]}`, `{"success":true,"data":[1]}`, false},
		{"data scalar", `{"success":true,"data":"x"}`, `{"success":true,"data":"x"}`, false},
		{"data missing", `{"success":true}`, `{"success":true}`, false},
		{"bare OpenAI body", `{"id":"x","choices":[]}`, `{"id":"x","choices":[]}`, false},
		{"invalid json", `not json`, `not json`, false},
		{"json null", `null`, `null`, false},
		{"json array", `[{"success":true,"data":{}}]`, `[{"success":true,"data":{}}]`, false},
	} {
		got, ok := UnwrapEnvelope([]byte(tc.body))
		if string(got) != tc.want || ok != tc.unwrapped {
			t.Errorf("%s: got %q,%v want %q,%v", tc.name, got, ok, tc.want, tc.unwrapped)
		}
	}
}

func TestAppliesToExactlyTheTwoClineProviders(t *testing.T) {
	for id, want := range map[string]bool{"cline": true, "clinepass": true, "openai": false, "kilocode": false, "": false} {
		if Applies(id) != want {
			t.Errorf("Applies(%q)=%v want %v", id, !want, want)
		}
	}
}
