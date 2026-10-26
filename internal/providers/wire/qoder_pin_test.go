package wire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testdata/qoder_pin.json is what the pinned Qoder executor, model service and
// shared/qoder modules do, recorded by compat/parity/generate-qoder.mjs at 1c3b9f47
// with the network, clock, uuid, random and the COSY signer scripted. Nothing here
// calls Qoder and no credential is used. Long runs of one character are stored as
// `@@rep:"unit":n@@` and expanded here, in the inputs and the expected outputs alike.

var qoderNow = time.UnixMilli(1_700_000_000_000)

var qoderMarker = regexp.MustCompile(`@@rep:("(?:[^"\\]|\\.)*"):(\d+)@@`)

func expandQoderMarkers(value any) any {
	switch v := value.(type) {
	case string:
		if !strings.Contains(v, "@@rep:") {
			return v
		}
		return qoderMarker.ReplaceAllStringFunc(v, func(m string) string {
			parts := qoderMarker.FindStringSubmatch(m)
			unquoted, err := strconv.Unquote(parts[1])
			if err != nil {
				panic(err)
			}
			n, _ := strconv.Atoi(parts[2])
			return strings.Repeat(unquoted, n)
		})
	case []any:
		for i := range v {
			v[i] = expandQoderMarkers(v[i])
		}
	case *jsObject:
		for k, item := range v.values {
			v.values[k] = expandQoderMarkers(item)
		}
	}
	return value
}

var qoderPinCache *jsObject

func qoderPin(t *testing.T) *jsObject {
	t.Helper()
	if qoderPinCache == nil {
		raw, err := os.ReadFile("testdata/qoder_pin.json")
		if err != nil {
			t.Fatal(err)
		}
		value, err := parseJS(raw)
		if err != nil {
			t.Fatal(err)
		}
		qoderPinCache = expandQoderMarkers(value).(*jsObject)
	}
	return qoderPinCache
}

// ---- fixture readers -----------------------------------------------------------------

func fxObj(v any) *jsObject { o, _ := v.(*jsObject); return o }
func fxList(v any) []any    { l, _ := v.([]any); return l }
func fxStr(v any) string    { s, _ := v.(string); return s }
func fxNum(v any) float64   { f, _ := jsNumeric(v); return f }
func fxText(v any) string {
	if v == nil {
		return ""
	}
	return jsToString(v)
}

func fxCreds(v any) QoderCredentials {
	o := fxObj(v)
	psd := fxObj(o.get("providerSpecificData"))
	return QoderCredentials{
		APIKey: fxStr(o.get("apiKey")), AccessToken: fxStr(o.get("accessToken")), RefreshToken: fxStr(o.get("refreshToken")),
		UserID: fxText(psd.get("userId")), MachineID: fxStr(psd.get("machineId")),
		DisplayName: fxStr(o.get("displayName")), Email: fxStr(o.get("email")),
	}
}

func fxCount(t *testing.T, section string) int {
	t.Helper()
	n := len(fxList(qoderPin(t).get(section)))
	if n == 0 {
		t.Fatalf("fixture section %q is empty", section)
	}
	return n
}

func sameJS(t *testing.T, label string, got, want any) {
	t.Helper()
	if g, w := stringifyJS(got), stringifyJS(want); g != w {
		t.Errorf("%s\n got  %s\n want %s", label, clip(g), clip(w))
	}
}

func clip(s string) string {
	if len(s) > 1500 {
		return s[:1500] + fmt.Sprintf("... (%d bytes)", len(s))
	}
	return s
}

func sequentialUUID() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
}

func testEnv() qoderEnv {
	return qoderEnv{uuid: sequentialUUID(), now: func() time.Time { return qoderNow }}
}

// ---- encoding ---------------------------------------------------------------------------

