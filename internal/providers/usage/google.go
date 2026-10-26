package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// Cloud Code endpoints and client identity, from the pin's registry usage blocks
// (open-sse/providers/registry/antigravity.js:38-43, gemini-cli.js:26-29) and
// open-sse/providers/shared.js:92-94. Antigravity reads model and summary quota
// on the daily host; project discovery stays on the production host.
const (
	cloudCodeLoadURL      = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiQuotaURL        = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	antigravityModelsURL  = "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels"
	antigravitySummaryURL = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityVersion    = "2.11.0"
	antigravityUserAgent  = "antigravity/ide/" + antigravityVersion + " darwin/arm64"
)

// clientMetadata is CLIENT_METADATA (open-sse/config/appConstants.js:35-70): the
// IDE and plugin enums are fixed, the platform enum describes the host.
func clientMetadata() string {
	platform := 0
	switch runtime.GOOS {
	case "darwin":
		platform = 1
		if runtime.GOARCH == "arm64" {
			platform = 2
		}
	case "linux":
		platform = 3
		if runtime.GOARCH == "arm64" {
			platform = 4
		}
	case "windows":
		platform = 5
	}
	return fmt.Sprintf(`{"ideType":9,"platform":%d,"pluginType":2}`, platform)
}

// geminiReader mirrors getGeminiUsage (open-sse/services/usage/google.js:22-90).
//
// Per-model buckets from Cloud Code Assist retrieveUserQuota. The project id
// comes from the connection (stored at sign-in); only without one does the
// reader ask loadCodeAssist, which also supplies the tier name as the plan.
type geminiReader struct{ Client }

func (r geminiReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("gemini-cli access token is required")
	}
	projectID := strings.TrimSpace(credential.ProviderData["projectId"])
	plan := "Free"
	if projectID == "" {
		resolved, tier := r.subscription(ctx, credential.AccessToken)
		projectID = resolved
		if tier != "" {
			plan = tier
		}
	}
	if projectID == "" {
		return failure("gemini-cli project id is unavailable (plan %s); reconnect the account or configure a Google Cloud project with Gemini Code Assist access", plan)
	}
	encoded, _ := json.Marshal(projectID)
	reply, err := r.call(ctx, http.MethodPost, geminiQuotaURL,
		map[string]string{
			"Authorization": "Bearer " + credential.AccessToken,
			"Content-Type":  "application/json",
		},
		[]byte(fmt.Sprintf(`{"project":%s}`, encoded)))
	if err != nil {
		return failure("gemini-cli quota request failed: %v", err)
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("gemini-cli quota returned HTTP %d", reply.status)
	}
	var body struct {
		Buckets json.RawMessage `json:"buckets"`
	}
	if err := decode(reply.body, &body); err != nil {
		return failure("gemini-cli quota response: %v", err)
	}
	// A non-array buckets field is no quota rather than a failure (google.js:67).
	var buckets []struct {
		ModelID           string          `json:"modelId"`
		RemainingFraction json.RawMessage `json:"remainingFraction"`
		ResetTime         any             `json:"resetTime"`
	}
	_ = json.Unmarshal(body.Buckets, &buckets)
	var windows []quota.Window
	position := map[string]int{}
	for _, bucket := range buckets {
		raw := strings.TrimSpace(string(bucket.RemainingFraction))
		if bucket.ModelID == "" || raw == "" || raw == "null" {
			continue
		}
		// Number(x) || 0: a present but unreadable fraction is exhausted.
		remaining, _ := jsNumber(bucket.RemainingFraction)
		next := percentWindow(bucket.ModelID, remaining*100, resetAt(bucket.ResetTime))
		// Buckets are keyed by model: a repeated model replaces the earlier
		// value in place, as the reference's object assignment does.
		if index, seen := position[bucket.ModelID]; seen {
			windows[index] = next
			continue
		}
		position[bucket.ModelID] = len(windows)
		windows = append(windows, next)
	}
	return r.state(plan, windows...), nil
}

// subscription resolves the Cloud Code Assist project id and tier name. Any
// failure is no discovery, never an error (google.js:95-115).
func (r geminiReader) subscription(ctx context.Context, accessToken string) (string, string) {
	reply, err := r.call(ctx, http.MethodPost, cloudCodeLoadURL,
		map[string]string{
			"Authorization": "Bearer " + accessToken,
			"Content-Type":  "application/json",
		},
		[]byte(`{"metadata":`+clientMetadata()+`}`))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return "", ""
	}
	var body struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
		Tier    struct {
			Name string `json:"name"`
		} `json:"currentTier"`
	}
	if err := decode(reply.body, &body); err != nil {
		return "", ""
	}
	return projectIDFrom(body.Project), body.Tier.Name
}

