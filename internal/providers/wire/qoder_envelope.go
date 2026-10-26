package wire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// This file is the request half of executors/qoder.js at the pin: the Chat body
// becomes Qoder's agent envelope (buildQoderRequestBody, :210-335), with the
// message normalization (:50-157), the stable ids (:159-205) and the context tier
// (shared/qoder/contextTier.js). Nothing here touches the network; the live
// model_config and the attachment upload arrive as inputs.

const (
	qoderDefaultMaxTokens = 32768 // executors/qoder.js:257
	qoderNameLimit        = 30    // :327
)

// QoderModelConfig is one `chat[]` entry of Qoder's model list, kept as the
// server sent it: the pin forwards the whole block as `model_config`, and a wrong
// or trimmed block makes the gateway quietly serve another model (qoderModels.js:7-9).
type QoderModelConfig struct{ obj *jsObject }

// ParseQoderModelConfig reads a model_config JSON object.
func ParseQoderModelConfig(data []byte) (QoderModelConfig, error) {
	value, err := parseJS(data)
	if err != nil {
		return QoderModelConfig{}, err
	}
	obj, ok := value.(*jsObject)
	if !ok {
		return QoderModelConfig{}, errors.New("qoder: model_config is not a JSON object")
	}
	return QoderModelConfig{obj: obj}, nil
}

// withKey is `{ ...config, key }` (qoderModels.js:286): the key keeps its place when
// the entry has one and is appended when it does not.
func (c QoderModelConfig) withKey(key string) QoderModelConfig {
	return QoderModelConfig{obj: c.obj.clone().set("key", key)}
}

// JSON is the config as JSON.stringify writes it.
func (c QoderModelConfig) JSON() []byte { return []byte(stringifyJS(c.obj)) }

// QoderEnvelopeInput is what buildQoderRequestBody reads.
type QoderEnvelopeInput struct {
	// Model is the routed model, with or without the "qoder/" prefix.
	Model string
	// Body is the Chat Completions request, after the translator and the transforms.
	Body []byte
	// UserID is the connection's providerSpecificData.userId; it seeds session_id.
	UserID string
	// Config is the model's live model_config.
	Config QoderModelConfig
	// ContextTier is the QODER_CONTEXT_TIER preference: "" or "auto", "max",
	// "default", or a tier name or token count. The pin reads it from the process
	// environment; Routeweft takes it as a value.
	ContextTier string
	// Upload puts an inlined image on Qoder's file API. Nil keeps every image inline
	// when it is small and replaces it with a stub when it is not.
	Upload QoderUploader
}

// QoderEnvelope is the built request.
type QoderEnvelope struct {
	Key         string // the model key, without "qoder/"
	Payload     []byte // JSON.stringify of the agent envelope
	ModelSource string // model_config.source, or "system"
}

type qoderEnv struct {
	uuid func() string
	now  func() time.Time
}

var qoderLive = qoderEnv{uuid: uuid.NewString, now: time.Now}

// BuildQoderEnvelope builds the agent_chat_generation body.
func BuildQoderEnvelope(ctx context.Context, in QoderEnvelopeInput) (QoderEnvelope, error) {
	return buildQoderEnvelope(ctx, in, qoderLive)
}