func TestQoderEncodeBodyMatchesPin(t *testing.T) {
	for i, c := range fxList(qoderPin(t).get("encoding")) {
		o := fxObj(c)
		plain, err := hex.DecodeString(fxStr(o.get("hex")))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(QoderEncodeBody(plain)), fxStr(o.get("encoded")); got != want {
			t.Errorf("case %d (%d bytes): %q, want %q", i, len(plain), clip(got), clip(want))
		}
	}
	fxCount(t, "encoding")
}

// ---- usage and the coalescer ------------------------------------------------------------

func TestQoderUsageMatchesPin(t *testing.T) {
	for i, c := range fxList(qoderPin(t).get("usage")) {
		o := fxObj(c)
		var got any
		if usage := canonicalQoderUsage(o.get("usage")); usage != nil {
			got = usage
		}
		sameJS(t, fmt.Sprintf("case %d usage %s", i, stringifyJS(o.get("usage"))), got, o.get("canonical"))
	}
	fxCount(t, "usage")
}

type scriptedBody struct {
	pieces [][]byte
	fail   error
	hold   chan struct{} // an open socket: nothing more arrives until it closes
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if len(b.pieces) > 0 {
		n := copy(p, b.pieces[0])
		if n < len(b.pieces[0]) {
			b.pieces[0] = b.pieces[0][n:]
		} else {
			b.pieces = b.pieces[1:]
		}
		return n, nil
	}
	if b.fail != nil {
		return 0, b.fail
	}
	if b.hold != nil {
		<-b.hold
	}
	return 0, io.EOF
}

func scriptedPieces(chunks []any) [][]byte {
	var out [][]byte
	for _, c := range chunks {
		if o, ok := c.(*jsObject); ok {
			raw, _ := hex.DecodeString(fxStr(o.get("hex")))
			out = append(out, raw)
		} else {
			out = append(out, []byte(fxStr(c)))
		}
	}
	return out
}

func unwrap(t *testing.T, o *jsObject) (body string, err error) {
	t.Helper()
	hold := make(chan struct{})
	defer close(hold)
	src := &scriptedBody{pieces: scriptedPieces(fxList(o.get("chunks")))}
	if fail := fxStr(o.get("fail")); fail != "" {
		src.fail = errors.New(fail)
	}
	if o.get("keepOpen") == true {
		src.hold = hold
	}
	var out bytes.Buffer
	finished := make(chan error, 1)
	go func() {
		finished <- QoderUnwrapSSE(context.Background(), src, &out, QoderStreamOptions{Model: orDefault(fxStr(o.get("model")), "qoder/auto"), Now: func() time.Time { return qoderNow }})
	}()
	select {
	case err = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the stream did not end (the socket is open)", fxStr(o.get("name")))
	}
	return out.String(), err
}

func TestQoderUnwrapSSEMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("sse")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			body, err := unwrap(t, o)
			if rejected := fxStr(o.get("rejected")); rejected != "" {
				if err == nil || err.Error() != rejected || body != "" {
					t.Fatalf("err=%v body=%q, want the read failure %q and no output", err, body, rejected)
				}
				return
			}
			status := int(fxNum(o.get("status")))
			var answer *QoderAnswerError
			if errors.As(err, &answer) {
				if body != "" {
					t.Errorf("a first-frame error wrote %q", body)
				}
				if answer.Status != status || string(answer.Body()) != fxStr(o.get("body")) || fxStr(o.get("contentType")) != "application/json" {
					t.Errorf("answer %d %s, want %d %s", answer.Status, answer.Body(), status, fxStr(o.get("body")))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if status != 200 {
				t.Fatalf("pin answered %d %s, this wrote a stream", status, fxStr(o.get("body")))
			}
			if body != fxStr(o.get("body")) {
				t.Errorf("stream\n got  %q\n want %q", body, fxStr(o.get("body")))
			}
		})
	}
	fxCount(t, "sse")
}

func TestQoderBillingBlockMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("billing")) {
		o := fxObj(c)
		input, isText := o.get("input").(string)
		if got := isText && IsQoderBillingBlock(input); got != (o.get("billing") == true) {
			t.Errorf("%s: %v", stringifyJS(o.get("input")), got)
		}
	}
	fxCount(t, "billing")
}

