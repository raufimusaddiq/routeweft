package ingress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
)

func TestSystemOneStreamUsageAtEOFAndAfterOversizedOpaqueLine(t *testing.T) {
	s := &usageScanner{family: shared.FamilySystemOne}
	chunk := []byte(strings.Repeat("x", 32<<10))
	for n := 0; n < 130; n++ {
		s.Write(chunk)
	}
	if len(s.buffer) > 4<<20 {
		t.Fatal("unbounded native telemetry buffer")
	}
	s.Write([]byte("\ndata: {\"result\":{\"usage\":{\"input_tokens\":18,\"output_tokens\":2}}}"))
	usage, ok := s.usage()
	if !ok || usage.InputTokens != 18 || usage.OutputTokens != 2 || !s.reportsUsage(true) || s.reportsUsage(false) {
		t.Fatalf("clean EOF usage=%+v found=%v", usage, ok)
	}
}

type brokenSystemOneBody struct{}

func (brokenSystemOneBody) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
func (brokenSystemOneBody) Close() error             { return nil }

func TestSystemOneRelayDistinguishesReadFailureFromEOF(t *testing.T) {
	h := New(nil, Options{})
	r := httptest.NewRequest(http.MethodPost, "/v1/systemone", nil)
	response := &http.Response{Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}
	if err := h.copyNativeSSE(httptest.NewRecorder(), r, response); err != nil {
		t.Fatalf("clean EOF: %v", err)
	}
	response.Body = brokenSystemOneBody{}
	if err := h.copyNativeSSE(httptest.NewRecorder(), r, response); err == nil {
		t.Fatal("truncated stream reported clean EOF")
	}
}
