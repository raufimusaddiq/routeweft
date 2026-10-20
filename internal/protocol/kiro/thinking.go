package kiro

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// This file is Kiro's reading of a request's thinking intent: config/kiroConstants.js
// and the parts of translator/concerns/thinkingUnified.js and thinking.js it calls.
// It lives here, not in concerns, because the shapes are Kiro's.

const (
	agenticSuffix  = "-agentic"
	thinkingSuffix = "-thinking"
	// defaultThinkingBudget is KIRO_THINKING_BUDGET_DEFAULT (kiroConstants.js:44).
	defaultThinkingBudget = 16000
	maxThinkingBudget     = 32000
)

// levelBudgets is LEVEL_TO_BUDGET (concerns/thinking.js:9-17).
var levelBudgets = map[string]int64{
	"none": 0, "minimal": 512, "low": 1024, "medium": 8192, "high": 24576, "xhigh": 32768, "max": 128000,
}

// override is the "model(value)" suffix (thinkingUnified.js:34-46).
type override struct {
	mode   string // none, auto, level, budget
	level  string
	budget int64
}

var suffixPattern = regexp.MustCompile(`^(.*)\(([^()]+)\)\s*$`)
var digitsPattern = regexp.MustCompile(`^\d+$`)

// parseSuffix splits "model(high)" into the model and its override. A suffix it
// does not recognise is consumed without an override, as the pin does.
func parseSuffix(model string) (string, *override) {
	match := suffixPattern.FindStringSubmatch(model)
	if match == nil {
		return model, nil
	}
	clean, raw := strings.TrimSpace(match[1]), strings.ToLower(strings.TrimSpace(match[2]))
	switch {
	case raw == "none" || raw == "off":
		return clean, &override{mode: "none"}
	case raw == "auto":
		return clean, &override{mode: "auto"}
	case raw == "ultra":
		return clean, &override{mode: "level", level: raw}
	}
	if budget, err := strconv.ParseInt(raw, 10, 64); err == nil && digitsPattern.MatchString(raw) {
		return clean, &override{mode: "budget", budget: budget}
	}
	if _, known := levelBudgets[raw]; known {
		return clean, &override{mode: "level", level: raw}
	}
	return clean, nil
}

// modelIntent is resolveKiroModelIntent (kiroConstants.js:50-57).
type modelIntent struct {
	model    string // without the (level) suffix, with -thinking/-agentic
	upstream string // the model Kiro is asked for
	agentic  bool
	override *override
}

func resolveModel(model string) modelIntent {
	clean, over := parseSuffix(model)
	upstream := clean
	intent := modelIntent{model: clean, override: over}
	if strings.HasSuffix(upstream, agenticSuffix) {
		intent.agentic = true
		upstream = strings.TrimSuffix(upstream, agenticSuffix)
	}
	upstream = strings.TrimSuffix(upstream, thinkingSuffix)
	intent.upstream = upstream
	return intent
}

// thinkingConfig is extractThinking's result.
type thinkingConfig struct {
	mode   string // none, auto, level, budget
	level  string
	budget int64
}

