package usage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// grokCLIReader mirrors getGrokCliUsage (open-sse/services/usage/grok-cli.js:349-424).
// Billing and the subscription profile are read in turn on every call, as the
// pin starts both before awaiting either. With no REST window, the weekly pool
// comes from the gRPC-web GetGrokCreditsConfig frame (:300-342).
type grokCLIReader struct{ Client }

const (
	grokBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	grokUserURL    = "https://cli-chat-proxy.grok.com/v1/user?include=subscription"
	grokCreditsURL = "https://grok.com/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig"
)

func (r grokCLIReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if credential.AccessToken == "" {
		return failure("grok-cli access token is required")
	}
	headers := map[string]string{
		"Authorization": "Bearer " + credential.AccessToken, "Accept": "application/json",
		"x-xai-token-auth": "xai-grok-cli", "User-Agent": "grok-shell/1.0.44 (linux; x86_64)",
		"x-grok-client-identifier": "grok-shell", "x-grok-client-version": "1.0.44", "x-grok-client-mode": "headless",
	}
	if email := credential.ProviderData["email"]; email != "" {
		headers["x-email"] = email
	}
	userID := credential.ProviderData["userId"]
	if userID == "" {
		userID = credential.ProviderData["principalId"]
	}
	if userID != "" {
		headers["x-userid"] = userID
	}
	reply, err := r.call(ctx, http.MethodGet, grokBillingURL, headers, nil)
	profile, profileErr := r.call(ctx, http.MethodGet, grokUserURL, headers, nil)
	if err != nil {
		return failure("grok-cli billing request failed: %v", err)
	}
	if reply.status == http.StatusUnauthorized || reply.status == http.StatusForbidden {
		return failure("grok-cli authentication expired")
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("grok-cli billing returned HTTP %d", reply.status)
	}
	billing := json.RawMessage(bytes.TrimSpace(reply.body))
	if !json.Valid(billing) || billing[0] != '{' && billing[0] != '[' {
		return failure("grok-cli billing response was not JSON")
	}
	var user json.RawMessage
	profileKnown := profileErr == nil && profile.status >= 200 && profile.status < 300 && json.Valid(profile.body) && !shared.Nullish(profile.body)
	if profileKnown {
		user = profile.body
	}
	parsed := parseGrokBilling(billing, user, profileKnown)
	plan := grokTokenPlan(credential.AccessToken)
	if plan == "" {
		plan = parsed.plan
	}
	if len(parsed.windows) > 0 {
		return r.state(plan, parsed.windows...), nil
	}
	if parsed.unknownTier {
		// The pin reads an unknown tier as free and shows a synthetic exhausted
		// row; a paid account whose profile read failed would then never route.
		return failure("grok-cli tier unknown: the subscription profile read failed")
	}
	if weekly, ok := r.weeklyPool(ctx, credential.AccessToken); ok {
		return r.state(plan, weekly), nil
	}
	message := "Grok Build connected, but no credit allotment was returned. Free promo may be exhausted."
	if parsed.subscription {
		message = "Subscription access is active; Grok does not expose a numeric included quota."
	}
	return quota.State{Plan: plan, Message: message, ObservedAt: r.now()}, nil
}

// weeklyPool reads the SuperGrok weekly pool; every failure is no pool (:305-329).
func (r grokCLIReader) weeklyPool(ctx context.Context, token string) (quota.Window, bool) {
	reply, err := r.call(ctx, http.MethodPost, grokCreditsURL, map[string]string{
		"Authorization": "Bearer " + token, "Content-Type": "application/grpc-web+proto",
		"X-Grpc-Web": "1", "Accept": "application/grpc-web+proto",
	}, []byte{0, 0, 0, 0, 0}) // an empty request frame; without it upstream answers grpc-status 13
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return quota.Window{}, false
	}
	percent, reset, ok := decodeGrokCreditsFrame(reply.body)
	if !ok {
		return quota.Window{}, false
	}
	used := math.Floor(math.Max(0, math.Min(100, percent)) + 0.5) // Math.round
	return quota.Window{Name: "Weekly SuperGrok", Remaining: fraction(100 - used), ResetAt: reset}, true
}

type grokBilling struct {
	plan         string
	windows      []quota.Window
	subscription bool
	unknownTier  bool
}

