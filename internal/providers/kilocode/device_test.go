package kilocode

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type scripted struct {
	status int
	body   string
	seen   []*http.Request
}

func (s *scripted) Do(request *http.Request) (*http.Response, error) {
	s.seen = append(s.seen, request)
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader(s.body)), Header: http.Header{}}, nil
}

func TestRequestDevice(t *testing.T) {
	client := &scripted{status: 200, body: `{"code":"ABCD-1234","verificationUrl":"https://kilo.ai/device-auth?code=ABCD-1234","expiresIn":600}`}
	device, err := RequestDevice(context.Background(), Config{}, client)
	if err != nil {
		t.Fatal(err)
	}
	if device.Code != "ABCD-1234" || device.VerificationURL != "https://kilo.ai/device-auth?code=ABCD-1234" || device.ExpiresIn.Seconds() != 600 || device.Interval != DefaultPollInterval {
		t.Fatalf("unexpected device %+v", device)
	}
	if request := client.seen[0]; request.Method != http.MethodPost || request.URL.String() != DefaultInitiateURL {
		t.Fatalf("unexpected request %s %s", request.Method, request.URL)
	}
}

func TestRequestDeviceDefaultsAndRejections(t *testing.T) {
	device, err := RequestDevice(context.Background(), Config{}, &scripted{status: 200, body: `{"code":"A1","verificationUrl":"https://kilo.ai/x"}`})
	if err != nil || device.ExpiresIn.Seconds() != 300 {
		t.Fatalf("missing expiresIn should default to 300s: %+v %v", device, err)
	}
	if _, err := RequestDevice(context.Background(), Config{}, &scripted{status: 429}); err != ErrRateLimited {
		t.Fatalf("429 must map to ErrRateLimited, got %v", err)
	}
	for name, body := range map[string]string{
		"no code":         `{"verificationUrl":"https://kilo.ai/x"}`,
		"path code":       `{"code":"../x","verificationUrl":"https://kilo.ai/x"}`,
		"http url":        `{"code":"A1","verificationUrl":"http://kilo.ai/x"}`,
		"javascript url":  `{"code":"A1","verificationUrl":"javascript:alert(1)"}`,
		"invalid payload": `not json`,
	} {
		if _, err := RequestDevice(context.Background(), Config{}, &scripted{status: 200, body: body}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := RequestDevice(context.Background(), Config{}, &scripted{status: 500, body: "secret upstream text"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("upstream error text must not leak: %v", err)
	}
}

func TestPollDeviceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   Outcome
		token  string
	}{
		{"pending 202", 202, "", OutcomePending, ""},
		{"denied", 403, "", OutcomeDenied, ""},
		{"expired", 410, "", OutcomeExpired, ""},
		{"approved", 200, `{"status":"approved","token":"tok-sentinel","userEmail":"a@b.c"}`, OutcomeApproved, "tok-sentinel"},
		{"200 not yet approved", 200, `{"status":"pending"}`, OutcomePending, ""},
		{"approved without token", 200, `{"status":"approved"}`, OutcomePending, ""},
	} {
		client := &scripted{status: tc.status, body: tc.body}
		poll, err := PollDevice(context.Background(), Config{}, "ABCD-1234", client)
		if err != nil || poll.Outcome != tc.want || poll.Token != tc.token {
			t.Errorf("%s: got %+v err=%v", tc.name, poll, err)
		}
		if client.seen[0].Method != http.MethodGet || client.seen[0].URL.String() != DefaultInitiateURL+"/ABCD-1234" {
			t.Errorf("%s: unexpected poll request %s", tc.name, client.seen[0].URL)
		}
	}
	if _, err := PollDevice(context.Background(), Config{}, "ABCD-1234", &scripted{status: 500}); err == nil {
		t.Fatal("a 500 poll is a fault, not an outcome")
	}
	client := &scripted{status: 200}
	if _, err := PollDevice(context.Background(), Config{}, "../../admin", client); err == nil || len(client.seen) != 0 {
		t.Fatal("a code that is not one path segment must be refused before any request")
	}
}