// projectIDFrom accepts either a bare id or an object with an id field, which is
// how both Cloud Code endpoints report the companion project
// (normalizeCloudCodeProjectId, open-sse/services/usage/shared.js:54-60).
func projectIDFrom(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		return strings.TrimSpace(id)
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return strings.TrimSpace(object.ID)
	}
	return ""
}

// antigravityReader mirrors getAntigravityUsage (google.js:120-280) plus the
// weekly overlay in antigravity-weekly.js. The weekly summary is best-effort: a
// failure there never removes the per-model rows. A rejected model read stops
// the whole read, weekly included, as in the reference.
type antigravityReader struct{ Client }

func (r antigravityReader) Usage(ctx context.Context, credential quota.Credential) (quota.State, error) {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return failure("antigravity access token is required")
	}
	project, plan, paid := r.subscription(ctx, credential.AccessToken)
	reply, err := r.call(ctx, http.MethodPost, antigravityModelsURL,
		antigravityHeaders(credential.AccessToken, true), projectBody(project))
	if err != nil {
		return failure("antigravity quota request failed: %v", err)
	}
	switch {
	case reply.status == http.StatusForbidden:
		return failure("antigravity quota API access forbidden; chat may still work")
	case reply.status == http.StatusUnauthorized:
		return failure("antigravity quota API authentication expired; chat may still work")
	case reply.status < 200 || reply.status >= 300:
		return failure("antigravity quota returned HTTP %d", reply.status)
	}
	var models []quota.Window
	// Free-tier accounts have only the weekly window: fetchAvailableModels
	// reports misleading per-model numbers for them, so it is not parsed.
	if paid {
		models, err = antigravityModels(reply.body)
		if err != nil {
			return failure("antigravity quota response: %v", err)
		}
	} else if err := json.Unmarshal(reply.body, new(any)); err != nil {
		return failure("antigravity quota response: %v", err)
	}
	weekly := r.weekly(ctx, credential.AccessToken, project)
	reconcileSession(weekly, models, "gemini_session", func(id string) bool {
		return strings.HasPrefix(id, "gemini-") && !strings.Contains(id, "image")
	})
	reconcileSession(weekly, models, "claude_gpt_session", func(id string) bool {
		return strings.HasPrefix(id, "claude-")
	})
	windows := append(models, weekly...)
	// The reference returns an empty quota map here; Routeweft keeps its rule
	// that a read with no windows is a failure, never a plan with nothing behind
	// it (TestEveryReaderMakesARequestOrFails).
	if len(windows) == 0 {
		return failure("antigravity returned no quota windows (plan %s)", plan)
	}
	return quota.State{Plan: plan, Windows: windows, ObservedAt: r.now()}, nil
}

func antigravityHeaders(accessToken string, client bool) map[string]string {
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"User-Agent":    antigravityUserAgent,
		"Content-Type":  "application/json",
	}
	if client {
		headers["X-Client-Name"] = "antigravity"
		headers["X-Client-Version"] = antigravityVersion
	}
	return headers
}

// projectBody sends the companion project exactly as loadCodeAssist returned it
// when it is truthy (google.js:124,135-137): the reference does not normalize it.
func projectBody(project json.RawMessage) []byte {
	var compact bytes.Buffer
	if !truthy(project) || json.Compact(&compact, project) != nil {
		return []byte(`{}`)
	}
	return []byte(`{"project":` + compact.String() + `}`)
}

// truthy is JavaScript truthiness for one JSON value.
func truthy(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	switch {
	case text == "" || text == "null" || text == "false":
		return false
	case strings.HasPrefix(text, `"`):
		return text != `""`
	case strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") || text == "true":
		return true
	}
	value, ok := jsNumber(raw)
	return ok && value != 0
}

