package usage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

type mimoClientFunc func(*http.Request) (*http.Response, error)

func (f mimoClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestMimoAccountSessionUsage(t *testing.T) {
	for _, tc := range []struct {
		body  string
		want  float64
		reset time.Time
	}{
		{`{"code":0,"data":{"percent":94,"resetDate":"2026-10-27"}}`, .94, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)},
		{`{"code":0,"data":{"percent":0,"resetDate":"2026-10-27","resetAt":1793232000}}`, 0, time.Unix(1793232000, 0).UTC()},
		{`{"code":0,"data":{"percent":12.6}}`, .13, time.Time{}},
		{`{"code":0,"data":{"percent":"94"}}`, .94, time.Time{}},
		{`{"code":"0","data":{"percent":94}}`, .94, time.Time{}},
		{`{"code":0,"data":{"percent":94,"resetAt":"1793232000"}}`, .94, time.Unix(1793232000, 0).UTC()},
		{`{"code":"0","data":{"percent":"94","resetDate":"2026-10-27","resetAt":"1793232000"}}`, .94, time.Unix(1793232000, 0).UTC()},
		{`{"code":0,"data":{"percent":"94","resetDate":"2026-10-27","resetAt":"unknown"}}`, .94, time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.body, func(t *testing.T) {
			calls := 0
			callback := mimoAccountBase + "/api/sts"
			client := mimoClientFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Header.Get("Authorization") != "" {
					t.Fatal("API key sent to account service")
				}
				h := http.Header{}
				body := ""
				status := http.StatusOK
				switch calls {
				case 1:
					if req.URL.String() != mimoAccountBase+"/api/user/xiaomi/me" || !strings.Contains(req.Header.Get("Cookie"), "passToken=fixture-pass") {
						t.Fatal("missing account credential")
					}
					h.Set("Location", "https://account.xiaomi.com/pass/serviceLogin?callback="+url.QueryEscape(callback))
					status = 302
				case 2:
					if req.URL.Host != "account.xiaomi.com" || req.URL.Query().Get("sid") != "passportapi" {
						t.Fatal("passportapi request")
					}
					body = `&&&START&&&{"nonce":123,"ssecurity":"fixture-security","location":"https://account.xiaomi.com/pass/confirm?nonce=123"}`
				case 3:
					digest := sha1.Sum([]byte("nonce=123&fixture-security"))
					if req.URL.Query().Get("clientSign") != base64.StdEncoding.EncodeToString(digest[:]) {
						t.Fatal("incorrect signature")
					}
					h.Add("Set-Cookie", "serviceToken=fixture-passport; Path=/")
					status = 302
				case 4:
					if req.URL.Query().Get("sid") != "mimopc" || req.URL.Query().Get("callback") != callback || !strings.Contains(req.Header.Get("Cookie"), "serviceToken=fixture-passport") {
						t.Fatal("mimopc request")
					}
					body = `&&&START&&&{"location":"` + callback + `?ticket=fixture-ticket"}`
				case 5:
					if req.URL.Path != "/api/sts" || !strings.Contains(req.Header.Get("Cookie"), "serviceToken=fixture-passport") {
						t.Fatal("sts request")
					}
					h.Add("Set-Cookie", `serviceToken="fixture-session"; Path=/`)
					h.Add("Set-Cookie", "malformed")
					h.Add("Set-Cookie", "mimopc_slh=; Path=/")
					h.Add("Set-Cookie", "mimopc_ph=fixture-ph; Path=/")
				case 6:
					if req.URL.String() != mimoAccountBase+"/api/user/usage" || req.Header.Get("User-Agent") != mimoAPIUserAgent {
						t.Fatal("usage endpoint")
					}
					cookie := req.Header.Get("Cookie")
					if !strings.Contains(cookie, "serviceToken=fixture-session") || !strings.Contains(cookie, "mimopc_ph=fixture-ph") || strings.Contains(cookie, "passToken") || strings.Contains(cookie, "cUserId") {
						t.Fatal("usage cookie scope")
					}
					body = tc.body
				default:
					t.Fatal("unexpected request")
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			readers, err := Readers(client, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture", ProviderData: map[string]string{"mimoPassToken": "fixture-pass", "mimoUserId": "fixture-user", "mimoCUserId": "fixture-cuser"}})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 6 || len(state.Windows) != 1 {
				t.Fatalf("calls=%d windows=%d", calls, len(state.Windows))
			}
			w := state.Windows[0]
			if w.Name != "Weekly" || w.Remaining == nil || *w.Remaining != tc.want {
				t.Fatal("incorrect remaining quota")
			}
			if tc.reset.IsZero() {
				if w.ResetAt != nil {
					t.Fatal("invented reset")
				}
			} else if w.ResetAt == nil || !w.ResetAt.Equal(tc.reset) {
				t.Fatal("incorrect reset")
			}
		})
	}
}

func TestMimoMissingAndFailedSessionNeverInventsQuotaOrLeaks(t *testing.T) {
	var logs bytes.Buffer
	oldLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLog) })
	oldSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(oldSlog) })
	for _, data := range []map[string]string{nil, {"mimoPassToken": "fixture-account-secret", "mimoUserId": "fixture-account-user", "mimoCUserId": "fixture-account-cuser"}} {
		client := mimoClientFunc(func(req *http.Request) (*http.Response, error) {
			if data == nil {
				return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}
			return nil, errors.New("fixture-account-secret fixture-account-user fixture-account-cuser")
		})
		readers, _ := Readers(client, nil)
		state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture", ProviderData: data})
		if err == nil || err.Error() != mimoSessionRequired || len(state.Windows) != 0 {
			t.Fatal("missing session must report unknown/message without windows")
		}
		encoded, _ := json.Marshal(state)
		if strings.Contains(string(encoded), `"remaining":0`) {
			t.Fatal("unknown became exhausted")
		}
		for _, secret := range []string{"fixture-account-secret", "fixture-account-user", "fixture-account-cuser"} {
			if strings.Contains(string(encoded)+err.Error()+logs.String(), secret) {
				t.Fatal("account credential leaked")
			}
		}
	}
}

