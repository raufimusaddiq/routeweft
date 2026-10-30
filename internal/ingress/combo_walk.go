package ingress

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// walkCombo is handleComboChat for a Combo with another Combo among its members
// (open-sse combo.js:280-382, chat.js:177-229). Each member, in the order the Combo
// plans, is served as a request of its own, exactly as the oracle calls
// handleSingleModelChat for each: a member that is itself a Combo answers with its
// own strategy, rotation and failure answer, and a member that is a Fusion Combo
// answers as Fusion. The first member that answers, or fails in a way that is the
// answer (a 4xx the Combo does not fall past), is what the client gets, written as
// it is; a member that fails and is fallen past is held back and the next one is
// tried.
func (h *Handler) walkCombo(w http.ResponseWriter, r *http.Request, serve http.HandlerFunc, snapshot *runtime.RuntimeSnapshot, name string, body []byte, depth int) {
	planned, ok := h.planCombo(snapshot, callerKey(r, snapshot), name, detectRequiredCapabilities(body))
	if !ok || len(planned) == 0 {
		h.writeError(w, http.StatusServiceUnavailable, "no_eligible_accounts", "no eligible provider/account")
		return
	}
	trace := newComboTrace()
	for index, member := range planned {
		target := member.ModelID
		if !member.IsCombo() {
			target = member.ProviderID + "/" + member.ModelID
		}
		memberBody := body
		if member.Adapter {
			// A model the gateway added is called with its history fitted to its
			// own window (capacityAdapter.js:154-168).
			memberBody = stripHistoryForContext(body, member.ContextWindow)
		}
		probe := &probeWriter{w: w, header: http.Header{}}
		serve(probe, subRequest(r.Context(), r, memberBody, target, name, fmt.Sprintf("member-%d", index+1), depth, ""))
		if r.Context().Err() != nil {
			return
		}
		if probe.committed {
			return
		}
		text := failureText(probe.status, "", probe.held.Bytes())
		if probe.status == 0 || !shouldFallbackFailure(probe.status, text) {
			probe.commit()
			return
		}
		trace.noteBody(index, probe.status, "", probe.held.Bytes())
		if err := trace.wait(r.Context(), index); err != nil {
			return
		}
	}
	h.dispatchError(w, r, trace.exhausted())
}

// probeWriter lets a member answer straight to the client when it succeeds and
// holds an error back until the Combo has decided whether to fall past it. A 2xx
// status commits at once, so a stream is never buffered.
type probeWriter struct {
	w         http.ResponseWriter
	header    http.Header
	status    int
	committed bool
	held      bytes.Buffer
}

// heldLimit bounds an error body kept back; the rest is dropped (the message the
// Combo reads is at the front of any error body).
const heldLimit = 64 << 10

func (p *probeWriter) Header() http.Header {
	if p.committed {
		return p.w.Header()
	}
	return p.header
}

func (p *probeWriter) WriteHeader(status int) {
	if p.status != 0 {
		return
	}
	p.status = status
	if status >= 200 && status < 300 {
		p.commit()
	}
}

func (p *probeWriter) Write(data []byte) (int, error) {
	if p.status == 0 {
		p.WriteHeader(http.StatusOK)
	}
	if p.committed {
		return p.w.Write(data)
	}
	if room := heldLimit - p.held.Len(); room > 0 {
		p.held.Write(data[:min(len(data), room)])
	}
	return len(data), nil
}

func (p *probeWriter) Flush() {
	if flusher, ok := p.w.(http.Flusher); ok && p.committed {
		flusher.Flush()
	}
}

// commit sends what the member answered to the client: the held headers, the
// status and any body held back.
func (p *probeWriter) commit() {
	if p.committed {
		return
	}
	p.committed = true
	for name, values := range p.header {
		p.w.Header()[name] = values
	}
	status := p.status
	if status == 0 {
		status = http.StatusOK
	}
	p.w.WriteHeader(status)
	if p.held.Len() > 0 {
		_, _ = io.Copy(p.w, &p.held)
	}
}
