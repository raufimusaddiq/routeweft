package ingress

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// dispatch resolves the requested model, builds one bounded plan, selects the
// native or translated mode, and relays the upstream response with cancellation
// and incremental streaming (SPEC §8-§9). nativeBody/nativeHeaders run only on
// the native path; translated is the cross-protocol hook for later adapters.
func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request, source, model string, native nativeSpec, translated translateHook, stream bool) {
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	if h.opts.ProviderResolver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no_provider", "no provider is configured")
		return
	}
	provider, ok := h.opts.ProviderResolver(snapshot, model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured for inference", model))
		return
	}
	plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: source, TargetProtocol: provider.Protocol, Provider: provider})
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "route_unavailable", err.Error())
		return
	}
	endpoint := native.endpoint
	var headers http.Header
	var outbound []byte
	if plan.NativePath() {
		// Multi-transport providers may serve a different path per native
		// protocol; the registry owns that mapping (SPEC §11).
		if h.opts.EndpointFor != nil {
			if override, ok := h.opts.EndpointFor(provider.ProviderID, provider.Protocol); ok {
				endpoint = override
			}
		}
		outbound, err = native.body(provider.UpstreamModel)
		if native.headers != nil {
			headers = native.headers(provider)
		}
	} else if translated != nil {
		var full ChatTranslation
		full, err = translated(plan.TargetProtocol)
		endpoint, outbound, headers = full.Endpoint, full.Body, full.Headers
	} else {
		err = fmt.Errorf("no %s translator registered for target protocol %q", source, plan.TargetProtocol)
	}
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
		return
	}
	upstreamRequest, err := h.newUpstreamRequest(r, provider, endpoint, outbound, headers)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
		return
	}
	response, err := h.clientFor(provider).Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		prefix, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
		h.recordUpstreamFailure(provider, response.StatusCode, response.Header, prefix)
		_, _ = w.Write(prefix)
		_, _ = io.Copy(w, response.Body)
		return
	}
	if stream {
		if h.opts.OnUsage != nil {
			scanner := &usageScanner{family: provider.Protocol}
			response.Body = struct {
				io.Reader
				io.Closer
			}{Reader: io.TeeReader(response.Body, scanner), Closer: response.Body}
			h.copyNativeSSE(w, r, response)
			if usage, ok := scanner.usage(); ok && scanner.complete {
				h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
			}
			return
		}
		h.copyNativeSSE(w, r, response)
		return
	}
	if h.opts.OnUsage != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr == nil {
			if usage, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
				h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
			}
		}
		_, _ = w.Write(responseBody)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// nativeSpec describes one protocol's native endpoint, body encoder and
// provider-credential headers.
type nativeSpec struct {
	endpoint string
	body     func(upstreamModel string) ([]byte, error)
	headers  func(routing.ProviderRef) http.Header
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
	for _, model := range snapshot.Models() {
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
	request, err := geminiadapter.ParseRequest(model, stream, body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	native := nativeSpec{
		endpoint: "v1beta/models/" + model + ":" + method,
		body:     func(string) ([]byte, error) { return request.MarshalBody("") },
		headers:  geminiHeaders,
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
	request, err := openaiadapter.ParseChatRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if h.opts.ProviderResolver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no_provider", "no Ollama-compatible provider is configured")
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	provider, ok := h.opts.ProviderResolver(snapshot, request.Model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", "model is not configured")
		return
	}
	if provider.Protocol != ollamaadapter.Protocol {
		if h.opts.TranslateChat == nil {
			h.writeError(w, http.StatusBadGateway, "request_translation_failed", "no Chat-to-provider translator registered")
			return
		}
		translated, err := h.opts.TranslateChat(request, provider.Protocol)
		if err != nil {
			h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
			return
		}
		translatedRequest, err := h.newUpstreamRequest(r, provider, translated.Endpoint, translated.Body, translated.Headers)
		if err != nil {
			h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
			return
		}
		response, err := h.clientFor(provider).Do(translatedRequest)
		if err != nil {
			if r.Context().Err() == nil {
				h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
			}
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			prefix, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
			h.recordUpstreamFailure(provider, response.StatusCode, response.Header, prefix)
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(prefix)
			_, _ = io.Copy(w, response.Body)
			return
		}
		if request.Stream {
			h.copyOllamaStream(w, r, response, request.Model)
			return
		}
		if h.opts.OnUsage != nil {
			responseBody, readErr := io.ReadAll(response.Body)
			if readErr == nil {
				if usage, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
					h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
				}
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(responseBody)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return
	}
	requestBody, err := request.MarshalBody(provider.UpstreamModel)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
		return
	}
	upstreamRequest, err := h.newUpstreamRequest(r, provider, "api/chat", requestBody, nil)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
		return
	}
	response, err := h.clientFor(provider).Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() == nil {
			h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		}
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		prefix, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
		h.recordUpstreamFailure(provider, response.StatusCode, response.Header, prefix)
		_, _ = w.Write(prefix)
		_, _ = io.Copy(w, response.Body)
		return
	}
	if request.Stream {
		if h.opts.OnUsage != nil {
			h.copyOllamaNativeStream(w, r, response, provider)
			return
		}
		h.copyNativeSSE(w, r, response)
		return
	}
	if h.opts.OnUsage != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr == nil {
			if usage, ok := shared.ParseUsage(provider.Protocol, responseBody); ok {
				h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
			}
		}
		_, _ = w.Write(responseBody)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// copyOllamaNativeStream relays a native Ollama ndjson stream byte-for-byte
// while extracting usage from each line. Ollama reports prompt_eval_count and
// eval_count on the final {"done":true} object (PRD §13), so the last complete
// object wins and a cancelled stream reports nothing.
func (h *Handler) copyOllamaNativeStream(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef) {
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	var pending []byte
	for {
		if r.Context().Err() != nil {
			return
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return
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
				if usage, ok := shared.ParseUsage(provider.Protocol, bytes.TrimSpace(pending[:index])); ok {
					h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
				}
				pending = pending[index+1:]
			}
		}
		if err != nil {
			if len(pending) > 0 {
				if usage, ok := shared.ParseUsage(provider.Protocol, bytes.TrimSpace(pending)); ok {
					h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
				}
			}
			return
		}
	}
}

func (h *Handler) copyOllamaStream(w http.ResponseWriter, r *http.Request, response *http.Response, model string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	writeLine := func(payload map[string]any) {
		_ = json.NewEncoder(w).Encode(payload)
		if canFlush {
			flusher.Flush()
		}
	}
	for {
		if r.Context().Err() != nil {
			return
		}
		payload, ok := nextSSEData(reader)
		if !ok {
			return
		}
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			writeLine(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": choice.Delta.Content}, "done": false})
		}
		if choice.FinishReason != nil {
			break
		}
	}
	writeLine(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": ""}, "done": true})
}

func (h *Handler) handleSystemOne(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
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
	}
	h.dispatch(w, r, systemoneadapter.Protocol, request.Model, native, systemoneTranslate(h.opts.TranslateSystemOne, request), false)
}

func systemoneTranslate(translator SystemOneTranslator, request *systemoneadapter.Request) translateHook {
	if translator == nil {
		return nil
	}
	return func(target string) (ChatTranslation, error) { return translator(request, target) }
}

// copyNativeSSE relays Gemini/Ollama compatibility streams without decoding the
// body, preserving client-disconnect cancellation and incremental flushing.
func (h *Handler) copyNativeSSE(w http.ResponseWriter, r *http.Request, response *http.Response) {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		if r.Context().Err() != nil {
			return
		}
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return
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