func buildQoderEnvelope(ctx context.Context, in QoderEnvelopeInput, env qoderEnv) (QoderEnvelope, error) {
	key := strings.TrimPrefix(in.Model, "qoder/")
	if in.Config.obj == nil {
		return QoderEnvelope{}, fmt.Errorf("qoder: model_config for %q not yet known (run a model list fetch or check upstream connectivity)", key)
	}
	config := in.Config.withKey(key)

	parsed, err := parseJS(in.Body)
	if err != nil {
		return QoderEnvelope{}, fmt.Errorf("qoder: request body is not JSON: %w", err)
	}
	body, _ := parsed.(*jsObject)
	incoming := []any{}
	if list, ok := body.get("messages").([]any); ok {
		incoming = list
	}
	rewriteQoderAttachments(ctx, incoming, in.Upload)

	messages, system := normalizeQoderMessages(incoming)
	tools := body.get("tools")
	isReasoning := jsIsTruthy(config.obj.get("is_reasoning"))

	maxTokens := float64(qoderDefaultMaxTokens)
	if limit := jsToNumber(config.obj.get("max_output_tokens")); limit > 0 {
		maxTokens = limit
	}
	for _, name := range []string{"max_tokens", "max_completion_tokens"} {
		if n, ok := jsNumeric(body.get(name)); ok && n > 0 && n < maxTokens {
			maxTokens = n
		}
	}

	lastUser := lastQoderUserText(messages)
	sessionID := qoderStableHash("qoder-session", in.UserID, key)
	recordID, err := qoderChatRecordID(key, messages, tools, maxTokens)
	if err != nil {
		return QoderEnvelope{}, err
	}

	tier := resolveQoderContextTier(config.obj, qoderTierPrompt{system: system, messages: messages, tools: tools}, in.ContextTier)

	parameters := newJSObject().set("max_tokens", maxTokens)
	extra := newJSObject().
		set("context", []any{}).
		set("modelConfig", newJSObject().set("key", key).set("is_reasoning", isReasoning)).
		set("originalContent", lastUser)
	chatContext := newJSObject().
		set("chatPrompt", "").set("imageUrls", nil).set("extra", extra).
		set("features", []any{}).set("text", lastUser)
	toolList, ok := tools.([]any)
	if !ok {
		toolList = []any{}
	}
	payload := newJSObject().
		set("request_id", env.uuid()).
		set("request_set_id", recordID).
		set("chat_record_id", recordID).
		set("session_id", sessionID).
		set("stream", true).
		set("chat_task", "FREE_INPUT").
		set("is_reply", true).
		set("is_retry", false).
		set("source", float64(1)).
		set("version", "3").
		set("session_type", "qodercli").
		set("agent_id", "agent_common").
		set("task_id", "common").
		set("code_language", "").
		set("chat_prompt", "").
		set("image_urls", nil).
		set("aliyun_user_type", "").
		set("system", system).
		set("messages", messages).
		set("tools", toolList).
		set("parameters", parameters).
		set("chat_context", chatContext).
		set("model_config", config.obj).
		set("business", newJSObject().
			set("product", "cli").set("version", "1.0.0").set("type", "agent").set("stage", "start").
			set("id", env.uuid()).
			set("name", truncateUTF16(lastUser, qoderNameLimit)).
			set("begin_at", float64(env.now().UnixMilli())))
	if tier != nil {
		applyQoderContextTier(payload, tier.tokenCount)
	}

	source := "system"
	if mc, ok := payload.get("model_config").(*jsObject); ok && jsIsTruthy(mc.get("source")) {
		source = jsToString(mc.get("source"))
	}
	return QoderEnvelope{Key: key, Payload: []byte(stringifyJS(payload)), ModelSource: source}, nil
}

func prop(value any, key string) any {
	if o, ok := value.(*jsObject); ok {
		return o.get(key)
	}
	return nil
}

// normalizeQoderMessages hoists role:"system" out and flattens each message's
// content (executors/qoder.js:50-68).
func normalizeQoderMessages(messages []any) ([]any, string) {
	out := []any{}
	var system []string
	for _, message := range messages {
		switch message.(type) {
		case *jsObject, []any:
		default:
			continue
		}
		if prop(message, "role") == "system" {
			if text := extractQoderText(prop(message, "content")); text != "" {
				system = append(system, text)
			}
			continue
		}
		cloned := spreadObject(message)
		cloned.set("content", normalizeQoderContent(prop(message, "content")))
		out = append(out, cloned)
	}
	return out, strings.Join(system, "\n\n")
}

// normalizeQoderContent is :86-138: text-only content becomes one string; with an
// image the content stays an array of OpenAI blocks.
func normalizeQoderContent(content any) any {
	list, isList := content.([]any)
	if !isList {
		if content == nil {
			return ""
		}
		return jsToString(content)
	}
	var blocks, textParts []any
	hasImage := false
	pushText := func(text string) {
		if text == "" {
			return
		}
		if hasImage || len(blocks) > 0 {
			blocks = append(blocks, newJSObject().set("type", "text").set("text", text))
		} else {
			textParts = append(textParts, text)
		}
	}
	imageBlock := func(url string) *jsObject {
		return newJSObject().set("type", "image_url").set("image_url", newJSObject().set("url", url))
	}
	for _, entry := range list {
		item, ok := entry.(*jsObject)
		if !ok {
			continue
		}
		kind := item.get("type")
		name := func(value any, fallback string) string {
			if jsIsTruthy(value) {
				return jsToString(value)
			}
			return fallback
		}
		switch {
		case kind == "image_url" && qoderImageURL(item) != "":
			blocks = append(blocks, imageBlock(qoderImageURL(item)))
			hasImage = true
		case kind == "image" && jsIsTruthy(item.get("source")):
			src := item.get("source")
			url := ""
			if prop(src, "type") == "base64" && jsIsTruthy(prop(src, "data")) {
				url = "data:" + name(prop(src, "media_type"), "image/png") + ";base64," + jsToString(prop(src, "data"))
			} else if u, ok := prop(src, "url").(string); ok {
				url = u
			}
			if url != "" {
				blocks = append(blocks, imageBlock(url))
				hasImage = true
			}
		case kind == "file":
			label := name(prop(item.get("file"), "filename"), "")
			if label == "" {
				label = name(prop(item.get("file"), "name"), "file")
			}
			pushText(qoderFileOmitted(label))
		case kind == "document":
			pushText(qoderFileOmitted(name(item.get("title"), "document")))
		default:
			if text, ok := item.get("text").(string); ok {
				pushText(text)
			}
		}
	}
	if !hasImage {
		parts := make([]string, len(textParts))
		for i, p := range textParts {
			parts[i] = p.(string)
		}
		return strings.Join(parts, "\n")
	}
	if len(textParts) > 0 {
		joined := make([]string, len(textParts))
		for i, p := range textParts {
			joined[i] = p.(string)
		}
		blocks = append([]any{newJSObject().set("type", "text").set("text", strings.Join(joined, "\n"))}, blocks...)
	}
	return blocks
}