// subscription reads loadCodeAssist once for the project, plan and tier. Any
// failure leaves the plan Unknown and the account treated as free tier.
func (r antigravityReader) subscription(ctx context.Context, accessToken string) (json.RawMessage, string, bool) {
	reply, err := r.call(ctx, http.MethodPost, cloudCodeLoadURL,
		antigravityHeaders(accessToken, false),
		[]byte(`{"metadata":`+clientMetadata()+`,"mode":1}`))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return nil, "Unknown", false
	}
	var body struct {
		Project json.RawMessage `json:"cloudaicompanionProject"`
		Tier    struct {
			Name string `json:"name"`
		} `json:"currentTier"`
		PaidTier struct {
			ID string `json:"id"`
		} `json:"paidTier"`
	}
	if err := decode(reply.body, &body); err != nil {
		return nil, "Unknown", false
	}
	plan := body.Tier.Name
	if plan == "" {
		plan = "Unknown"
	}
	paid := body.PaidTier.ID != "" && body.PaidTier.ID != "free-tier"
	return body.Project, plan, paid
}

// antigravityModels reads per-model quota for paid accounts in response order.
// Only the models the reference considers important are surfaced, because the
// endpoint returns a long tail of internal models (google.js:169-221).
func antigravityModels(payload []byte) ([]quota.Window, error) {
	var body struct {
		Models json.RawMessage `json:"models"`
	}
	if err := decode(payload, &body); err != nil {
		return nil, err
	}
	var windows []quota.Window
	err := eachMember(body.Models, func(id string, raw json.RawMessage) {
		var model struct {
			DisplayName string          `json:"displayName"`
			Internal    bool            `json:"isInternal"`
			QuotaInfo   json.RawMessage `json:"quotaInfo"`
		}
		if json.Unmarshal(raw, &model) != nil || !truthy(model.QuotaInfo) || model.Internal || !antigravityImportant(id) {
			return
		}
		var info struct {
			RemainingFraction json.RawMessage `json:"remainingFraction"`
			ResetTime         any             `json:"resetTime"`
		}
		_ = json.Unmarshal(model.QuotaInfo, &info)
		// remainingFraction || 0: an exhausted model omits the fraction.
		remaining := 0.0
		if truthy(info.RemainingFraction) {
			value, ok := jsNumber(info.RemainingFraction)
			if !ok {
				value = math.NaN()
			}
			remaining = value
		}
		display := model.DisplayName
		if display == "" {
			display = id
		}
		windows = append(windows, quota.Window{
			Name:        id,
			DisplayName: display,
			Remaining:   fraction(remaining * 100),
			ResetAt:     resetAt(info.ResetTime),
		})
	})
	return windows, err
}

// eachMember visits a JSON object's members in document order. A value that is
// not an object has no members.
func eachMember(raw json.RawMessage, visit func(string, json.RawMessage)) error {
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		visit(token.(string), value)
	}
	return nil
}

// antigravityImportant mirrors the reference's allowlist of models worth showing.
func antigravityImportant(id string) bool {
	switch id {
	case "gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
		"gemini-3.7-flash-high", "gemini-3.7-flash-medium", "gemini-3.7-flash-low",
		"gemini-3.6-flash-high", "gemini-3.6-flash-medium", "gemini-3.6-flash-low",
		"gemini-3.5-flash-low", "gemini-3.5-flash-extra-low",
		"gemini-pro-agent", "gemini-3.1-pro-low",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium",
		"gemini-3.1-flash-image":
		return true
	}
	return false
}

// weekly reads the shared 5h and weekly pools from retrieveUserQuotaSummary.
// Every failure is no overlay (antigravity-weekly.js:116-166).
func (r antigravityReader) weekly(ctx context.Context, accessToken string, project json.RawMessage) []quota.Window {
	reply, err := r.call(ctx, http.MethodPost, antigravitySummaryURL,
		antigravityHeaders(accessToken, true), projectBody(project))
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return nil
	}
	return parseWeeklySummary(reply.body)
}

// antigravityFamilies are GROUP_CONFIGS (antigravity-weekly.js:29-40): the
// first family whose pattern matches the group's displayName owns its buckets.
var antigravityFamilies = []struct {
	match                func(string) bool
	weekly, session      string
	weeklyName, sessName string
}{
	{func(name string) bool { return strings.Contains(name, "gemini") },
		"gemini_weekly", "gemini_session", "Gemini (Weekly)", "Gemini (5h)"},
	{func(name string) bool { return strings.Contains(name, "claude") || strings.Contains(name, "gpt") },
		"claude_gpt_weekly", "claude_gpt_session", "Claude & GPT (Weekly)", "Claude & GPT (5h)"},
}

