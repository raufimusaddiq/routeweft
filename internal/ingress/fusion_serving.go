package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Fusion serving (open-sse/services/combo.js:471-625, chat.js:116-133). A Fusion
// Combo fans the request out to its panel models, collects the answers with a
// quorum, a grace window and a hard timeout, and has a judge model write the one
// answer the client sees. The panel and the judge are ordinary requests to the
// same handler, made in process with the caller's own credentials, so every
// protocol, translation, fallback and accounting path is the one a client request
// takes. Only the answer of the last call reaches the client.

// internalCall marks a request the gateway makes on behalf of a client: the model
// it serves instead of the one in the body, and how deeply Fusion calls nest. The
// key is unexported, so no client can set it.
type internalCall struct {
	target string
	depth  int
}

type internalCallKey struct{}

func internalCallOf(r *http.Request) *internalCall {
	call, _ := r.Context().Value(internalCallKey{}).(*internalCall)
	return call
}

// fused wraps an inference handler: a request for a Fusion Combo is answered by
// the panel and the judge, anything else goes straight to the handler. The check
// costs one snapshot read when no Combo is served as Fusion.
func (h *Handler) fused(next http.HandlerFunc) http.HandlerFunc {
	var gated http.HandlerFunc
	gated = func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := h.snapshot.Load()
		if err != nil || !snapshot.HasFusion() {
			next(w, r)
			return
		}
		call := internalCallOf(r)
		rest := r.PathValue("rest")
		gemini := strings.Contains(rest, ":") // v1beta/models/{model}:{method}
		var body []byte
		target := ""
		if call != nil {
			target = call.target
		} else if gemini {
			target, _, _ = strings.Cut(rest, ":")
		}
		if call == nil || call.target == "" {
			var ok bool
			if body, ok = h.readFusionBody(w, r); !ok {
				return
			}
			if call == nil && !gemini {
				var peek struct {
					Model string `json:"model"`
				}
				_ = json.Unmarshal(body, &peek)
				target = peek.Model
			}
		}
		combo, isFusion := snapshot.FusionCombo(target)
		if !isFusion {
			if body != nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			next(w, r)
			return
		}
		if call == nil {
			if !h.authorize(w, r) {
				return
			}
			if !requestPermitted(callerKey(r, snapshot), snapshot, target) {
				h.writeError(w, http.StatusForbidden, "model_not_allowed", modelNotAllowed(target))
				return
			}
		}
		if body == nil {
			// A Fusion Combo reached as a member of another call: the body is the
			// one that call made.
			var ok bool
			if body, ok = h.readFusionBody(w, r); !ok {
				return
			}
		}
		depth := 0
		if call != nil {
			depth = call.depth
		}
		if depth >= runtime.MaxComboDepth {
			h.writeError(w, http.StatusBadGateway, "combo_too_deep", "Fusion Combos nest too deeply")
			return
		}
		h.serveFusion(w, r, gated, combo, body, gemini, depth)
	}
	return gated
}

func (h *Handler) readFusionBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds configured limit")
		} else if r.Context().Err() == nil {
			h.writeError(w, http.StatusBadRequest, "request_read_failed", "request body could not be read")
		}
		return nil, false
	}
	return body, true
}

// subRequest is the request the gateway makes to serve model with body: the
// client's request, with its credentials, path and headers, and another body.
func subRequest(ctx context.Context, r *http.Request, body []byte, target string, depth int, geminiMethod string) *http.Request {
	sub := r.Clone(context.WithValue(ctx, internalCallKey{}, &internalCall{target: target, depth: depth + 1}))
	sub.Body = io.NopCloser(bytes.NewReader(body))
	sub.ContentLength = int64(len(body))
	sub.Header = sub.Header.Clone()
	sub.Header.Del("Content-Length")
	sub.Header.Set("Content-Type", "application/json")
	if geminiMethod != "" {
		model, _, _ := strings.Cut(r.PathValue("rest"), ":")
		sub.SetPathValue("rest", model+":"+geminiMethod)
	}
	return sub
}

