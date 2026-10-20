package hub_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/ingress"
	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

const fixtureProtocol = "fixture-eventstream"

type fixtureSnapshot struct{ snapshot *runtime.RuntimeSnapshot }

func (s fixtureSnapshot) Load() (*runtime.RuntimeSnapshot, error) { return s.snapshot, nil }

func fixtureFrame(kind, payload string) []byte {
	header := []byte{11}
	header = append(header, []byte(":event-type")...)
	header = append(header, 7, 0, byte(len(kind)))
	header = append(header, []byte(kind)...)
	frame := make([]byte, 16+len(header)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(header)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], header)
	copy(frame[12+len(header):], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

func fixtureDirectRoutes(t *testing.T) {
	t.Helper()
	hub.WithTestLegs(t,
		hub.Leg{From: hub.Hub, To: fixtureProtocol, Kind: hub.Request},
		hub.Leg{From: fixtureProtocol, To: hub.Hub, Kind: hub.Response},
		hub.Leg{From: anthropic.MessagesProtocol, To: fixtureProtocol, Kind: hub.Request},
		hub.Leg{From: fixtureProtocol, To: anthropic.MessagesProtocol, Kind: hub.Response},
	)
	for _, direction := range []struct {
		from, to string
		kind     hub.Kind
	}{
		{anthropic.MessagesProtocol, fixtureProtocol, hub.Request},
		{fixtureProtocol, anthropic.MessagesProtocol, hub.Response},
	} {
		route, ok := hub.Route(direction.from, direction.to, direction.kind)
		if !ok || len(route) != 1 || route[0] != (hub.Leg{From: direction.from, To: direction.to, Kind: direction.kind}) {
			t.Fatalf("direct route lost to available Chat pivot: %+v, %v", route, ok)
		}
	}
	if !hub.CanTranslate(anthropic.MessagesProtocol, fixtureProtocol) {
		t.Fatal("direct pair unavailable")
	}
	if hub.CanTranslate(fixtureProtocol, anthropic.MessagesProtocol) {
		t.Fatal("provider wire advertised as client wire")
	}
}

func fixtureTranslation(request *anthropic.MessagesRequest, target string) (ingress.ChatTranslation, error) {
	route, ok := hub.Route(anthropic.MessagesProtocol, target, hub.Request)
	if !ok || len(route) != 1 || route[0].To != fixtureProtocol {
		return ingress.ChatTranslation{}, errors.New("fixture requires direct request leg")
	}
	responseRoute, ok := hub.Route(target, anthropic.MessagesProtocol, hub.Response)
	if !ok || len(responseRoute) != 1 {
		return ingress.ChatTranslation{}, errors.New("fixture requires direct response leg")
	}
	return ingress.ChatTranslation{
		Endpoint: "direct",
		Body:     request.Raw,
		Response: func([]byte) ([]byte, error) { return nil, errors.New("fixture streams only") },
		Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
			writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
			terminal := false
			err := concerns.ReadEventStream(ctx, src, func(event concerns.EventStreamEvent) (bool, error) {
				if terminal {
					return false, errors.New("fixture event after terminal")
				}
				switch event.Headers[":event-type"] {
				case "piece":
					value, err := concerns.WireObject(event.Payload)
					if err != nil {
						return false, err
					}
					return false, writer.Event("fixture_piece", value)
				case "end":
					if string(event.Payload) != "null" {
						return false, errors.New("invalid fixture terminal")
					}
					terminal = true
					return true, nil
				default:
					return false, errors.New("unknown fixture event")
				}
			})
			if err != nil {
				if ctx.Err() == nil {
					_ = writer.Event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}})
				}
				return err
			}
			return writer.Event("message_stop", map[string]any{"type": "message_stop"})
		},
	}, nil
}