// ---- the envelope ---------------------------------------------------------------------------

type uploadRecord struct {
	MediaType string
	Bytes     int
	Sha256    string
}

func scriptedUploader(script *jsObject, record *[]uploadRecord) QoderUploader {
	if script == nil {
		return nil
	}
	return func(_ context.Context, up QoderUpload) (string, error) {
		sum := sha256.Sum256(up.Data)
		*record = append(*record, uploadRecord{up.MediaType, len(up.Data), hex.EncodeToString(sum[:])})
		if failure := fxStr(script.get("throws")); failure != "" {
			return "", errors.New(failure)
		}
		return fxStr(script.get("url")), nil
	}
}

func sameUploads(t *testing.T, got []uploadRecord, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("uploads: %d, want %d", len(got), len(want))
		return
	}
	for i, w := range want {
		o := fxObj(w)
		if got[i] != (uploadRecord{fxStr(o.get("mediaType")), int(fxNum(o.get("bytes"))), fxStr(o.get("sha256"))}) {
			t.Errorf("upload %d: %+v, want %s", i, got[i], stringifyJS(w))
		}
	}
}

func TestQoderEnvelopeMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("envelopes")) {
		o := fxObj(c)
		scenario := fxObj(o.get("scenario"))
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			model := fxStr(scenario.get("model"))
			key := strings.TrimPrefix(model, "qoder/")
			var config QoderModelConfig
			catalog := fxObj(scenario.get("catalog"))
			if found := fxObj(catalog.get(key)); found != nil {
				config = QoderModelConfig{obj: found}
			} else if found := fxObj(fxObj(scenario.get("refresh")).get(key)); found != nil {
				config = QoderModelConfig{obj: found}
			}
			var uploads []uploadRecord
			creds := fxCreds(scenario.get("credentials"))
			envelope, err := buildQoderEnvelope(context.Background(), QoderEnvelopeInput{
				Model: model, Body: []byte(fxStr(scenario.get("body"))), UserID: creds.UserID, Config: config,
				ContextTier: fxStr(fxObj(scenario.get("env")).get("QODER_CONTEXT_TIER")),
				Upload:      scriptedUploader(fxObj(scenario.get("upload")), &uploads),
			}, testEnv())
			if want := fxStr(o.get("error")); want != "" {
				if err == nil || err.Error() != want {
					t.Fatalf("error %v, want %q", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(envelope.Payload), fxStr(o.get("payload")); got != want {
				t.Errorf("payload\n got  %s\n want %s", clip(got), clip(want))
			}
			if envelope.Key != fxStr(o.get("key")) {
				t.Errorf("key %q, want %q", envelope.Key, fxStr(o.get("key")))
			}
			sameUploads(t, uploads, fxList(o.get("uploads")))
		})
	}
	fxCount(t, "envelopes")
}

// ---- attachments ----------------------------------------------------------------------------------

func TestQoderAttachmentsMatchPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("attachments")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			var uploads []uploadRecord
			messages := fxList(o.get("messages"))
			stats := rewriteQoderAttachments(context.Background(), messages, scriptedUploader(fxObj(o.get("upload")), &uploads))
			sameJS(t, "messages", messages, o.get("result"))
			want := fxObj(o.get("stats"))
			var urls []any
			for _, u := range stats.ImageURLs {
				urls = append(urls, u)
			}
			if urls == nil {
				urls = []any{}
			}
			sameJS(t, "image urls", urls, want.get("imageUrls"))
			if float64(stats.Uploaded) != fxNum(want.get("uploaded")) || float64(stats.Stubbed) != fxNum(want.get("stubbed")) {
				t.Errorf("stats %+v, want %s", stats, stringifyJS(want))
			}
			sameUploads(t, uploads, fxList(o.get("uploads")))
		})
	}
	fxCount(t, "attachments")
}

type scriptedDoer struct {
	steps []*jsObject
	calls []recordedRequest
}

type recordedRequest struct {
	Method, URL string
	Header      http.Header
	Body        []byte
	Length      int64
}

