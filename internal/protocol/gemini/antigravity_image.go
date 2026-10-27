package gemini

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// An Antigravity model whose name says image is served by the image_gen request
// (executors/antigravity.js:58-90,144-189): a Chat request whose answer carries
// the picture in its text, as a markdown data-URI image (ResponseOptions
// ImagesAsText, nonStreamingHandler.js:218-223). It is the same Chat route as any other
// model, so it is no standalone media surface (PRD 4.2, BDR-016; PARITY 6.26).
//
// The pin sends the image request text only, with fixed sampling, and drops the
// rest without a word. Here what the pin would drop is refused, so a request is
// never answered as if it had been read whole (PARITY 6.9 A4, V23).

// AntigravityImageRequestType is the envelope's requestType for an image model
// (executors/antigravity.js:185). The agent path sends none (:296-302).
const AntigravityImageRequestType = "image_gen"

// The image request's generationConfig defaults (executors/antigravity.js:170-175).
const (
	antigravityImageTemperature = 1.0
	antigravityImageTopP        = 0.95
	antigravityImageTopK        = 40
	antigravityImageMaxOutput   = 8192
)

var (
	// imageSize is the suffix parseImageConfig reads (:76): two runs of ASCII
	// digits around an x, at the end of the name, with or without a hyphen.
	imageSize = regexp.MustCompile(`([0-9]+)x([0-9]+)$`)
	// imageSuffix is the suffix stripped from the model sent upstream (:148),
	// which needs the hyphen.
	imageSuffix = regexp.MustCompile(`-[0-9]+x[0-9]+$`)
)

// IsAntigravityImageModel is isImageModel (executors/antigravity.js:59-69), which
// chatCore.js:149-154 also applies to force the upstream call non-streaming. The
// three patterns /image/i, /imagen/i and /image-generation/i all contain "image",
// so one test decides. JavaScript's /i without the u flag folds ASCII letters
// only, so this does too.
func IsAntigravityImageModel(model string) bool {
	return strings.Contains(asciiLower(model), "image")
}

func asciiLower(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, text)
}

// AntigravityImageModel is the model the image request names upstream: the
// requested one without a -WxH suffix (:148).
func AntigravityImageModel(model string) string {
	return imageSuffix.ReplaceAllString(model, "")
}

// antigravityImageConfig is parseImageConfig (:74-90): 1:1 unless the name ends
// in WxH. Both at most 16 is a ratio as written (16x9 is 16:9, 0x5 is 0:5); any
// other pair is a resolution reduced by its greatest common divisor (1024x768
// is 4:3). JavaScript reads the digits as doubles, so a side above 2^53 is not
// the number written; such a name is refused rather than given a ratio the pin
// would compute from a rounded value.
func antigravityImageConfig(model string) (map[string]any, error) {
	match := imageSize.FindStringSubmatch(model)
	if match == nil {
		return map[string]any{"aspectRatio": "1:1"}, nil
	}
	const exact = uint64(1) << 53
	w, errW := strconv.ParseUint(match[1], 10, 64)
	h, errH := strconv.ParseUint(match[2], 10, 64)
	if errW != nil || errH != nil || w > exact || h > exact {
		return nil, unsupported("model %q: the image size in its name is too large to read", model)
	}
	if w > 16 || h > 16 {
		d := gcd(w, h)
		w, h = w/d, h/d
	}
	return map[string]any{"aspectRatio": strconv.FormatUint(w, 10) + ":" + strconv.FormatUint(h, 10)}, nil
}

// gcd is the pin's recursive gcd. One side is above 16 when it is called, so
// the result is never 0.
func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// antigravityImageGeneration keeps the three sampling controls a client can set
// and the image request carries (:170-175); each the client did not set takes
// the pin's value. topK is always the pin's: Chat has no top_k (PARITY 6.9 F1).
// Every other control is refused: the pin's request has no place for it.
func antigravityImageGeneration(config map[string]any, model string) (map[string]any, error) {
	out := map[string]any{
		"temperature":     antigravityImageTemperature,
		"topP":            antigravityImageTopP,
		"topK":            antigravityImageTopK,
		"maxOutputTokens": antigravityImageMaxOutput,
	}
	for key, value := range config {
		switch key {
		case "temperature", "topP", "maxOutputTokens":
			out[key] = value
		default:
			return nil, unsupported("generationConfig.%s: the Antigravity image request carries only the temperature, top_p and max_tokens", key)
		}
	}
	imageConfig, err := antigravityImageConfig(model)
	if err != nil {
		return nil, err
	}
	out["imageConfig"] = imageConfig
	return out, nil
}

