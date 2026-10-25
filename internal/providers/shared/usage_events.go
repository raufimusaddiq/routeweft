package shared

import (
	"bytes"
	"context"
	"encoding/binary"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// The two providers whose usage is not in a JSON body or an SSE event: Kiro
// (an AWS event stream) and CommandCode (AI SDK NDJSON). The same readers the
// conversion legs use decode them, and the counts are the ones the legs report
// (concerns.KiroUsage, concerns.CommandCodeUsage), so the client's figures and the
// recorded figures cannot disagree.
const (
	FamilyKiro        = "kiro"
	FamilyCommandCode = "commandcode"
)

// KiroUsageScanner reads usage out of an event stream as its bytes pass, without
// holding more than one frame. A frame it cannot read ends the scan: usage seen
// so far stands, and Complete stays false.
type KiroUsageScanner struct {
	buffer   []byte
	usage    promptcache.Usage
	found    bool
	complete bool
	broken   bool
}

func (s *KiroUsageScanner) Write(chunk []byte) (int, error) {
	if s.broken {
		return len(chunk), nil
	}
	s.buffer = append(s.buffer, chunk...)
	for len(s.buffer) >= 12 {
		length := int(binary.BigEndian.Uint32(s.buffer[:4]))
		if length < 16 || length > concerns.MaxEventStreamMessageBytes {
			s.broken, s.buffer = true, nil
			break
		}
		if len(s.buffer) < length {
			break
		}
		frame := s.buffer[:length]
		// ReadEventStream validates the frame before it hands it over and reports a
		// missing terminal for a lone frame, which is of no interest here.
		_ = concerns.ReadEventStream(context.Background(), bytes.NewReader(frame), s.consume)
		s.buffer = s.buffer[length:]
	}
	return len(chunk), nil
}

func (s *KiroUsageScanner) consume(event concerns.EventStreamEvent) (bool, error) {
	kind, _ := event.Headers[":event-type"].(string)
	switch kind {
	case "messageStopEvent":
		s.complete = true
	case "metricsEvent":
		object, err := concerns.WireObject(event.Payload)
		if err != nil {
			return false, nil
		}
		if nested := concerns.Object(object["metricsEvent"]); nested != nil {
			object = nested
		}
		if usage, observed := concerns.KiroUsage(object); observed {
			// KiroUsage counts the prompt with its cache; the record keeps the
			// cache apart, as an Anthropic upstream reports it.
			s.usage = promptcache.Usage{
				InputTokens: usage.Prompt - usage.Cached - usage.CacheCreation, OutputTokens: usage.Completion,
				CacheReadTokens: usage.Cached, CacheWriteTokens: usage.CacheCreation,
			}
			s.found = true
		}
	}
	return false, nil
}

// Usage is what the stream reported, and whether it reported any.
func (s *KiroUsageScanner) Usage() (promptcache.Usage, bool) { return s.usage, s.found }

// Complete reports that the stream's terminal frame went by.
func (s *KiroUsageScanner) Complete() bool { return s.complete }

func parseKiroUsage(body []byte) (promptcache.Usage, bool) {
	var scanner KiroUsageScanner
	_, _ = scanner.Write(body)
	return scanner.Usage()
}

// CommandCodeLineUsage reads one AI SDK event: a finish-step's usage, or the
// finish event's totalUsage (commandcode-to-openai.js:155-163), which wins when
// both were seen because the later report replaces the earlier. complete is true
// for the finish event. The pin reads the prompt and output only; cachedInputTokens
// is an observation beside the counts, never taken out of them (PARITY V37).
func CommandCodeLineUsage(line []byte) (usage promptcache.Usage, found, complete bool) {
	line = bytes.TrimSpace(line)
	line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(line) == 0 || line[0] != '{' {
		return promptcache.Usage{}, false, false
	}
	event, err := concerns.WireObject(line)
	if err != nil {
		return promptcache.Usage{}, false, false
	}
	var raw map[string]any
	switch concerns.Text(event["type"]) {
	case "finish-step":
		raw = concerns.Object(event["usage"])
	case "finish":
		complete = true
		raw = concerns.Object(event["totalUsage"])
	default:
		return promptcache.Usage{}, false, false
	}
	if raw == nil {
		return promptcache.Usage{}, false, complete
	}
	counted, _, observed := concerns.CommandCodeUsage(raw)
	if !observed {
		return promptcache.Usage{}, false, complete
	}
	return promptcache.Usage{InputTokens: counted.Prompt, OutputTokens: counted.Completion}, true, complete
}

func parseCommandCodeUsage(body []byte) (promptcache.Usage, bool) {
	var merged promptcache.Usage
	found := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		if usage, ok, _ := CommandCodeLineUsage(line); ok {
			merged, found = merged.Merge(usage), true
		}
	}
	return merged, found
}
