package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// maxStreamLine bounds the data line the empty-tool_calls strip will buffer. A
// longer line is relayed as it came: the strip is a compatibility nicety and must
// not hold a stream back or grow without limit.
const maxStreamLine = 1 << 20

// ResponseStream wraps the body of a Chat stream relayed to the client with the
// provider's response-side rewrite, or returns it untouched when the provider has
// none. The wrapper reads and writes whole lines, so no event is delayed past its
// terminating newline.
func ResponseStream(providerID string, body io.ReadCloser) io.ReadCloser {
	if !For(providerID).StripEmptyToolCalls {
		return body
	}
	return &lineFilter{source: body, reader: bufio.NewReaderSize(body, 32*1024), rewrite: stripEmptyToolCalls}
}

type lineFilter struct {
	source  io.Closer
	reader  *bufio.Reader
	rewrite func([]byte) []byte
	pending []byte
	long    bool // inside a line longer than maxStreamLine, relayed as read
	err     error
}

func (f *lineFilter) Close() error { return f.source.Close() }

func (f *lineFilter) Read(out []byte) (int, error) {
	for len(f.pending) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		line, err := f.next()
		f.pending, f.err = line, err
	}
	n := copy(out, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

// next returns one line (or an oversized line's next piece) and the read error
// that follows it.
func (f *lineFilter) next() ([]byte, error) {
	line, err := f.reader.ReadSlice('\n')
	piece := append([]byte(nil), line...)
	for !f.long && err == bufio.ErrBufferFull && len(piece) < maxStreamLine {
		line, err = f.reader.ReadSlice('\n')
		piece = append(piece, line...)
	}
	if err == bufio.ErrBufferFull {
		f.long = true
		return piece, nil
	}
	if f.long {
		f.long = false
		return piece, err
	}
	return f.rewrite(piece), err
}

var toolCallsKey = []byte(`"tool_calls"`)

// stripEmptyToolCalls removes `tool_calls: []` from choices[*].delta of one SSE
// data line (open-sse/utils/stream.js:201-213). Some CodeBuddy deltas carry the
// empty array on every chunk, and an AI SDK client that tests `delta.tool_calls
// != null` reads each as the start of a tool call and ends reasoning early. An
// empty array carries no call, so removing it loses nothing.
//
// A line without the key, that is not a data line, is [DONE], does not parse as a
// JSON object, or whose tool_calls is null, populated or any other type is
// returned byte for byte. Only a line that had the empty array is re-encoded, so
// its keys come out in alphabetical order.
func stripEmptyToolCalls(line []byte) []byte {
	if !bytes.HasPrefix(line, []byte("data:")) || !bytes.Contains(line, toolCallsKey) {
		return line
	}
	ending := line[len(bytes.TrimRight(line, "\r\n")):]
	payload := bytes.TrimSpace(line[len("data:"):])
	var chunk map[string]json.RawMessage
	if json.Unmarshal(payload, &chunk) != nil || chunk == nil {
		return line
	}
	var choices []json.RawMessage
	if json.Unmarshal(chunk["choices"], &choices) != nil {
		return line
	}
	changed := false
	for i, raw := range choices {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) != nil || choice == nil {
			continue
		}
		var delta map[string]json.RawMessage
		if json.Unmarshal(choice["delta"], &delta) != nil || delta == nil {
			continue
		}
		var calls []json.RawMessage
		if !isArrayRaw(delta["tool_calls"]) || json.Unmarshal(delta["tool_calls"], &calls) != nil || len(calls) != 0 {
			continue
		}
		delete(delta, "tool_calls")
		encodedDelta, err := json.Marshal(delta)
		if err != nil {
			return line
		}
		choice["delta"] = encodedDelta
		encodedChoice, err := json.Marshal(choice)
		if err != nil {
			return line
		}
		choices[i], changed = encodedChoice, true
	}
	if !changed {
		return line
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return line
	}
	chunk["choices"] = encodedChoices
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
	return append(append([]byte("data: "), encoded...), ending...)
}