func number(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && !math.IsInf(f, 0)
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func effortConfig(value string) *thinkingConfig {
	e := strings.ToLower(value)
	switch e {
	case "none", "off":
		return &thinkingConfig{mode: "none"}
	case "auto":
		return &thinkingConfig{mode: "auto"}
	}
	return &thinkingConfig{mode: "level", level: e}
}

// extractThinking is thinkingUnified.js:50-103 for the shapes a Chat body can
// carry. The Gemini envelope shapes nested under generationConfig and request
// are not read: those fields are refused as unknown, not ignored.
func extractThinking(body map[string]any) *thinkingConfig {
	if effort, ok := concerns.Object(body["output_config"])["effort"].(string); ok && effort != "" {
		return effortConfig(effort)
	}
	effort, _ := body["reasoning_effort"].(string)
	if body["reasoning_effort"] == nil {
		effort, _ = concerns.Object(body["reasoning"])["effort"].(string)
	}
	if effort != "" {
		return effortConfig(effort)
	}
	if thinking := concerns.Object(body["thinking"]); thinking != nil {
		switch thinking["type"] {
		case "disabled":
			return &thinkingConfig{mode: "none"}
		case "adaptive", "enabled":
			if budget := concerns.Int(thinking["budget_tokens"]); budget > 0 {
				return &thinkingConfig{mode: "budget", budget: budget}
			}
			return &thinkingConfig{mode: "auto"}
		}
	}
	if config := concerns.Object(body["thinkingConfig"]); config != nil {
		if level, ok := config["thinkingLevel"].(string); ok {
			return &thinkingConfig{mode: "level", level: strings.ToLower(level)}
		}
		if config["thinkingBudget"] != nil {
			budget := concerns.Int(config["thinkingBudget"])
			switch {
			case budget == 0:
				return &thinkingConfig{mode: "none"}
			case budget < 0:
				return &thinkingConfig{mode: "auto"}
			}
			return &thinkingConfig{mode: "budget", budget: budget}
		}
	}
	switch body["enable_thinking"] {
	case false:
		return &thinkingConfig{mode: "none"}
	case true:
		if budget := concerns.Int(body["thinking_budget"]); budget > 0 {
			return &thinkingConfig{mode: "budget", budget: budget}
		}
		return &thinkingConfig{mode: "auto"}
	}
	return nil
}

// applyOverride is applyKiroThinkingOverride (kiroConstants.js:60-77). It works
// on a copy of the fields it touches.
func applyOverride(body map[string]any, over *override) map[string]any {
	if over == nil {
		return body
	}
	next := make(map[string]any, len(body)+1)
	for key, value := range body {
		next[key] = value
	}
	if over.mode == "budget" {
		delete(next, "output_config")
		delete(next, "reasoning_effort")
		delete(next, "reasoning")
		next["thinking"] = map[string]any{"type": "enabled", "budget_tokens": json.Number(strconv.FormatInt(over.budget, 10))}
		return next
	}
	config := map[string]any{}
	for key, value := range concerns.Object(body["output_config"]) {
		config[key] = value
	}
	config["effort"] = over.mode
	if over.mode == "level" {
		config["effort"] = over.level
	}
	next["output_config"] = config
	return next
}

func containsThinkingTag(text string) bool {
	return strings.Contains(text, "<thinking_mode>enabled</thinking_mode>") ||
		strings.Contains(text, "<thinking_mode>interleaved</thinking_mode>")
}

// hasThinkingModeTag is containsThinkingModeTag (kiroConstants.js:367-384).
func hasThinkingModeTag(body map[string]any) bool {
	for _, raw := range concerns.Items(body["messages"]) {
		message := concerns.Object(raw)
		if role := concerns.Text(message["role"]); role != "system" && role != "user" {
			continue
		}
		if text, ok := message["content"].(string); ok {
			if containsThinkingTag(text) {
				return true
			}
			continue
		}
		for _, part := range concerns.Items(message["content"]) {
			if containsThinkingTag(concerns.Text(concerns.Object(part)["text"])) {
				return true
			}
		}
	}
	// The pin reads only a string system (kiroConstants.js:379-380). A Messages
	// client sends a list of text blocks, and the prefix the leg builds carries
	// their text to Kiro either way, so the tag in one must also switch on the tag
	// reading of the answer.
	if containsThinkingTag(concerns.Text(body["system"])) {
		return true
	}
	for _, block := range concerns.Items(body["system"]) {
		if containsThinkingTag(concerns.Text(concerns.Object(block)["text"])) {
			return true
		}
	}
	return false
}

// resolveBudget is resolveKiroThinkingBudget (kiroConstants.js:144-169). beta is
// the client's inbound anthropic-beta header, which only a Messages request has
// (translator/request/claude-to-kiro.js:241 hands the pin's rawHeaders to it; the
// pin sends no such header to Kiro). It is read after the body's own thinking
// configuration, so explicit thinking, disabled included, wins over it. ok is
// false when thinking is off.
func resolveBudget(body map[string]any, model, beta string) (budget int64, ok bool) {
	if config := extractThinking(body); config != nil {
		switch config.mode {
		case "none":
			return 0, false
		case "budget":
			return config.budget, true
		case "level":
			if config.level == "disabled" {
				return 0, false
			}
			if mapped, known := levelBudgets[config.level]; known {
				return mapped, true
			}
		}
		return defaultThinkingBudget, true
	}
	if strings.Contains(strings.ToLower(beta), "interleaved-thinking") {
		return defaultThinkingBudget, true
	}
	if hasThinkingModeTag(body) {
		return defaultThinkingBudget, true
	}
	lower := strings.ToLower(model)
	if strings.Contains(lower, "thinking") || strings.Contains(lower, "-reason") {
		return defaultThinkingBudget, true
	}
	return 0, false
}

// thinkingPrefix is buildThinkingSystemPrefix (kiroConstants.js:348-351).
func thinkingPrefix(budget int64) string {
	if budget < 1 {
		budget = defaultThinkingBudget
	}
	if budget > maxThinkingBudget {
		budget = maxThinkingBudget
	}
	return fmt.Sprintf("<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>%d</max_thinking_length>", budget)
}

type claudeVersion struct {
	major  int
	minor  int
	hasMin bool
}

var claudeVersionPattern = regexp.MustCompile(`(?:^|[/.])claude(?:[/.][a-z]+)*[/.](\d+)(?:[/.](\d+))?(?:[/.]|$)`)
var gptSixPattern = regexp.MustCompile(`(?:^|[/.])gpt[/.]5[/.]6(?:[/.]|$)`)

func parseClaudeVersion(model string) *claudeVersion {
	match := claudeVersionPattern.FindStringSubmatch(strings.ReplaceAll(strings.ToLower(model), "-", "."))
	if match == nil {
		return nil
	}
	major, _ := strconv.Atoi(match[1])
	version := &claudeVersion{major: major}
	if match[2] != "" {
		version.minor, _ = strconv.Atoi(match[2])
		version.hasMin = true
	}
	return version
}

// effortPath is resolveKiroEffortPath (kiroConstants.js:231-248): "reasoning"
// for GPT 5.6, "output_config" for Claude 4.6 and later, "" for the rest.
func effortPath(model string) string {
	normalized := strings.ReplaceAll(strings.ToLower(model), "-", ".")
	if gptSixPattern.MatchString(normalized) {
		return "reasoning"
	}
	if !strings.Contains(normalized, "claude") {
		return ""
	}
	version := parseClaudeVersion(model)
	if version == nil {
		return ""
	}
	dateSuffix := version.hasMin && version.minor >= 1000
	if version.major < 4 || (version.major == 4 && (!version.hasMin || version.minor <= 5 || dateSuffix)) {
		return ""
	}
	return "output_config"
}

// firstEffort is `output_config.effort ?? reasoning_effort ?? reasoning.effort`:
// the first that is not null, whatever its type.
func firstEffort(body map[string]any) (value any, outputConfig bool) {
	if effort := concerns.Object(body["output_config"])["effort"]; effort != nil {
		return effort, true
	}
	if body["reasoning_effort"] != nil {
		return body["reasoning_effort"], false
	}
	return concerns.Object(body["reasoning"])["effort"], false
}

// claudeEffort is extractKiroEffortLevel (:186-198).
func claudeEffort(body map[string]any, model string) string {
	raw, outputConfig := firstEffort(body)
	effort, isText := raw.(string)
	if !isText {
		return ""
	}
	switch normalized := strings.ToLower(effort); normalized {
	case "none", "off", "disabled":
		return ""
	case "xhigh":
		version := parseClaudeVersion(model)
		if version == nil || (version.major == 4 && version.hasMin && version.minor <= 6) {
			return "high"
		}
		return "xhigh"
	case "max":
		if outputConfig {
			return "max"
		}
		return "high"
	case "low", "medium", "high":
		return normalized
	}
	return ""
}

// gptEffort is extractKiroGptEffortLevel (:200-213).
func gptEffort(body map[string]any) string {
	raw, _ := firstEffort(body)
	effort, isText := raw.(string)
	if !isText {
		return ""
	}
	switch normalized := strings.ToLower(effort); normalized {
	case "max":
		return "xhigh"
	case "low", "medium", "high", "xhigh":
		return normalized
	}
	return ""
}

// additionalFields is buildKiroAdditionalModelRequestFieldsForModel (:255-259).
func additionalFields(body map[string]any, upstream string) map[string]any {
	switch effortPath(upstream) {
	case "reasoning":
		if effort := gptEffort(body); effort != "" {
			return map[string]any{"reasoning": map[string]any{"effort": effort}}
		}
	case "output_config":
		if effort := claudeEffort(body, upstream); effort != "" {
			return map[string]any{
				"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
				"output_config": map[string]any{"effort": effort},
			}
		}
	}
	return nil
}

// usesNativeGPTEffort is usesKiroNativeGptEffort (:250-253).
func usesNativeGPTEffort(body map[string]any, upstream string) bool {
	return effortPath(upstream) == "reasoning" && gptEffort(body) != ""
}

func validateThinking(body map[string]any) error {
	for _, key := range []string{"reasoning_effort", "enable_thinking", "thinking_budget"} {
		value := body[key]
		if value == nil {
			continue
		}
		switch key {
		case "reasoning_effort":
			if !validEffort(value) {
				return unsupported("%s must name a supported thinking effort", key)
			}
		case "enable_thinking":
			if _, ok := value.(bool); !ok {
				return unsupported("%s must be a boolean", key)
			}
		case "thinking_budget":
			if _, _, err := maxTokens(map[string]any{"max_tokens": value}); err != nil || body["enable_thinking"] != true {
				return unsupported("thinking_budget needs a positive integer and enable_thinking:true")
			}
		}
	}
	for _, key := range []string{"reasoning", "output_config", "thinking", "thinkingConfig"} {
		if body[key] == nil {
			continue
		}
		object, ok := body[key].(map[string]any)
		if !ok || len(object) == 0 {
			return unsupported("%s must be a non-empty object", key)
		}
		keys := []string{"effort"}
		switch key {
		case "thinking":
			keys = []string{"type", "budget_tokens"}
		case "thinkingConfig":
			keys = []string{"thinkingLevel", "thinkingBudget"}
		}
		if err := requestKeys(object, key, keys...); err != nil {
			return err
		}
		for _, field := range []string{"effort", "thinkingLevel"} {
			if value := object[field]; value != nil {
				if !validEffort(value) {
					return unsupported("%s.%s must name a supported thinking effort", key, field)
				}
			}
		}
		if key == "reasoning" || key == "output_config" {
			if object["effort"] == nil {
				return unsupported("%s needs effort", key)
			}
		}
		if key == "thinking" {
			switch object["type"] {
			case "enabled", "adaptive", "disabled":
			default:
				return unsupported("thinking.type must be enabled, adaptive or disabled")
			}
		}
		for _, field := range []string{"budget_tokens", "thinkingBudget"} {
			if value := object[field]; value != nil {
				n, ok := value.(json.Number)
				if !ok {
					return unsupported("%s.%s must be an integer", key, field)
				}
				parsed, err := n.Int64()
				if err != nil || (field == "budget_tokens" && parsed <= 0) {
					return unsupported("%s.%s is not a supported integer budget", key, field)
				}
			}
		}
		if key == "thinkingConfig" && object["thinkingLevel"] == nil && object["thinkingBudget"] == nil {
			return unsupported("thinkingConfig needs thinkingLevel or thinkingBudget")
		}
	}
	return nil
}

func validEffort(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	switch strings.ToLower(text) {
	case "none", "off", "disabled", "auto", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	}
	return false
}
