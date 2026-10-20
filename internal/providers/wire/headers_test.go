package wire

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// delta reports how headers differ from base: added/changed and removed names.
func delta(base, got http.Header) (changed map[string]string, removed []string) {
	changed = map[string]string{}
	for name, values := range got {
		if !reflect.DeepEqual(base[name], values) {
			changed[name] = values[0]
		}
	}
	for name := range base {
		if _, ok := got[name]; !ok {
			removed = append(removed, name)
		}
	}
	return changed, removed
}

func TestAzureHeadersCrossProduct(t *testing.T) {
	for _, token := range []string{"", "az-key"} {
		for _, org := range []string{"", "  ", "org-1"} {
			ref := ref("azure", chat, "https://a.example", routing.ProviderSettings{Organization: org})
			ref.APIToken = token
			got := OpenAIHeaders(ref)
			if got.Get("Authorization") != "" {
				t.Errorf("token %q org %q: bearer sent to Azure: %v", token, org, got)
			}
			if (got.Get("Api-Key") != "") != (token != "") || got.Get("Api-Key") != token {
				t.Errorf("token %q: api-key %q", token, got.Get("Api-Key"))
			}
			wantOrg := ""
			if org == "org-1" {
				wantOrg = "org-1"
			}
			if got.Get("OpenAI-Organization") != wantOrg {
				t.Errorf("org %q: header %q", org, got.Get("OpenAI-Organization"))
			}
		}
	}
}

func TestKimiHeadersCrossProduct(t *testing.T) {
	for _, device := range []string{"", "  ", "dev-123"} {
		for _, protocol := range []string{chat, responses, messages} {
			for _, token := range []string{"", "k"} {
				r := ref("kimi", protocol, "https://k.example", routing.ProviderSettings{DeviceID: device})
				r.APIToken = token
				var got http.Header
				var legacy http.Header
				if protocol == messages {
					got, legacy = MessagesHeaders(r, "", ""), legacyAnthropicHeaders("", "", r)
				} else {
					got, legacy = OpenAIHeaders(r), legacyOpenAIProviderHeaders(r)
				}
				changed, removed := delta(legacy, got)
				wantID := "dev-123"
				if device != "dev-123" {
					wantID = "routeweft"
				}
				want := map[string]string{"X-Msh-Platform": "routeweft", "X-Msh-Device-Id": wantID, "X-Msh-Device-Name": "routeweft"}
				if !reflect.DeepEqual(changed, want) || len(removed) != 0 {
					t.Errorf("device %q %s token %q: delta %v removed %v", device, protocol, token, changed, removed)
				}
			}
		}
	}
}

func TestKilocodeOrgHeaderCrossProduct(t *testing.T) {
	for _, org := range []string{"", "  ", "org-9"} {
		for _, token := range []string{"", "t"} {
			r := ref("kilocode", chat, "https://k.example", routing.ProviderSettings{OrgID: org})
			r.APIToken = token
			changed, removed := delta(legacyOpenAIProviderHeaders(r), OpenAIHeaders(r))
			want := map[string]string{}
			if org == "org-9" {
				want["X-Kilocode-Organizationid"] = "org-9"
			}
			if !reflect.DeepEqual(changed, want) || len(removed) != 0 {
				t.Errorf("org %q token %q: delta %v removed %v", org, token, changed, removed)
			}
		}
	}
}

// Providers that got no header change still match the frozen legacy result; the
// three that did are the only ones whose OpenAI headers differ.
func TestOnlyDeclaredProvidersChangedHeaders(t *testing.T) {
	for _, id := range providerIDs() {
		r := ref(id, chat, "https://x.example", routing.ProviderSettings{DeviceID: "d", OrgID: "o", Organization: "g"})
		same := reflect.DeepEqual(OpenAIHeaders(r), legacyOpenAIProviderHeaders(r))
		if same == headersChanged[id] {
			t.Errorf("%s: unchanged=%v but headersChanged=%v", id, same, headersChanged[id])
		}
	}
}
