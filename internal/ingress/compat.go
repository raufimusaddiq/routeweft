package ingress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// dispatch resolves the requested model, builds one bounded plan, selects the
// native or translated mode, and relays the upstream response with cancellation
// and incremental streaming (SPEC §8-§9). nativeBody/nativeHeaders run only on
// the native path; translated is the cross-protocol hook for later adapters.
func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request, source, model string, native nativeSpec, translated translateHook, stream bool) {
	started := time.Now()
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	response, provider, err := h.dispatchAttempts(r, snapshot, model, source, false, stream, func(provider routing.ProviderRef) (ChatTranslation, error) {
		plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: source, Provider: provider})
		if err != nil {
			return ChatTranslation{}, err
		}
		if !plan.NativePath() {
			if translated != nil {
				return translated(plan.TargetProtocol)
			}
			return ChatTranslation{}, fmt.Errorf("no %s translator registered for target protocol %q", source, plan.TargetProtocol)
		}
		endpoint := native.endpoint
		if native.endpointFor != nil && provider.UpstreamModel != "" {
			endpoint = native.endpointFor(provider.UpstreamModel)
		}
		if provider.Endpoint != "" {
			endpoint = provider.Endpoint
		}
		body, err := native.body(provider.UpstreamModel)
		var headers http.Header
		if native.headers != nil {
			headers = native.headers(provider)
		}
		return ChatTranslation{Endpoint: endpoint, Body: body, Headers: headers}, err
	})
	if err != nil {
		h.dispatchError(w, r, err)
		return
	}
	defer response.Body.Close()
	if source == systemoneadapter.Protocol {
		stream = strings.EqualFold(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]), "text/event-stream")
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		h.relayUpstreamError(w, response)
		h.emitOutcome(r, provider, response.StatusCode, stream, promptcache.Usage{}, true, started)
		return
	}
	if stream {
		if h.opts.OnRequestComplete != nil {
			scanner := &usageScanner{family: provider.Protocol}
			response.Body = struct {
				io.Reader
				io.Closer
			}{Reader: io.TeeReader(response.Body, scanner), Closer: response.Body}
			relayErr := h.copyNativeSSE(w, r, response)
			usage, _ := scanner.usage()
			observed := scanner.reportsUsage(relayErr == nil && r.Context().Err() == nil)
			if !observed {
				usage = promptcache.Usage{}
			}
			h.emitOutcome(r, provider, response.StatusCode, true, usage, observed, started)
			return
		}
		h.copyNativeSSE(w, r, response)
		return
	}
	if h.opts.OnRequestComplete != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		usage, observed := promptcache.Usage{}, false
		if readErr == nil {
			if parsed, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
				usage, observed = parsed, true
			}
		}
		_, _ = w.Write(responseBody)
		h.emitOutcome(r, provider, response.StatusCode, false, usage, observed, started)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// nativeSpec describes one protocol's native endpoint, body encoder and
// provider-credential headers.
type nativeSpec struct {
	endpoint string
	// endpointFor, when set, names the endpoint after the model the provider
	// serves, which differs from the requested one for an alias or a Combo member.
	endpointFor func(upstreamModel string) string
	body        func(upstreamModel string) ([]byte, error)
	headers     func(routing.ProviderRef) http.Header
}

// translateHook adapts one protocol's translator to the shared dispatcher.
type translateHook func(target string) (ChatTranslation, error)

// readBody enforces the configured request-body limit (PRD-API-005).
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err == nil {
		return body, true
	}
	var limitErr *http.MaxBytesError
	if errors.As(err, &limitErr) {
		h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds configured limit")
	} else if r.Context().Err() == nil {
		h.writeError(w, http.StatusBadRequest, "request_read_failed", "request body could not be read")
	}
	return nil, false
}

// geminiHeaders uses Gemini API-key auth for the provider credential.
func geminiHeaders(provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	if provider.APIToken != "" {
		headers.Set("X-Goog-Api-Key", provider.APIToken)
	}
	return headers
}

