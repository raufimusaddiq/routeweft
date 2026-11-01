package usage

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/mimoaccount"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

var mimoAccountBase = mimoaccount.Base

const mimoAPIUserAgent = mimoaccount.UserAgent
const mimoSessionRequired = "Weekly quota requires Xiaomi account session. API key alone is insufficient."

// xiaomi mirrors getXiaomiMimoUsage (open-sse/services/usage/xiaomi-mimo.js:22-125).
// With an account credential the weekly quota is read through the account
// session (mimoAccount.js:246-264); if that yields no finite percent, or there
// is no account credential, the key path reads the aistudio endpoint.
//
// The pin reads MiMo Desktop's cookie store for the session whenever the
// connection stores none (mimoAccount.js:181-185). Here the reader never opens
// it: it asks desktop (MimoDesktopRenewal), which reads and seals a session
// only with the host's and the operator's consent (#299, BDR-028), when there
// is no sealed session or the sealed one just failed the handshake.
func (r apiKeyReader) xiaomi(ctx context.Context, credential quota.Credential) (quota.State, error) {
	// Bound the SSO exchange and key fallback together, beyond Client.call’s per-hop deadline.
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	account := credential.ProviderData
	renew := account["mimoPassToken"] == ""
	if !renew {
		state, read, handshakeFailed, err := r.mimoAccountUsage(ctx, account)
		if read {
			return r.mimoDesktopNote(state, account), err
		}
		renew = handshakeFailed && ctx.Err() == nil
	}
	if renew && r.desktop != nil {
		if renewed := r.desktop.Renew(ctx, credential.ConnectionID, account); renewed["mimoPassToken"] != "" {
			account = renewed
			if state, read, _, err := r.mimoAccountUsage(ctx, account); read {
				return r.mimoDesktopNote(state, account), err
			}
		}
	}
	// `accessToken || providerSpecificData.apiKey`, then trimmed (xiaomi-mimo.js:47-58).
	key := credential.AccessToken
	if key == "" {
		key = credential.ProviderData["apiKey"]
	}
	if key = shared.Trim(key); key == "" {
		return failure("Xiaomi MiMo Desktop not connected. Add credentials to view usage.")
	}
	reply, err := r.callWithin(ctx, mimoUsageTimeout, http.MethodGet, "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage", map[string]string{
		"Authorization": "Bearer " + key, "X-Mimo-Source": "mimocode-cli", "Accept": "application/json",
	}, nil)
	if err != nil {
		return failure("Xiaomi MiMo Desktop usage request failed.")
	}
	if reply.status == http.StatusUnauthorized {
		// The key cannot read the weekly quota: unsupported, not an outage.
		return quota.State{Plan: mimoPlan, Message: mimoSessionRequired, ObservedAt: r.now()}, nil
	}
	if reply.status < 200 || reply.status >= 300 {
		return failure("Usage API error (%d)", reply.status)
	}
	data, ok := mimoData(reply.body)
	if !ok {
		return failure("Usage endpoint returned unexpected response.")
	}
	percent, ok := shared.IsNumber(shared.Field(data, "percent"))
	if !ok {
		return quota.State{Plan: mimoPlan, Message: "Usage data missing percent field.", ObservedAt: r.now()}, nil
	}
	// The key path reads resetDate only (xiaomi-mimo.js:101-108).
	return r.state(mimoPlan, mimoWeekly(percent, mimoResetDate(shared.Field(data, "resetDate")))), nil
}

const mimoPlan = "Xiaomi MiMo Desktop"

// MimoDesktopRenewal reads and seals MiMo Desktop's account session for one
// connection, or returns nil; onboarding.MimoDesktopRenewal in production.
type MimoDesktopRenewal interface {
	Renew(ctx context.Context, connectionID string, stored map[string]string) map[string]string
}

// WithMimoDesktopRenewal gives the Xiaomi MiMo reader in readers the consented
// host renewal (#299). Without it the reader never reads host credentials.
func WithMimoDesktopRenewal(readers map[string]quota.UsageClient, renewal MimoDesktopRenewal) {
	if reader, ok := readers["xiaomi-mimo"].(apiKeyReader); ok {
		reader.desktop = renewal
		readers["xiaomi-mimo"] = reader
	}
}