// serveFusion is handleFusionChat (combo.js:547-625).
func (h *Handler) serveFusion(w http.ResponseWriter, r *http.Request, serve http.HandlerFunc, combo runtime.Combo, body []byte, gemini bool, depth int) {
	members := combo.Resolve()
	panel := make([]string, 0, len(members))
	for _, member := range members {
		if member.IsCombo() {
			panel = append(panel, member.ModelID)
		} else {
			panel = append(panel, member.ProviderID+"/"+member.ModelID)
		}
	}
	// A single-model Fusion has nothing to fuse (combo.js:556-559).
	if len(panel) == 1 {
		serve(w, subRequest(r.Context(), r, body, panel[0], depth, ""))
		return
	}
	defaults := routing.DefaultFusionConfig()
	if len(panel) > defaults.MaxPanels {
		h.writeError(w, http.StatusUnprocessableEntity, "fusion_panel_limit", "this Fusion Combo has more panel models than the gateway fans out to")
		return
	}
	config, err := combo.FusionConfig(defaults)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "fusion_config_invalid", err.Error())
		return
	}
	select {
	case h.fusionSlots <- struct{}{}:
		defer func() { <-h.fusionSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusServiceUnavailable, "fusion_busy", "too many Fusion requests are in flight")
		return
	}
	method := ""
	if gemini {
		method = "generateContent"
	}
	panelJSON, ok := panelBody(body, !gemini)
	if !ok {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object")
		return
	}

	panelCtx, cancel := context.WithCancel(r.Context())
	results := h.collectPanel(panelCtx, r, serve, panel, panelJSON, method, depth, config)
	cancel()

	var answers []fusionAnswer
	for index, result := range results {
		if result == nil || !result.ok() {
			continue
		}
		answer, ok := decodeObject(result.body.Bytes())
		if !ok {
			continue
		}
		if text := extractPanelText(answer); text != "" {
			answers = append(answers, fusionAnswer{model: panel[index], text: text})
		}
	}
	switch len(answers) {
	case 0:
		h.writeError(w, http.StatusServiceUnavailable, "fusion_panel_failed", "All fusion panel models failed")
	case 1:
		// Only one panel model answered: the client gets that model's own answer,
		// streamed if it asked (combo.js:616-619).
		serve(w, subRequest(r.Context(), r, body, answers[0].model, depth, ""))
	default:
		judgeBody, ok := appendUserTurn(body, buildJudgePrompt(answers))
		if !ok {
			h.writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must be a JSON object")
			return
		}
		judge := strings.TrimSpace(combo.JudgeModel)
		if judge == "" {
			judge = panel[0]
		}
		serve(w, subRequest(r.Context(), r, judgeBody, judge, depth, ""))
	}
}

// bufferedResponse is the answer to one panel call, kept in memory up to the byte
// cap. A call whose answer is larger is cancelled and does not count.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	limit  int64
	over   bool
	cancel context.CancelFunc
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *bufferedResponse) Flush() {}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.over {
		return 0, io.ErrShortWrite
	}
	if b.limit > 0 && int64(b.body.Len()+len(p)) > b.limit {
		b.over = true
		b.cancel()
		return 0, io.ErrShortWrite
	}
	return b.body.Write(p)
}

// ok is the oracle's res.ok: a 2xx answer.
func (b *bufferedResponse) ok() bool { return !b.over && b.status >= 200 && b.status < 300 }

// collectPanel is combo.js:495-522 and 580-583. It starts every panel call and
// returns, in panel order, what had settled when the collection ended: all of them
// settled, or the grace window after minPanel successes ran out, or the hard
// timeout. A call still running then is cancelled and its slot is nil.
func (h *Handler) collectPanel(ctx context.Context, r *http.Request, serve http.HandlerFunc, panel []string, panelJSON []byte, method string, depth int, config routing.FusionConfig) []*bufferedResponse {
	type settled struct {
		index    int
		response *bufferedResponse
	}
	arrived := make(chan settled, len(panel))
	for index, target := range panel {
		callCtx, cancel := context.WithCancel(ctx)
		go func() {
			defer cancel()
			recorder := &bufferedResponse{header: http.Header{}, limit: config.MaxResponseBytes, cancel: cancel}
			func() {
				defer func() {
					// A handler that panics must not take the gateway down with a panel call.
					if recover() != nil && recorder.status == 0 {
						recorder.status = http.StatusInternalServerError
					}
				}()
				serve(recorder, subRequest(callCtx, r, panelJSON, target, depth, method))
			}()
			arrived <- settled{index, recorder}
		}()
	}
	out := make([]*bufferedResponse, len(panel))
	hard := time.NewTimer(config.HardTimeout)
	defer hard.Stop()
	var grace <-chan time.Time
	var graceTimer *time.Timer
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	successes, done := 0, 0
	for done < len(panel) {
		select {
		case result := <-arrived:
			out[result.index] = result.response
			done++
			if result.response.ok() {
				successes++
			}
			if successes >= config.MinPanelQuorum && graceTimer == nil {
				graceTimer = time.NewTimer(config.Grace)
				grace = graceTimer.C
			}
		case <-grace:
			return out
		case <-hard.C:
			return out
		case <-ctx.Done():
			return out
		}
	}
	return out
}
