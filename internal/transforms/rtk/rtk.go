// Package rtk owns RTK request transformation (SPEC §17.1): compress
// tool-result/context structures while preserving required semantics.
package rtk

import (
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

// Transform compresses tool-result turns.
type Transform struct {
	Enabled bool
}

func (Transform) Name() string { return "rtk" }

// Apply folds consecutive duplicate lines in tool results, preserving exact
// content and order while reducing repeated context. Non-tool turns pass
// through unchanged.
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
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		return content
	}
	folded := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		j := i + 1
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		if j-i > 1 {
			folded = append(folded, lines[i]+" [repeated "+strconv.Itoa(j-i)+" times]")
		} else {
			folded = append(folded, lines[i])
		}
		i = j
	}
	return strings.Join(folded, "\n")
}
