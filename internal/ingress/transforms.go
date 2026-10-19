package ingress

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

func (h *Handler) finalBody(r *http.Request, snapshot *runtime.RuntimeSnapshot, protocol string, body []byte) []byte {
	bypass := false
	for _, name := range []string{"X-9router-Token-Saver", "X-Literouter-Token-Saver"} {
		for _, value := range r.Header.Values(name) {
			bypass = bypass || strings.EqualFold(strings.TrimSpace(value), "off")
		}
	}
	if !bypass && h.opts.TransformFinalBody != nil {
		// A failed or malformed saver must not mutate the body's fail-open copy.
		if transformed, err := h.opts.TransformFinalBody(r.Context(), snapshot, protocol, append([]byte(nil), body...)); err == nil {
			trimmed := bytes.TrimSpace(transformed)
			if len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed) {
				body = transformed
			}
		}
	}
	if protocol == anthropic.MessagesProtocol {
		if anchored, err := promptcache.Anchor(body, promptcache.DefaultBudget); err == nil {
			body = anchored
		}
	}
	return body
}