func (d *scriptedDoer) Do(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	d.calls = append(d.calls, recordedRequest{r.Method, r.URL.String(), r.Header.Clone(), body, r.ContentLength})
	if len(d.steps) == 0 {
		return nil, errors.New("no scripted answer")
	}
	step := d.steps[min(len(d.calls)-1, len(d.steps)-1)]
	if failure := fxStr(step.get("network")); failure != "" {
		return nil, errors.New(failure)
	}
	status := 200
	if s := step.get("status"); s != nil {
		status = int(fxNum(s))
	}
	response := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(fxStr(step.get("body"))))}
	if headers := fxObj(step.get("headers")); headers != nil {
		for k, v := range headers.values {
			response.Header.Set(k, fxStr(v))
		}
	}
	return response, nil
}

func stepsOf(v any) []*jsObject {
	var steps []*jsObject
	for _, s := range fxList(v) {
		steps = append(steps, fxObj(s))
	}
	return steps
}

type signedCall struct {
	Body []byte
	URL  string
	CosyCredentials
}

func recordingSigner(calls *[]signedCall) cosySigner {
	return func(body []byte, url string, creds CosyCredentials) (map[string]string, error) {
		*calls = append(*calls, signedCall{body, url, creds})
		return map[string]string{"Authorization": "COSY.stub", "Cosy-Stub": "1"}, nil
	}
}

// pinHeaders are the headers of a recorded request the pin sets itself: the COSY set
// and the bearer are the signer's.
func pinHeaders(o *jsObject) map[string]string {
	out := map[string]string{}
	for _, k := range o.keys {
		if !strings.EqualFold(k, "Authorization") && !strings.HasPrefix(strings.ToLower(k), "cosy-") {
			out[http.CanonicalHeaderKey(k)] = fxStr(o.values[k])
		}
	}
	return out
}

func sameSigned(t *testing.T, got []signedCall, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("signed %d requests, want %d", len(got), len(want))
	}
	for i, w := range want {
		o := fxObj(w)
		creds := fxObj(o.get("creds"))
		expect := signedCall{[]byte(fxStr(o.get("body"))), fxStr(o.get("url")), CosyCredentials{
			UserID: fxText(creds.get("userId")), AuthToken: fxStr(creds.get("authToken")), Name: fxStr(creds.get("name")),
			Email: fxStr(creds.get("email")), MachineID: fxStr(creds.get("machineId")),
		}}
		if !bytes.Equal(got[i].Body, expect.Body) || got[i].URL != expect.URL || got[i].CosyCredentials != expect.CosyCredentials {
			t.Errorf("signed %d\n got  %s %+v (%d bytes)\n want %s %+v (%d bytes)", i, got[i].URL, got[i].CosyCredentials, len(got[i].Body), expect.URL, expect.CosyCredentials, len(expect.Body))
		}
	}
}

func TestQoderImageUploadRequestMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("uploadWire")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			doer := &scriptedDoer{steps: []*jsObject{{keys: []string{"status", "body"}, values: map[string]any{"status": o.get("status"), "body": o.get("body")}}}}
			var signed []signedCall
			upload := qoderImageUploader(doer, fxCreds(o.get("credentials")), testEnv(), recordingSigner(&signed), func() string { return strconv.FormatInt(qoderNow.UnixMilli(), 16) + "8" })
			url, err := upload(context.Background(), QoderUpload{Data: []byte("PNGDATA"), MediaType: "image/png"})
			if want := fxStr(o.get("error")); want != "" {
				if err == nil || err.Error() != want {
					t.Fatalf("error %v, want %q", err, want)
				}
			} else if err != nil || url != fxStr(o.get("url")) {
				t.Fatalf("url %q err %v, want %q", url, err, fxStr(o.get("url")))
			}
			sent := fxObj(fxList(o.get("calls"))[0])
			got := doer.calls[0]
			if got.Method != fxStr(sent.get("method")) || got.URL != fxStr(sent.get("url")) || string(got.Body) != fxStr(sent.get("body")) {
				t.Errorf("request %s %s\n%q\nwant %s %s\n%q", got.Method, got.URL, got.Body, fxStr(sent.get("method")), fxStr(sent.get("url")), fxStr(sent.get("body")))
			}
			headers := map[string]string{}
			for k := range got.Header {
				if k != "Authorization" && !strings.HasPrefix(k, "Cosy-") {
					headers[k] = got.Header.Get(k)
				}
			}
			headers["Content-Length"] = strconv.FormatInt(got.Length, 10)
			want := pinHeaders(fxObj(sent.get("headers")))
			if stringifyMap(headers) != stringifyMap(want) {
				t.Errorf("headers %s, want %s", stringifyMap(headers), stringifyMap(want))
			}
			sameSigned(t, signed, fxList(o.get("signed")))
		})
	}
	fxCount(t, "uploadWire")
}

