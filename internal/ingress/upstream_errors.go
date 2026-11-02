package ingress

import (
	"encoding/json"
	"io"
	"net/http"
)

// An upstream error body is relayed whole for diagnosis, but a 64 KiB cap keeps
// one mislabelled answer from buffering an unbounded stream.
const upstreamErrorRelayLimit = 64 << 10

// modelUnavailableMessage is the answer when an upstream reports a model it
// cannot serve. The upstream's own suggestion can name an id this route does not
// list; that is what sent callers to "kenari-free", which is not in the
// catalogue (#360). This text names no model at all.
const modelUnavailableMessage = "The requested model is currently unavailable. Pick another model from /v1/models."

// relayUpstreamError relays a non-2xx upstream answer, replacing a
// model_unavailable message with one that names no id the catalogue may not
// contain. Other statuses, bodies and fields pass through unchanged.
func (h *Handler) relayUpstreamError(w http.ResponseWriter, response *http.Response) {
	body, err := io.ReadAll(io.LimitReader(response.Body, upstreamErrorRelayLimit+1))
	if err != nil || len(body) > upstreamErrorRelayLimit {
		_, _ = w.Write(body)
		_, _ = io.Copy(w, response.Body)
		return
	}
	_, _ = w.Write(rewriteModelUnavailable(body))
}

func rewriteModelUnavailable(body []byte) []byte {
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil {
		return body
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(document["error"], &detail) != nil {
		return body
	}
	if !namesModelUnavailable(detail) {
		return body
	}
	message, err := json.Marshal(modelUnavailableMessage)
	if err != nil {
		return body
	}
	detail["message"] = message
	if document["error"], err = json.Marshal(detail); err != nil {
		return body
	}
	rewritten, err := json.Marshal(document)
	if err != nil {
		return body
	}
	return rewritten
}

func namesModelUnavailable(detail map[string]json.RawMessage) bool {
	for _, key := range []string{"code", "type"} {
		var token string
		if json.Unmarshal(detail[key], &token) == nil && token == "model_unavailable" {
			return true
		}
	}
	return false
}
