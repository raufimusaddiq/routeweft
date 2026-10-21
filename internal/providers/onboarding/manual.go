package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
		codebuddyFlow("codebuddy-cn", "https://copilot.tencent.com", "copilot.tencent.com", "CLI"),
		codebuddyFlow("codebuddy-intl", "https://www.codebuddy.ai", "www.codebuddy.ai", "ide"),
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
			providerData := map[string]string{"apiKey": refreshed.Data.APIKey}
			// The pin keeps the key's expireTime beside the cookie (:115-118), so
			// the operator can see when the key it minted lapses.
			if expire := strings.Trim(strings.TrimSpace(string(refreshed.Data.ExpireTime)), `"`); expire != "" && expire != "null" {
				providerData["expireTime"] = expire
			}
			return Result{
				AuthKind: credentials.AuthCookie,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  refreshed.Data.APIKey,
					Cookie:       authCookie,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// kiroAPIKeyFlow is Kiro's manual-credential flow. It covers the reference's three
// import routes (kiro/api-key, kiro/import, kiro/import-cli-proxy); kiroImport
// chooses among them from the pasted input. A bare string is an API key, stored
// with authMethod api_key so the quota reader sends the API-key header.
func kiroAPIKeyFlow() Flow {
	return Flow{
		Provider:   "kiro",
		AuthKind:   credentials.AuthAPIKey,
		MetaKeys:   []string{"region"},
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Import:     kiroImport,
	}
}

// kiroAPIKeyImport mirrors src/app/api/oauth/kiro/api-key/route.js and
// KiroService.validateApiKey (services/kiro.js:299-350): the key is proven against
// the Amazon Q model catalog, because a bearer-only profile call answers 200 with an
// empty list for an arbitrary key and so proves nothing about inference.
func kiroAPIKeyImport(ctx context.Context, p Params) (Result, error) {
	apiKey := strings.TrimSpace(p.Input)
	if apiKey == "" {
		return Result{}, fmt.Errorf("kiro API key is required")
	}
	region := metaOr(p, "region", "us-east-1")
	if !awsRegionPattern.MatchString(region) {
		return Result{}, fmt.Errorf("kiro region is invalid")
	}
	var catalog struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := getJSON(ctx, p.Client,
		fmt.Sprintf("https://q.%s.amazonaws.com/ListAvailableModels?origin=AI_EDITOR", region),
		map[string]string{
			"Authorization":    "Bearer " + apiKey,
			"TokenType":        "API_KEY",
			"User-Agent":       "AWS-SDK-JS/3.0.0 kiro-ide/1.0.0",
			"X-Amz-User-Agent": "aws-sdk-js/3.0.0 kiro-ide/1.0.0",
		}, &catalog); err != nil || len(catalog.Models) == 0 {
		return Result{}, fmt.Errorf("kiro API key validation failed")
	}
	return Result{
		AuthKind: credentials.AuthAPIKey,
		Identity: kiroEmail(apiKey),
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
}

// xiaomiAPIKeyFlow mirrors src/app/api/oauth/xiaomi-mimo/api-key/route.js: an
// sk- key is validated against the models endpoint, and a failed validation is
// soft so a network-restricted install can still store the key.
//
// The input is the bare key, or JSON carrying the rest of the pin's body
// ({apiKey, uid, baseUrl, mimoPassToken, mimoUserId, mimoCUserId}), or the contents
// of MiMo Code's auth.json (xiaomi-mimo/auto-import/route.js:15-22,85-104) pasted by
// the operator. The account-session fields travel in the sealed credential rather
// than in plain-text operator fields, and Routeweft never reads the file itself.
func xiaomiAPIKeyFlow() Flow {
	return Flow{
		Provider:   "xiaomi-mimo",
		AuthKind:   credentials.AuthAPIKey,
		MetaKeys:   []string{"baseUrl", "uid"},
		Type:       FlowManualToken,
		Completion: CompletionToken,
		Import: func(ctx context.Context, p Params) (Result, error) {
			account, err := parseXiaomiInput(p)
			if err != nil {
				return Result{}, err
			}
			baseURL := strings.TrimSuffix(firstNonEmpty(account.baseURL, "https://api.xiaomimimo.com/v1"), "/")
			providerData := map[string]string{
				"apiKey":     account.key,
				"baseUrl":    baseURL,
				"authMethod": "api_key",
			}
			// The pin counts the catalog it just read (:36-47). A failed read is
			// unknown, not zero, so no count is recorded for it.
			var models struct {
				Data []json.RawMessage `json:"data"`
			}
			if err := getJSON(ctx, p.Client, baseURL+"/models", map[string]string{
				"Authorization": "Bearer " + account.key,
				"X-Mimo-Source": "mimocode-cli",
			}, &models); err == nil {
				providerData["modelCount"] = strconv.Itoa(len(models.Data))
			}
			identity := ""
			if account.uid != "" {
				identity = account.uid + "@xiaomi"
				providerData["uid"] = account.uid
			}
			// Per-account session credential: what lets several Xiaomi accounts
			// rotate independently (:73-76,110-113).
			for key, value := range map[string]string{
				"mimoPassToken": account.passToken,
				"mimoUserId":    account.userID,
				"mimoCUserId":   account.cUserID,
			} {
				if value != "" {
					providerData[key] = value
				}
			}
			return Result{
				AuthKind: credentials.AuthAPIKey,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  account.key,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// xiaomiAccount is one parsed Xiaomi MiMo import.
type xiaomiAccount struct {
	key, uid, baseURL          string
	passToken, userID, cUserID string
}

// parseXiaomiInput accepts the three input shapes and returns the account. The key
// format check is the pin's (:22-28, auto-import :93-100).
func parseXiaomiInput(p Params) (xiaomiAccount, error) {
	input := strings.TrimSpace(p.Input)
	account := xiaomiAccount{key: input, uid: strings.TrimSpace(p.Meta["uid"]), baseURL: strings.TrimSpace(p.Meta["baseUrl"])}
	if strings.HasPrefix(input, "{") {
		var document struct {
			APIKey    string `json:"apiKey"`
			UID       string `json:"uid"`
			Endpoint  string `json:"baseUrl"`
			PassToken string `json:"mimoPassToken"`
			UserID    string `json:"mimoUserId"`
			CUserID   string `json:"mimoCUserId"`
			Xiaomi    struct {
				Key      string `json:"key"`
				Metadata struct {
					UID      string `json:"uid"`
					Endpoint string `json:"base_url"`
				} `json:"metadata"`
			} `json:"xiaomi"`
		}
		if err := json.Unmarshal([]byte(input), &document); err != nil {
			return xiaomiAccount{}, fmt.Errorf("xiaomi-mimo import expects a key or a JSON object")
		}
		account = xiaomiAccount{
			key:       strings.TrimSpace(firstNonEmpty(document.APIKey, document.Xiaomi.Key)),
			uid:       firstNonEmpty(document.UID, document.Xiaomi.Metadata.UID, account.uid),
			baseURL:   firstNonEmpty(document.Endpoint, document.Xiaomi.Metadata.Endpoint, account.baseURL),
			passToken: strings.TrimSpace(document.PassToken),
			userID:    strings.TrimSpace(document.UserID),
			cUserID:   strings.TrimSpace(document.CUserID),
		}
	}
	if !strings.HasPrefix(account.key, "sk-") {
		return xiaomiAccount{}, fmt.Errorf("xiaomi-mimo key must start with sk-")
	}
	return account, nil
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