// parseWeeklySummary is parseWeeklyQuotaSummary (antigravity-weekly.js:49-111).
// Groups sit at groups or quotaSummary.groups and are matched on displayName.
// A disabled weekly bucket is dropped; a disabled session bucket stays as an
// exhausted row because upstream disables it when the weekly pool is spent.
// The first bucket per family and window wins.
func parseWeeklySummary(payload []byte) []quota.Window {
	var body struct {
		Groups       json.RawMessage `json:"groups"`
		QuotaSummary json.RawMessage `json:"quotaSummary"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return nil
	}
	var groups []json.RawMessage
	if !isArray(body.Groups) || json.Unmarshal(body.Groups, &groups) != nil {
		var nested struct {
			Groups json.RawMessage `json:"groups"`
		}
		_ = json.Unmarshal(body.QuotaSummary, &nested)
		groups = nil
		if !isArray(nested.Groups) || json.Unmarshal(nested.Groups, &groups) != nil {
			return nil
		}
	}
	var windows []quota.Window
	seen := map[string]bool{}
	for _, rawGroup := range groups {
		var group struct {
			DisplayName any               `json:"displayName"`
			Buckets     []json.RawMessage `json:"buckets"`
		}
		if json.Unmarshal(rawGroup, &group) != nil {
			continue
		}
		name, _ := group.DisplayName.(string)
		name = strings.ToLower(name)
		for _, rawBucket := range group.Buckets {
			var bucket struct {
				Window            any             `json:"window"`
				BucketID          any             `json:"bucketId"`
				DisplayName       any             `json:"displayName"`
				Disabled          any             `json:"disabled"`
				RemainingFraction json.RawMessage `json:"remainingFraction"`
				ResetTime         any             `json:"resetTime"`
			}
			if json.Unmarshal(rawBucket, &bucket) != nil {
				continue
			}
			kind := strings.ToLower(jsString(bucket.Window))
			text := strings.ToLower(jsString(bucket.BucketID) + " " + jsString(bucket.DisplayName))
			isWeekly := kind == "weekly" || strings.Contains(text, "weekly")
			isSession := kind == "5h" || kind == "daily" || strings.Contains(text, "five hour") ||
				strings.Contains(text, "5h") || strings.Contains(text, "daily")
			disabled := bucket.Disabled == true
			if !isWeekly && !isSession || disabled && isWeekly {
				continue
			}
			remaining := 0.0
			if !disabled {
				value, ok := jsNumber(bucket.RemainingFraction)
				if !ok {
					continue
				}
				remaining = value
			}
			for _, family := range antigravityFamilies {
				if !family.match(name) {
					continue
				}
				key, display := family.session, family.sessName
				if isWeekly {
					key, display = family.weekly, family.weeklyName
				}
				if !seen[key] {
					seen[key] = true
					windows = append(windows, quota.Window{
						Name:        key,
						DisplayName: display,
						Remaining:   fraction(remaining * 100),
						ResetAt:     resetAt(bucket.ResetTime),
					})
				}
				break
			}
		}
	}
	return windows
}

// reconcileSession marks a family's 5h row exhausted when every per-model row in
// that family is at zero, taking the latest model reset (google.js:231-264). The
// weekly row is never touched.
func reconcileSession(weekly, models []quota.Window, key string, member func(string) bool) {
	session := -1
	for index, window := range weekly {
		if window.Name == key {
			session = index
		}
	}
	if session < 0 || weekly[session].Remaining == nil || *weekly[session].Remaining <= 0 {
		return
	}
	var latest *time.Time
	found := false
	for _, model := range models {
		if !member(model.Name) {
			continue
		}
		found = true
		if model.Remaining == nil || *model.Remaining != 0 {
			return
		}
		if latest == nil || (model.ResetAt != nil && model.ResetAt.After(*latest)) {
			latest = model.ResetAt
		}
	}
	if !found {
		return
	}
	zero := 0.0
	weekly[session].Remaining = &zero
	if latest != nil {
		weekly[session].ResetAt = latest
	}
}

func isArray(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
}

// jsString is String(value || "") for the scalar shapes a bucket label takes.
func jsString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if typed == 0 {
			return ""
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
	}
	return ""
}

// jsNumber is Number(value) restricted to finite results: null, false and blank
// strings are zero; an absent, non-numeric or structured value is not a number.
func jsNumber(raw json.RawMessage) (float64, bool) {
	text := strings.TrimSpace(string(raw))
	switch text {
	case "":
		return 0, false
	case "null", "false":
		return 0, true
	case "true":
		return 1, true
	}
	var quoted string
	if json.Unmarshal(raw, &quoted) == nil {
		quoted = strings.TrimSpace(quoted)
		if quoted == "" {
			return 0, true
		}
		value, err := strconv.ParseFloat(quoted, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return 0, false
		}
		return value, true
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}
