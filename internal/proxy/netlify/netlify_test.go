package netlify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type pin struct {
	Bundle struct {
		FunctionCode string `json:"functionCode"`
		IndexHTML    string `json:"indexHtml"`
		ZipBase64    string `json:"zipBase64"`
		ZipSHA256    string `json:"zipSha256"`
		IndexSHA1    string `json:"indexSha1"`
		FunctionName string `json:"functionName"`
		FunctionPath string `json:"functionPath"`
		API          string `json:"api"`
		RelayURLs    []struct {
			Site *string `json:"site"`
			URL  string  `json:"url"`
		} `json:"relayUrls"`
		Headers map[string]string `json:"headers"`
	} `json:"bundle"`
	Deploy []struct {
		Name      string          `json:"name"`
		Body      Request         `json:"body"`
		API       map[string]any  `json:"api"`
		Throws    *string         `json:"throws"`
		ClockStep int64           `json:"clockStep"`
		Calls     []pinnedCall    `json:"calls"`
		Created   []pinnedPool    `json:"created"`
		Status    int             `json:"status"`
		Answer    json.RawMessage `json:"answer"`
	} `json:"deploy"`
}

type pinnedCall struct {
	Key     string            `json:"key"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

type pinnedPool struct {
	Name        string `json:"name"`
	ProxyURL    string `json:"proxyUrl"`
	Type        string `json:"type"`
	NoProxy     string `json:"noProxy"`
	IsActive    bool   `json:"isActive"`
	StrictProxy bool   `json:"strictProxy"`
}

func loadPin(t *testing.T) pin {
	t.Helper()
	raw, err := os.ReadFile("../testdata/proxy_pools_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var p pin
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// The relay function, the archive and the digests are the oracle's, byte for byte.
func TestRelayBundleIsTheOraclesByteForByte(t *testing.T) {
	p := loadPin(t)
	if RelayFunctionCode != p.Bundle.FunctionCode || IndexHTML != p.Bundle.IndexHTML {
		t.Fatal("the relay function or the placeholder page differs from the oracle's")
	}
	archive, sum := RelayFunctionZip()
	want, _ := base64.StdEncoding.DecodeString(p.Bundle.ZipBase64)
	if !bytes.Equal(archive, want) || sum != p.Bundle.ZipSHA256 {
		t.Fatalf("archive differs from the oracle's (sha256 %s, oracle %s)", sum, p.Bundle.ZipSHA256)
	}
	if _, indexSum := IndexFile(); indexSum != p.Bundle.IndexSHA1 {
		t.Fatalf("index sha1 %s, oracle %s", indexSum, p.Bundle.IndexSHA1)
	}
	if FunctionName != p.Bundle.FunctionName || FunctionPath != p.Bundle.FunctionPath || API != p.Bundle.API {
		t.Fatal("endpoint constants differ from the oracle's")
	}
	for _, c := range p.Bundle.RelayURLs {
		site := ""
		if c.Site != nil {
			site = *c.Site
		}
		if got := RelayURL(site); got != c.URL {
			t.Errorf("RelayURL(%q) = %q, oracle %q", site, got, c.URL)
		}
	}
}

type recorded struct {
	key     string
	headers http.Header
	body    []byte
}

// TestProvisionReplaysTheOraclesDeployRoute runs the oracle's 25 deploy scenarios
// against a scripted Netlify API: the same calls in the same order with the same
// bodies, and the same status and message for every outcome.
func TestProvisionReplaysTheOraclesDeployRoute(t *testing.T) {
	p := loadPin(t)
	for _, c := range p.Deploy {
		t.Run(c.Name, func(t *testing.T) {
			token := c.Body.Token()
			if token == "" {
				if c.Status != http.StatusBadRequest {
					t.Fatalf("oracle answered %d for a request with no token", c.Status)
				}
				return // the handler answers 400 before any call; see the API tests
			}
			var mu sync.Mutex
			var seen []recorded
			queues := map[string]int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.Throws != nil {
					hijack(w)
					return
				}
				body, _ := io.ReadAll(r.Body)
				key := r.Method + " " + strings.TrimPrefix(r.URL.RequestURI(), "")
				mu.Lock()
				seen = append(seen, recorded{key, r.Header.Clone(), body})
				queue := c.API[key]
				var scripted map[string]any
				switch typed := queue.(type) {
				case []any:
					index := queues[key]
					if index >= len(typed) {
						index = len(typed) - 1
					}
					queues[key]++
					scripted, _ = typed[index].(map[string]any)
				case map[string]any:
					scripted = typed
				}
				mu.Unlock()
				if scripted == nil {
					t.Errorf("unscripted call %s", key)
					http.Error(w, "unscripted", 599)
					return
				}
				w.WriteHeader(int(scripted["status"].(float64)))
				if answer, present := scripted["json"]; present {
					_ = json.NewEncoder(w).Encode(answer)
				}
			}))
			defer server.Close()
			var clock int64 = 1700000000000
			client := Client{API: server.URL, HTTP: server.Client(),
				Now:   func() time.Time { clock += c.ClockStep; return time.UnixMilli(clock) },
				Sleep: func(context.Context, time.Duration) error { return nil }}
			start := time.UnixMilli(1700000000000)
			result, failure := client.Provision(context.Background(), token, SiteName(c.Body.RequestedName(), start))

			var want struct {
				Error     string `json:"error"`
				ProxyPool *struct {
					Name     string `json:"name"`
					ProxyURL string `json:"proxyUrl"`
				} `json:"proxyPool"`
				DeployURL string `json:"deployUrl"`
			}
			_ = json.Unmarshal(c.Answer, &want)
			if c.Status == http.StatusCreated {
				if failure != nil {
					t.Fatalf("failed with %d %q; oracle created the pool", failure.Status, failure.Message)
				}
				if result.RelayURL != want.DeployURL || result.RelayURL != want.ProxyPool.ProxyURL || result.Name != want.ProxyPool.Name {
					t.Errorf("result %+v, oracle %+v", result, want)
				}
			} else {
				if failure == nil {
					t.Fatalf("succeeded; oracle answered %d %s", c.Status, c.Answer)
				}
				if failure.Status != c.Status {
					t.Errorf("status %d, oracle %d", failure.Status, c.Status)
				}
				if c.Throws == nil && failure.Message != want.Error {
					t.Errorf("message %q, oracle %q", failure.Message, want.Error)
				}
			}
			if c.Throws != nil {
				return // the oracle's network error text is Node's, not ours
			}
			if len(seen) != len(c.Calls) {
				t.Fatalf("%d calls, oracle made %d", len(seen), len(c.Calls))
			}
			for i, want := range c.Calls {
				got := seen[i]
				if got.key != want.Key {
					t.Errorf("call %d: %s, oracle %s", i, got.key, want.Key)
				}
				if auth := got.headers.Get("Authorization"); auth != want.Headers["Authorization"] {
					t.Errorf("call %d: Authorization %q, oracle %q", i, auth, want.Headers["Authorization"])
				}
				if got.headers.Get("User-Agent") != want.Headers["User-Agent"] {
					t.Errorf("call %d: User-Agent %q, oracle %q", i, got.headers.Get("User-Agent"), want.Headers["User-Agent"])
				}
				if want.Headers["Content-Type"] != "" && got.headers.Get("Content-Type") != want.Headers["Content-Type"] {
					t.Errorf("call %d: Content-Type %q, oracle %q", i, got.headers.Get("Content-Type"), want.Headers["Content-Type"])
				}
				compareBody(t, i, got.body, want.Body)
			}
		})
	}
}

func compareBody(t *testing.T, call int, got []byte, want json.RawMessage) {
	t.Helper()
	if string(want) == "null" || len(want) == 0 {
		if len(got) != 0 {
			t.Errorf("call %d: sent a body the oracle did not", call)
		}
		return
	}
	var binary struct {
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(want, &binary) == nil && binary.SHA256 != "" {
		sum := sha256.Sum256(got)
		if len(got) != binary.Bytes || hex.EncodeToString(sum[:]) != binary.SHA256 {
			t.Errorf("call %d: upload of %d bytes (sha256 %x), oracle %d bytes (%s)", call, len(got), sum, binary.Bytes, binary.SHA256)
		}
		return
	}
	var a, b any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal(want, &b)
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if !bytes.Equal(ga, gb) {
		t.Errorf("call %d: body %s, oracle %s", call, ga, gb)
	}
}

func hijack(w http.ResponseWriter) {
	if h, ok := w.(http.Hijacker); ok {
		if conn, _, err := h.Hijack(); err == nil {
			conn.Close()
		}
	}
}

func TestSiteNameSanitizing(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	for requested, want := range map[string]string{
		"My Relay_01!!": "my-relay-01", "  Edge  ": "edge", "a--b": "a-b", "-x-": "x", "UPPER": "upper", "ünï": "n", "": "relay-loyw3v28", "!!!": "relay-loyw3v28",
	} {
		if got := SiteName(requested, now); got != want {
			t.Errorf("SiteName(%q) = %q, want %q", requested, got, want)
		}
	}
}

func TestTokenNeverAppearsInAFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hijack(w) }))
	defer server.Close()
	client := Client{API: server.URL + "/secret-nfp_token_value", HTTP: server.Client()}
	_, failure := client.Provision(context.Background(), "nfp_token_value", "x")
	if failure == nil || strings.Contains(failure.Message, "nfp_token_value") {
		t.Fatalf("failure=%+v", failure)
	}
}