func stringifyMap(m map[string]string) string {
	o := newJSObject()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o.set(k, m[k])
	}
	return stringifyJS(o)
}

func TestQoderUploadResponseReaderMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("uploadResponses")) {
		o := fxObj(c)
		if got := qoderUploadURLFromResponse(o.get("json")); got != fxStr(o.get("url")) {
			t.Errorf("%s: %q, want %q", stringifyJS(o.get("json")), got, fxStr(o.get("url")))
		}
	}
	fxCount(t, "uploadResponses")
}

// ---- PAT exchange and the catalog -------------------------------------------------------------

func clientAt(doer QoderHTTPDoer, sign cosySigner, clock *time.Duration) *QoderClient {
	return &QoderClient{HTTP: doer, sign: sign, env: testEnv(), Now: func() time.Time { return qoderNow.Add(*clock) }}
}

func sameRequests(t *testing.T, got []recordedRequest, want []any) {
	t.Helper()
	if len(got) != len(want) {
		var urls []string
		for _, g := range got {
			urls = append(urls, g.Method+" "+g.URL)
		}
		t.Fatalf("%d requests %v, want %d", len(got), urls, len(want))
	}
	for i, w := range want {
		o := fxObj(w)
		g := got[i]
		if g.Method != fxStr(o.get("method")) || g.URL != fxStr(o.get("url")) {
			t.Errorf("request %d: %s %s, want %s %s", i, g.Method, g.URL, fxStr(o.get("method")), fxStr(o.get("url")))
		}
		if body := fxStr(o.get("body")); string(g.Body) != body {
			t.Errorf("request %d body %q, want %q", i, g.Body, body)
		}
		for k, v := range pinHeaders(fxObj(o.get("headers"))) {
			if g.Header.Get(k) != v {
				t.Errorf("request %d header %s = %q, want %q", i, k, g.Header.Get(k), v)
			}
		}
	}
}

func TestQoderPATExchangeMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("pat")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			doer := &scriptedDoer{steps: stepsOf(o.get("fetches"))}
			var clock time.Duration
			client := clientAt(doer, nil, &clock)
			for i, call := range fxList(o.get("calls")) {
				co := fxObj(call)
				clock = time.Duration(fxNum(co.get("clock"))) * time.Millisecond
				input := fxCreds(co.get("input"))
				got, err := client.ResolveCredentials(context.Background(), input)
				if want := fxStr(co.get("error")); want != "" {
					if err == nil || err.Error() != want {
						t.Errorf("call %d error %v, want %q", i, err, want)
					}
					continue
				}
				if err != nil {
					t.Errorf("call %d: %v", i, err)
					continue
				}
				resolved := fxObj(co.get("resolved"))
				psd := fxObj(resolved.get("providerSpecificData"))
				want := input
				want.AccessToken, want.APIKey = fxStr(resolved.get("accessToken")), fxStr(resolved.get("apiKey"))
				want.UserID, want.MachineID = fxText(psd.get("userId")), fxStr(psd.get("machineId"))
				if got != want {
					t.Errorf("call %d\n got  %+v\n want %+v", i, got, want)
				}
			}
			sameRequests(t, doer.calls, fxList(o.get("fetched")))
		})
	}
	fxCount(t, "pat")
}

func TestQoderCatalogMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("catalog")) {
		o := fxObj(c)
		if len(fxList(o.get("calls"))) == 0 {
			continue // the concurrent case is TestQoderCatalogSharesConcurrentMisses
		}
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			doer := &scriptedDoer{steps: stepsOf(o.get("fetches"))}
			var clock time.Duration
			var signed []signedCall
			client := clientAt(doer, recordingSigner(&signed), &clock)
			for i, call := range fxList(o.get("calls")) {
				co := fxObj(call)
				clock += time.Duration(fxNum(co.get("advance"))) * time.Millisecond
				catalog, err := client.Catalog(context.Background(), fxCreds(co.get("credentials")), co.get("force") == true)
				want := fxList(o.get("results"))[i]
				if failure := fxStr(fxObj(want).get("error")); failure != "" {
					if err == nil || err.Error() != failure {
						t.Errorf("call %d error %v, want %q", i, err, failure)
					}
					continue
				}
				if err != nil {
					t.Errorf("call %d: %v", i, err)
					continue
				}
				if want == nil {
					if catalog != nil {
						t.Errorf("call %d: a catalog, want none", i)
					}
					continue
				}
				if catalog == nil {
					t.Errorf("call %d: no catalog", i)
					continue
				}
				w := fxObj(want)
				var models []any
				for _, m := range catalog.Models {
					models = append(models, newJSObject().set("id", m.ID).set("name", m.Name).set("contextLength", m.ContextLength).
						set("isVL", m.IsVL).set("isReasoning", m.IsReasoning).set("maxOutputTokens", m.MaxOutputTokens).set("description", m.Description))
				}
				if models == nil {
					models = []any{}
				}
				sameJS(t, fmt.Sprintf("call %d models", i), models, w.get("models"))
				keys := []any{}
				configs := newJSObject()
				for _, k := range catalog.Keys() {
					keys = append(keys, k)
					config, _ := catalog.Config(k)
					configs.set(k, config.obj)
				}
				sameJS(t, fmt.Sprintf("call %d keys", i), keys, w.get("keys"))
				sameJS(t, fmt.Sprintf("call %d configs", i), configs, w.get("rawConfigs"))
			}
			var requests []any
			for _, f := range fxList(o.get("fetched")) {
				requests = append(requests, f)
			}
			sameRequests(t, doer.calls, requests)
			// The list request is signed over an empty body, with the identity of the connection.
			var listings []any
			for _, s := range fxList(o.get("signed")) {
				if strings.HasSuffix(fxStr(fxObj(s).get("url")), "/model/list") {
					listings = append(listings, s)
				}
			}
			var got []signedCall
			for _, s := range signed {
				got = append(got, s)
			}
			sameSigned(t, got, listings)
		})
	}
	fxCount(t, "catalog")
}

// blockingDoer holds each model-list request until released, so the test fixes the order
// in which the callers reach the cache.
type blockingDoer struct {
	started chan struct{}
	release chan struct{}
	body    string
	count   int
}

func (d *blockingDoer) Do(*http.Request) (*http.Response, error) {
	d.count++
	d.started <- struct{}{}
	<-d.release
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(d.body))}, nil
}