func fixtureHandler(t *testing.T, upstream string, outcomes *[]ingress.RequestOutcome) http.Handler {
	t.Helper()
	snapshot, err := (runtime.Compiler{}).Compile(runtime.Config{Settings: map[string]string{"requireApiKey": "false"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ingress.New(fixtureSnapshot{snapshot}, ingress.Options{
		AllowPrivateUpstreams: true,
		TranslateMessages:     fixtureTranslation,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "fixture", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, _, source string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "fixture", ConnectionID: "fixture", Protocol: fixtureProtocol, BaseURL: upstream}, hub.CanTranslate(source, fixtureProtocol)
		},
		OnRequestComplete: func(outcome ingress.RequestOutcome) { *outcomes = append(*outcomes, outcome) },
	}).Attach(mux)
	return mux
}

func TestDirectLegEventStreamThroughIngress(t *testing.T) {
	fixtureDirectRoutes(t)
	requestBody := `{"model":"fixture/model","stream":true,"max_tokens":7,"system":"direct-only","messages":[{"role":"user","content":[{"type":"text","text":"ping","fixture_extension":42}]}]}`
	piece := fixtureFrame("piece", `{"text":"synthetic"}`)
	end := fixtureFrame("end", "null")
	corrupt := bytes.Clone(end)
	corrupt[len(corrupt)-1] ^= 1
	for _, tc := range []struct {
		name    string
		frames  []byte
		status  int
		success bool
	}{
		{"complete", append(bytes.Clone(piece), end...), 200, true},
		{"absent", nil, 200, false},
		{"no terminal", piece, 200, false},
		{"truncated", append(bytes.Clone(piece), end[:len(end)-1]...), 200, false},
		{"bad CRC", append(bytes.Clone(piece), corrupt...), 200, false},
		{"unknown content event", append(fixtureFrame("new-piece", `{"text":"must not disappear"}`), end...), 200, false},
		{"null content", append(fixtureFrame("piece", "null"), end...), 200, false},
		{"array content", append(fixtureFrame("piece", "[]"), end...), 200, false},
		{"trailing garbage", append(append(bytes.Clone(piece), end...), byte(0)), 200, false},
		{"trailing frame", append(append(bytes.Clone(piece), end...), piece...), 200, false},
		{"upstream error", []byte(`{"error":"fixture denied"}`), 400, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != requestBody || r.URL.Path != "/direct" {
					t.Errorf("direct request changed: path=%s body=%s err=%v", r.URL.Path, body, err)
				}
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.WriteHeader(tc.status)
				for _, value := range tc.frames {
					_, _ = w.Write([]byte{value})
					w.(http.Flusher).Flush()
				}
			}))
			defer upstream.Close()
			var outcomes []ingress.RequestOutcome
			handler := fixtureHandler(t, upstream.URL, &outcomes)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(requestBody)))
			if calls.Load() != 1 {
				t.Fatalf("replayed %d attempts", calls.Load())
			}
			if tc.status != 200 {
				if w.Code != tc.status || !bytes.Equal(w.Body.Bytes(), tc.frames) {
					t.Fatalf("upstream error changed: %d %s", w.Code, w.Body)
				}
				return
			}
			body := w.Body.String()
			if strings.Contains(body, "event: message_stop") != tc.success || strings.Contains(body, "event: error") == tc.success {
				t.Fatalf("success=%v body=%s", tc.success, body)
			}
			if tc.success && strings.Count(body, "event: message_stop") != 1 {
				t.Fatal("duplicate success terminal")
			}
			wantStatus := http.StatusBadGateway
			if tc.success {
				wantStatus = http.StatusOK
			}
			if len(outcomes) != 1 || outcomes[0].Status != wantStatus || outcomes[0].InputTokens != 0 || outcomes[0].OutputTokens != 0 {
				t.Fatalf("outcomes=%+v", outcomes)
			}
		})
	}
}

func TestDirectLegEventStreamCancellation(t *testing.T) {
	fixtureDirectRoutes(t)
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(fixtureFrame("piece", `{"text":"first"}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()
	var outcomes []ingress.RequestOutcome
	server := httptest.NewServer(fixtureHandler(t, upstream.URL, &outcomes))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{"model":"fixture/model","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	prefix := make([]byte, len("event: fixture_piece\n"))
	if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != "event: fixture_piece\n" {
		t.Fatalf("incremental output=%s err=%v", prefix, err)
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream read survived client disconnect")
	}
}
