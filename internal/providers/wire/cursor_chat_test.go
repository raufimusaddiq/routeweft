package wire

// The hex below is the output of the pin's own encoder (cursorProtobuf.js
// generateCursorBody) at raufimusaddiq/literouter main
// 4279093457458e168a2431604935f0145f2e76ec, run under Node with uuid v4 fixed
// to 00000000-0000-4000-8000-<n>, process.platform/arch/version, cwd and Date
// fixed. These are the pin's bytes, not an assertion about them.

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	cursorleg "github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
)

const (
	pinChatSimple = "000000011c0a99020a340a046c6f6f6b10016a2430303030303030302d303030302d343030302d383030302d303030303030303030303031e80100f8020110011a0020012a0b0a076770742d352e322200420068017a1f0a11637572736f725c616973657474696e67731a0032040a00120040014801980101ba012430303030303030302d303030302d343030302d383030302d303030303030303030303032d201320a056c696e757812037836341a077632302e302e3022012f2a18323032362d31312d30315430303a30303a30302e3030305ad80100f201280a2430303030303030302d303030302d343030302d383030302d3030303030303030303030311801980200b00200f00201fa0200800301880300980300a80301b2030341736b"
	pinChatFull   = "000000034d0aca060a4f0a1f5b53797374656d20496e737472756374696f6e735d0a42652062726965662e10016a2430303030303030302d303030302d343030302d383030302d303030303030303030303031e80101f802020a340a046c6f6f6b10016a2430303030303030302d303030302d343030302d383030302d303030303030303030303032e80101f802020a350a05666972737410026a2430303030303030302d303030302d343030302d383030302d303030303030303030303033e80101f802020aa7010a733c746f6f6c5f726573756c743e0a3c746f6f6c5f6e616d653e6765745f776561746865723c2f746f6f6c5f6e616d653e0a3c746f6f6c5f63616c6c5f69643e63313c2f746f6f6c5f63616c6c5f69643e0a3c726573756c743e6f6b3c2f726573756c743e0a3c2f746f6f6c5f726573756c743e10016a2430303030303030302d303030302d343030302d383030302d303030303030303030303034e80101f802029a03010110011a0020012a130a0f636c617564652d342e362d6f7075732200420068017a1f0a11637572736f725c616973657474696e67731a0032040a00120040014801980101ba012430303030303030302d303030302d343030302d383030302d303030303030303030303035d201320a056c696e757812037836341a077632302e302e3022012f2a18323032362d31312d30315430303a30303a30302e3030305ad80101ea010101f201280a2430303030303030302d303030302d343030302d383030302d3030303030303030303030311801f201280a2430303030303030302d303030302d343030302d383030302d3030303030303030303030321801f201280a2430303030303030302d303030302d343030302d383030302d3030303030303030303030331802f201280a2430303030303030302d303030302d343030302d383030302d303030303030303030303034180192026d0a0b6765745f776561746865721207576561746865721a4d7b2270726f70657274696573223a7b2263697479223a7b2274797065223a22737472696e67227d7d2c227265717569726564223a5b2263697479225d2c2274797065223a226f626a656374227d2206637573746f6d980200b00200f00202fa0200800300880302980300a80301b203054167656e74"
)

