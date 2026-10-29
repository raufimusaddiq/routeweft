package ingress

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Hard capabilities are input modalities: a model missing one drops request
// data. Routeweft spells them as its adapter settings do (audio-input,
// video-input); the oracle's audioInput/videoInput are the same capabilities.
const (
	capVision     = "vision"
	capPDF        = "pdf"
	capAudioInput = "audio-input"
	capVideoInput = "video-input"
)

// hardCapabilities is the oracle's CAPABILITY_KEYS order
// (open-sse/services/capacityAdapter.js:13), which fixes the order adapter
// pools are flattened in.
var hardCapabilities = []string{capVision, capPDF, capAudioInput, capVideoInput}

var dataMIME = regexp.MustCompile(`^data:([^;,]+)`)

// requiredSet is an insertion-ordered set of capabilities, like the oracle's JS
// Set: the first required capability decides which adapter strategy applies
// (getActiveAdapterStrategy, capacityAdapter.js:64-72).
type requiredSet struct {
	order []string
	seen  map[string]bool
}

func (s *requiredSet) add(capability string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if !s.seen[capability] {
		s.seen[capability] = true
		s.order = append(s.order, capability)
	}
}

func (s *requiredSet) addByMIME(mime any) {
	value, ok := mime.(string)
	if !ok {
		return
	}
	switch {
	case strings.HasPrefix(value, "image/"):
		s.add(capVision)
	case value == "application/pdf":
		s.add(capPDF)
	case strings.HasPrefix(value, "audio/"):
		s.add(capAudioInput)
	case strings.HasPrefix(value, "video/"):
		s.add(capVideoInput)
	}
}

// detectRequiredCapabilities ports open-sse/services/combo.js:94-184
// (trailingUserItems + detectRequiredCapabilities): modalities are read from the
// current user turn only, in every body shape the oracle knows. A body that is
// not a JSON object requires nothing.
func detectRequiredCapabilities(body []byte) []string {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil
	}
	var required requiredSet
	for _, message := range trailingUserItems(root["messages"]) {
		scanMessage(&required, message)
	}
	for _, item := range trailingUserItems(root["input"]) {
		if object, ok := item.(map[string]any); ok {
			scanContent(&required, object["content"])
		}
	}
	contents := root["contents"]
	if contents == nil {
		if request, ok := root["request"].(map[string]any); ok {
			contents = request["contents"]
		}
	}
	for _, item := range trailingUserItems(contents) {
		if object, ok := item.(map[string]any); ok {
			scanContent(&required, object["parts"])
		}
	}
	return required.order
}

// trailingUserItems is the run of items after the last assistant/model turn
// (combo.js:94-100): older history media must not pin a Combo to a vision model.
func trailingUserItems(raw any) []any {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	index := len(items) - 1
	for index >= 0 {
		if object, ok := items[index].(map[string]any); ok {
			if role, _ := object["role"].(string); role == "assistant" || role == "model" {
				break
			}
		}
		index--
	}
	return items[index+1:]
}

func scanContent(required *requiredSet, content any) {
	blocks, ok := content.([]any)
	if !ok {
		return
	}
	for _, block := range blocks {
		scanBlock(required, block)
	}
}

func str(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

// scanBlock is combo.js:117-135.
func scanBlock(required *requiredSet, raw any) {
	block, ok := raw.(map[string]any)
	if !ok {
		return
	}
	switch str(block, "type") {
	case "image_url", "image", "input_image":
		required.add(capVision)
	case "input_audio", "audio_url", "audio":
		required.add(capAudioInput)
	case "input_video", "video_url", "video":
		required.add(capVideoInput)
	}
	switch str(block, "type") {
	case "file", "document", "input_file":
		var mime any
		if audio, ok := block["input_audio"].(map[string]any); ok && truthy(audio["format"]) {
			mime = "audio/" + jsString(audio["format"])
		} else if file, ok := block["file"].(map[string]any); ok && truthy(file["file_data"]) {
			mime = dataURIMIME(jsString(file["file_data"]))
		} else if source, ok := block["source"].(map[string]any); ok && truthy(source["media_type"]) {
			mime = source["media_type"]
		} else if source, ok := block["source"].(map[string]any); ok && truthy(source["data"]) {
			mime = dataURIMIME(jsString(source["data"]))
		}
		if truthy(mime) {
			required.addByMIME(mime)
		} else {
			required.add(capPDF)
		}
	}
	// Gemini parts carry the mime on inlineData/fileData.
	for _, key := range []string{"inlineData", "fileData"} {
		if part, ok := block[key].(map[string]any); ok && truthy(part["mimeType"]) {
			required.addByMIME(part["mimeType"])
			break
		}
	}
}

// scanMessage is combo.js:141-173.
func scanMessage(required *requiredSet, raw any) {
	message, ok := raw.(map[string]any)
	if !ok {
		return
	}
	if images, ok := message["images"].([]any); ok && len(images) > 0 {
		required.add(capVision)
	}
	attachments := message["experimental_attachments"]
	if !truthy(attachments) {
		attachments = message["attachments"]
	}
	if list, ok := attachments.([]any); ok {
		for _, entry := range list {
			attachment, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			var mime any
			for _, key := range []string{"contentType", "mediaType"} {
				if truthy(attachment[key]) {
					mime = attachment[key]
					break
				}
			}
			if mime == nil {
				if url, ok := attachment["url"].(string); ok {
					mime = dataURIMIME(url)
				}
			}
			if truthy(mime) {
				required.addByMIME(mime)
			} else if truthy(attachment["url"]) || truthy(attachment["data"]) {
				required.add(capVision)
			}
		}
	}
	if truthy(message["image_url"]) || truthy(message["image"]) {
		required.add(capVision)
	}
	if truthy(message["audio_url"]) || truthy(message["audio"]) {
		required.add(capAudioInput)
	}
	scanContent(required, message["content"])
	if text, ok := message["content"].(string); ok {
		switch {
		case strings.Contains(text, "data:image/"):
			required.add(capVision)
		case strings.Contains(text, "data:audio/"):
			required.add(capAudioInput)
		case strings.Contains(text, "data:application/pdf"):
			required.add(capPDF)
		}
	}
}

// dataURIMIME is JS `String(v).match(/^data:([^;,]+)/)?.[1]`.
func dataURIMIME(value string) any {
	if match := dataMIME.FindStringSubmatch(value); match != nil {
		return match[1]
	}
	return nil
}

// truthy is JS truthiness for decoded JSON values.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	}
	return true
}

func jsString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
