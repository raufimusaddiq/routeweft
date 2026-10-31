package wire

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
)

// Shape names how a provider's listing response is read. The zero value is the
// shared OpenAI/Gemini/bare-array catalog, which the discovery package parses.
type Shape string

const (
	ShapeDefault      Shape = ""
	ShapeCodex        Shape = "codex"
	ShapeGoogleModels Shape = "google-models"
	ShapeGitHub       Shape = "github"
	ShapeKimchi       Shape = "kimchi"
	ShapeKiro         Shape = "kiro"
	ShapeCline        Shape = "cline"
	ShapeClinePass    Shape = "clinepass"
	ShapeGrokCLI      Shape = "grok-cli"
	ShapeQoder        Shape = "qoder"
	ShapeCursor       Shape = "cursor"
	ShapeZed          Shape = "zed"
)

// ListedModel is one model a provider listing returned.
type ListedModel struct {
	ID            string
	Name          string
	ContextWindow int
	Capabilities  []string
}

// ParseListing reads a provider-specific listing. An error leaves the caller's
// durable catalog untouched; an empty result is not an error (the reference
// treats it as "no live models" and says so).
func ParseListing(shape Shape, body []byte) ([]ListedModel, error) {
	var models []ListedModel
	var err error
	switch shape {
	case ShapeCodex:
		models, err = parseCodex(body)
	case ShapeGoogleModels:
		models, err = parseGoogleModels(body)
	case ShapeGitHub:
		models, err = parseGitHub(body)
	case ShapeKimchi:
		models, err = parseKimchi(body)
	case ShapeKiro:
		models, err = parseKiro(body)
	case ShapeCline, ShapeClinePass:
		models, err = parseCline(body, shape == ShapeClinePass)
	case ShapeGrokCLI:
		models, err = parseGrokCLI(body)
	case ShapeQoder:
		models, err = parseQoder(body)
	case ShapeCursor:
		models, err = parseCursor(body)
	case ShapeZed:
		models, err = parseZed(body)
	default:
		return nil, errors.New("unknown listing shape " + string(shape))
	}
	if err != nil {
		return nil, err
	}
	return dedupe(models), nil
}

func dedupe(models []ListedModel) []ListedModel {
	seen := make(map[string]bool, len(models))
	out := make([]ListedModel, 0, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if strings.TrimSpace(model.Name) == "" {
			model.Name = model.ID
		}
		out = append(out, model)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var errListingJSON = errors.New("model listing returned invalid JSON")

type object = map[string]any

func decodeListing(body []byte) (any, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, errListingJSON
	}
	return value, nil
}

// listOf is the reference's parseOpenAIStyleModels (route.js:26-29): a bare
// array, or the first of data, models and results.
func listOf(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case object:
		for _, key := range []string{"data", "models", "results"} {
			if list, ok := typed[key].([]any); ok {
				return list
			}
		}
	}
	return nil
}

func objects(list []any) []object {
	out := make([]object, 0, len(list))
	for _, item := range list {
		if entry, ok := item.(object); ok {
			out = append(out, entry)
		}
	}
	return out
}

