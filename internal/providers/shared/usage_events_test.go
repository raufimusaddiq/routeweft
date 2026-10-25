package shared

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/commandcode"
)

func kiroFrame(kind, payload string) []byte {
	header := []byte{11}
	header = append(header, ":event-type"...)
	header = append(header, 7, 0, byte(len(kind)))
	header = append(header, kind...)
	frame := make([]byte, 16+len(header)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(header)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], header)
	copy(frame[12+len(header):], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

// kiro.js:917: inputTokens excludes the cache, which is kept apart; the stream may
// arrive in any chunking and the record is the same.
func TestKiroUsageScannerReadsMetricsEvents(t *testing.T) {
	var stream []byte
	for _, frame := range [][]byte{
		kiroFrame("assistantResponseEvent", `{"content":"hi"}`),
		kiroFrame("metricsEvent", `{"metricsEvent":{"inputTokens":30,"outputTokens":42,"cacheReadInputTokens":100,"cacheCreationInputTokens":5}}`),
		kiroFrame("messageStopEvent", `{}`),
	} {
		stream = append(stream, frame...)
	}
	for _, size := range []int{1, 7, 64, len(stream)} {
		var scanner KiroUsageScanner
		for rest := stream; len(rest) > 0; {
			n := min(size, len(rest))
			_, _ = scanner.Write(rest[:n])
			rest = rest[n:]
		}
		usage, found := scanner.Usage()
		if !found || !scanner.Complete() || usage.InputTokens != 30 || usage.OutputTokens != 42 || usage.CacheReadTokens != 100 || usage.CacheWriteTokens != 5 {
			t.Errorf("chunks of %d: %+v found=%v complete=%v", size, usage, found, scanner.Complete())
		}
	}
	if usage, found := ParseUsage(FamilyKiro, stream); !found || usage.OutputTokens != 42 {
		t.Errorf("buffered body: %+v %v", usage, found)
	}
	// A frame that fails its CRC is not read, and what came before it stands.
	broken := append(append([]byte(nil), stream...), kiroFrame("metricsEvent", `{"inputTokens":1,"outputTokens":1}`)...)
	broken[len(broken)-1] ^= 0xff
	if usage, _ := ParseUsage(FamilyKiro, broken); usage.OutputTokens != 42 {
		t.Errorf("after a corrupt frame: %+v", usage)
	}
	if _, found := ParseUsage(FamilyKiro, kiroFrame("assistantResponseEvent", `{"content":"hi"}`)); found {
		t.Error("usage was invented for a stream that reported none")
	}
	// A length no frame can have ends the scan instead of buffering for it.
	var scanner KiroUsageScanner
	_, _ = scanner.Write([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0})
	if _, found := scanner.Usage(); found || !scanner.broken {
		t.Error("an impossible frame length was accepted")
	}
}

// commandcode-to-openai.js:155-163: the finish event's totalUsage, else the last
// finish-step's usage; the prompt and output only (PARITY V37).
func TestCommandCodeUsageIsTheFinishEventsCounts(t *testing.T) {
	body := `{"type":"text-delta","text":"hi"}
{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":10,"outputTokens":5}}
data: {"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":130,"outputTokens":42,"cachedInputTokens":100}}
`
	usage, found := ParseUsage(FamilyCommandCode, []byte(body))
	if !found || usage.InputTokens != 130 || usage.OutputTokens != 42 || usage.CacheReadTokens != 0 {
		t.Fatalf("%+v %v", usage, found)
	}
	// A finish-step is held for the finish event to report; alone it reports nothing.
	stepOnly := strings.Split(body, "\n")[1]
	if usage, found := ParseUsage(FamilyCommandCode, []byte(stepOnly)); found {
		t.Errorf("finish-step alone: %+v", usage)
	}
	if _, found := ParseUsage(FamilyCommandCode, []byte(`{"type":"finish","finishReason":"stop"}`+"\n"+`{"type":"finish","totalUsage":{"inputTokens":0,"outputTokens":0}}`)); found {
		t.Error("a measured zero was recorded for counts the upstream did not report")
	}
	if _, found := ParseUsage(FamilyCommandCode, []byte("not json\n")); found {
		t.Error("usage from a line that is not an event")
	}
}

func TestGeminiFamilyUsageIsReadInsideTheCloudCodeEnvelope(t *testing.T) {
	metadata := `{"promptTokenCount":130,"candidatesTokenCount":42,"cachedContentTokenCount":100}`
	for _, family := range []string{FamilyGemini, FamilyVertex, FamilyGeminiCLI, FamilyAntigravity} {
		for name, body := range map[string]string{"plain": `{"usageMetadata":` + metadata + `}`, "wrapped": `{"response":{"usageMetadata":` + metadata + `},"traceId":"t"}`} {
			usage, found := ParseUsage(family, []byte(body))
			if !found || usage.InputTokens != 130 || usage.OutputTokens != 42 || usage.CacheReadTokens != 100 {
				t.Errorf("%s %s: %+v %v", family, name, usage, found)
			}
		}
		if _, found := ParseUsage(family, []byte(`{"response":{"candidates":[]}}`)); found {
			t.Errorf("%s: usage from a body that reported none", family)
		}
	}
}

// The report is the finish event's, wholesale: a field the finish event leaves at
// zero is zero, not the number an earlier finish-step carried. Each case is also
// read by the conversion leg, and the two must agree on what was reported.
func TestCommandCodeUsageIsReplacedWholesaleAndAgreesWithTheLeg(t *testing.T) {
	step := func(in, out int) string {
		return `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":` + itoa(in) + `,"outputTokens":` + itoa(out) + `}}` + "\n"
	}
	finish := func(total string) string {
		return `{"type":"finish","finishReason":"stop"` + total + `}` + "\n"
	}
	totals := func(in, out int) string {
		return `,"totalUsage":{"inputTokens":` + itoa(in) + `,"outputTokens":` + itoa(out) + `}`
	}
	for _, c := range []struct {
		name     string
		body     string
		in, out  int64
		reported bool
	}{
		{"a zero output in the total replaces the step's", step(10, 5) + finish(totals(130, 0)), 130, 0, true},
		{"a zero input in the total replaces the step's", step(10, 5) + finish(totals(0, 42)), 0, 42, true},
		{"the last of several steps is the report", step(10, 5) + step(20, 0) + step(30, 7) + finish(""), 30, 7, true},
		{"a later step with a zero field replaces an earlier one", step(10, 5) + step(20, 0) + finish(""), 20, 0, true},
		{"the total wins over every step", step(10, 5) + step(20, 6) + finish(totals(130, 0)), 130, 0, true},
		{"an all-zero total is no usage, not the step's", step(10, 5) + finish(totals(0, 0)), 0, 0, false},
		{"no usage anywhere", finish(""), 0, 0, false},
		{"nothing is reported before the finish event", step(10, 5), 0, 0, false},
		{"lines after the finish event are not read", step(10, 5) + finish(totals(130, 0)) + finish(totals(1, 1)) + step(2, 2), 130, 0, true},
	} {
		usage, found := ParseUsage(FamilyCommandCode, []byte(c.body))
		if found != c.reported || usage.InputTokens != c.in || usage.OutputTokens != c.out || usage.CacheReadTokens != 0 {
			t.Errorf("%s: recorded %+v found=%v, want %d/%d found=%v", c.name, usage, found, c.in, c.out, c.reported)
		}
		// Streamed in awkward chunks, through the reader the scanner uses.
		var reader CommandCodeUsageReader
		for _, line := range strings.SplitAfter(c.body, "\n") {
			reader.Line([]byte(line))
		}
		if streamed, ok := reader.Usage(); ok != c.reported || streamed != usage {
			t.Errorf("%s: streamed %+v %v differs from buffered %+v %v", c.name, streamed, ok, usage, found)
		}
		// The leg is the authority on what the client is told.
		converted, err := commandcode.CommandCodeResponseToChat([]byte(c.body), "m", nil)
		if err != nil {
			continue // a stream the leg refuses (no finish) is not a served answer
		}
		var chat struct {
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(converted, &chat) != nil {
			t.Fatalf("%s: %s", c.name, converted)
		}
		told := chat.Usage != nil
		if told != c.reported || (told && (chat.Usage.Prompt != c.in || chat.Usage.Completion != c.out)) {
			t.Errorf("%s: the leg tells the client %+v, the record says %d/%d reported=%v", c.name, chat.Usage, c.in, c.out, c.reported)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