func qoderFileOmitted(name string) string {
	return "[file omitted: " + name + " — Qoder reads documents via its file API, not inlined bytes]"
}

// qoderImageURL is imageUrlOf (:101-105), "" when the block has none.
func qoderImageURL(item *jsObject) string {
	switch v := item.get("image_url").(type) {
	case string:
		if v != "" {
			return v
		}
	case *jsObject:
		if url, ok := v.get("url").(string); ok {
			return url
		}
	}
	return ""
}

// extractQoderText is :140-157.
func extractQoderText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, entry := range v {
			if item, ok := entry.(*jsObject); ok {
				if text, ok := item.get("text").(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return jsToString(content)
}

// lastQoderUserText is :159-167: the last user turn whose content is a string or an array.
func lastQoderUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if prop(messages[i], "role") != "user" {
			continue
		}
		switch content := prop(messages[i], "content").(type) {
		case string:
			return content
		case []any:
			return extractQoderText(content)
		}
	}
	return ""
}

// qoderStableHash is :169-177.
func qoderStableHash(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// qoderChatRecordID is :179-201. A role that is not a string makes Node's hash
// throw, which the pin answers as a 400; so does this.
func qoderChatRecordID(model string, messages []any, tools any, maxTokens float64) (string, error) {
	h := sha256.New()
	h.Write([]byte("qoder-record\x00" + model))
	for _, message := range messages {
		if role := prop(message, "role"); jsIsTruthy(role) {
			text, ok := role.(string)
			if !ok {
				return "", fmt.Errorf("The \"data\" argument must be of type string or an instance of Buffer, TypedArray, or DataView. Received type %s (%s)", qoderJSType(role), jsToString(role))
			}
			h.Write([]byte("\x00" + text))
		}
		switch content := prop(message, "content").(type) {
		case string:
			if content != "" {
				h.Write([]byte("\x00" + content))
			}
		case []any:
			h.Write([]byte("\x00" + stringifyJS(content)))
		}
	}
	if jsIsTruthy(tools) {
		h.Write([]byte("\x00" + stringifyJS(tools)))
	}
	h.Write([]byte("\x00mt=" + jsNumberString(maxTokens)))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

func qoderJSType(value any) string {
	if _, ok := jsNumeric(value); ok {
		return "number"
	}
	if _, ok := value.(bool); ok {
		return "boolean"
	}
	return "object"
}

// ---- context tiers (shared/qoder/contextTier.js) ---------------------------------

const qoderTierHeadroom = 0.15 // constants.js:46

type qoderTier struct {
	name       string
	tokenCount int
	isDefault  bool
}

type qoderTierPrompt struct {
	system   string
	messages []any
	tools    any
}

var qoderTierCount = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KM])?$`)

// parseQoderTierCount is parseTierTokenCount (:27-34): 0 when it is not a count.
func parseQoderTierCount(value any) int {
	if v, ok := jsNumeric(value); ok {
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 {
			return int(math.Floor(v))
		}
		return 0
	}
	switch v := value.(type) {
	case string:
		m := qoderTierCount.FindStringSubmatch(strings.ToUpper(jsTrim(v)))
		if m == nil {
			return 0
		}
		n, _ := jsFinite(m[1])
		switch m[2] {
		case "K":
			n *= 1_000
		case "M":
			n *= 1_000_000
		}
		if n > 0 && !math.IsInf(n, 0) {
			return int(math.Floor(n))
		}
	}
	return 0
}

// firstNullish is `a ?? b ?? ...` over the named fields of an entry.
func firstNullish(o *jsObject, names ...string) any {
	for _, n := range names {
		if v := o.get(n); v != nil {
			return v
		}
	}
	return nil
}

func qoderTierName(entry *jsObject, count int) string {
	if raw, ok := firstNullish(entry, "name", "label", "display_name", "displayName", "key", "id").(string); ok && jsTrim(raw) != "" {
		return jsTrim(raw)
	}
	switch {
	case count >= 1_000_000 && count%1_000_000 == 0:
		return fmt.Sprintf("%dM", count/1_000_000)
	case count >= 1_000 && count%1_000 == 0:
		return fmt.Sprintf("%dK", count/1_000)
	}
	return fmt.Sprint(count)
}

// qoderContextTiers is getQoderContextTiers (:48-67): the model's tiers, ascending.
func qoderContextTiers(config *jsObject) []qoderTier {
	list, _ := firstNullish(config, "context_config", "contextConfig").([]any)
	byCount := map[int]qoderTier{}
	for _, raw := range list {
		entry, ok := raw.(*jsObject)
		if !ok {
			continue
		}
		count := parseQoderTierCount(firstNullish(entry, "tokenCount", "token_count", "max_input_tokens", "maxInputTokens", "contextLength", "context_length"))
		if count == 0 {
			continue
		}
		isDefault := entry.get("isDefault") == true || entry.get("is_default") == true || entry.get("default") == true
		byCount[count] = qoderTier{name: qoderTierName(entry, count), tokenCount: count, isDefault: byCount[count].isDefault || isDefault}
	}
	tiers := make([]qoderTier, 0, len(byCount))
	for _, t := range byCount {
		tiers = append(tiers, t)
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].tokenCount < tiers[j].tokenCount })
	return tiers
}

// estimateQoderPromptTokens is :76-85: a CJK character counts one token, anything
// else a quarter, over the UTF-16 length of the JSON the gateway will read.
func estimateQoderPromptTokens(p qoderTierPrompt) int {
	tools := p.tools
	if !jsIsTruthy(tools) {
		tools = []any{}
	}
	text := stringifyJS(newJSObject().set("system", p.system).set("messages", p.messages).set("tools", tools))
	cjk, length := 0, 0
	for _, r := range text {
		if r >= 0x10000 {
			length += 2
			continue
		}
		length++
		if (r >= 0x1100 && r <= 0x11ff) || (r >= 0x2e80 && r <= 0x9fff) || (r >= 0xac00 && r <= 0xd7af) || (r >= 0xf900 && r <= 0xfaff) || (r >= 0xff00 && r <= 0xffef) {
			cjk++
		}
	}
	return int(math.Ceil(float64(cjk) + float64(length-cjk)/4))
}

// resolveQoderContextTier is :107-139; nil leaves the payload as it is.
func resolveQoderContextTier(config *jsObject, prompt qoderTierPrompt, preference string) *qoderTier {
	tiers := qoderContextTiers(config)
	if len(tiers) == 0 {
		return nil
	}
	mode := jsTrim(preference)
	if mode == "" {
		mode = "auto"
	}
	largest := tiers[len(tiers)-1]
	defaultTier := tiers[0]
	for _, t := range tiers {
		if t.isDefault {
			defaultTier = t
			break
		}
	}
	need := int(math.Ceil(float64(estimateQoderPromptTokens(prompt)) * (1 + qoderTierHeadroom)))
	switch lower := strings.ToLower(mode); lower {
	case "max":
		return &largest
	case "default":
		return &defaultTier
	case "auto":
	default:
		wanted := strings.ToUpper(strings.Join(strings.FieldsFunc(mode, isJSSpace), ""))
		count := parseQoderTierCount(wanted)
		for _, t := range tiers {
			if strings.ToUpper(strings.Join(strings.FieldsFunc(t.name, isJSSpace), "")) == wanted || (count != 0 && t.tokenCount == count) {
				return &t
			}
		}
	}
	current := parseQoderTierCount(firstNullish(config, "max_input_tokens", "maxInputTokens"))
	limit := current
	if limit == 0 {
		limit = defaultTier.tokenCount
	}
	if need <= limit {
		return nil
	}
	chosen := largest
	for _, t := range tiers {
		if t.tokenCount >= need && t.tokenCount > limit {
			chosen = t
			break
		}
	}
	if chosen.tokenCount <= limit {
		return nil
	}
	return &chosen
}

// applyQoderContextTier is :145-160: the choice is written where the IDE writes it.
func applyQoderContextTier(payload *jsObject, tokens int) {
	count := float64(tokens)
	payload.set("parameters", payload.get("parameters").(*jsObject).clone().set("context_length", count))
	context := payload.get("chat_context").(*jsObject)
	extra := context.get("extra").(*jsObject).clone()
	override := newJSObject()
	if existing, ok := extra.get("ideModelConfigOverride").(*jsObject); ok {
		override = existing.clone()
	}
	extra.set("ideModelConfigOverride", override.set("max_input_tokens", count))
	context.set("extra", extra)
	payload.set("model_config", payload.get("model_config").(*jsObject).clone().set("max_input_tokens", count))
}