// parseGrokBilling is parseGrokCliBilling (:141-298) with the pin's window keys
// and order. Each counted window keeps its amounts.
func parseGrokBilling(root, user json.RawMessage, profileKnown bool) grokBilling {
	config := root
	if _, ok := shared.Object(shared.Field(root, "config")); ok {
		config = shared.Field(root, "config")
	}
	get := func(object json.RawMessage, keys ...string) json.RawMessage {
		values := make([]json.RawMessage, len(keys))
		for i, key := range keys {
			values[i] = shared.Field(object, key)
		}
		return shared.FirstTruthy(values...)
	}
	periodEnd := firstReset(shared.Field(config, "billingPeriodEnd"), shared.Field(config, "billing_period_end"),
		shared.Field(shared.Field(config, "currentPeriod"), "end"), get(config, "resetAt", "resetsAt", "periodEnd"),
		shared.Field(root, "billingPeriodEnd"), shared.Field(root, "billing_period_end"), get(root, "resetAt", "resetsAt", "periodEnd"))
	tier := grokTier(user, config)
	result := grokBilling{subscription: tier != "" && !grokFreeTier.MatchString(tier)}
	nonNullish := func(pairs ...json.RawMessage) json.RawMessage { return shared.FirstNonNullish(pairs...) }
	both := func(key string) []json.RawMessage {
		return []json.RawMessage{shared.Field(config, key), shared.Field(root, key)}
	}

	if limit, ok := grokValue(nonNullish(shared.Field(config, "monthlyLimit"), shared.Field(config, "monthly_limit"),
		shared.Field(root, "monthlyLimit"), shared.Field(root, "monthly_limit"))); ok && limit > 0 {
		used, ok := grokValue(nonNullish(shared.Field(config, "includedUsed"), shared.Field(config, "included_used"),
			shared.Field(root, "includedUsed"), shared.Field(root, "included_used")))
		if !ok {
			used, _ = grokValue(nonNullish(shared.Field(config, "totalUsed"), shared.Field(config, "total_used"),
				shared.Field(root, "totalUsed"), shared.Field(root, "total_used")))
		}
		result.windows = append(result.windows, grokCounted("Monthly included", used, limit, periodEnd))
	}
	capValue, hasCap := grokValue(nonNullish(both("onDemandCap")...))
	usedValue, hasUsed := grokValue(nonNullish(both("onDemandUsed")...))
	switch {
	case hasCap && capValue > 0:
		result.windows = append(result.windows, grokCounted("On-demand", math.Max(0, usedValue), capValue, periodEnd))
	case !result.subscription && hasCap && capValue == 0 && hasUsed && !profileKnown:
		result.unknownTier = true
	case !result.subscription && hasCap && capValue == 0 && hasUsed:
		// Cap 0 is the exhausted free/promo state (chat answers 402 spending-limit).
		result.windows = append(result.windows, quota.Window{Name: "On-demand", Remaining: fraction(0), ResetAt: periodEnd,
			Count: &quota.Count{Used: &usedValue, Total: &capValue}})
	}
	if prepaid, ok := grokValue(nonNullish(both("prepaidBalance")...)); ok && prepaid > 0 {
		// A balance with no allotment: the pin shows it full, with no reset.
		result.windows = append(result.windows, quota.Window{Name: "Prepaid", Remaining: fraction(100), Count: &quota.Count{Remaining: &prepaid}})
	}
	if percent, ok := grokValue(nonNullish(shared.Field(config, "creditUsagePercent"), shared.Field(config, "credit_usage_percent"),
		shared.Field(root, "creditUsagePercent"))); ok && percent >= 0 {
		// One shared pool; productUsage is a breakdown of it, never separate windows.
		used := math.Max(0, math.Min(100, percent))
		result.windows = append(result.windows, quota.Window{Name: "Weekly SuperGrok", Remaining: fraction(100 - used), ResetAt: periodEnd})
	}
	for _, bag := range []json.RawMessage{shared.Field(root, "credits"), shared.Field(root, "creditBalance"), shared.Field(root, "usage"),
		shared.Field(config, "credits"), shared.Field(config, "includedCredits"), shared.Field(config, "subscriptionCredits")} {
		if _, ok := shared.Object(bag); !ok {
			continue
		}
		total, hasTotal := grokValue(nonNullish(shared.Field(bag, "total"), shared.Field(bag, "limit"), shared.Field(bag, "cap"),
			shared.Field(bag, "allocation"), shared.Field(bag, "amount")))
		used, hasUsed := grokValue(nonNullish(shared.Field(bag, "used"), shared.Field(bag, "spent"), shared.Field(bag, "consumed")))
		remaining, hasRemaining := grokValue(nonNullish(shared.Field(bag, "remaining"), shared.Field(bag, "balance"), shared.Field(bag, "left")))
		if hasTotal && total > 0 {
			switch {
			case hasUsed:
			case hasRemaining:
				used = math.Max(0, total-remaining)
			default:
				used = 0
			}
			reset := shared.ResetTime(get(bag, "resetAt", "resetsAt", "end"))
			if reset == nil {
				reset = periodEnd
			}
			window := grokCounted("Credits", used, total, reset)
			if hasRemaining {
				window.Count.Remaining = &remaining
			}
			result.windows = append(result.windows, window)
			break
		}
		if hasRemaining && remaining >= 0 {
			percent := 0.0
			if remaining > 0 {
				percent = 100
			}
			result.windows = append(result.windows, quota.Window{Name: "Credits", Remaining: fraction(percent), ResetAt: periodEnd,
				Count: &quota.Count{Remaining: &remaining}})
			break
		}
	}
	result.plan = grokPlan(user, config, tier)
	return result
}

