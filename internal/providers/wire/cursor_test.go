package wire

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The hex below is the output of the pin's own code (commit 1c3b9f47,
// open-sse/executors/cursor.js buildAgentRunFrame and encodeKvClientMessage,
// utils/cursorChecksum.js generateCursorChecksum), run under Node with
// crypto.randomUUID fixed to cursorTestUUID and Date.now fixed to 1.8e12. The pin
// has no byte fixture of its own (tests/unit/cursor-agent-exec-request.test.js
// decodes fields), so these are the pin's bytes, not an assertion about them.
// The schemas use keys already in sorted order: the pin writes map keys in
// insertion order and this encoder sorts them.
const (
	cursorTestUUID  = "00000000-0000-4000-8000-000000000000"
	pinRunSimple    = "00000000620a600a0012320a300a2e0a026869122430303030303030302d303030302d343030302d383030302d3030303030303030303030301a0020011a1b0a076770742d352e321a076770742d352e3222076770742d352e324a0b0a076770742d352e323801"
	pinRunGPTEffort = "00000000750a730a0012320a300a2e0a026869122430303030303030302d303030302d343030302d383030302d3030303030303030303030301a0020011a1b0a076770742d352e321a076770742d352e3222076770742d352e324a1e0a076770742d352e3238011a110a09726561736f6e696e67120468696768"
	pinRunFull      = "000000023f0abc040a001297010a94010a3d0a1142652062726965662e0a0a7365636f6e64122430303030303030302d303030302d343030302d383030302d3030303030303030303030301a0020013a530a0d0a0b0a090a070a0566697273740a4212400a3e0a3c0a3a6f6b0a5b746f6f6c5f63616c6c2069643d6331206e616d653d6765745f7765617468657220617267733d7b2263697479223a224f736c6f227d5d1a330a0f636c617564652d342e362d6f7075731a0f636c617564652d342e362d6f707573220f636c617564652d342e362d6f70757322c4020aea010a0b6765745f776561746865721207576561746865721abb012ab8010a8b010a0a70726f70657274696573127d2a7b0a1c0a046369747912142a120a100a047479706512081a06737472696e670a1d0a046461797312152a130a110a047479706512091a07696e74656765720a3c0a05666c61677312332a310a1e0a056974656d7312152a130a110a047479706512091a07626f6f6c65616e0a0f0a047479706512071a0561727261790a160a087265717569726564120a32080a061a04636974790a100a047479706512081a066f626a656374220739726f757465722a0b6765745f776561746865720a550a0470696e6712001a3c2a3a0a1a0a146164646974696f6e616c50726f70657274696573120220000a100a036d617812091100000000000004400a0a0a046e6f6e6512020800220739726f757465722a0470696e674a220a0f636c617564652d342e362d6f70757338011a0d0a066566666f727412036c6f77"
	pinKVGet        = "000000000c1a0a080712020a0022026d64"
	pinKVSet        = "00000000061a0408071a00"
	pinChecksum     = "0pOVkeqvmmmmmmmm"
)

func pinHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestCursorRunBytesMatchThePin(t *testing.T) {
	full := `{"model":"claude-4.6-opus","reasoning_effort":"minimal","messages":[
	  {"role":"system","content":"Be brief."},
	  {"role":"user","content":"first"},
	  {"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]},
	  {"role":"user","content":"second"}],
	 "tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"properties":{"city":{"type":"string"},"days":{"type":"integer"},"flags":{"items":{"type":"boolean"},"type":"array"}},"required":["city"],"type":"object"}}},
	  {"type":"function","function":{"name":"ping","description":"","parameters":{"additionalProperties":false,"max":2.5,"none":null}}}]}`
	for name, test := range map[string]struct{ body, want string }{
		"simple":     {`{"model":"gpt-5.2","messages":[{"role":"user","content":"hi"}]}`, pinRunSimple},
		"gpt effort": {`{"model":"gpt-5.2","reasoning":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, pinRunGPTEffort},
		"full":       {full, pinRunFull},
	} {
		got, model, err := cursorRun([]byte(test.body), cursorTestUUID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := pinHex(t, test.want); !bytes.Equal(got, want) {
			t.Fatalf("%s: frame differs from the pin\n got %x\nwant %x", name, got, want)
		}
		if model == "" {
			t.Fatalf("%s: no model", name)
		}
	}
}

func TestCursorKVAcknowledgementsMatchThePin(t *testing.T) {
	if got := cursorKVReply(7, pbBytes(2, pbBytes(1, nil)), []byte("md")); !bytes.Equal(got, pinHex(t, pinKVGet)) {
		t.Fatalf("kv get ack %x, want %s", got, pinKVGet)
	}
	if got := cursorKVReply(7, pbBytes(3, nil), nil); !bytes.Equal(got, pinHex(t, pinKVSet)) {
		t.Fatalf("kv set ack %x, want %s", got, pinKVSet)
	}
}

func TestCursorChecksumMatchesThePin(t *testing.T) {
	if got := cursorChecksum(time.UnixMilli(1_800_000_000_000), "mmmmmmmm"); got != pinChecksum {
		t.Fatalf("checksum %q, want %q", got, pinChecksum)
	}
}

func cursorProvider() routing.ProviderRef {
	provider := routing.ProviderRef{ProviderID: "cursor", BaseURL: "https://api2.cursor.sh", APIToken: "user_1::tok-en", UpstreamModel: "gpt-5.2"}
	provider.Connection.Settings.MachineID = strings.Repeat("a", 64)
	return provider
}

func TestCursorHeadersFollowThePin(t *testing.T) {
	headers, err := cursorHeaders(cursorProvider(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Authorization": "Bearer tok-en", "Content-Type": "application/connect+proto", "Connect-Protocol-Version": "1",
		"Connect-Accept-Encoding": "gzip", "User-Agent": "connect-es/1.6.1", "X-Cursor-Client-Version": "3.12.17",
		"X-Cursor-Client-Type": "ide", "X-Ghost-Mode": "true",
	} {
		if headers[name] != want {
			t.Fatalf("%s = %q, want %q", name, headers[name], want)
		}
	}
	sum := sha256.Sum256([]byte("tok-en"))
	if headers["X-Client-Key"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("X-Client-Key %q is not the sha256 of the bare token", headers["X-Client-Key"])
	}
	if want := uuid.NewSHA1(uuid.NameSpaceDNS, []byte("tok-en")).String(); headers["X-Session-Id"] != want {
		t.Fatalf("X-Session-Id %q, want %q", headers["X-Session-Id"], want)
	}
	if !strings.HasSuffix(headers["X-Cursor-Checksum"], strings.Repeat("a", 64)) || len(headers["X-Cursor-Checksum"]) != 8+64 {
		t.Fatalf("checksum header %q", headers["X-Cursor-Checksum"])
	}
	for _, mutate := range []func(*routing.ProviderRef){
		func(p *routing.ProviderRef) { p.Connection.Settings.MachineID = "" },
		func(p *routing.ProviderRef) { p.APIToken = "" },
	} {
		provider := cursorProvider()
		mutate(&provider)
		var credential *CredentialError
		if _, err := cursorHeaders(provider, nil, ""); !errors.As(err, &credential) {
			t.Fatalf("missing credential gave %v, want a CredentialError", err)
		}
	}
}

func TestCursorIsDeclaredAsATransport(t *testing.T) {
	declared := false
	for _, spec := range registry.Builtins() {
		if spec.ID == "cursor" {
			declared = len(spec.Transports) == 1 && spec.Transports[0] == registry.TransportCursor && spec.DefaultBaseURL == "https://api2.cursor.sh"
		}
	}
	if !declared {
		t.Fatal("the registry does not declare cursor as the cursor transport")
	}
	hook := For("cursor")
	if hook.Do == nil || hook.Target == nil || hook.RequestHeaders == nil || !hook.ForceStream || !hook.RequireCredential {
		t.Fatalf("cursor hook is missing a part of the transport: %+v", hook)
	}
	target, err := ResolveTarget(cursorProvider(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if target.BaseURL != "https://agent.api5.cursor.sh" || target.Endpoint != "agent.v1.AgentService/Run" {
		t.Fatalf("target %+v", target)
	}
	custom := cursorProvider()
	custom.BaseURL = "https://relay.example/cursor"
	if target, _ = ResolveTarget(custom, ""); target.BaseURL != custom.BaseURL {
		t.Fatalf("an operator's base was replaced: %+v", target)
	}
	for _, model := range []string{"auto", "default"} {
		auto := cursorProvider()
		auto.UpstreamModel = model
		var refused *TargetError
		if _, err := ResolveTarget(auto, ""); !errors.As(err, &refused) || !errors.Is(err, ErrCursorUnsupported) {
			t.Fatalf("model %q gave %v, want a named TargetError", model, err)
		}
	}
}

func TestCursorRunRefusesWhatItCannotCarry(t *testing.T) {
	for name, body := range map[string]string{
		"not json":            `[`,
		"auto model":          `{"model":"auto","messages":[{"role":"user","content":"x"}]}`,
		"no model":            `{"messages":[{"role":"user","content":"x"}]}`,
		"no messages":         `{"model":"m","messages":[]}`,
		"no user turn":        `{"model":"m","messages":[{"role":"assistant","content":"x"}]}`,
		"turn after the user": `{"model":"m","messages":[{"role":"user","content":"x"},{"role":"assistant","content":"y"}]}`,
		"parts content":       `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"x"}]}]}`,
		"tool role":           `{"model":"m","messages":[{"role":"tool","content":"x"}]}`,
		"unnamed tool":        `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"description":"d"}}]}`,
		"not a function tool": `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"custom"}]}`,
		"deep schema":         `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"t","parameters":` + strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70) + `}}]}`,
	} {
		if _, _, err := cursorRun([]byte(body), cursorTestUUID); !errors.Is(err, ErrCursorUnsupported) {
			t.Errorf("%s: %v, want %v", name, err, ErrCursorUnsupported)
		}
	}
}

// --- reply ---

func updateFrame(field uint64, text string) []byte {
	return cursorFrame(pbBytes(1, pbBytes(field, pbString(1, text))))
}

func trailer(body string) []byte {
	frame := cursorFrame([]byte(body))
	frame[0] = 2
	return frame
}

func turnEnded() []byte { return cursorFrame(pbBytes(1, pbBytes(14, nil))) }

func execFrame(number uint64, execID string, variant uint64, body []byte) []byte {
	return cursorFrame(pbBytes(2, pbJoin(pbVarint(1, number), pbString(15, execID), pbBytes(variant, body))))
}

func mcpFrame(name, callID string, entries ...[]byte) []byte {
	args := pbJoin(pbString(1, name), pbJoin(entries...), pbString(3, callID), pbString(5, name))
	return execFrame(4, "e1", 11, args)
}

func mapEntry(key string, value []byte) []byte {
	return pbBytes(2, pbJoin(pbString(1, key), pbBytes(2, value)))
}

func mustMessage(t *testing.T, parent pbMessage, number uint64) (pbMessage, bool, error) {
	t.Helper()
	message, ok, err := parent.message(number)
	if err != nil || !ok {
		t.Fatalf("field %d: %v %v", number, ok, err)
	}
	return message, ok, err
}

func nan() float64 { return math.NaN() }

func consume(t *testing.T, model string, frames ...[]byte) (sse string, acks []byte, err error) {
	t.Helper()
	var out, ack bytes.Buffer
	err = cursorConsume(context.Background(), bytes.NewReader(bytes.Join(frames, nil)), &ack, &out, model)
	return out.String(), ack.Bytes(), err
}

func TestCursorConsumeStreamsTextAndStops(t *testing.T) {
	gzipped := new(bytes.Buffer)
	zw := gzip.NewWriter(gzipped)
	zw.Write(pbBytes(1, pbBytes(1, pbString(1, "llo"))))
	zw.Close()
	frame := append([]byte{1, 0, 0, 0, byte(gzipped.Len())}, gzipped.Bytes()...)
	sse, _, err := consume(t, "gpt-5.2", updateFrame(1, "he"), frame, turnEnded())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"content":"he"`, `"content":"llo"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(sse, want) {
			t.Errorf("stream lacks %s:\n%s", want, sse)
		}
	}
	if strings.Contains(sse, "reasoning_content") {
		t.Errorf("the pin forwards no reasoning:\n%s", sse)
	}
}

func TestCursorConsumeKeepsReasoningUpstream(t *testing.T) {
	sse, _, err := consume(t, "gpt-5.2", updateFrame(4, "private reasoning"), updateFrame(1, "answer"), turnEnded())
	if err != nil || strings.Contains(sse, "private") {
		t.Fatalf("err %v, stream:\n%s", err, sse)
	}
}

func TestCursorConsumeComposerAnswerAfterThinking(t *testing.T) {
	sse, _, err := consume(t, "cursor/composer-2.5", updateFrame(4, "plan</think>"), updateFrame(4, "  The"), updateFrame(4, " answer"), turnEnded())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sse, `"content":"The"`) || !strings.Contains(sse, `"content":" answer"`) {
		t.Fatalf("composer text not streamed after </think>:\n%s", sse)
	}
	// A model that answers only in thinking falls back to it at turn end (cursor.js:665-674).
	sse, _, err = consume(t, "gpt-5.2", updateFrame(4, " only thinking "), turnEnded())
	if err != nil || !strings.Contains(sse, `"content":"only thinking"`) {
		t.Fatalf("fallback: %v\n%s", err, sse)
	}
}

func TestCursorConsumeToolCall(t *testing.T) {
	structure := pbBytes(5, pbBytes(1, pbJoin(pbString(1, "k"), pbBytes(2, pbString(3, "v")))))
	list := pbBytes(6, pbJoin(pbBytes(1, pbDouble(2, 3)), pbBytes(1, pbVarint(4, 1)), pbBytes(1, pbVarint(1, 0))))
	sse, _, err := consume(t, "gpt-5.2", mcpFrame("get_weather", "call_9",
		mapEntry("city", pbString(3, "Oslo")), mapEntry("n", pbDouble(2, 2.5)), mapEntry("obj", structure), mapEntry("list", list), mapEntry("", pbString(3, "skipped"))))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"call_9"`, `"name":"get_weather"`, `"finish_reason":"tool_calls"`,
		`"arguments":"{\"city\":\"Oslo\",\"list\":[3,true,null],\"n\":2.5,\"obj\":{\"k\":\"v\"}}"`} {
		if !strings.Contains(sse, want) {
			t.Errorf("stream lacks %s:\n%s", want, sse)
		}
	}
	if strings.Contains(sse, "skipped") {
		t.Errorf("an empty argument key was kept:\n%s", sse)
	}
}

