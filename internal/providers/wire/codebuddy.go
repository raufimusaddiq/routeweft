package wire

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// CodeBuddy CN and international (open-sse/executors/codebuddy-cn.js,
// codebuddy-intl.js, codebuddy-error.js, registry/codebuddy-{cn,intl}.js). Both
// are one OpenAI Chat wire to /v2/chat/completions, so the base needs no hook.
//
// What is deliberately not here, by decision (see docs/PARITY.md, CodeBuddy):
//   - no client identity: the reference sends a CodeBuddy CLI/IDE User-Agent and
//     X-Product, X-IDE-Type, X-IDE-Name, x-requested-with, x-codebuddy-request
//     (registry/codebuddy-cn.js:28-35, codebuddy-intl.js:28-35). Routeweft sends
//     its own User-Agent and none of those. If the gateway needs them, CodeBuddy
//     is dropped, not softened;
//   - no content-filter rewrite: the CN executor replaces any system prompt over
//     2000 characters or matching an agent-identity pattern with a neutral one
//     (codebuddy-cn.js:22-49). System prompts are carried verbatim, and a
//     rejection by the gateway's filter reaches the client unchanged;
//   - no invented system prompt: the international executor drops every caller
//     system/developer message and prepends "You are CodeBuddy Code."
//     (codebuddy-intl.js:28-39).
var codebuddyCN = Hook{
	ForceStream:         true,
	StripEmptyToolCalls: true,
	Finalize:            chain(codebuddyStream, codebuddyReasoning),
	ClassifyError:       codebuddyFrequencyLimit,
}

var codebuddyIntl = Hook{
	ForceStream:         true,
	StripEmptyToolCalls: true,
	Finalize:            chain(codebuddyStream, codebuddyReasoning, codebuddyIntlMessages),
	ClassifyError:       codebuddyFrequencyLimit,
}

// codebuddyStream: the gateway rejects a non-stream request (HTTP 400, code
// 11101 "Non-stream chat request is currently not supported"), so stream is
// true whatever the client sent or omitted (codebuddy-cn.js:20, intl:19). The
// response is folded back to JSON for a client that did not ask to stream
// (Hook.ForceStream, internal/ingress dispatchAttempts).
func codebuddyStream(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	if string(bytes.TrimSpace(body["stream"])) == "true" {
		return false
	}
	body["stream"] = json.RawMessage("true")
	return true
}

// codebuddyReasoning is the reasoning fix (codebuddy-cn.js:55-62, intl:21-26):
// the gateway surfaces reasoning only when a request carries reasoning_effort
// together with reasoning_summary:"auto", and answers a content-filter error
// when they are forced on a request that did not ask (#2071), so they are opt-in.
//
// Over reasoning_effort: absent, null, "" and a value of the wrong type leave the
// body exactly as received (the pin treats null and "" as unset, and a number,
// bool, object or array as set; here a value that is not a string is the
// gateway's to judge, and no summary is added to it). "none" and "off" remove the
// effort, since the gateway has no such level; the match ignores case and
// surrounding space (the pin's is exact, so " Off" reached the gateway as an
// unknown level). Any other string keeps the effort and sets the summary.
//
// Divergence from the pin: the pin overwrites reasoning_summary with "auto"
// whenever an effort is set, replacing an operator's explicit "concise" or
// "detailed". An explicit reasoning_summary, whatever its value including null
// and "", is kept; "auto" is only the default for an absent one.
func codebuddyReasoning(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	raw, ok := body["reasoning_effort"]
	if !ok {
		return false
	}
	var effort string
	if json.Unmarshal(raw, &effort) != nil {
		return false
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	switch effort {
	case "":
		return false
	case "none", "off":
		delete(body, "reasoning_effort")
		return true
	}
	if _, explicit := body["reasoning_summary"]; explicit {
		return false
	}
	body["reasoning_summary"] = json.RawMessage(`"auto"`)
	return true
}

// codebuddyIntlMessages is the international message shape (codebuddy-intl.js:
// 28-39): the gateway answers 11101 to a plain OpenAI shape, which wants a
// leading system message and user content as typed blocks.
//
//   - system and developer messages move, in order, to the front as role
//     "system" with their content and every other field untouched. The pin drops
//     them and prepends a fixed identity prompt. Nothing is invented: a request
//     with no system message gets none (whether the gateway then answers 11101 is
//     not verifiable offline, PARITY U-cb1);
//   - a user message whose content is a string becomes [{type:"text",text}], an
//     empty string included. Array content, other roles and everything else pass
//     through as received;
//   - an element of messages that is not an object stays where it is (the pin
//     skips it, which drops it);
//   - messages that is not an array is left alone and never replaced by a
//     fabricated one (Chat parsing already refuses a request without messages).
func codebuddyIntlMessages(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil || messages == nil {
		return false
	}
	var leading, rest []json.RawMessage
	changed := false
	for _, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil {
			rest = append(rest, raw)
			continue
		}
		var role string
		_ = json.Unmarshal(message["role"], &role)
		switch role {
		case "system", "developer":
			if role == "developer" {
				message["role"] = json.RawMessage(`"system"`)
				encoded, err := json.Marshal(message)
				if err != nil {
					return false
				}
				raw = encoded
				changed = true
			}
			leading = append(leading, raw)
			continue
		case "user":
			var text string
			if trimmed := bytes.TrimSpace(message["content"]); len(trimmed) > 0 && trimmed[0] == '"' && json.Unmarshal(trimmed, &text) == nil {
				block, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
				if err != nil {
					return false
				}
				message["content"] = block
				encoded, err := json.Marshal(message)
				if err != nil {
					return false
				}
				raw = encoded
				changed = true
			}
		}
		rest = append(rest, raw)
	}
	ordered := append(leading, rest...)
	if !changed && len(leading) > 0 {
		// Already leading, in order, when nothing but position could change.
		for i, raw := range ordered {
			if !bytes.Equal(raw, messages[i]) {
				changed = true
				break
			}
		}
	}
	if !changed {
		return false
	}
	return setJSON(body, "messages", ordered)
}