// grokCounted is makeQuota (:112-135) for a positive total.
func grokCounted(name string, used, total float64, reset *time.Time) quota.Window {
	used = math.Max(0, used)
	return quota.Window{Name: name, Remaining: fraction(math.Max(0, total-used) / total * 100), ResetAt: reset,
		Count: &quota.Count{Used: &used, Total: &total}}
}

// grokValue is unwrapVal(value, NaN) (:46-52): `{val}` or a plain number or
// numeric string; ok is false where the pin has NaN.
func grokValue(raw json.RawMessage) (float64, bool) {
	if shared.Nullish(raw) {
		return 0, false
	}
	if object, ok := shared.Object(raw); ok {
		if value, has := object["val"]; has {
			return shared.FiniteNumber(value)
		}
	}
	return shared.FiniteNumber(raw)
}

func firstReset(values ...json.RawMessage) *time.Time {
	for _, value := range values {
		if reset := shared.ResetTime(value); reset != nil {
			return reset
		}
	}
	return nil
}

// grokTier is subscriptionTier (:72-80), trimmed as String.prototype.trim does.
func grokTier(user, config json.RawMessage) string {
	raw := shared.FirstNonNullish(shared.Field(user, "subscriptionTier"), shared.Field(user, "subscription_tier"),
		shared.Field(shared.Field(user, "subscription"), "tier"), shared.Field(config, "subscriptionTier"), shared.Field(config, "subscription_tier"))
	var tier string
	if json.Unmarshal(raw, &tier) != nil {
		return ""
	}
	return shared.Trim(tier)
}

// grokPlan is resolvePlan (:82-92): the tier with separators as spaces and each
// word capitalized, else the access flags.
func grokPlan(user, config json.RawMessage, tier string) string {
	if tier != "" {
		words := []byte(grokSeparators.ReplaceAllString(tier, " "))
		for i, c := range words {
			if grokWordByte(c) && (i == 0 || !grokWordByte(words[i-1])) && c >= 'a' && c <= 'z' {
				words[i] = c - 'a' + 'A'
			}
		}
		return string(words)
	}
	if string(shared.Field(user, "hasGrokCodeAccess")) == "true" {
		return "Grok Code"
	}
	return "Grok Build"
}

// grokWordByte is a JavaScript \w character.
func grokWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

var (
	grokFreeTier   = regexp.MustCompile(`(?i)^(free|none|null)$`)
	grokSeparators = regexp.MustCompile(`[_-]+`)
)

var grokTierPlans = map[string]string{"0": "Free", "1": "SuperGrok", "2": "X Basic", "3": "X Premium", "4": "X Premium Plus",
	"5": "SuperGrok Heavy", "6": "SuperGrok Lite"}

// grokTokenPlan is planFromAccessToken (:95-110): the tier claim of the access
// token's payload, read for display only; it is never trusted for access.
func grokTokenPlan(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(parts[1]), "="))
	if err != nil {
		return ""
	}
	return grokTierPlans[shared.Template(shared.Field(payload, "tier"))]
}