func TestCursorConsumeAnswersTheServer(t *testing.T) {
	_, acks, err := consume(t, "gpt-5.2",
		cursorFrame(pbBytes(4, pbJoin(pbVarint(1, 7), pbBytes(2, nil), pbBytes(4, []byte("md"))))),
		cursorFrame(pbBytes(4, pbJoin(pbVarint(1, 8), pbBytes(3, nil)))),
		execFrame(3, "e-ctx", 10, nil),
		execFrame(4, "e-shell", 2, nil),
		execFrame(5, "e-diag", 9, nil),
		updateFrame(1, "ok"), turnEnded())
	if err != nil {
		t.Fatal(err)
	}
	reject := func(number uint64, id string, field uint64) []byte {
		return cursorExecReply(number, id, pbBytes(field, pbBytes(2, pbString(2, cursorRejectedTool))))
	}
	want := bytes.Join([][]byte{
		cursorKVReply(7, pbBytes(2, pbBytes(1, nil)), []byte("md")),
		cursorKVReply(8, pbBytes(3, nil), nil),
		cursorExecReply(3, "e-ctx", pbBytes(10, pbBytes(1, pbBytes(1, nil)))),
		reject(4, "e-shell", 2),
		cursorExecReply(5, "e-diag", pbBytes(9, nil)),
	}, nil)
	if !bytes.Equal(acks, want) {
		t.Fatalf("acknowledgements\n got %x\nwant %x", acks, want)
	}
}