// executors fixture 'concurrent misses share one request': two callers share a request, a forced
// refresh makes its own, and each caller gets the catalog its request returned.
func TestQoderCatalogSharesConcurrentMisses(t *testing.T) {
	doer := &blockingDoer{started: make(chan struct{}, 4), release: make(chan struct{}), body: `{"chat":[{"key":"auto"}]}`}
	var clock time.Duration
	var signed []signedCall
	client := clientAt(doer, recordingSigner(&signed), &clock)
	creds := QoderCredentials{AccessToken: "dt-x", UserID: "u1"}
	type result struct {
		catalog *QoderCatalog
		err     error
	}
	run := func() chan result {
		out := make(chan result, 1)
		go func() {
			catalog, err := client.Catalog(context.Background(), creds, false)
			out <- result{catalog, err}
		}()
		return out
	}
	first := run()
	<-doer.started
	second := run()
	time.Sleep(50 * time.Millisecond) // the second caller has found the first one's request
	forced := make(chan result, 1)
	go func() {
		catalog, err := client.Catalog(context.Background(), creds, true)
		forced <- result{catalog, err}
	}()
	<-doer.started
	close(doer.release)
	a, b, f := <-first, <-second, <-forced
	if a.err != nil || b.err != nil || f.err != nil {
		t.Fatal(a.err, b.err, f.err)
	}
	if a.catalog != b.catalog || a.catalog == f.catalog || doer.count != 2 {
		t.Errorf("shared=%v forcedSeparate=%v requests=%d, want true true 2", a.catalog == b.catalog, a.catalog != f.catalog, doer.count)
	}
	fixture := qoderPin(t)
	for _, c := range fxList(fixture.get("catalog")) {
		if o := fxObj(c); fxStr(o.get("name")) == "concurrent misses share one request" {
			sameJS(t, "pin", []any{a.catalog == b.catalog, a.catalog == f.catalog}, o.get("results"))
			if len(fxList(o.get("fetched"))) != doer.count {
				t.Errorf("pin made %d requests, this %d", len(fxList(o.get("fetched"))), doer.count)
			}
		}
	}
}

// ---- the chat request ---------------------------------------------------------------------------------------

func TestQoderPrepareChatMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("execute")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			scenario := fxObj(o.get("scenario"))
			creds := fxCreds(scenario.get("credentials"))
			var signed []signedCall
			doer := &scriptedDoer{steps: []*jsObject{{keys: []string{"status", "body"}, values: map[string]any{"status": float64(404), "body": ""}}}}
			if pat := fxObj(scenario.get("pat")); pat != nil && fxStr(pat.get("fail")) != "" {
				doer.steps = []*jsObject{{keys: []string{"network"}, values: map[string]any{"network": pat.get("fail")}}}
			}
			var clock time.Duration
			client := clientAt(doer, recordingSigner(&signed), &clock)
			if strings.HasPrefix(fxStr(o.get("name")), "no machine id") {
				client.sign = BuildCosyHeaders
			}
			// The pin's catalog is scripted; the cache holds the same configs.
			resolved := creds
			if pat := fxObj(scenario.get("pat")); pat != nil && fxStr(pat.get("token")) != "" {
				client.pats = map[string]qoderPAT{creds.token(): {accessToken: fxStr(pat.get("token")), userID: fxStr(pat.get("userId")), expires: qoderNow.Add(48 * time.Hour)}}
				resolved, _ = client.ResolveCredentials(context.Background(), creds)
			}
			catalog := &QoderCatalog{configs: map[string]QoderModelConfig{}, expires: qoderNow.Add(time.Hour)}
			for k, v := range fxObj(scenario.get("catalog")).values {
				catalog.keys = append(catalog.keys, k)
				catalog.configs[k] = QoderModelConfig{obj: fxObj(v)}
			}
			client.catalogs = map[string]*QoderCatalog{qoderCatalogKey(resolved): catalog}

			request, err := client.PrepareChat(context.Background(), QoderChatInput{
				Model: fxStr(scenario.get("model")), Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), Credentials: creds,
			})
			result := fxObj(o.get("result"))
			if strings.HasPrefix(fxStr(o.get("name")), "no machine id") {
				var local *QoderLocalError
				if !errors.As(err, &local) || local.Status != 401 || !errors.Is(errors.Unwrap(err), nil) && !strings.Contains(local.Message, ErrCosyMachineID.Error()) {
					t.Fatalf("a connection with no machine id must be refused, not signed with a made-up one: %v", err)
				}
				return // the pin invents a machine id here; Routeweft never does (AGENTS §3)
			}
			// A pin result with an error is the send failing, after the request was prepared.
			if headers := fxObj(result.get("headers")); result.get("error") == nil && (result.get("url") == nil || (headers != nil && len(headers.keys) == 0)) {
				var local *QoderLocalError
				if !errors.As(err, &local) {
					t.Fatalf("error %v, want a local answer", err)
				}
				var message string
				if body, _ := parseJS([]byte(fxStr(result.get("body")))); body != nil {
					message = fxStr(fxObj(fxObj(body).get("error")).get("message"))
				}
				if float64(local.Status) != fxNum(result.get("status")) || local.Message != message {
					t.Errorf("local answer %d %q, want %v %q", local.Status, local.Message, fxNum(result.get("status")), message)
				}
				for _, s := range signed {
					if !strings.HasSuffix(s.URL, "/model/list") {
						t.Errorf("signed a chat request before refusing: %s", s.URL)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.get("error") != nil {
				sameSigned(t, signed, fxList(o.get("signed")))
				return
			}
			if request.URL != fxStr(result.get("url")) {
				t.Errorf("url %s, want %s", request.URL, fxStr(result.get("url")))
			}
			want := pinHeaders(fxObj(result.get("headers")))
			for k, v := range want {
				if request.Headers[k] != v {
					t.Errorf("header %s = %q, want %q", k, request.Headers[k], v)
				}
			}
			if signedStub := request.Headers["Authorization"]; signedStub != "COSY.stub" {
				t.Errorf("the COSY headers are the signer's: %q", signedStub)
			}
			if payload := fxStr(result.get("transformedBody")); payload != "" && !bytes.Equal(request.Body, QoderEncodeBody([]byte(payload))) {
				t.Errorf("the body is not the encoded envelope")
			}
			sameSigned(t, signed, fxList(o.get("signed")))
		})
	}
	fxCount(t, "execute")
}

