package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// builtinFlows is the implemented flow set. It mirrors the reference's provider
// map (src/lib/oauth/providers/index.js) plus the separate onboarding routes that
// are auth modes in their own right (token import, cookie, PAT, API key).
//
// The parity gate compares this set against the oracle, so a provider added there
// shows up as missing here instead of silently disappearing.
func builtinFlows() []Flow {
	return []Flow{
		claudeFlow(),
		codexFlow(),
		xaiFlow(),
		geminiCLIFlow(),
		antigravityFlow(),
		iflowFlow(),
		gitlabFlow(),
		clineFlow("cline", "https://api.cline.bot/api/v1/auth/authorize", "https://api.cline.bot/api/v1/auth/token"),
		clineFlow("clinepass", "https://api.cline.bot/api/v1/auth/authorize", "https://api.cline.bot/api/v1/auth/token"),
		kimchiFlow(),
		qoderFlow(),
		githubFlow(),
		kiroFlow(),
		cursorFlow(),
		kimiFlow(),
		kilocodeFlow(),
		codebuddyFlow("codebuddy-cn", "https://copilot.tencent.com", "copilot.tencent.com", "CLI/2.63.2 CodeBuddy/2.63.2", "CLI"),
		codebuddyFlow("codebuddy-intl", "https://www.codebuddy.ai", "www.codebuddy.ai", "IDE/2.63.2 CodeBuddy/2.63.2", "ide"),
		traeFlow(),
		windsurfFlow(),
		zedFlow(),
		xiaomiFlow(),
		grokCLIFlow(),
	}
}

// ManualFlows returns the provider id → flow for auth modes that need no browser
// step: token import, cookie, PAT and API-key connections.
//
// These mirror the reference's separate routes:
//
//	codex/import-token, codex/bulk-import, grok-cli/bulk-import
//	iflow/cookie, kiro/api-key, kiro/auto-import, cursor/import
//	xiaomi-mimo/api-key, xiaomi-mimo/auto-import, gitlab/pat
func ManualFlows() map[string]Flow {
	flows := make(map[string]Flow)
	for _, flow := range []Flow{
		codexImportFlow(),
		grokCLIImportFlow(),
		iflowCookieFlow(),
		kiroAPIKeyFlow(),
		xiaomiAPIKeyFlow(),
		cursorFlow(),
	} {
		flows[flow.Provider] = flow
	}
	return flows
}