// text is the first non-blank string among keys.
func text(entry object, keys ...string) string {
	for _, key := range keys {
		if value, ok := entry[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// number is the first positive number among keys.
func number(entry object, keys ...string) int {
	for _, key := range keys {
		switch value := entry[key].(type) {
		case float64:
			if value > 0 {
				return int(value)
			}
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func child(entry object, key string) object {
	value, _ := entry[key].(object)
	return value
}

// Codex (route.js:54-73): id is id, slug, model or name; every chat model also
// gets a "-review" twin that bills the review quota family.
func parseCodex(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	var models []ListedModel
	for _, entry := range objects(listOf(value)) {
		id := text(entry, "id", "slug", "model", "name")
		if id == "" {
			continue
		}
		name := text(entry, "display_name", "displayName", "name")
		if name == "" {
			name = id
		}
		window := number(entry, "context_window", "contextWindow", "max_context_window")
		models = append(models, ListedModel{ID: id, Name: name, ContextWindow: window})
		kind, _ := entry["type"].(string)
		chat := kind != "image" && !strings.Contains(strings.ToLower(id), "embed")
		if chat && !strings.HasSuffix(id, codexprovider.ReviewSuffix) {
			models = append(models, ListedModel{ID: id + codexprovider.ReviewSuffix, Name: name + " Review", ContextWindow: window})
		}
	}
	return models, nil
}

// Gemini CLI and Antigravity (route.js:31-52): models is an array of objects, or
// an object keyed by id whose internal entries are not offered.
func parseGoogleModels(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	var models []ListedModel
	switch typed := root["models"].(type) {
	case []any:
		for _, entry := range objects(typed) {
			// The pin filters isInternal only on the keyed form (route.js:42-49); an
			// entry flagged internal is not offered in either form of one payload.
			if internal, _ := entry["isInternal"].(bool); internal {
				continue
			}
			id := text(entry, "id", "model", "name")
			if id == "" {
				continue
			}
			models = append(models, ListedModel{ID: id, Name: text(entry, "displayName", "name", "id")})
		}
	case object:
		for id, raw := range typed {
			info, _ := raw.(object)
			if internal, _ := info["isInternal"].(bool); internal {
				continue
			}
			models = append(models, ListedModel{ID: id, Name: text(info, "displayName", "name")})
		}
	}
	return models, nil
}

// GitHub Copilot (route.js:186-199): chat models only, and none the policy
// disabled. An embeddings model in the catalog is not a chat model.
func parseGitHub(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	list, _ := root["data"].([]any)
	var models []ListedModel
	for _, entry := range objects(list) {
		capabilities := child(entry, "capabilities")
		if kind, _ := capabilities["type"].(string); kind != "chat" {
			continue
		}
		if state, _ := child(entry, "policy")["state"].(string); state == "disabled" {
			continue
		}
		models = append(models, ListedModel{
			ID: text(entry, "id"), Name: text(entry, "name", "id"),
			ContextWindow: number(child(capabilities, "limits"), "max_context_window_tokens"),
		})
	}
	return models, nil
}

// Kimchi (kimchiModels.js:49-90,134): models[] with slug as the id, the limits
// object for the window and input_modalities for vision.
func parseKimchi(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	list, _ := root["models"].([]any)
	var models []ListedModel
	for _, entry := range objects(list) {
		id := text(entry, "slug", "id", "model", "name")
		if id == "" {
			continue
		}
		model := ListedModel{
			ID: id, Name: text(entry, "display_name", "displayName", "name"),
			ContextWindow: firstNumber(number(child(entry, "limits"), "context_window"), number(entry, "contextLength", "context_length")),
		}
		if modalities, _ := entry["input_modalities"].([]any); containsString(modalities, "image") {
			model.Capabilities = append(model.Capabilities, "vision")
		}
		if reasoning, _ := entry["reasoning"].(bool); reasoning {
			model.Capabilities = append(model.Capabilities, "reasoning")
		}
		models = append(models, model)
	}
	return models, nil
}

func firstNumber(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func containsString(list []any, want string) bool {
	for _, item := range list {
		if value, _ := item.(string); value == want {
			return true
		}
	}
	return false
}

// Kiro (kiroModels.js:106-157,289-310): each upstream model becomes four
// Routeweft ids. The -thinking and -agentic suffixes do not exist upstream; the
// request side strips them. auto has no -agentic form (the chunked-write prompt
// is meaningless when the server picks the model).
func parseKiro(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	list, _ := root["models"].([]any)
	var models []ListedModel
	for _, entry := range objects(list) {
		upstream := text(entry, "modelId", "id")
		if upstream == "" {
			continue
		}
		// An unknown window stays 0, as in the seeds: a guessed one would drive
		// combo context budgets.
		window := number(child(entry, "tokenLimits"), "maxInputTokens")
		base := strings.TrimSuffix(strings.TrimSuffix(upstream, "-agentic"), "-thinking")
		display := kiroDisplayName(text(entry, "modelName"), upstream, entry["rateMultiplier"])
		models = append(models,
			ListedModel{ID: base, Name: display, ContextWindow: window},
			ListedModel{ID: base + "-thinking", Name: display + " (Thinking)", ContextWindow: window, Capabilities: []string{"reasoning"}},
		)
		if base != "auto" {
			models = append(models,
				ListedModel{ID: base + "-agentic", Name: display + " (Agentic)", ContextWindow: window},
				ListedModel{ID: base + "-thinking-agentic", Name: display + " (Thinking + Agentic)", ContextWindow: window, Capabilities: []string{"reasoning"}},
			)
		}
	}
	return models, nil
}

func kiroDisplayName(modelName, id string, rate any) string {
	base := strings.TrimSpace(modelName)
	if base == "" {
		base = id
	}
	multiplier, ok := rate.(float64)
	if !ok || multiplier <= 0 || math.Abs(multiplier-1) < 1e-9 {
		return "Kiro " + base
	}
	return "Kiro " + base + " (" + strconv.FormatFloat(multiplier, 'f', 1, 64) + "x credit)"
}

// Cline and ClinePass (clinepassModels.js:25-94): a bare array or data[]; ClinePass
// keeps only the cline-pass/ ids, Cline keeps every id.
func parseCline(body []byte, passOnly bool) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	var list []any
	switch typed := value.(type) {
	case []any:
		list = typed
	case object:
		list, _ = typed["data"].([]any)
	}
	var models []ListedModel
	for _, entry := range objects(list) {
		id, _ := entry["id"].(string)
		if strings.TrimSpace(id) == "" || passOnly && !strings.HasPrefix(id, "cline-pass/") {
			continue
		}
		models = append(models, ListedModel{ID: id, Name: text(entry, "name")})
	}
	return models, nil
}

// Grok CLI (grokCliModels.js:13-48): the list may be an array, or an object keyed
// by id; an item may be a bare string; the id has seven spellings.
func parseGrokCLI(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		key string
		raw any
	}
	collect := func(list any) {
		switch typed := list.(type) {
		case []any:
			for _, raw := range typed {
				entries = append(entries, struct {
					key string
					raw any
				}{"", raw})
			}
		case object:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				entries = append(entries, struct {
					key string
					raw any
				}{key, typed[key]})
			}
		}
	}
	if root, ok := value.(object); ok {
		for _, key := range []string{"data", "models", "results"} {
			if root[key] != nil {
				collect(root[key])
				break
			}
		}
	} else {
		collect(value)
	}
	var models []ListedModel
	for _, entry := range entries {
		var item object
		switch typed := entry.raw.(type) {
		case string:
			item = object{"id": typed}
		case object:
			item = typed
		default:
			continue
		}
		id := text(item, "id", "model_id", "modelId", "model", "slug")
		if id == "" {
			id = strings.TrimSpace(entry.key)
		}
		if id == "" {
			id = text(item, "name")
		}
		window := number(item, "context_length", "contextLength", "context_window", "contextWindow")
		models = append(models, ListedModel{ID: id, Name: text(item, "display_name", "displayName", "name"), ContextWindow: window})
	}
	return models, nil
}

// Qoder (qoderModels.js:244-278): chat[] keyed by key; an entry the IDE hides
// (enable:false) is not offered by the provider listing. The id is the bare key,
// which is what the catalog seeds and the router use.
func parseQoder(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	list, ok := root["chat"].([]any)
	if !ok {
		return nil, errors.New("qoder model list has no chat catalog")
	}
	var models []ListedModel
	for _, entry := range objects(list) {
		if enabled, present := entry["enable"].(bool); present && !enabled {
			continue
		}
		key := text(entry, "key")
		if key == "" {
			continue
		}
		model := ListedModel{ID: key, Name: text(entry, "display_name"), ContextWindow: number(entry, "max_input_tokens")}
		if vision, _ := entry["is_vl"].(bool); vision {
			model.Capabilities = append(model.Capabilities, "vision")
		}
		if reasoning, _ := entry["is_reasoning"].(bool); reasoning {
			model.Capabilities = append(model.Capabilities, "reasoning")
		}
		models = append(models, model)
	}
	return models, nil
}

// Zed (zedAuth.js:336-359,362-404; route.js:293-326): a models[] array whose ids
// may be strings, an [id, ...] array, or an object carrying id; snake_case and
// camelCase field spellings are both read; is_disabled entries are not offered.
func parseZed(body []byte) ([]ListedModel, error) {
	value, err := decodeListing(body)
	if err != nil {
		return nil, err
	}
	root, _ := value.(object)
	list, _ := root["models"].([]any)
	var models []ListedModel
	for _, entry := range objects(list) {
		id := zedListingID(entry["id"])
		if id == "" || listingFlag(entry, "is_disabled", "isDisabled") {
			continue
		}
		model := ListedModel{
			ID: id, Name: text(entry, "display_name", "displayName", "name"),
			ContextWindow: number(entry, "max_token_count", "maxTokenCount", "contextLength", "context_length"),
		}
		if listingFlag(entry, "supports_images", "supportsImages") {
			model.Capabilities = append(model.Capabilities, "vision")
		}
		if listingFlag(entry, "supports_thinking", "supportsThinking") {
			model.Capabilities = append(model.Capabilities, "reasoning")
		}
		models = append(models, model)
	}
	return models, nil
}

// zedListingID is normalizeZedModelId (zedAuth.js:326-334): the id is a string,
// the first string of an array, or a nested id.
func zedListingID(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		if len(typed) > 0 {
			if id, _ := typed[0].(string); strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	case object:
		if id, _ := typed["id"].(string); strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// listingFlag is the first boolean among the snake_case and camelCase spellings
// a provider's listing uses for one flag.
func listingFlag(entry object, keys ...string) bool {
	for _, key := range keys {
		switch value := entry[key].(type) {
		case bool:
			return value
		case string:
			return strings.EqualFold(strings.TrimSpace(value), "true")
		}
	}
	return false
}

// Cursor (cursorModels.js:15-33,68-91): GetUsableModelsResponse carries repeated
// ModelDetails in field 1: id 1, display model id 3, display name 4, short name 5.
func parseCursor(body []byte) ([]ListedModel, error) {
	message, err := pbParse(body)
	if err != nil {
		return nil, errors.New("cursor model list is not a protobuf message")
	}
	var models []ListedModel
	for _, field := range message.all(1) {
		if field.kind != pbLenType {
			continue
		}
		detail, err := pbParse(field.data)
		if err != nil {
			return nil, errors.New("cursor model list holds a malformed model")
		}
		id, _ := detail.text(1)
		name, _ := detail.text(4)
		if strings.TrimSpace(name) == "" {
			name, _ = detail.text(5)
		}
		if strings.TrimSpace(name) == "" {
			name, _ = detail.text(3)
		}
		models = append(models, ListedModel{ID: id, Name: strings.TrimSpace(name)})
	}
	return models, nil
}