// mimoDesktopNote records on an account-path read when a quota read last sealed
// a session from MiMo Desktop (#299): the time only, never a value.
func (r apiKeyReader) mimoDesktopNote(state quota.State, data map[string]string) quota.State {
	if at := data["mimoDesktopReadAt"]; at != "" {
		if state.Metadata == nil {
			state.Metadata = map[string]string{}
		}
		state.Metadata["desktopSessionReadAt"] = at
	}
	return state
}

// mimoAccountUsage is the account path. read is false when the pin would move
// on to the key: no session, or a usage answer without a finite percent.
// handshakeFailed is whether the session itself could not be used, as opposed
// to a usage answer that could not be read with it.
func (r apiKeyReader) mimoAccountUsage(ctx context.Context, account map[string]string) (state quota.State, read, handshakeFailed bool, err error) {
	cookie, err := r.mimo.Cookie(ctx, account["mimoPassToken"], r.now, func() (string, error) { return mimoaccount.ServiceCookie(ctx, r.HTTP, account) })
	if err != nil {
		return quota.State{}, false, true, nil
	}
	reply, err := r.callWithin(ctx, mimoUsageTimeout, http.MethodGet, mimoAccountBase+"/api/user/usage", map[string]string{
		"User-Agent": mimoAPIUserAgent, "Cookie": cookie, "Accept": "application/json",
	}, nil)
	if err != nil || reply.status < 200 || reply.status >= 300 {
		return quota.State{}, false, false, nil
	}
	data, ok := mimoData(reply.body)
	if !ok {
		return quota.State{}, false, false, nil
	}
	percent, ok := shared.IsNumber(shared.Field(data, "percent"))
	if !ok {
		return quota.State{}, false, false, nil
	}
	// A positive numeric resetAt is Unix seconds and wins over resetDate
	// (xiaomi-mimo.js:30-36). One past the Date range throws outside the pin's
	// try, so the whole read fails.
	reset := mimoResetDate(shared.Field(data, "resetDate"))
	if seconds, ok := shared.IsNumber(shared.Field(data, "resetAt")); ok && seconds > 0 {
		if math.Abs(seconds*1000) > jsMaxTime {
			state, err = failure("Xiaomi MiMo usage reset is out of range")
			return state, true, false, err
		}
		at := time.UnixMilli(int64(seconds * 1000)).UTC()
		reset = &at
	}
	return r.state(mimoPlan, mimoWeekly(percent, reset)), true, false, nil
}

// mimoData is `data.data` of a usage answer whose `code` is the number 0;
// anything else (not JSON, another code, a falsy data) is unexpected.
func mimoData(body []byte) (json.RawMessage, bool) {
	payload, ok := jsonBody(body)
	if !ok {
		return nil, false
	}
	code, ok := shared.IsNumber(shared.Field(payload, "code"))
	data := shared.Field(payload, "data")
	return data, ok && code == 0 && shared.Truthy(data)
}

// mimoWeekly is the one Weekly window: `percent` is the remaining percentage,
// rounded and clamped to [0, 100].
func mimoWeekly(percent float64, reset *time.Time) quota.Window {
	return percentWindow("Weekly", math.Max(0, math.Min(100, jsRound(percent))), reset)
}

// mimoResetDate is `new Date(`${resetDate}T00:00:00Z`)` for a string resetDate:
// an ISO date (year, year-month or full date) at UTC midnight.
func mimoResetDate(raw json.RawMessage) *time.Time {
	text, ok := stringValue(raw)
	if !ok || text == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout+"T15:04:05Z07:00", text+"T00:00:00Z"); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}
	return nil
}

// mimoUsageTimeout bounds each usage read: the reference sets
// AbortSignal.timeout(10000) on both the account-session read
// (open-sse/shared/mimoAccount.js:252-254) and the API-key fallback
// (xiaomi-mimo.js:53-62). The SSO hops before them keep the shared bound.
const mimoUsageTimeout = 10 * time.Second
