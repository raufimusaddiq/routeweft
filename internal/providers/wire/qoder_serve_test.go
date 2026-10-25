package wire

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// qoderDecodeBody undoes QoderEncodeBody; the pin has no decoder (the gateway holds it),
// so this exists only to prove the encoding loses nothing.
func qoderDecodeBody(encoded []byte) []byte {
	var inverse [256]byte
	for i := range inverse {
		inverse[i] = byte(i)
	}
	for i := 0; i < 64; i++ {
		inverse[qoderCustomAlphabet[i]] = qoderStdAlphabet[i]
	}
	inverse['$'] = '='
	std := make([]byte, len(encoded))
	for i, c := range encoded {
		std[i] = inverse[c]
	}
	n := len(std)
	a := n / 3
	// encoded = [tail a][middle][head a]
	original := append(append(append([]byte{}, std[n-a:]...), std[a:n-a]...), std[:a]...)
	plain, err := base64.StdEncoding.DecodeString(string(original))
	if err != nil {
		panic(err)
	}
	return plain
}

func TestQoderEncodeBodyRoundTrips(t *testing.T) {
	for n := 0; n < 400; n++ {
		plain := make([]byte, n)
		for i := range plain {
			plain[i] = byte(i*131 + n)
		}
		encoded := QoderEncodeBody(plain)
		for _, c := range encoded {
			if c >= 0x80 {
				t.Fatalf("%d bytes: encoded byte %#x is not ASCII", n, c)
			}
		}
		if got := qoderDecodeBody(encoded); !bytes.Equal(got, plain) {
			t.Fatalf("%d bytes do not round trip", n)
		}
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after--; w.after < 0 {
		return 0, errors.New("client gone")
	}
	return len(p), nil
}

func TestQoderUnwrapSSEStopsWhenTheClientLeaves(t *testing.T) {
	const frames = "data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"a\\\"}}]}\"}\n\n"
	t.Run("a write error ends it", func(t *testing.T) {
		err := QoderUnwrapSSE(context.Background(), strings.NewReader(frames+frames), &failingWriter{}, QoderStreamOptions{Model: "qoder/auto"})
		if err == nil || err.Error() != "client gone" {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("a cancelled context ends it before reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		src := &scriptedBody{pieces: [][]byte{[]byte(frames)}}
		var out bytes.Buffer
		if err := QoderUnwrapSSE(ctx, src, &out, QoderStreamOptions{}); !errors.Is(err, context.Canceled) || out.Len() != 0 {
			t.Fatalf("error %v wrote %q", err, out.String())
		}
	})
	t.Run("the terminal frame ends it without waiting for the socket", func(t *testing.T) {
		hold := make(chan struct{})
		defer close(hold)
		src := &scriptedBody{pieces: [][]byte{[]byte(frames + "data: [DONE]\n\n")}, hold: hold}
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- QoderUnwrapSSE(context.Background(), src, &out, QoderStreamOptions{}) }()
		select {
		case err := <-done:
			if err != nil || !strings.HasSuffix(out.String(), "data: [DONE]\n\n") {
				t.Fatalf("error %v wrote %q", err, out.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("still reading the keepalive socket after the terminal frame")
		}
	})
}

// A first-frame error is reported before a byte is written, so the caller can still
// answer with an HTTP status (billing as 403, which is what lets the account fall back).
func TestQoderFirstFrameErrorWritesNothing(t *testing.T) {
	var out bytes.Buffer
	src := strings.NewReader(": keepalive\n\ndata: {\"statusCodeValue\":403,\"body\":\"{\\\"code\\\":\\\"112\\\",\\\"pricingUrl\\\":\\\"x\\\"}\"}\n\n")
	err := QoderUnwrapSSE(context.Background(), src, &out, QoderStreamOptions{})
	var answer *QoderAnswerError
	if !errors.As(err, &answer) || !answer.Billing || answer.Status != 403 || out.Len() != 0 {
		t.Fatalf("error %v answer %+v wrote %q", err, answer, out.String())
	}
}

// Signing covers the bytes that are sent: the encoded body, not the envelope before it.
func TestQoderPrepareChatSignsTheEncodedBody(t *testing.T) {
	doer := &scriptedDoer{}
	var clock time.Duration
	client := clientAt(doer, BuildCosyHeaders, &clock)
	creds := QoderCredentials{AccessToken: "dt-synthetic", UserID: "user-1", MachineID: "machine-1", DisplayName: "n", Email: "e@example.invalid"}
	catalog := &QoderCatalog{configs: map[string]QoderModelConfig{}, expires: qoderNow.Add(time.Hour)}
	config, err := ParseQoderModelConfig([]byte(`{"key":"auto","max_output_tokens":1024}`))
	if err != nil {
		t.Fatal(err)
	}
	catalog.keys, catalog.configs["auto"] = []string{"auto"}, config
	client.catalogs = map[string]*QoderCatalog{qoderCatalogKey(creds): catalog}

	prepare := func() QoderChatRequest {
		request, err := client.PrepareChat(context.Background(), QoderChatInput{Model: "qoder/auto", Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`), Credentials: creds})
		if err != nil {
			t.Fatal(err)
		}
		return request
	}
	first, second := prepare(), prepare()
	if len(doer.calls) != 0 {
		t.Fatalf("preparing a request made %d calls; the catalog was cached", len(doer.calls))
	}
	if !strings.HasSuffix(first.URL, "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1") {
		t.Errorf("url %s", first.URL)
	}
	plain := qoderDecodeBody(first.Body)
	if !strings.HasPrefix(string(plain), `{"request_id":`) || !strings.Contains(string(plain), `"messages":[{"role":"user","content":"hi"}]`) {
		t.Errorf("the body does not decode to the envelope: %s", plain)
	}
	h := first.Headers
	sum := md5.Sum(first.Body)
	if h["Cosy-Bodyhash"] != hex.EncodeToString(sum[:]) || h["Cosy-Bodylength"] != strconv.Itoa(len(first.Body)) {
		t.Errorf("body hash %s length %s do not describe the %d bytes sent", h["Cosy-Bodyhash"], h["Cosy-Bodylength"], len(first.Body))
	}
	payload := strings.Split(strings.TrimPrefix(h["Authorization"], "Bearer COSY."), ".")
	if got := cosySignature(payload[0], h["Cosy-Key"], h["Cosy-Date"], first.Body, h["Cosy-Sigpath"]); got != payload[1] {
		t.Errorf("signature %s does not cover the sent bytes (%s)", payload[1], got)
	}
	if h["Cosy-Sigpath"] != "/api/v2/service/pro/sse/agent_chat_generation" || h["Accept-Encoding"] != "identity" || h["X-Model-Key"] != "auto" || h["X-Model-Source"] != "system" {
		t.Errorf("headers %v", h)
	}
	// A signature carries its request id, which the gateway refuses to see twice (code 103):
	// every request is signed afresh, never replayed.
	if first.Headers["Authorization"] == second.Headers["Authorization"] || first.Headers["X-Request-Id"] == second.Headers["X-Request-Id"] {
		t.Error("two requests share a signature")
	}
}

// -race: the caches are shared by every request.
func TestQoderClientIsSafeUnderConcurrency(t *testing.T) {
	var clock time.Duration
	client := clientAt(lockedDoer{}, stubSigner, &clock)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			creds := QoderCredentials{APIKey: "pt-" + strconv.Itoa(i%3), UserID: "stored", MachineID: "m"}
			if _, err := client.Catalog(context.Background(), creds, i%5 == 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

// lockedDoer answers the three Qoder endpoints from any goroutine.
type lockedDoer struct{}

func (lockedDoer) Do(r *http.Request) (*http.Response, error) {
	body := `{"chat":[{"key":"auto"}]}`
	switch {
	case strings.HasSuffix(r.URL.Path, "/jobToken/exchange"):
		body = `{"token":"jt-job"}`
	case strings.HasSuffix(r.URL.Path, "/userinfo"):
		body = `{"id":"user-from-info"}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func stubSigner(_ []byte, _ string, _ CosyCredentials) (map[string]string, error) {
	return map[string]string{"Authorization": "COSY.stub"}, nil
}