func TestCursorConsumeFailsClosed(t *testing.T) {
	truncated := updateFrame(1, "partial")
	oversize := []byte{0, 0x7f, 0xff, 0xff, 0xff}
	badGzip := []byte{1, 0, 0, 0, 3, 'n', 'o', 'p'}
	for name, test := range map[string]struct {
		frames [][]byte
		want   error
	}{
		"eof before the turn ended": {[][]byte{updateFrame(1, "text")}, ErrCursorFraming},
		"empty input":               {nil, ErrCursorFraming},
		"truncated header":          {[][]byte{{0, 0, 0}}, ErrCursorFraming},
		"truncated payload":         {[][]byte{truncated[:len(truncated)-2]}, ErrCursorFraming},
		"unknown flag":              {[][]byte{{4, 0, 0, 0, 0}}, ErrCursorFraming},
		"oversize frame":            {[][]byte{oversize}, ErrCursorFraming},
		"bad gzip":                  {[][]byte{badGzip}, ErrCursorFraming},
		"bad protobuf":              {[][]byte{cursorFrame([]byte{0x0a, 0x05, 'x'})}, ErrCursorFraming},
		"group wire type":           {[][]byte{cursorFrame([]byte{0x0b})}, ErrCursorFraming},
		"invalid utf-8 text":        {[][]byte{cursorFrame(pbBytes(1, pbBytes(1, pbBytes(1, []byte{0xff}))))}, ErrCursorFraming},
		"trailer first":             {[][]byte{trailer(`{}`)}, ErrCursorFraming},
		"trailer with an error":     {[][]byte{trailer(`{"error":{"code":"unavailable"}}`)}, ErrCursorFraming},
		"empty turn":                {[][]byte{turnEnded()}, ErrCursorEmptyTurn},
		"unknown IDE tool":          {[][]byte{execFrame(1, "e", 99, nil)}, ErrCursorUnsupported},
		"exec with no variant":      {[][]byte{cursorFrame(pbBytes(2, pbString(15, "e")))}, ErrCursorUnsupported},
		"kv neither get nor set":    {[][]byte{cursorFrame(pbBytes(4, pbVarint(1, 1)))}, ErrCursorUnsupported},
		"mcp without a name":        {[][]byte{execFrame(1, "e", 11, nil)}, ErrCursorUnsupported},
		"mcp argument not a Value":  {[][]byte{mcpFrame("t", "c", mapEntry("a", []byte{0xff}))}, ErrCursorFraming},
		"mcp nan argument":          {[][]byte{mcpFrame("t", "c", mapEntry("a", pbDouble(2, nan())))}, ErrCursorFraming},
	} {
		sse, _, err := consume(t, "gpt-5.2", test.frames...)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
		if strings.Contains(sse, `"finish_reason":"stop"`) || strings.Contains(sse, "[DONE]") {
			t.Errorf("%s: a failed turn was finished:\n%s", name, sse)
		}
	}
}

