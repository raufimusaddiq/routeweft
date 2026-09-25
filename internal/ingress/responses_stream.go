package ingress

import (
	"bytes"
	"encoding/json"
)

// responsesEventTerminal reports whether one complete SSE event is a terminal
// Responses event. It accepts both the event: name and the JSON type field so
// upstream formatting variance cannot bypass the contract.
func responsesEventTerminal(event []byte) bool {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.Equal(line, []byte("event: response.completed")),
			bytes.Equal(line, []byte("event: response.failed")),
			bytes.Equal(line, []byte("event: response.incomplete")):
			return true
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 {
				continue
			}
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				continue
			}
			switch envelope.Type {
			case "response.completed", "response.failed", "response.incomplete":
				return true
			}
		}
	}
	return false
}

// responsesIncompleteEvent is emitted only when an upstream stream closes
// without a terminal event, so clients never see a silently truncated stream.
func responsesIncompleteEvent() []byte {
	return []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":0,\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_incomplete\",\"message\":\"upstream stream ended without a terminal event\"}}}\n\n")
}
