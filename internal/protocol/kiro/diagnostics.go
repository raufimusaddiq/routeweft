package kiro

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// The terminal diagnostics of executors/kiro.js:661-676 and the failure codes the
// pin gives each way an answer can end (:690-700, :996-1133). Every failure a
// Kiro response leg or the integrity gate reports carries them as details, in the
// pin's own names.

// kiroEventTypes is KIRO_EVENT_TYPES (:19-30); any other type is counted as "other".
var kiroEventTypes = map[string]bool{
	"assistantResponseEvent": true, "reasoningContentEvent": true, "codeEvent": true, "toolUseEvent": true,
	"messageStopEvent": true, "metadataEvent": true, "MetadataEvent": true, "contextUsageEvent": true,
	"meteringEvent": true, "metricsEvent": true,
}

func eventCountKey(kind string) string {
	if kiroEventTypes[kind] {
		return kind
	}
	return "other"
}

type pinFailure struct {
	code, message string
	provenance    string
	transport     string
	disposition   string // empty: derived from the stop reason
	pending       int
}

// diagnostics is the terminal state as the pin reports it.
func (m *machine) diagnostics(provenance, transport, disp string, pending int) map[string]any {
	if provenance == "" {
		provenance = m.provenance
	}
	if provenance == "" {
		provenance = "clean_eventstream_eof"
	}
	if disp == "" {
		disp = disposition(m.stopReason, m.emittedCalls > 0)
	}
	state := "no_semantic_output"
	switch {
	case m.emittedCalls > 0:
		state = "valid_tool"
	case m.delivered:
		state = "text_reasoning"
	case m.explicitStop:
		state = "explicit_stop"
	}
	counts := map[string]any{}
	for kind, n := range m.counts {
		counts[kind] = n
	}
	var stop any
	if m.stopReason != "" {
		stop = m.stopReason
	}
	return map[string]any{
		"terminal_provenance": provenance, "transport_state": transport, "stop_reason": stop,
		"stop_disposition": disp, "response_state": state, "event_counts": counts,
		"incomplete_frame_bytes": pending,
	}
}

func (m *machine) fail(f pinFailure) error {
	if f.transport == "" {
		f.transport = "clean_eof"
	}
	return &concerns.StreamFailure{Value: map[string]any{
		"type": "upstream_error", "code": f.code, "message": f.message,
		"details": m.diagnostics(f.provenance, f.transport, f.disposition, f.pending),
	}}
}

// stopFailure is the failure of a declared stop reason (:1019-1034, :1084-1099).
func (m *machine) stopFailure(reason, disp string) error {
	code := map[string]string{
		dispRetryable: "kiro_retryable_protocol_failure", dispRefusal: "kiro_terminal_refusal",
		dispIncomplete: "kiro_terminal_incomplete", dispUnknown: "kiro_unknown_stop_reason",
	}[disp]
	provenance := m.provenance
	if provenance == "" {
		provenance = "metadata_stop_reason"
	}
	return m.fail(pinFailure{code: code, message: "Kiro ended with non-success stop reason: " + reason, provenance: provenance, disposition: disp})
}

// toolFailure is a turn with no usable tool call and nothing else (:1036-1047, :1053-1062).
func (m *machine) toolFailure(message string) error {
	return m.fail(pinFailure{code: "invalid_kiro_tool_call", message: message, provenance: "invalid_tool_call", disposition: dispRetryable})
}

// classify gives a failure the pin's code and diagnostics. Cancellation and the
// failures that already carry them pass through unchanged.
func (m *machine) classify(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var own interface{ PassThrough() }
	if errors.As(err, &own) {
		return err
	}
	var failure *concerns.StreamFailure
	if errors.As(err, &failure) {
		if _, has := failure.Value["details"]; has {
			return err
		}
		message := concerns.Text(failure.Value["message"])
		switch concerns.Text(failure.Value["code"]) {
		case "upstream_eventstream_error":
			return m.fail(pinFailure{code: "kiro_upstream_eventstream_error", message: message, provenance: "upstream_eventstream_error", transport: "upstream_error", disposition: dispIncomplete})
		case "upstream_buffer_exceeded":
			return m.fail(pinFailure{code: "kiro_integrity_buffer_exceeded", message: "Kiro buffered tool input exceeded the integrity memory bound", provenance: "integrity_buffer_exceeded", transport: "valid_complete_frame", disposition: dispIncomplete})
		}
		return m.fail(pinFailure{code: concerns.Text(failure.Value["code"]), message: message, provenance: concerns.Text(failure.Value["code"]), transport: "valid_complete_frame", disposition: dispIncomplete})
	}
	var truncated *concerns.TruncatedFrameError
	switch {
	case errors.As(err, &truncated):
		return m.fail(pinFailure{code: "kiro_missing_terminal", message: "Kiro EventStream ended with a truncated frame", provenance: "incomplete_eventstream_frame", transport: "incomplete_frame", disposition: dispIncomplete, pending: truncated.Pending})
	}
	if text := err.Error(); strings.Contains(text, "EventStream") || strings.Contains(text, "eventstream") {
		return m.fail(pinFailure{code: "kiro_missing_terminal", message: text, provenance: "corrupt_eventstream_frame", transport: "corrupt_frame", disposition: dispIncomplete})
	}
	return m.fail(pinFailure{code: "kiro_missing_terminal", message: err.Error(), provenance: "upstream_read_error", transport: "upstream_error", disposition: dispIncomplete})
}

// run reads a whole EventStream through the machine and finishes it: the one path
// every response leg and the gate take, so they cannot disagree about an answer.
func (m *machine) run(ctx context.Context, src io.Reader) (string, error) {
	err := concerns.ReadEventStream(ctx, src, m.handle)
	// A clean end with output and no stop event completes the answer, as at the pin
	// (executors/kiro.js:1008-1075, kiro-terminal-integrity.test.js "accepts
	// CLI-compatible text and usage frames at clean EOF without messageStop"). A cut
	// stream is still a failure: only a stream that ended between frames qualifies.
	var open *concerns.NoTerminalError
	if errors.As(err, &open) {
		err = nil
	}
	reason := ""
	if err == nil {
		reason, err = m.finish()
	}
	if err != nil {
		return "", m.classify(ctx, err)
	}
	return reason, nil
}