// decodeGrokCreditsFrame is grokCliQuotaFrame.js:167-191: the usage ratio and
// reset of top-level field 1 in the first data frame, or an unframed message.
func decodeGrokCreditsFrame(buffer []byte) (percent float64, reset *time.Time, ok bool) {
	if len(buffer) == 0 {
		return 0, nil, false
	}
	payload := buffer
	if _, _, framed := grokFrameHeader(buffer, 0); framed {
		payload = nil
		for offset := 0; offset < len(buffer); {
			start, length, valid := grokFrameHeader(buffer, offset)
			if !valid {
				return 0, nil, false
			}
			if buffer[offset]&0x80 == 0 {
				payload = buffer[start : start+length]
				break
			}
			offset = start + length
		}
		if payload == nil {
			return 0, nil, false
		}
	}
	top, valid := protoFields(payload)
	if !valid {
		return 0, nil, false
	}
	info, exists := top[1]
	if !exists || info.wire != 2 {
		return 0, nil, false
	}
	credits, valid := protoFields(info.bytes)
	if !valid {
		return 0, nil, false
	}
	ratio := 0.0 // proto3 omits a zero ratio
	if field, exists := credits[1]; exists {
		switch field.wire {
		case 5:
			ratio = float64(math.Float32frombits(binary.LittleEndian.Uint32(field.bytes)))
		case 1:
			ratio = math.Float64frombits(binary.LittleEndian.Uint64(field.bytes))
		default:
			return 0, nil, false
		}
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 {
		return 0, nil, false
	}
	return math.Min(100, ratio*100), grokResetAt(credits[5]), true
}

// grokResetAt reads a google.protobuf.Timestamp (:146-160); a malformed one is no reset.
func grokResetAt(field protoField) *time.Time {
	if field.wire != 2 {
		return nil
	}
	stamp, valid := protoFields(field.bytes)
	if !valid {
		return nil
	}
	seconds, nanos := 0.0, 0.0
	if value := stamp[1]; value.wire == 0 && value.present {
		seconds = float64(value.varint)
	}
	if value := stamp[2]; value.wire == 0 && value.present {
		nanos = float64(value.varint)
	}
	millis := seconds*1000 + math.Floor(nanos/1e6+0.5)
	if math.Abs(millis) > 8.64e15 {
		return nil
	}
	at := time.UnixMilli(int64(millis)).UTC()
	return &at
}

// grokFrameHeader is probeFrameHeader (:31-39).
func grokFrameHeader(buffer []byte, offset int) (start, length int, ok bool) {
	if offset < 0 || len(buffer)-offset < 5 {
		return 0, 0, false
	}
	switch buffer[offset] {
	case 0x00, 0x01, 0x80, 0x81:
	default:
		return 0, 0, false
	}
	size := binary.BigEndian.Uint32(buffer[offset+1:])
	if uint64(size) > uint64(len(buffer)-offset-5) {
		return 0, 0, false
	}
	return offset + 5, int(size), true
}

type protoField struct {
	present bool
	wire    uint64
	varint  uint64
	bytes   []byte
}

// protoFields is decodeFields (:107-117): a later field of the same number
// replaces an earlier one, and any malformed field fails the message.
func protoFields(buffer []byte) (map[uint64]protoField, bool) {
	fields := map[uint64]protoField{}
	for offset := 0; offset < len(buffer); {
		tag, next, ok := protoVarint(buffer, offset)
		if !ok || uint32(tag)>>3 == 0 {
			return nil, false
		}
		number, field := uint64(uint32(tag)>>3), protoField{present: true, wire: tag & 7}
		switch field.wire {
		case 0:
			if field.varint, next, ok = protoVarint(buffer, next); !ok {
				return nil, false
			}
		case 2:
			var size uint64
			if size, next, ok = protoVarint(buffer, next); !ok || size > uint64(len(buffer)-next) {
				return nil, false
			}
			field.bytes, next = buffer[next:next+int(size)], next+int(size)
		case 1, 5:
			width := 8
			if field.wire == 5 {
				width = 4
			}
			if next+width > len(buffer) {
				return nil, false
			}
			field.bytes, next = buffer[next:next+width], next+width
		default:
			return nil, false
		}
		fields[number] = field
		offset = next
	}
	return fields, true
}

// protoVarint is readVarint (:41-55), which gives up past a 70-bit shift.
func protoVarint(buffer []byte, offset int) (uint64, int, bool) {
	var value uint64
	for shift := uint(0); ; shift += 7 {
		if offset >= len(buffer) || shift > 70 {
			return 0, 0, false
		}
		b := buffer[offset]
		offset++
		if shift < 64 {
			value |= uint64(b&0x7f) << shift
		}
		if b&0x80 == 0 {
			return value, offset, true
		}
	}
}
