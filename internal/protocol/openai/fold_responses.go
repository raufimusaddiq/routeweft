package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// FoldResponsesStream folds an OpenAI Responses SSE stream into the one
// response document a non-stream client asked for. It is for an endpoint that
// only answers streams (Codex and Grok CLI declare forceStream: registry/codex.js:40,
// grok-cli.js:46; the pin folds in transformer/streamToJsonConverter.js:53-114).
//
// The terminal event's own response object is the document: it carries the id,
// model, status and the usage the endpoint reported, including the cached-token
// detail. Differences from the pin, each so nothing is invented or lost:
//
//   - the pin rebuilds a response from the stream and reports zero tokens when the
//     terminal event had none (:46,106-110). An absent usage stays absent here: a
//     value nobody reported is unknown, not zero;
//   - the pin keeps only its own subset of the terminal response and drops the
//     model and the usage detail. The terminal response is returned whole;
//   - output items the terminal response omits are filled from the
//     response.output_item.done events, in output_index order. The pin fills an
//     index it never saw with an empty assistant message (:99-102); here a missing
//     index is a gap in the stream, so the fold fails instead of presenting a
//     message nobody wrote;
//   - a stream that ends before a terminal event is truncated and fails (the pin
//     answers status "completed", :108);
//   - response.failed and error events fail the fold as a StreamError. The
//     pin returns a 200 document with status "failed" (:37-43).
//
// response.incomplete is a terminal event: its document says why it stopped.
func FoldResponsesStream(source io.Reader) (Folded, error) {
	reader := bufio.NewReaderSize(io.LimitReader(source, MaxTranslatedResponseBytes+1), 64*1024)
	var (
		event    []byte
		total    int
		terminal map[string]json.RawMessage
		items    = map[int]json.RawMessage{}
	)
	flush := func() error {
		payload := bytes.TrimSpace(event)
		event = nil
		if len(payload) == 0 || terminal != nil || bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		var frame struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Index    *int            `json:"output_index"`
			Error    json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			return fmt.Errorf("stream carried a data payload that is not JSON: %w", err)
		}
		switch frame.Type {
		case "response.output_item.done":
			if frame.Index == nil || len(frame.Item) == 0 {
				return errors.New("stream carried an output item without its index or body")
			}
			items[*frame.Index] = frame.Item
		case "response.completed", "response.done", "response.incomplete":
			var response map[string]json.RawMessage
			if json.Unmarshal(frame.Response, &response) != nil || response == nil {
				return fmt.Errorf("stream %s event carried no response object", frame.Type)
			}
			terminal = response
		case "response.failed":
			var response struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(frame.Response, &response)
			return &StreamError{Raw: firstRaw(response.Error, frame.Error, payload)}
		case "error":
			return &StreamError{Raw: firstRaw(frame.Error, payload)}
		}
		return nil
	}
	for {
		line, err := reader.ReadBytes('\n')
		total += len(line)
		if total > MaxTranslatedResponseBytes {
			return Folded{}, errors.New("stream exceeds the size the fold accepts")
		}
		if len(line) > 0 {
			text := bytes.TrimRight(line, "\r\n")
			switch {
			case len(text) == 0:
				if ferr := flush(); ferr != nil {
					return Folded{}, ferr
				}
			case bytes.HasPrefix(text, []byte("data:")):
				data := bytes.TrimPrefix(text[len("data:"):], []byte(" "))
				if len(event) > 0 {
					event = append(event, '\n')
				}
				event = append(event, data...)
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return Folded{}, fmt.Errorf("stream read failed: %w", err)
			}
			break
		}
	}
	if ferr := flush(); ferr != nil {
		return Folded{}, ferr
	}
	if terminal == nil {
		return Folded{}, errors.New("stream ended before a terminal response event")
	}
	if !outputPresent(terminal["output"]) && len(items) == 0 {
		// The terminal event's own answer is no output: say so, as a Responses
		// object must, instead of leaving the key out.
		terminal["output"] = json.RawMessage("[]")
	}
	if !outputPresent(terminal["output"]) && len(items) > 0 {
		indexes := make([]int, 0, len(items))
		for index := range items {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		output := make([]json.RawMessage, 0, len(indexes))
		for position, index := range indexes {
			if index != position {
				return Folded{}, fmt.Errorf("stream skipped output item %d", position)
			}
			output = append(output, items[index])
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return Folded{}, err
		}
		terminal["output"] = encoded
	}
	body, err := json.Marshal(terminal)
	if err != nil {
		return Folded{}, err
	}
	return Folded{Body: body}, nil
}

// outputPresent reports whether the terminal response already lists its items.
func outputPresent(raw json.RawMessage) bool {
	var list []json.RawMessage
	return json.Unmarshal(raw, &list) == nil && len(list) > 0
}

func firstRaw(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if trimmed := bytes.TrimSpace(value); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			return value
		}
	}
	return json.RawMessage("null")
}
