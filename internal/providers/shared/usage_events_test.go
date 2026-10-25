package shared

import (
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
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
	stepOnly := strings.Split(body, "\n")[1]
	if usage, found := ParseUsage(FamilyCommandCode, []byte(stepOnly)); !found || usage.InputTokens != 10 {
		t.Errorf("finish-step alone: %+v %v", usage, found)
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
