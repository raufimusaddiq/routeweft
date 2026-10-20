package ingress

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestQoderDispatchSignsFinalRequest(t *testing.T) {
	up := newRecorder(t)
	ref := routing.ProviderRef{ProviderID: "qoder", Protocol: "openai-chat", BaseURL: up.srv.URL + "/algo/final", APIToken: "fixture-token", UpstreamModel: "upstream-model",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{UserID: "fixture-user", MachineID: "stored-machine"}}}
	out := send(t, ref, "/v1/chat/completions", chatBody)
	if out.Code != 200 {
		t.Fatalf("status %d %s", out.Code, out.Body)
	}
	got := up.only(t)
	if got.Path != "/algo/final/chat/completions" || !strings.Contains(got.Body, `"model":"upstream-model"`) {
		t.Fatalf("final request path %q body %s", got.Path, got.Body)
	}
	hash := func(value string) string {
		sum := md5.Sum([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	parts := strings.Split(strings.TrimPrefix(got.Header.Get("Authorization"), "Bearer COSY."), ".")
	if len(parts) != 2 {
		t.Fatal("missing COSY authorization")
	}
	canonical := parts[0] + "\n" + got.Header.Get("Cosy-Key") + "\n" + got.Header.Get("Cosy-Date") + "\n" + got.Body + "\n/final/chat/completions"
	if parts[1] != hash(canonical) {
		t.Fatal("signature does not cover the final body and resolved URL")
	}
	want := map[string]string{
		"Authorization": got.Header.Get("Authorization"), "Cosy-Key": got.Header.Get("Cosy-Key"),
		"Cosy-User": "fixture-user", "Cosy-Date": got.Header.Get("Cosy-Date"), "Cosy-Version": "1.0.0",
		"Cosy-Machineid": "stored-machine", "Cosy-Machinetoken": "stored-machine", "Cosy-Machinetype": "5",
		"Cosy-Machineos": "x86_64_windows", "Cosy-Clienttype": "5", "Cosy-Clientip": "127.0.0.1",
		"Cosy-Bodyhash": hash(got.Body), "Cosy-Bodylength": strconv.Itoa(len(got.Body)),
		"Cosy-Sigpath": "/final/chat/completions", "Cosy-Data-Policy": "disagree",
		"Cosy-Organization-Id": "", "Cosy-Organization-Tags": "", "Login-Version": "v2",
		"X-Request-Id": got.Header.Get("X-Request-Id"),
	}
	for name, value := range want {
		if values := got.Header.Values(name); len(values) != 1 || values[0] != value {
			t.Errorf("header %s = %q, want [%q]", name, values, value)
		}
	}
	for _, name := range []string{"Cosy-Key", "Cosy-Date", "X-Request-Id"} {
		if got.Header.Get(name) == "" {
			t.Errorf("empty %s", name)
		}
	}
}

func TestQoderMissingIdentityFailsBeforeNetwork(t *testing.T) {
	for _, missing := range []string{"machineId", "userId", "token"} {
		t.Run(missing, func(t *testing.T) {
			up := newRecorder(t)
			ref := routing.ProviderRef{ProviderID: "qoder", Protocol: "openai-chat", BaseURL: up.srv.URL, APIToken: "fixture-token",
				Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{UserID: "fixture-user", MachineID: "stored-machine"}}}
			switch missing {
			case "machineId":
				ref.Connection.Settings.MachineID = " "
			case "userId":
				ref.Connection.Settings.UserID = ""
			case "token":
				ref.APIToken = ""
			}
			out := send(t, ref, "/v1/chat/completions", chatBody)
			if out.Code < 400 {
				t.Fatalf("missing %s accepted: %d", missing, out.Code)
			}
			if missing == "machineId" && (!strings.Contains(out.Body.String(), "machineId") || !strings.Contains(out.Body.String(), "re-run Qoder sign-in")) {
				t.Fatalf("missing recovery instruction: %s", out.Body)
			}
			up.mu.Lock()
			defer up.mu.Unlock()
			if len(up.seen) != 0 {
				t.Fatal("unsigned request reached upstream")
			}
		})
	}
}
