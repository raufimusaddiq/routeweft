// Package rtk owns RTK request transformation (SPEC §17.1): compress
// tool-result/context structures while preserving required semantics.
package rtk

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

// Transform compresses tool-result turns.
type Transform struct {
	Enabled bool
}

func (Transform) Name() string { return "rtk" }

// Apply minifies JSON tool results by removing insignificant whitespace between
// tokens. Structurally non-JSON results are left byte-identical, so RTK never
// invents or drops content (SPEC §17.1).
func (t Transform) Apply(request transforms.Request) (transforms.Request, error) {
	if !t.Enabled {
		return request, nil
	}
	for i := range request.Messages {
		message := &request.Messages[i]
		if !message.ToolResult {
			continue
		}
		message.Content = compress(message.Content)
	}
	return request, nil
}

func compress(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return content
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return content
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, []byte(trimmed)); err != nil {
		return content
	}
	minified := buffer.String()
	if minified == "" {
		return content
	}
	return minified
}