// codexImportFlow mirrors src/app/api/oauth/codex/import-token/route.js: a
// ChatGPT access token pasted from the website, with identity read from its JWT.
func codexImportFlow() Flow {
	return Flow{
		Provider:   "codex",
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Import: func(_ context.Context, p Params) (Result, error) {
			token := strings.TrimSpace(p.Input)
			if token == "" {
				return Result{}, fmt.Errorf("codex access token is required")
			}
			identity, providerData := codexAccount("", token)
			if providerData == nil {
				providerData = map[string]string{}
			}
			providerData["authMethod"] = "access_token"
			return Result{
				AuthKind: credentials.AuthOAuth,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  token,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// grokCLIImportFlow mirrors src/app/api/oauth/grok-cli/bulk-import/route.js: the
// operator pastes account JSON in either snake_case or camelCase.
func grokCLIImportFlow() Flow {
	return Flow{
		Provider:   "grok-cli",
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Bulk:       grokBulkAccount,
		Import: func(_ context.Context, p Params) (Result, error) {
			var account struct {
				AccessToken      string          `json:"access_token"`
				AccessTokenCamel string          `json:"accessToken"`
				RefreshToken     string          `json:"refresh_token"`
				RefreshCamel     string          `json:"refreshToken"`
				IDToken          string          `json:"id_token"`
				IDTokenCamel     string          `json:"idToken"`
				Email            string          `json:"email"`
				ExpiresAt        any             `json:"expiresAt"`
				ExpiresIn        json.RawMessage `json:"expires_in"`
				DisplayName      string          `json:"displayName"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(p.Input)), &account); err != nil {
				return Result{}, fmt.Errorf("grok-cli import expects a JSON account object")
			}
			access := firstNonEmpty(account.AccessToken, account.AccessTokenCamel)
			if access == "" {
				return Result{}, fmt.Errorf("grok-cli import requires an access token")
			}
			refresh := firstNonEmpty(account.RefreshToken, account.RefreshCamel)
			idToken := firstNonEmpty(account.IDToken, account.IDTokenCamel)
			identity := account.Email
			if identity == "" {
				if claims := jwtClaims(idToken); claims != nil {
					if value, ok := claims["email"].(string); ok {
						identity = value
					}
				}
			}
			expiry := resetAtValue(account.ExpiresAt)
			if expiry.IsZero() {
				expiry = expiryFrom(account.ExpiresIn, p.now())
			}
			providerData := map[string]string{"authMethod": "device_code"}
			if idToken != "" {
				providerData["idToken"] = idToken
			}
			if identity != "" {
				providerData["email"] = identity
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    identity,
				DisplayName: account.DisplayName,
				Secret: credentials.Secret{
					AccessToken:  access,
					RefreshToken: refresh,
					Expiry:       expiry,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// iflowCookieFlow mirrors src/app/api/oauth/iflow/cookie/route.js: a browser
// cookie is exchanged for the account's API key, and only BXAuth is retained.
func iflowCookieFlow() Flow {
	return Flow{
		Provider:         "iflow",
		AuthKind:         credentials.AuthCookie,
		Type:             FlowManualToken,
		Completion:       CompletionToken,
		ResolvesIdentity: true,
		Import: func(ctx context.Context, p Params) (Result, error) {
			cookie := strings.TrimSpace(p.Input)
			if !strings.Contains(cookie, "BXAuth=") {
				return Result{}, fmt.Errorf("iflow cookie must contain BXAuth")
			}
			if !strings.HasSuffix(cookie, ";") {
				cookie += ";"
			}
			// The API-key list gives the key name the refresh call needs.
			var listing struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
				Data    struct {
					Name string `json:"name"`
				} `json:"data"`
			}
			if err := getJSON(ctx, p.Client, "https://platform.iflow.cn/api/openapi/apikey", map[string]string{
				"Cookie": cookie,
			}, &listing); err != nil || !listing.Success {
				return Result{}, fmt.Errorf("iflow cookie was rejected")
			}
			if listing.Data.Name == "" {
				return Result{}, fmt.Errorf("iflow returned no API key name")
			}
			body, err := json.Marshal(map[string]string{"name": listing.Data.Name})
			if err != nil {
				return Result{}, err
			}
			var refreshed struct {
				Success bool   `json:"success"`
				Message string `json:"message"`
				Data    struct {
					APIKey     string          `json:"apiKey"`
					Name       string          `json:"name"`
					ExpireTime json.RawMessage `json:"expireTime"`
				} `json:"data"`
			}
			status, err := postJSONTargetWithHeaders(ctx, p.Client, "https://platform.iflow.cn/api/openapi/apikey", map[string]string{
				"Cookie":  cookie,
				"Origin":  "https://platform.iflow.cn",
				"Referer": "https://platform.iflow.cn/",
			}, string(body), &refreshed)
			if err != nil || status < 200 || status >= 300 || !refreshed.Success {
				return Result{}, fmt.Errorf("iflow API key refresh failed")
			}
			if refreshed.Data.APIKey == "" {
				return Result{}, fmt.Errorf("iflow returned no API key")
			}
			// Only the auth cookie is retained; the rest is tracking state.
			authCookie := ""
			if match := bxAuthPattern.FindStringSubmatch(cookie); len(match) == 2 {
				authCookie = "BXAuth=" + match[1] + ";"
			}
			identity := refreshed.Data.Name
			if identity == "" {
				identity = listing.Data.Name
			}
			return Result{
				AuthKind: credentials.AuthCookie,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken: refreshed.Data.APIKey,
					Cookie:      authCookie,
					ProviderData: map[string]string{
						"apiKey": refreshed.Data.APIKey,
					},
				},
			}, nil
		},
	}
}

// kiroAPIKeyFlow mirrors src/app/api/oauth/kiro/api-key/route.js: a long-lived
// bearer key, validated against the Amazon Q surface and stored with authMethod
// api_key so the quota reader sends the API-key header.
func kiroAPIKeyFlow() Flow {
	return Flow{
		Provider:   "kiro",
		AuthKind:   credentials.AuthAPIKey,
		MetaKeys:   []string{"region"},
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Import: func(ctx context.Context, p Params) (Result, error) {
			apiKey := strings.TrimSpace(p.Input)
			if apiKey == "" {
				return Result{}, fmt.Errorf("kiro API key is required")
			}
			region := metaOr(p, "region", "us-east-1")
			if !awsRegionPattern.MatchString(region) {
				return Result{}, fmt.Errorf("kiro region is invalid")
			}
			var catalog json.RawMessage
			if err := postJSONTargetWithHeadersAuth(ctx, p.Client,
				fmt.Sprintf("https://q.%s.amazonaws.com/listAvailableModels", region),
				apiKey, `{"origin":"AI_EDITOR"}`, &catalog); err != nil {
				return Result{}, fmt.Errorf("kiro API key validation failed")
			}
			identity := ""
			if claims := jwtClaims(apiKey); claims != nil {
				if value, ok := claims["email"].(string); ok {
					identity = value
				}
			}
			return Result{
				AuthKind: credentials.AuthAPIKey,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken: apiKey,
					// An API key never refreshes on a schedule; a long horizon keeps
					// the proactive refresh path from attempting one.
					Expiry: p.now().Add(365 * 24 * time.Hour),
					ProviderData: map[string]string{
						"region":     region,
						"authMethod": "api_key",
					},
				},
			}, nil
		},
	}
}

// xiaomiAPIKeyFlow mirrors src/app/api/oauth/xiaomi-mimo/api-key/route.js: an
// sk- key is validated against the models endpoint, and a failed validation is
// soft so a network-restricted install can still store the key.
func xiaomiAPIKeyFlow() Flow {
	return Flow{
		Provider:   "xiaomi-mimo",
		AuthKind:   credentials.AuthAPIKey,
		MetaKeys:   []string{"baseUrl", "uid"},
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Import: func(ctx context.Context, p Params) (Result, error) {
			key := strings.TrimSpace(p.Input)
			if !strings.HasPrefix(key, "sk-") {
				return Result{}, fmt.Errorf("xiaomi-mimo key must start with sk-")
			}
			baseURL := strings.TrimSuffix(metaOr(p, "baseUrl", "https://api.xiaomimimo.com/v1"), "/")
			var models json.RawMessage
			_ = getJSON(ctx, p.Client, baseURL+"/models", map[string]string{
				"Authorization": "Bearer " + key,
				"X-Mimo-Source": "mimocode-cli",
			}, &models)
			identity := ""
			if uid := p.Meta["uid"]; uid != "" {
				identity = uid + "@xiaomi"
			}
			providerData := map[string]string{
				"apiKey":     key,
				"baseUrl":    baseURL,
				"authMethod": "api_key",
			}
			if uid := p.Meta["uid"]; uid != "" {
				providerData["uid"] = uid
			}
			return Result{
				AuthKind: credentials.AuthAPIKey,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  key,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// bxAuthPattern extracts the auth cookie value.
var bxAuthPattern = regexp.MustCompile(`BXAuth=([^;]+)`)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