func TestMimoRejectsUntrustedSessionRedirects(t *testing.T) {
	for _, raw := range []string{"http://account.xiaomi.com/pass/serviceLogin", "https://account.xiaomi.com.evil.test/pass/serviceLogin", "https://account.xiaomi.com:444/pass/serviceLogin", "https://user:password@account.xiaomi.com/pass/serviceLogin", "https://127.0.0.1/pass/serviceLogin"} {
		calls := 0
		client := mimoClientFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {raw}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		readers, _ := Readers(client, nil)
		state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{ProviderData: map[string]string{"mimoPassToken": "fixture-pass"}})
		if err == nil || calls != 1 || len(state.Windows) != 0 {
			t.Fatal("untrusted redirect was not refused")
		}
	}
}

func TestMimoEmptyCredentialIsUnknown(t *testing.T) {
	readers, _ := Readers(mimoClientFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("empty credential made a request")
		return nil, nil
	}), nil)
	state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{})
	if err == nil || err.Error() != "Xiaomi MiMo Desktop not connected. Add credentials to view usage." || len(state.Windows) != 0 {
		t.Fatal("empty credential did not stay unknown")
	}
}

func TestMimoSessionFailureStagesDoNotExposeCredentials(t *testing.T) {
	for failed := 1; failed <= 6; failed++ {
		calls := 0
		client := mimoClientFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == failed {
				return nil, errors.New("fixture-private-pass fixture-private-user fixture-private-cuser")
			}
			h := http.Header{}
			body := ""
			switch calls {
			case 1:
				h.Set("Location", "https://account.xiaomi.com/pass/serviceLogin?callback="+url.QueryEscape(mimoAccountBase+"/api/sts"))
			case 2:
				body = `&&&START&&&{"location":"https://account.xiaomi.com/pass/confirm?nonce=123"}`
			case 3:
				h.Add("Set-Cookie", "serviceToken=fixture-account; Path=/")
			case 4:
				body = `&&&START&&&{"location":"` + mimoAccountBase + `/api/sts?ticket=fixture"}`
			case 5:
				h.Add("Set-Cookie", "serviceToken=fixture-service; Path=/")
			}
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		readers, _ := Readers(client, nil)
		state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{ProviderData: map[string]string{"mimoPassToken": "fixture-private-pass", "mimoUserId": "fixture-private-user", "mimoCUserId": "fixture-private-cuser"}})
		if err == nil || len(state.Windows) != 0 || calls != failed {
			t.Fatalf("failure stage %d did not stop cleanly", failed)
		}
		if strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("account credential leaked in failure")
		}
	}
}

func TestMimoInvalidValuesNeverInventQuota(t *testing.T) {
	for _, body := range []string{
		`{"code":"7","data":{"percent":"94"}}`,
		`{"code":null,"data":{"percent":"94"}}`,
		`{"code":"invalid","data":{"percent":"94"}}`,
		`{"code":"0","data":{"percent":null}}`,
		`{"code":"0","data":{"percent":"invalid"}}`,
		`{"code":"0","data":{"percent":"NaN"}}`,
		`{"code":"0","data":{"percent":"Inf"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			state, err := reader(t, "xiaomi-mimo", &stubClient{status: http.StatusOK, body: body}).Usage(context.Background(), quota.Credential{AccessToken: "sk-fixture"})
			if err == nil || len(state.Windows) != 0 {
				t.Fatal("invalid quota value accepted")
			}
		})
	}
}

func TestMimoReadRejectsCookieInjection(t *testing.T) {
	for _, field := range []string{"mimoPassToken", "mimoUserId", "mimoCUserId"} {
		data := map[string]string{"mimoPassToken": "fixture-pass"}
		data[field] = "fixture-private; other=secret"
		readers, _ := Readers(mimoClientFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid account credential made a request")
			return nil, nil
		}), nil)
		state, err := readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{ProviderData: data})
		if err == nil || len(state.Windows) != 0 || strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("invalid account value accepted or echoed")
		}
	}
}
