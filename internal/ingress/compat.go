package ingress

import (
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
	response, err := h.client.Do(upstreamRequest)
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
	if stream {
		h.copyNativeSSE(w, r, response)
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
		response, err := h.client.Do(translatedRequest)
		if err != nil {
			if r.Context().Err() == nil {
				h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
			}
			return
		}
		defer response.Body.Close()
		if request.Stream {
			h.copyOllamaStream(w, r, response, request.Model)
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
	response, err := h.client.Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() == nil {
			h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		}
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(response.StatusCode)
	if request.Stream {
		h.copyNativeSSE(w, r, response)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

func (h *Handler) copyOllamaStream(w http.ResponseWriter, r *http.Request, response *http.Response, model string) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, canFlush := w.(http.Flusher)
	decoder := json.NewDecoder(response.Body)
	for {
		if r.Context().Err() != nil {
			return
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			return
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": choice.Delta.Content}, "done": false})
			if canFlush {
				flusher.Flush()
			}
		}
		if choice.FinishReason != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "message": map[string]string{"role": "assistant", "content": ""}, "done": true})
			if canFlush {
				flusher.Flush()
			}
			return
		}
	}
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