// codebuddyLimitText is the reference's text test (codebuddy-error.js:6).
var codebuddyLimitText = regexp.MustCompile(`(?i)超出频率限制|frequency limit|限额`)

// codebuddyReset reads "YYYY-MM-DD HH:MM:SS" with an optional "UTC", sign, hours
// and minutes (codebuddy-error.js:9).
var codebuddyReset = regexp.MustCompile(`(?i)(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})(?:\s*UTC\s*([+-])?(\d{1,2})(?::?(\d{2}))?)?`)

// codebuddyFrequencyLimit classifies a CodeBuddy frequency-limit error as a
// quota lock with its reset time (codebuddy-error.js:1-21). A frequency limit is
// code 6004 or a message naming one; the message is the first non-empty string of
// msg, message and error.message.
//
// Divergences from the pin, all in the direction of reporting what the gateway
// said without guessing:
//   - a message that is not a string (the pin's .match throws and the limit is
//     missed) is skipped: code 6004 alone is still a limit;
//   - the code is read as a number or a numeric string;
//   - the zone sign is parsed: the pin's "UTC-5" fell back to +08:00;
//     "UTC+5:30", which the pin turned into an invalid date, is read; a zone that
//     is out of range, or a time that does not parse, gives no reset time and
//     never a guessed one;
//   - a reset that is not in the future is ignored.
//
// The client-visible status and body are not touched: this is only Routeweft's
// own cooldown accounting. The pin rewrites the status to 429.
func codebuddyFrequencyLimit(_ int, body []byte, now time.Time) (routing.Classification, bool) {
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil || document == nil {
		return routing.Classification{}, false
	}
	message := firstString(document["msg"], document["message"], nestedMessage(document["error"]))
	if !isFrequencyCode(document["code"]) && !codebuddyLimitText.MatchString(message) {
		return routing.Classification{}, false
	}
	classification := routing.Classification{Outcome: routing.OutcomeQuotaLock}
	if reset, ok := codebuddyResetTime(message); ok && reset.After(now) {
		classification.ResetAt = reset.UTC().Format(time.RFC3339)
	}
	return classification, true
}

func nestedMessage(raw json.RawMessage) json.RawMessage {
	var nested map[string]json.RawMessage
	if json.Unmarshal(raw, &nested) != nil {
		return nil
	}
	return nested["message"]
}

// firstString is the first of values that is a non-empty JSON string.
func firstString(values ...json.RawMessage) string {
	for _, raw := range values {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			return text
		}
	}
	return ""
}

func isFrequencyCode(raw json.RawMessage) bool {
	trimmed := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	number, err := strconv.ParseFloat(trimmed, 64)
	return err == nil && number == 6004
}

// codebuddyResetTime reads the reset moment out of a limit message. Without a
// zone the reference assumes +08:00, the gateway's own clock (codebuddy-error.js:
// 11-13), for both CN and international.
func codebuddyResetTime(message string) (time.Time, bool) {
	match := codebuddyReset.FindStringSubmatch(message)
	if match == nil {
		return time.Time{}, false
	}
	end := codebuddyReset.FindStringIndex(message)[1]
	tail := message[end:]
	if len(tail) > 0 && (tail[0] >= '0' && tail[0] <= '9' || tail[0] == ':') {
		return time.Time{}, false
	}
	if match[4] == "" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(tail)), "UTC") {
		return time.Time{}, false
	}
	offset := 8 * 60 // minutes east of UTC
	if match[4] != "" {
		hours, err := strconv.Atoi(match[4])
		if err != nil || hours > 14 {
			return time.Time{}, false
		}
		minutes := 0
		if match[5] != "" {
			if minutes, err = strconv.Atoi(match[5]); err != nil || minutes > 59 {
				return time.Time{}, false
			}
		}
		if hours == 14 && minutes != 0 {
			return time.Time{}, false
		}
		offset = hours*60 + minutes
		if match[3] == "-" {
			offset = -offset
		}
	}
	moment, err := time.ParseInLocation("2006-01-02 15:04:05", match[1]+" "+match[2], time.FixedZone("", offset*60))
	if err != nil {
		return time.Time{}, false
	}
	return moment, true
}