// --- the exchange ---

type cursorServer struct {
	*httptest.Server
	first chan []byte
	acks  chan []byte
	gone  chan struct{}
}

func newCursorServer(t *testing.T, script func(w http.ResponseWriter, flush func(), read func() []byte)) *cursorServer {
	t.Helper()
	server := &cursorServer{first: make(chan []byte, 1), acks: make(chan []byte, 16), gone: make(chan struct{})}
	server.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(server.gone)
		if r.Header.Get("Content-Type") != "application/connect+proto" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		read := func() []byte {
			payload, _, err := cursorReadFrame(r.Body)
			if err != nil {
				return nil
			}
			return payload
		}
		server.first <- read()
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		script(w, flusher.Flush, read)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func (s *cursorServer) do(t *testing.T, body string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, s.URL+"/agent.v1.AgentService/Run", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/connect+proto")
	request.Header.Set("Accept", "application/json")
	return CursorDo(s.Client(), request)
}

const cursorChat = `{"model":"gpt-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func TestCursorDoRunsTheDuplexExchange(t *testing.T) {
	var got [][]byte
	server := newCursorServer(t, func(w http.ResponseWriter, flush func(), read func() []byte) {
		send := func(frame []byte) { w.Write(frame); flush() }
		send(cursorFrame(pbBytes(4, pbJoin(pbVarint(1, 7), pbBytes(2, nil)))))
		got = append(got, read())
		send(execFrame(3, "ctx", 10, nil))
		got = append(got, read())
		send(updateFrame(1, "hello"))
		send(turnEnded())
	})
	response, err := server.do(t, cursorChat)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.ProtoMajor != 2 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response %s %q", response.Proto, response.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `"content":"hello"`) || !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
		t.Fatalf("body:\n%s", body)
	}
	run, err := pbParse(<-server.first)
	if err != nil {
		t.Fatal(err)
	}
	request, _, _ := run.message(1)
	requested, _, _ := mustMessage(t, request, 9)
	if model, _ := requested.text(1); model != "gpt-5.2" {
		t.Fatalf("the first frame is not the Run request for the model: %v", run)
	}
	if !bytes.Equal(got[0], cursorKVReply(7, pbBytes(2, pbBytes(1, nil)), nil)[5:]) ||
		!bytes.Equal(got[1], cursorExecReply(3, "ctx", pbBytes(10, pbBytes(1, pbBytes(1, nil))))[5:]) {
		t.Fatalf("acknowledgements %x", got)
	}
}

func TestCursorDoReportsAFailureInBandNotAsSuccess(t *testing.T) {
	server := newCursorServer(t, func(w http.ResponseWriter, flush func(), read func() []byte) {
		w.Write(updateFrame(1, "half an answer"))
		flush() // and the stream ends with no turn_ended
	})
	response, err := server.do(t, cursorChat)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), `"error"`) || !strings.Contains(string(body), "cursor_connect_framing") || strings.Contains(string(body), `"finish_reason":"stop"`) {
		t.Fatalf("a stream cut short was not reported as a failure:\n%s", body)
	}
}

func TestCursorDoPassesUpstreamRejectionsThrough(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"code":"resource_exhausted"}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(cursorChat))
	response, err := CursorDo(server.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 429 || string(body) != `{"code":"resource_exhausted"}` {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
}

func TestCursorDoRefusesWhatItCannotSend(t *testing.T) {
	// Connection: close, so the server does not wait out the request body the
	// client keeps open for acknowledgements before it replies.
	h1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Connection", "close") }))
	defer h1.Close()
	request, _ := http.NewRequest(http.MethodPost, h1.URL, strings.NewReader(cursorChat))
	if _, err := CursorDo(h1.Client(), request); !errors.Is(err, ErrCursorHTTP2) {
		t.Fatalf("HTTP/1.1 endpoint: %v, want %v", err, ErrCursorHTTP2)
	}
	request, _ = http.NewRequest(http.MethodPost, h1.URL, strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"x"}]}`))
	if _, err := CursorDo(h1.Client(), request); !errors.Is(err, ErrCursorUnsupported) {
		t.Fatalf("auto model: %v", err)
	}
	wrongType := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":"x"}`)
	}))
	wrongType.EnableHTTP2 = true
	wrongType.StartTLS()
	defer wrongType.Close()
	request, _ = http.NewRequest(http.MethodPost, wrongType.URL, strings.NewReader(cursorChat))
	if _, err := CursorDo(wrongType.Client(), request); !errors.Is(err, ErrCursorFraming) {
		t.Fatalf("a 200 that is not Connect: %v", err)
	}
}