func (h *Handler) handleGeminiModels(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	models := make([]map[string]any, 0)
	allowed := accessFilter(r, snapshot)
	for _, model := range snapshot.Models() {
		id := model.ProviderID + "/" + model.ID
		if model.Source == "alias" {
			id = model.ID
		}
		if !allowed(id) {
			continue
		}
		models = append(models, map[string]any{
			"name":                       "models/" + model.ID,
			"displayName":                model.Name,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h *Handler) handleGeminiRoute(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	rest := r.PathValue("rest")
	model, method, ok := strings.Cut(rest, ":")
	if !ok || strings.Contains(model, "/") || model == "" || (method != "generateContent" && method != "streamGenerateContent") {
		h.writeError(w, http.StatusNotFound, "route_not_found", "Gemini model method is not supported")
		return
	}
	stream := method == "streamGenerateContent"
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	r = withClientBody(r, body)
	request, err := geminiadapter.ParseRequest(model, stream, body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	setBodyRewriter(r, func(raw []byte) (func(), error) {
		return swapRequest(request, func() (*geminiadapter.Request, error) { return geminiadapter.ParseRequest(model, stream, raw) })
	})
	native := nativeSpec{
		endpoint: "v1beta/models/" + model + ":" + method,
		endpointFor: func(upstreamModel string) string {
			return "v1beta/models/" + upstreamModel + ":" + method
		},
		body:    func(string) ([]byte, error) { return request.MarshalBody("") },
		headers: geminiHeaders,
	}
	h.dispatch(w, r, geminiadapter.Protocol, request.Model, native, geminiTranslate(h.opts.TranslateGemini, request), stream)
}

func geminiTranslate(translator GeminiTranslator, request *geminiadapter.Request) translateHook {
	if translator == nil {
		return nil
	}
	return func(target string) (ChatTranslation, error) { return translator(request, target) }
}

func (h *Handler) handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	r = withClientBody(r, body)
	request, err := openaiadapter.ParseChatRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	if snapshot.AdaptersActive() {
		setBodyRewriter(r, func(raw []byte) (func(), error) {
			return swapRequest(request, func() (*openaiadapter.ChatRequest, error) { return openaiadapter.ParseChatRequest(raw) })
		})
	}
	started := time.Now()
	var translation ChatTranslation
	// /v1/api/chat is a Chat client whose answer is reshaped for Ollama. The
	// oracle serves it by running its Chat handler and converting the answer
	// afterwards (src/app/api/v1/api/chat/route.js:26-27), so a model and account
	// resolve exactly as they do on /v1/chat/completions; asking for candidates
	// in the Ollama protocol excluded every account that did not speak it (#377).
	response, provider, err := h.dispatchAttempts(r, snapshot, request.Model, openaiadapter.ChatProtocol, false, request.Stream, func(provider routing.ProviderRef) (ChatTranslation, error) {
		translation = ChatTranslation{}
		upstreamModel := provider.UpstreamModel
		if upstreamModel == "" {
			upstreamModel = request.Model
		}
		switch provider.Protocol {
		case ollamaadapter.Protocol, openaiadapter.ChatProtocol:
			body, err := request.MarshalBody(provider.UpstreamModel)
			endpoint := "chat/completions"
			if provider.Protocol == ollamaadapter.Protocol {
				endpoint = ollamaEndpoint
			}
			if provider.Endpoint != "" {
				endpoint = provider.Endpoint
			}
			return ChatTranslation{Endpoint: endpoint, Body: body, Headers: openAIProviderHeaders(provider)}, err
		}
		translated, err := h.translatedOllamaChat(r, provider, request, upstreamModel)
		translation = translated
		return translated, err
	})
	if err != nil {
		h.dispatchError(w, r, err)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// A relayed upstream error is one JSON document, not a stream (#375).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		h.relayUpstreamError(w, response)
		h.emitOutcome(r, provider, response.StatusCode, request.Stream, promptcache.Usage{}, true, started)
		return
	}
	if request.Stream {
		w.WriteHeader(response.StatusCode)
		if provider.Protocol == ollamaadapter.Protocol {
			if h.opts.OnRequestComplete != nil {
				usage, observed := h.copyOllamaNativeStream(w, r, response, provider)
				h.emitOutcome(r, provider, response.StatusCode, true, usage, observed, started)
				return
			}
			h.copyNativeSSE(w, r, response)
			return
		}
		var answer io.Reader = response.Body
		if translation.Integrity != nil {
			// No heartbeat: Ollama reads bare JSON lines and an SSE comment would
			// be an invalid line, unlike the Chat path (openai.go:278).
			gated, gateErr := h.gateAnswer(r.Context(), response, provider, translation.Integrity, nil)
			if gateErr != nil {
				h.abandon(r, provider, response.StatusCode, true, promptcache.Usage{}, started, true)
				return
			}
			answer = gated
		}
		usage, observed, relayErr := h.copyTranslatedOllamaStream(w, r, answer, translation, provider, request.Model)
		status := response.StatusCode
		if relayErr != nil && r.Context().Err() == nil {
			// The status line is gone, so the failure reaches the client as one
			// Ollama error document, never as a short answer ending in done:true.
			writeOllamaStreamError(w, relayErr)
			status = http.StatusBadGateway
			h.recordClassification(provider.ConnectionID, classifyResponse(status, nil), time.Now())
		}
		h.emitTranslated(r, provider, status, true, usage, observed, started)
		return
	}
	if provider.Protocol == ollamaadapter.Protocol {
		if h.opts.OnRequestComplete != nil {
			responseBody, readErr := io.ReadAll(response.Body)
			usage, observed := promptcache.Usage{}, false
			if readErr == nil {
				if parsed, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
					usage, observed = parsed, true
				}
			}
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(responseBody)
			h.emitOutcome(r, provider, response.StatusCode, false, usage, observed, started)
			return
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}
	// The provider answered Chat (natively or through its response leg and the
	// Kiro gate); the Ollama client reads one document with message.content and
	// done:true.
	var answer io.Reader = response.Body
	if translation.Integrity != nil {
		gated, gateErr := h.gateAnswer(r.Context(), response, provider, translation.Integrity, nil)
		if gateErr != nil {
			h.abandon(r, provider, response.StatusCode, false, promptcache.Usage{}, started, true)
			return
		}
		answer = gated
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(answer, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		return
	}
	var gateFailure *concerns.StreamFailure
	if readErr != nil && errors.As(readErr, &gateFailure) {
		// The gate's verdict, in the code the pin gives it (executors/kiro.js:493-504).
		h.writeError(w, http.StatusBadGateway, concerns.Text(gateFailure.Value["code"]), concerns.Text(gateFailure.Value["message"]))
		h.recordClassification(provider.ConnectionID, classifyResponse(http.StatusBadGateway, nil), time.Now())
		h.emitTranslated(r, provider, http.StatusBadGateway, false, promptcache.Usage{}, false, started)
		return
	}
	if readErr != nil || len(responseBody) > openaiadapter.MaxTranslatedResponseBytes {
		h.writeError(w, http.StatusBadGateway, "upstream_response_invalid", "upstream response could not be read")
		h.emitTranslated(r, provider, http.StatusBadGateway, false, promptcache.Usage{}, false, started)
		return
	}
	usage, observed := promptcache.Usage{}, false
	if parsed, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
		usage, observed = parsed, true
	}
	document := responseBody
	if translation.Response != nil {
		document, err = translation.Response(responseBody)
	}
	if err == nil {
		document, err = ollamaadapter.ChatResponseToOllama(document, request.Model)
	}
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_response_invalid", "upstream response could not be read")
		h.emitTranslated(r, provider, http.StatusBadGateway, false, usage, observed, started)
		return
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(document)
	h.emitTranslated(r, provider, response.StatusCode, false, usage, observed, started)
}

// translatedOllamaChat prepares a Chat client's attempt on a provider that
// speaks another wire, with the credential and identity headers the translated
// Chat path applies (openai.go): the surface changes the answer's shape, never
// the account-selection or authentication rules. A hook Codec comes first, as it
// does there: qoder, cursor, grok-web and perplexity-web have no TranslateChat
// arm and would be refused after being selected.
func (h *Handler) translatedOllamaChat(r *http.Request, provider routing.ProviderRef, request *openaiadapter.ChatRequest, upstreamModel string) (ChatTranslation, error) {
	var translated ChatTranslation
	var err error
	if wire.HasCodec(provider.Protocol) {
		translated, err = h.codecTranslation(provider, request, nil, nil, false, upstreamModel)
	} else if h.opts.TranslateChat != nil {
		translatedRequest := *request
		translatedRequest.Model = upstreamModel
		translatedRequest.Scope = translationScope(r, provider)
		translatedRequest.Project = provider.Connection.Settings.ProjectID
		translated, err = h.opts.TranslateChat(&translatedRequest, provider.Protocol)
	} else {
		return ChatTranslation{}, fmt.Errorf("no Chat-to-provider translator registered")
	}
	if err != nil {
		return ChatTranslation{}, err
	}
	if translated.Headers == nil {
		translated.Headers = http.Header{}
	}
	if takesProviderHeaders(provider.Protocol) {
		for name, values := range openAIProviderHeaders(provider) {
			translated.Headers[name] = values
		}
	} else if provider.Protocol == geminiadapter.Protocol {
		for name, values := range geminiHeaders(provider) {
			translated.Headers[name] = values
		}
	} else if provider.Protocol == anthropicadapter.MessagesProtocol {
		// A provider may rewrite the converted body (the Claude OAuth identity),
		// exactly as on the Chat path.
		if translated.Body, err = wire.PrepareMessages(provider.ProviderID, translated.Body); err != nil {
			return ChatTranslation{}, err
		}
		for name, values := range anthropicHeaders(r, provider) {
			translated.Headers[name] = values
		}
	}
	if provider.Endpoint != "" {
		translated.Endpoint = provider.Endpoint
	}
	return translated, nil
}

// copyTranslatedOllamaStream converts a provider's stream to Chat when its wire
// is not Chat, then relays the Chat SSE as Ollama ndjson. The provider's own
// usage is scanned, so the streamed and buffered branches account alike, and a
// conversion failure is returned rather than left as an early end of stream.
func (h *Handler) copyTranslatedOllamaStream(w http.ResponseWriter, r *http.Request, body io.Reader, translation ChatTranslation, provider routing.ProviderRef, model string) (promptcache.Usage, bool, error) {
	scanner := &usageScanner{family: provider.Protocol, whole: translation.WholeAnswer}
	answer := io.TeeReader(body, scanner)
	var err error
	if translation.Stream == nil {
		err = h.relayOllamaSSE(w, r, answer, model)
	} else {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		reader, writer := io.Pipe()
		done := make(chan struct{})
		go func() {
			// CloseWithError carries a conversion failure through the pipe. The
			// scanner is read only after this returns, so the tee has one writer.
			_ = writer.CloseWithError(translation.Stream(ctx, answer, writer))
			close(done)
		}()
		err = h.relayOllamaSSE(w, r, reader, model)
		// A relay that stopped early (the client left, or the terminal event) is
		// unblocked here; the converter's own error, if any, has already reached
		// the reader above.
		_ = reader.Close()
		<-done
	}
	usage, _ := scanner.usage()
	observed := err == nil && scanner.reportsUsage(r.Context().Err() == nil)
	if !observed {
		usage = promptcache.Usage{}
	}
	return usage, observed, err
}

// writeOllamaStreamError is the failure terminal of an answer whose status is
// already sent: one Ollama error document, in the relayed-error shape #375 set.
func writeOllamaStreamError(w http.ResponseWriter, err error) {
	document := map[string]any{"code": "upstream_stream_error", "message": err.Error(), "type": "upstream_stream_error"}
	var failure *concerns.StreamFailure
	if errors.As(err, &failure) {
		for _, key := range []string{"code", "message", "type"} {
			if value := concerns.Text(failure.Value[key]); value != "" {
				document[key] = value
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": document})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// copyOllamaNativeStream relays a native Ollama ndjson stream byte-for-byte
// while extracting usage from each line. Ollama reports prompt_eval_count and
// eval_count on the final {"done":true} object (PRD §13), so the last complete
// object wins. It returns whatever usage it parsed and whether any was found
// (also on cancellation or a failed client write: the upstream was billed for
// what it already reported), so the caller publishes one accurate record.
func (h *Handler) copyOllamaNativeStream(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef) (promptcache.Usage, bool) {
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	var pending []byte
	usage := promptcache.Usage{}
	observed := false
	for {
		if r.Context().Err() != nil {
			return usage, observed
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return usage, observed
			}
			if canFlush {
				flusher.Flush()
			}
			pending = append(pending, line...)
			for {
				index := bytes.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				if parsed, ok := shared.ParseUsage(provider.Protocol, bytes.TrimSpace(pending[:index])); ok {
					usage, observed = parsed, true
				}
				pending = pending[index+1:]
			}
		}
		if err != nil {
			if len(pending) > 0 {
				if parsed, ok := shared.ParseUsage(provider.Protocol, bytes.TrimSpace(pending)); ok {
					usage, observed = parsed, true
				}
			}
			return usage, observed
		}
	}
}

func (h *Handler) copyOllamaStream(w http.ResponseWriter, r *http.Request, response *http.Response, model string) {
	_ = h.relayOllamaSSE(w, r, response.Body, model)
}

// relayOllamaSSE relays Chat SSE as Ollama ndjson and reports the reader failure
// that cut the stream short; a clean end (the terminal event, or EOF after the
// last chunk) writes the final done object and returns nil.
func (h *Handler) relayOllamaSSE(w http.ResponseWriter, r *http.Request, body io.Reader, model string) error {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, canFlush := w.(http.Flusher)
	tracked := &trackedReader{source: body}
	reader := bufio.NewReaderSize(tracked, 32*1024)
	writeLine := func(payload map[string]any) {
		_ = json.NewEncoder(w).Encode(payload)
		if canFlush {
			flusher.Flush()
		}
	}
	for {
		if err := r.Context().Err(); err != nil {
			return err
		}
		payload, ok := nextSSEData(reader)
		if !ok {
			return tracked.err
		}
		if payload == "[DONE]" {
			break
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		// A converted stream reports its failure as a Chat error object before
		// [DONE] (concerns.FailChatStream); that is not a normal end.
		if failure := concerns.Object(chunk["error"]); failure != nil {
			return &concerns.StreamFailure{Value: failure}
		}
		choices := concerns.Items(chunk["choices"])
		if len(choices) == 0 {
			continue
		}
		choice := concerns.Object(choices[0])
		if content := concerns.Text(concerns.Object(choice["delta"])["content"]); content != "" {
			writeLine(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": content}, "done": false})
		}
		if choice["finish_reason"] != nil {
			break
		}
	}
	writeLine(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": ""}, "done": true})
	return nil
}

// trackedReader remembers the first non-EOF read failure, which nextSSEData
// otherwise folds into "the stream ended".
type trackedReader struct {
	source io.Reader
	err    error
}

func (t *trackedReader) Read(p []byte) (int, error) {
	n, err := t.source.Read(p)
	if err != nil && err != io.EOF && t.err == nil {
		t.err = err
	}
	return n, err
}

func (h *Handler) handleSystemOne(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	r = withClientBody(r, body)
	request, err := systemoneadapter.ParseRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	// System One providers configure the full typed endpoint as their base URL
	// (for example https://api.typesafe.ai/v1/systemone), so Routeweft posts to
	// that URL verbatim rather than appending a path (PRD §6 typesafe).
	native := nativeSpec{
		endpoint: "",
		body:     func(upstream string) ([]byte, error) { return request.MarshalBody(upstream) },
		headers:  openAIProviderHeaders,
	}
	h.dispatch(w, r, systemoneadapter.Protocol, strings.TrimSpace(request.Model), native, systemoneTranslate(h.opts.TranslateSystemOne, request), false)
}

func systemoneTranslate(translator SystemOneTranslator, request *systemoneadapter.Request) translateHook {
	if translator == nil {
		return nil
	}
	return func(target string) (ChatTranslation, error) { return translator(request, target) }
}

// copyNativeSSE relays native compatibility streams without decoding the
// body, preserving client-disconnect cancellation and incremental flushing.
func (h *Handler) copyNativeSSE(w http.ResponseWriter, r *http.Request, response *http.Response) error {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return writeErr
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// nextSSEData returns the next SSE data payload, skipping comments, event names
// and blank separators. It reports false when the stream ends.
func nextSSEData(reader *bufio.Reader) (string, bool) {
	for {
		line, err := reader.ReadString('\n')
		if line == "" && err != nil {
			return "", false
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if data, ok := strings.CutPrefix(trimmed, "data:"); ok {
			return strings.TrimSpace(data), true
		}
		if err != nil {
			return "", false
		}
	}
}