func pinChatIDs() func() string {
	n := 0
	return func() string {
		n++
		return "00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(itoa(n))) + itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func pinChatFrame(t *testing.T, body string) []byte {
	t.Helper()
	request, err := concerns.WireObject([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	messages, err := cursorleg.ChatMessages(request)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cursorChatTools(request["tools"])
	if err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	frame := cursorFrame(pbBytes(1, cursorChatRequest(messages, concerns.Text(request["model"]), tools, cursorEffort(request), now, pinChatIDs())))
	return frame
}

func TestCursorChatRequestBytesMatchThePin(t *testing.T) {
	const image = `{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}`
	for name, test := range map[string]struct{ body, want string }{
		"image turn": {`{"model":"gpt-5.2","messages":[{"role":"user","content":[{"type":"text","text":"look"},` + image + `]}]}`, pinChatSimple},
		"full": {`{"model":"claude-4.6-opus","reasoning_effort":"high","messages":[
			{"role":"system","content":"Be brief."},
			{"role":"user","content":[{"type":"text","text":"look"},` + image + `]},
			{"role":"assistant","content":"first"},
			{"role":"tool","tool_call_id":"c1","name":"get_weather","content":"ok"}],
		 "tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}}}]}`, pinChatFull},
	} {
		want, err := hex.DecodeString(test.want)
		if err != nil {
			t.Fatal(err)
		}
		got := pinChatFrame(t, test.body)
		if !bytes.Equal(got, want) {
			for i := 0; i < len(got) && i < len(want); i++ {
				if got[i] != want[i] {
					t.Fatalf("%s: first difference at byte %d: got %x want %x", name, i, got[i:], want[i:])
				}
			}
			t.Fatalf("%s: lengths %d and %d", name, len(got), len(want))
		}
	}
}

func TestCursorChatServiceDispatchFollowsTheGates(t *testing.T) {
	for name, test := range map[string]struct {
		body   string
		legacy bool
	}{
		"text only":         {`{"messages":[{"role":"user","content":"hi"}]}`, false},
		"null content":      {`{"messages":[{"role":"assistant","content":null},{"role":"user","content":"hi"}]}`, false},
		"text parts":        {`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, false},
		"image":             {`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]}]}`, true},
		"text and image":    {`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`, true},
		"audio":             {`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x"}}]}]}`, true},
		"no messages":       {`{}`, true},
		"empty list":        {`{"messages":[]}`, true},
		"unknown part":      {`{"messages":[{"role":"user","content":[{"type":"hologram"}]}]}`, true},
		"part without type": {`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`, true},
	} {
		got, err := cursorChatDispatches([]byte(test.body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != test.legacy {
			t.Fatalf("%s: legacy=%v, want %v", name, got, test.legacy)
		}
	}
	if _, err := cursorChatDispatches([]byte(`{"messages":`)); !errors.Is(err, ErrCursorUnsupported) {
		t.Fatalf("malformed body: %v", err)
	}
}

// chatServiceServer answers the ChatService POST with the frames given, and
// reports that the path, method and body reached it.
func chatServiceServer(t *testing.T, frames ...[]byte) (*httptest.Server, *string) {
	t.Helper()
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != cursorContentType {
			t.Errorf("%s %s content type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("body: %v", err)
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		for _, frame := range frames {
			w.Write(frame)
		}
	}))
	t.Cleanup(server.Close)
	return server, &path
}

func TestCursorChatServiceServesAnImageTurn(t *testing.T) {
	server, path := chatServiceServer(t, cursorFrame(pbBytes(2, pbString(1, "seen"))))
	defer func() {
		if *path != "/"+cursorChatPath {
			t.Fatalf("path %q", *path)
		}
	}()
	body := `{"model":"gpt-5.2","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]}]}`
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/agent.v1.AgentService/Run", strings.NewReader(body))
	response, err := CursorDo(server.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	sse, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	for _, want := range []string{`"role":"assistant"`, `"content":"seen"`, `"finish_reason":"stop"`, "data: [DONE]"} {
		if !strings.Contains(string(sse), want) {
			t.Fatalf("missing %q in\n%s", want, sse)
		}
	}
	if strings.Contains(string(sse), "usage") {
		t.Fatalf("usage estimated:\n%s", sse)
	}
}

func decodeChat(t *testing.T, frames ...[]byte) (string, error) {
	t.Helper()
	var buffer bytes.Buffer
	err := cursorChatStream(context.Background(), bytes.Join(frames, nil), &buffer, "gpt-5.2")
	return buffer.String(), err
}

func TestCursorChatStreamDecodesTextThinkingAndToolCalls(t *testing.T) {
	call := cursorFrame(pbBytes(1, pbJoin(
		pbString(3, "call_1\nmodel"),
		pbString(9, "look"),
		pbBytes(27, pbBytes(1, pbJoin(pbString(1, "look"), pbString(3, `{"city":"Oslo"}`)))),
		pbVarint(11, 1),
	)))
	sse, err := decodeChat(t,
		cursorFrame(pbBytes(2, pbString(1, "hello"))),
		call,
		cursorFrame(pbBytes(2, pbBytes(25, pbString(1, "musing")))),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"content":"hello"`, `"name":"look"`, `{\"city\":\"Oslo\"}`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(sse, want) {
			t.Fatalf("missing %q in\n%s", want, sse)
		}
	}
}

func TestCursorChatStreamRejectsAnErrorFrameBeforeContent(t *testing.T) {
	payload := `{"error":{"code":"resource_exhausted","message":"slow down","details":[{"debug":{"error":"quota","details":{"title":"quota"}}}]}}`
	_, err := decodeChat(t, cursorFrame([]byte(payload)))
	var report *cursorChatError
	if !errors.As(err, &report) || report.status != http.StatusTooManyRequests {
		t.Fatalf("%v", err)
	}
	var body struct {
		Error struct {
			Message, Type, Code string
		} `json:"error"`
	}
	if json.Unmarshal(report.body, &body) != nil || body.Error.Message != "quota" || body.Error.Type != "rate_limit_error" || body.Error.Code != "quota" {
		t.Fatalf("body %s", report.body)
	}
	// The same frame after content is the end of the stream, not a failure.
	sse, err := decodeChat(t,
		cursorFrame(pbBytes(2, pbString(1, "partial"))),
		cursorFrame([]byte(payload)))
	if err != nil || !strings.Contains(sse, `"content":"partial"`) {
		t.Fatalf("%v\n%s", err, sse)
	}
}

func TestCursorChatStreamDecompressesEveryFrameShape(t *testing.T) {
	payload := pbBytes(2, pbString(1, "z"))
	var gzipped, deflated, raw bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	gw.Write(payload)
	gw.Close()
	zw := zlib.NewWriter(&deflated)
	zw.Write(payload)
	zw.Close()
	fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	fw.Write(payload)
	fw.Close()
	for name, frame := range map[string][]byte{
		"gzip":         {1, 0, 0, 0, byte(gzipped.Len())},
		"zlib":         {2, 0, 0, 0, byte(deflated.Len())},
		"gzip trailer": {3, 0, 0, 0, byte(gzipped.Len())},
	} {
		data := gzipped.Bytes()
		if name == "zlib" || name == "gzip trailer" {
			data = deflated.Bytes()
			if name == "gzip trailer" {
				data = gzipped.Bytes()
			}
		}
		frame = append(frame, data...)
		sse, err := decodeChat(t, frame)
		if err != nil || !strings.Contains(sse, `"content":"z"`) {
			t.Fatalf("%s: %v\n%s", name, err, sse)
		}
	}
	rawFrame := append([]byte{0, 0, 0, 0, byte(raw.Len())}, raw.Bytes()...)
	if sse, err := decodeChat(t, rawFrame); err != nil || strings.Contains(sse, `"content":"z"`) {
		t.Fatalf("raw deflate is only a fallback for a flagged frame: %v\n%s", err, sse)
	}
}

func TestCursorChatStreamEndsAnEmptyAnswerAsThePinDoes(t *testing.T) {
	sse, err := decodeChat(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sse, `"content":""`) || !strings.Contains(sse, `"role":"assistant"`) || !strings.Contains(sse, `"finish_reason":"stop"`) {
		t.Fatalf("%s", sse)
	}
}