// ---- context tiers ----------------------------------------------------------------------------------------------

func TestQoderContextTierMatchesPin(t *testing.T) {
	for _, c := range fxList(qoderPin(t).get("tiers")) {
		o := fxObj(c)
		t.Run(fxStr(o.get("name")), func(t *testing.T) {
			config := fxObj(o.get("config"))
			prompt := fxObj(o.get("prompt"))
			p := qoderTierPrompt{system: fxStr(prompt.get("system")), messages: fxList(prompt.get("messages")), tools: prompt.get("tools")}
			var tiers []any
			for _, tier := range qoderContextTiers(config) {
				tiers = append(tiers, newJSObject().set("name", tier.name).set("tokenCount", tier.tokenCount).set("isDefault", tier.isDefault))
			}
			if tiers == nil {
				tiers = []any{}
			}
			sameJS(t, "tiers", tiers, o.get("tiers"))
			if got := estimateQoderPromptTokens(p); float64(got) != fxNum(o.get("estimated")) {
				t.Errorf("estimate %d, want %v", got, fxNum(o.get("estimated")))
			}
			choice := resolveQoderContextTier(config, p, fxStr(o.get("preference")))
			if want := fxObj(o.get("choice")); want == nil {
				if choice != nil {
					t.Errorf("tier %+v, want none", *choice)
				}
			} else if choice == nil || float64(choice.tokenCount) != fxNum(fxObj(want.get("tier")).get("tokenCount")) {
				t.Errorf("tier %+v, want %s", choice, stringifyJS(want))
			}
		})
	}
	for _, c := range fxList(qoderPin(t).get("tierCounts")) {
		o := fxObj(c)
		var input any = o.get("in")
		if marker := fxObj(input); marker != nil {
			if marker.get("undefined") == true {
				input = nil
			} else {
				input = jsStringToNumber(fxStr(marker.get("number")))
			}
		}
		if got := parseQoderTierCount(input); float64(got) != fxNum(o.get("out")) {
			t.Errorf("parseTierTokenCount(%s) = %d, want %v", stringifyJS(o.get("in")), got, fxNum(o.get("out")))
		}
	}
	fxCount(t, "tierCounts")
}