func TestCursorDoStopsUpstreamWhenTheClientLeaves(t *testing.T) {
	server := newCursorServer(t, func(w http.ResponseWriter, flush func(), read func() []byte) {
		w.Write(updateFrame(1, "first"))
		flush()
		read() // never ends the turn; only the request body ending can end this
	})
	response, err := server.do(t, cursorChat)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-server.gone:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the response did not cancel the upstream request")
	}
}

// The codec is the ingress arm: it hands CursorDo the Chat body, and CursorDo
// builds the Run frame and turns the answer back into Chat SSE, so the stream is
// already Chat and is copied through.
func TestCursorCodecHandsChatToCursorDo(t *testing.T) {
	chat := map[string]any{"model": "claude-4.5-sonnet", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	codec, ok, err := CodecFor(routing.ProviderRef{ProviderID: "cursor"}, nil, chat, "claude-4.5-sonnet")
	if !ok || err != nil || codec.Endpoint != cursorRunPath || !strings.Contains(string(codec.Body), `"role":"user"`) {
		t.Fatalf("codec ok=%v err=%v %+v", ok, err, codec)
	}
	var out strings.Builder
	if err := codec.Stream(context.Background(), strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"), &out); err != nil || !strings.Contains(out.String(), "hi") {
		t.Fatalf("stream err=%v out=%s", err, out.String())
	}
}
