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

// CommandCodeUsageReader follows the usage the way the conversion leg reports it
// (protocol/commandcode handle, commandcode-to-openai.js:155-163): a finish-step's
// usage replaces the one before it, and the finish event reports its totalUsage,
// else the last finish-step's, wholesale. The report is what that object says, zero
// fields included: it is never folded into an earlier one, and a report with no
// positive count is no usage at all. Nothing is reported before the finish event,
// and nothing after the first one is read, as the leg stops there. The pin reads the
// prompt and output only; cachedInputTokens is an observation beside the counts,
// never a cache read (PARITY V37).
type CommandCodeUsageReader struct {
	step     map[string]any
	usage    promptcache.Usage
	found    bool
	complete bool
}

// Line reads one NDJSON or `data:` line.
func (r *CommandCodeUsageReader) Line(line []byte) {
	if r.complete {
		return
	}
	line = bytes.TrimSpace(line)
	line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(line) == 0 || line[0] != '{' {
		return
	}
	event, err := concerns.WireObject(line)
	if err != nil {
		return
	}
	switch concerns.Text(event["type"]) {
	case "finish-step":
		if usage := concerns.Object(event["usage"]); usage != nil {
			r.step = usage
		}
	case "finish":
		r.complete = true
		raw := concerns.Object(event["totalUsage"])
		if raw == nil {
			raw = r.step
		}
		if counted, _, observed := concerns.CommandCodeUsage(raw); observed {
			r.usage, r.found = promptcache.Usage{InputTokens: counted.Prompt, OutputTokens: counted.Completion}, true
		}
	}
}

// Usage is the finish event's report, and whether it made one.
func (r *CommandCodeUsageReader) Usage() (promptcache.Usage, bool) { return r.usage, r.found }

// Complete reports that the finish event went by.
func (r *CommandCodeUsageReader) Complete() bool { return r.complete }

func parseCommandCodeUsage(body []byte) (promptcache.Usage, bool) {
	var reader CommandCodeUsageReader
	for _, line := range bytes.Split(body, []byte("\n")) {
		reader.Line(line)
	}
	return reader.Usage()
}
