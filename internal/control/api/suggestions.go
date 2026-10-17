package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type suggestedModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"contextLength,omitempty"`
	IsFree        bool   `json:"isFree,omitempty"`
}

func filterSuggestions(body []byte, filter string) ([]suggestedModel, error) {
	body = bytes.TrimSpace(body)
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	raw := json.RawMessage(body)
	if len(body) > 0 && body[0] != '[' {
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		if len(envelope.Data) > 0 {
			raw = envelope.Data
		} else if len(envelope.Models) > 0 {
			raw = envelope.Models
		}
	}
	var models []struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Context   int             `json:"context_length"`
		Endpoints []string        `json:"endpoints"`
		Pricing   json.RawMessage `json:"pricing"`
		Tier      string          `json:"tier"`
		Chat      bool            `json:"supports_chat"`
		Media     string          `json:"media_type"`
		Free      bool            `json:"isFree"`
	}
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, err
	}
	items := []suggestedModel{}
	for _, m := range models {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		keep := false
		switch filter {
		case "kenari":
			keep = slicesContain(m.Endpoints, "chat")
		case "openrouter-free":
			var pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			}
			keep = json.Unmarshal(m.Pricing, &pricing) == nil && pricing.Prompt == "0" && pricing.Completion == "0" && m.Context >= 200000
		case "opencode-free":
			keep = (strings.HasSuffix(m.ID, "-free") || m.ID == "big-pickle") && m.ID != "deepseek-v4-flash-free"
		case "mimo-free":
			keep = strings.HasPrefix(m.ID, "mimo") || strings.Contains(strings.ToLower(m.Name), "mimo")
		case "airforce-free":
			keep = (m.Tier == "free" || strings.HasSuffix(m.ID, ":free")) && m.Chat && (m.Media == "" || m.Media == "chat" || m.Media == "text")
		case "kilo-free":
			keep = m.Free
		default:
			return nil, errors.New("unknown suggestion filter")
		}
		if !keep {
			continue
		}
		name := m.Name
		if name == "" || filter == "kenari" || filter == "opencode-free" {
			name = m.ID
		}
		context := m.Context
		if filter == "opencode-free" || filter == "mimo-free" {
			context = 0
		}
		items = append(items, suggestedModel{ID: m.ID, Name: name, ContextLength: context, IsFree: filter == "kilo-free"})
	}
	if filter == "openrouter-free" {
		sort.SliceStable(items, func(i, j int) bool { return items[i].ContextLength > items[j].ContextLength })
	}
	if filter == "airforce-free" {
		sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	}
	return items, nil
}

func (h *Handler) fetchSuggestions(ctx context.Context, rawURL, filter string) ([]suggestedModel, error) {
	parsed, err := h.validateOutboundURL(rawURL)
	if err != nil {
		return nil, errors.New("catalog URL is not allowed")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: roundTripperFromClient(h.opts.DiscoveryClient.HTTP), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("catalog request failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("catalog upstream returned an error")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, errors.New("catalog response unreadable or larger than 4 MiB")
	}
	return filterSuggestions(body, filter)
}

func (h *Handler) handleSuggestedModels(w http.ResponseWriter, r *http.Request) {
	filter, rawURL := r.URL.Query().Get("type"), r.URL.Query().Get("url")
	if rawURL == "" || !slicesContain([]string{"kenari", "openrouter-free", "opencode-free", "mimo-free", "airforce-free"}, filter) {
		writeError(w, 400, "invalid_suggestions", "url and a supported filter type are required")
		return
	}
	if _, err := h.validateOutboundURL(rawURL); err != nil {
		writeError(w, 400, "invalid_catalog_url", "catalog URL is not allowed")
		return
	}
	models, err := h.fetchSuggestions(r.Context(), rawURL, filter)
	if err != nil {
		models = []suggestedModel{}
	}
	writeJSON(w, 200, map[string]any{"data": models})
}

func (h *Handler) handleKiloFreeModels(w http.ResponseWriter, r *http.Request) {
	h.workflowMu.Lock()
	waited := false
	if done := h.kiloFlight; done != nil {
		h.workflowMu.Unlock()
		select {
		case <-done:
		case <-r.Context().Done():
			writeError(w, http.StatusRequestTimeout, "catalog_canceled", "catalog refresh was canceled")
			return
		}
		h.workflowMu.Lock()
		waited = true
	}
	models, at := h.kiloCache, h.kiloAt
	if waited || models != nil && time.Since(at) < time.Hour {
		h.workflowMu.Unlock()
		if models == nil {
			writeJSON(w, 502, map[string]any{"models": []suggestedModel{}, "error": "Kilo catalog fetch failed"})
			return
		}
		result := map[string]any{"models": models, "cached": true}
		if time.Since(at) >= time.Hour {
			result["warning"] = "catalog fetch failed; last known models retained"
		}
		writeJSON(w, 200, result)
		return
	}
	done := make(chan struct{})
	h.kiloFlight = done
	h.workflowMu.Unlock()
	defer func() {
		h.workflowMu.Lock()
		h.kiloFlight = nil
		close(done)
		h.workflowMu.Unlock()
	}()
	models, err := h.fetchSuggestions(r.Context(), "https://api.kilo.ai/api/gateway/models", "kilo-free")
	if err != nil {
		h.workflowMu.Lock()
		models = h.kiloCache
		h.workflowMu.Unlock()
		if models != nil {
			writeJSON(w, 200, map[string]any{"models": models, "cached": true, "warning": "catalog fetch failed; last known models retained"})
			return
		}
		writeJSON(w, 502, map[string]any{"models": []suggestedModel{}, "error": "Kilo catalog fetch failed"})
		return
	}
	h.workflowMu.Lock()
	h.kiloCache, h.kiloAt = models, time.Now()
	h.workflowMu.Unlock()
	writeJSON(w, 200, map[string]any{"models": models, "cached": false})
}