// antigravityImageContents is the pin's contents filter (:150-158): each turn
// keeps its text parts, as text alone, under its role (user when it has none);
// a turn with no text is left out. A thought signature is a replay token, not
// content, and goes as the pin drops it. Any other part is refused: an input
// image, a tool call or a tool result the model would never see.
func antigravityImageContents(raw json.RawMessage) ([]any, error) {
	var contents []map[string]any
	if err := json.Unmarshal(raw, &contents); err != nil {
		return nil, err
	}
	out := []any{}
	for _, content := range contents {
		var parts []any
		for _, item := range concerns.Items(content["parts"]) {
			part := concerns.Object(item)
			for key := range part {
				switch key {
				case "text", "thoughtSignature", "thought_signature":
				default:
					return nil, unsupported("%q part: the Antigravity image models are sent text only", key)
				}
			}
			if text, ok := part["text"].(string); ok {
				parts = append(parts, map[string]any{"text": text})
			}
		}
		if len(parts) == 0 {
			continue
		}
		role := concerns.Text(content["role"])
		if role == "" {
			role = "user"
		}
		out = append(out, map[string]any{"role": role, "parts": parts})
	}
	if len(out) == 0 {
		return nil, unsupported("the Antigravity image models need a text prompt")
	}
	return out, nil
}

// antigravityImageEnvelope is the image branch of transformRequest (:144-189):
// contents, generationConfig and the session, with no tools, system instruction
// or safety settings, under the model without its size suffix and the image_gen
// requestType. The Gemini body it reads is the one the Chat leg built.
func antigravityImageEnvelope(fields map[string]json.RawMessage, config map[string]any, options RequestOptions, cloud CloudCodeOptions) ([]byte, *concerns.ToolNames, error) {
	if raw, ok := fields["systemInstruction"]; ok && string(raw) != "null" {
		return nil, nil, unsupported("system messages: the Antigravity image request carries no system instruction")
	}
	if raw, ok := fields["tools"]; ok && string(raw) != "null" {
		return nil, nil, unsupported("tools: the Antigravity image request carries no tools")
	}
	contents, err := antigravityImageContents(fields["contents"])
	if err != nil {
		return nil, nil, err
	}
	generation, err := antigravityImageGeneration(config, options.Model)
	if err != nil {
		return nil, nil, err
	}
	request := map[string]any{"contents": contents, "generationConfig": generation}
	envelope, err := cloudCodeEnvelope(request, options, cloud, AntigravityProtocol, AntigravityImageModel(options.Model))
	if err != nil {
		return nil, nil, err
	}
	envelope["requestType"] = AntigravityImageRequestType
	out, err := json.Marshal(envelope)
	return out, nil, err
}

// imageTextChunk bounds one content delta of an image written as text. A Chat
// stream that a Responses or Messages client reads is parsed again, one event
// at most 4 MiB (concerns.ReadEvents), and a picture is often larger, so its
// text goes in pieces, which every Chat consumer joins back. The pin sends
// one JSON body (chatCore.js:149-154) and never splits; the pieces are this
// gateway's framing, and the text they join to is the pin's.
const imageTextChunk = 2 << 20

// splitText cuts text into pieces of at most size bytes, never inside a UTF-8
// sequence, so each piece is valid text on its own.
func splitText(text string, size int) []string {
	var pieces []string
	for len(text) > size {
		cut := size
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			cut = size
		}
		pieces = append(pieces, text[:cut])
		text = text[cut:]
	}
	return append(pieces, text)
}

// AntigravityImageStreamToChat answers a streaming client from the one JSON body
// generateContent returns for an image model (executors/antigravity.js:121-123).
// The body is read as a one-element array, which GeminiStreamToChat already
// reads incrementally and bounds, so the client gets the same chunks a stream
// would have produced. The pin's chatCore.js:149-154 sets stream false and
// answers that client with a JSON body it did not ask for.
func AntigravityImageStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, options ResponseOptions) error {
	array := io.MultiReader(strings.NewReader("["), src, strings.NewReader("]"))
	return GeminiStreamToChat(ctx, array, dst, options)
}
