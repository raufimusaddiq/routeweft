package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// traeFlow mirrors src/lib/oauth/providers/trae.js: a ByteDance handshake where
// GetLoginGuidance yields the login host, the callback carries a refresh token,
// and ExchangeToken trades it for an access token.
//
// The reference also accepts a pasted Cloud-IDE JWT. Both paths are kept because
// the paste path is what an operator uses when the local callback cannot be
// reached.
func traeFlow() Flow {
	const (
		clientID      = "ono9krqynydwx5"
		clientSecret  = "-"
		exchangePath  = "/cloudide/api/v3/trae/oauth/ExchangeToken"
		userInfoPath  = "/cloudide/api/v3/trae/GetUserInfo"
		authorization = "/authorization"
		userAgent     = "Trae/1.0.0 antigravity-cockpit-tools"
		tokenLifetime = 14 * 24 * time.Hour
		pluginVersion = "local"
		appVersion    = "3.5.54"
		deviceID      = "0"
	)
	// Only these origins are trusted for the exchange; a loginHost from the
	// callback is deliberately not honored, or a crafted callback could point the
	// exchange at an internal host.
	apiOrigins := []string{"https://api.marscode.com", "https://api.trae.ai", "https://www.trae.ai", "https://www.marscode.com"}
	loginGuidance := []string{
		"https://api.marscode.com/cloudide/api/v3/trae/GetLoginGuidance",
		"https://api.trae.ai/cloudide/api/v3/trae/GetLoginGuidance",
		"https://www.trae.ai/cloudide/api/v3/trae/GetLoginGuidance",
	}
	return Flow{
		Provider:     "trae",
		Type:         FlowAuthorizationCode,
		Completion:   CompletionCallback,
		CallbackPath: "/callback",
		Authorize: func(ctx context.Context, p Params) (AuthRequest, error) {
			traceID, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			host, err := traeLoginHost(ctx, p.Client, loginGuidance, traceID, userAgent)
			if err != nil {
				return AuthRequest{}, err
			}
			machineID, err := randomToken(16)
			if err != nil {
				return AuthRequest{}, err
			}
			values := url.Values{
				"login_version":     {"1"},
				"auth_from":         {"trae"},
				"login_channel":     {"native_ide"},
				"plugin_version":    {pluginVersion},
				"auth_type":         {"local"},
				"client_id":         {clientID},
				"redirect":          {"0"},
				"login_trace_id":    {traceID},
				"auth_callback_url": {p.RedirectURI},
				"machine_id":        {machineID},
				"device_id":         {deviceID},
				"x_device_id":       {deviceID},
				"x_machine_id":      {machineID},
				"x_device_brand":    {"unknown"},
				"x_device_type":     {"unknown"},
				"x_os_version":      {"unknown"},
				"x_env":             {""},
				"x_app_version":     {appVersion},
				"x_app_type":        {"stable"},
			}
			return pkceAuthorize(host+authorization, values)
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			trimmed := strings.TrimSpace(p.Input)
			// Paste-token mode: a raw Cloud-IDE JWT, with no refresh exchange.
			if !strings.Contains(trimmed, "refreshToken") && !strings.Contains(trimmed, "refresh_token") {
				token := trimmed
				for _, prefix := range []string{"Cloud-IDE-JWT ", "Bearer "} {
					token = strings.TrimPrefix(token, prefix)
				}
				if token == "" {
					return Result{}, fmt.Errorf("trae token is required")
				}
				return Result{
					AuthKind: credentials.AuthOAuth,
					Secret: credentials.Secret{
						AccessToken: token,
						Expiry:      p.now().Add(tokenLifetime),
						ProviderData: map[string]string{
							"authMethod": "imported",
							"aiRegion":   "US-East",
							"region":     "US-East",
							"scope":      "marscode-us",
						},
					},
				}, nil
			}
			refreshToken := callbackParam(trimmed, "refreshToken")
			if refreshToken == "" {
				refreshToken = callbackParam(trimmed, "refresh_token")
			}
			if refreshToken == "" {
				return Result{}, fmt.Errorf("trae callback carried no refresh token")
			}
			cloudideToken := callbackParam(trimmed, "x-cloudide-token")
			accessToken, newRefresh, expiresAt, err := traeExchange(ctx, p.Client, apiOrigins, exchangePath, refreshToken, cloudideToken, userAgent)
			if err != nil {
				return Result{}, err
			}
			if newRefresh == "" {
				newRefresh = refreshToken
			}
			expiry := p.now().Add(tokenLifetime)
			if !expiresAt.IsZero() {
				expiry = expiresAt
			}
			userInfo := traeUserInfo(ctx, p.Client, apiOrigins, userInfoPath, accessToken, userAgent)
			region := userInfo.AIRegion
			if region == "" {
				region = "US-East"
			}
			return Result{
				AuthKind:    credentials.AuthOAuth,
				Identity:    userInfo.Email,
				DisplayName: userInfo.Name,
				Secret: credentials.Secret{
					AccessToken:  accessToken,
					RefreshToken: newRefresh,
					Expiry:       expiry,
					ProviderData: map[string]string{
						"authMethod": "oauth",
						"aiRegion":   region,
						"region":     userInfo.Region,
						"tenant":     userInfo.Tenant,
						"userId":     userInfo.UserID,
						"scope":      traeScope(region),
					},
				},
			}, nil
		},
	}
}

// traeLoginHost asks GetLoginGuidance which host serves this login, trying each
// guidance endpoint in order.
func traeLoginHost(ctx context.Context, client HTTPClient, endpoints []string, traceID, userAgent string) (string, error) {
	body := fmt.Sprintf(`{"loginTraceID":%q,"login_trace_id":%q}`, traceID, traceID)
	for _, endpoint := range endpoints {
		var document map[string]any
		status, err := postJSONTargetWithHeaders(ctx, client, endpoint, map[string]string{
			"User-Agent": userAgent,
		}, body, &document)
		if err != nil || status < 200 || status >= 300 {
			continue
		}
		for _, path := range [][]string{
			{"Result", "LoginHost"}, {"Result", "loginHost"}, {"Result", "LoginURL"},
			{"result", "loginHost"}, {"data", "Result", "LoginHost"}, {"data", "loginHost"},
			{"LoginHost"}, {"loginHost"},
		} {
			if value := jsonPath(document, path); value != "" {
				if strings.HasPrefix(value, "http") {
					parsed, err := url.Parse(value)
					if err == nil {
						return parsed.Scheme + "://" + parsed.Host, nil
					}
					continue
				}
				return "https://" + strings.TrimLeft(value, "/"), nil
			}
		}
	}
	return "", fmt.Errorf("trae login guidance was unavailable")
}

// traeExchange trades the refresh token for an access token, trying each allowed
// origin because the regional hosts are interchangeable.
func traeExchange(ctx context.Context, client HTTPClient, origins []string, path, refreshToken, cloudideToken, userAgent string) (string, string, time.Time, error) {
	body, err := json.Marshal(map[string]string{
		"ClientID":     "ono9krqynydwx5",
		"RefreshToken": refreshToken,
		"ClientSecret": "-",
		"UserID":       "",
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	headers := map[string]string{"User-Agent": userAgent}
	if cloudideToken != "" {
		headers["x-cloudide-token"] = cloudideToken
	}
	var lastErr error
	for _, origin := range origins {
		var document map[string]any
		status, err := postJSONTargetWithHeaders(ctx, client, strings.TrimSuffix(origin, "/")+path, headers, string(body), &document)
		if err != nil || status < 200 || status >= 300 {
			lastErr = fmt.Errorf("trae exchange returned HTTP %d", status)
			continue
		}
		access := jsonPath(document, []string{"Result", "AccessToken"})
		if access == "" {
			access = jsonPath(document, []string{"Result", "accessToken"})
		}
		if access == "" {
			lastErr = fmt.Errorf("trae exchange returned no access token")
			continue
		}
		refresh := jsonPath(document, []string{"Result", "RefreshToken"})
		expires := jsonPath(document, []string{"Result", "ExpiresAt"})
		return access, refresh, resetAtValue(expires), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("trae exchange was unavailable")
	}
	return "", "", time.Time{}, lastErr
}

// traeUserInfo reads the identity fields the SOLO request scope needs.
func traeUserInfo(ctx context.Context, client HTTPClient, origins []string, path, accessToken, userAgent string) struct {
	Email    string
	Name     string
	AIRegion string
	Region   string
	Tenant   string
	UserID   string
} {
	type userInfo = struct {
		Email    string
		Name     string
		AIRegion string
		Region   string
		Tenant   string
		UserID   string
	}
	for _, origin := range origins {
		var document map[string]any
		status, err := postJSONTargetWithHeaders(ctx, client, strings.TrimSuffix(origin, "/")+path, map[string]string{
			"User-Agent":       userAgent,
			"x-cloudide-token": accessToken,
		}, "{}", &document)
		if err != nil || status < 200 || status >= 300 {
			continue
		}
		return userInfo{
			Email:    firstPath(document, [][]string{{"Result", "NonPlainTextEmail"}, {"Result", "Email"}, {"email"}}),
			Name:     firstPath(document, [][]string{{"Result", "ScreenName"}, {"Result", "Nickname"}, {"nickname"}}),
			AIRegion: firstPath(document, [][]string{{"Result", "AIRegion"}, {"Result", "aiRegion"}, {"aiRegion"}}),
			Region:   firstPath(document, [][]string{{"Result", "Region"}, {"region"}}),
			Tenant:   firstPath(document, [][]string{{"Result", "TenantID"}, {"tenantId"}}),
			UserID:   firstPath(document, [][]string{{"Result", "UserID"}, {"userId"}}),
		}
	}
	return userInfo{}
}

// traeScope maps the account region to the SOLO request scope.
func traeScope(region string) string {
	normalized := strings.ToLower(region)
	switch {
	case normalized == "sg" || strings.Contains(normalized, "singapore"):
		return "marscode-sg"
	case normalized == "cn" || strings.Contains(normalized, "china"):
		return "marscode-cn"
	default:
		return "marscode-us"
	}
}

// jsonPath walks a decoded document.
func jsonPath(root map[string]any, path []string) string {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	switch value := current.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return fmt.Sprintf("%.0f", value)
	}
	return ""
}

func firstPath(root map[string]any, paths [][]string) string {
	for _, path := range paths {
		if value := jsonPath(root, path); value != "" {
			return value
		}
	}
	return ""
}

// windsurfFlow mirrors src/lib/oauth/providers/windsurf.js: an implicit flow
// whose callback carries a Firebase JWT, exchanged for a seat API key.
//
// Both the browser path and the paste path are kept: the browser is the normal
// route and the paste is the fallback when the local callback cannot load.
func windsurfFlow() Flow {
	const (
		clientID       = "3GUryQ7ldAeKEuD2obYnppsnmj58eP5u"
		authBase       = "https://www.windsurf.com"
		signInPath     = "/windsurf/signin"
		registerBase   = "https://register.windsurf.com"
		registerPath   = "/exa.seat_management_pb.SeatManagementService/RegisterUser"
		defaultAPIBase = "https://server.codeium.com"
		userAgent      = "antigravity-cockpit-tools"
	)
	return Flow{
		Provider:     "windsurf",
		Type:         FlowAuthorizationCode,
		Completion:   CompletionCallback,
		CallbackPath: "/windsurf-auth-callback",
		Authorize: func(_ context.Context, p Params) (AuthRequest, error) {
			return pkceAuthorize(authBase+signInPath, url.Values{
				"response_type":            {"token"},
				"client_id":                {clientID},
				"redirect_uri":             {p.RedirectURI},
				"state":                    {p.State},
				"prompt":                   {"login"},
				"redirect_parameters_type": {"query"},
				"workflow":                 {"onboarding"},
			})
		},
		Exchange: func(ctx context.Context, p Params) (Result, error) {
			trimmed := strings.TrimSpace(p.Input)
			// Paste-token mode: either the seat API key itself or a Firebase JWT.
			if !strings.Contains(trimmed, "?") && !strings.Contains(trimmed, "access_token=") {
				clean := strings.TrimPrefix(trimmed, "Bearer ")
				if strings.HasPrefix(clean, "sk-ws-") {
					return Result{
						AuthKind: credentials.AuthAPIKey,
						Secret: credentials.Secret{
							AccessToken: clean,
							ProviderData: map[string]string{
								"authMethod":   "imported",
								"apiServerUrl": defaultAPIBase,
							},
						},
					}, nil
				}
				apiKey, serverURL, err := windsurfRegister(ctx, p.Client, registerBase+registerPath, clean, userAgent)
				if err != nil {
					return Result{}, err
				}
				return Result{
					AuthKind: credentials.AuthAPIKey,
					Secret: credentials.Secret{
						AccessToken: apiKey,
						ProviderData: map[string]string{
							"authMethod":      "imported",
							"apiServerUrl":    serverURL,
							"firebaseIdToken": clean,
						},
					},
				}, nil
			}
			firebase := callbackParam(trimmed, "access_token")
			if firebase == "" {
				firebase = callbackParam(trimmed, "token")
			}
			if firebase == "" {
				return Result{}, fmt.Errorf("windsurf callback carried no token")
			}
			apiKey, serverURL, err := windsurfRegister(ctx, p.Client, registerBase+registerPath, firebase, userAgent)
			if err != nil {
				return Result{}, err
			}
			providerData := map[string]string{
				"authMethod":   "oauth",
				"apiServerUrl": serverURL,
			}
			identity := ""
			var user struct {
				Email string `json:"email"`
				Name  string `json:"name"`
			}
			if err := getJSON(ctx, p.Client, serverURL+"/exa.seat_management_pb.SeatManagementService/GetCurrentUser", map[string]string{
				"Authorization": "Bearer " + firebase,
			}, &user); err == nil {
				identity = user.Email
			}
			return Result{
				AuthKind: credentials.AuthAPIKey,
				Identity: identity,
				Secret: credentials.Secret{
					AccessToken:  apiKey,
					ProviderData: providerData,
				},
			}, nil
		},
	}
}

// windsurfRegister trades the Firebase JWT for a seat API key, which is the
// credential inference actually uses.
func windsurfRegister(ctx context.Context, client HTTPClient, endpoint, firebaseIDToken, userAgent string) (string, string, error) {
	body, err := json.Marshal(map[string]string{"firebase_id_token": firebaseIDToken})
	if err != nil {
		return "", "", err
	}
	var document map[string]any
	status, err := postJSONTargetWithHeaders(ctx, client, endpoint, map[string]string{
		"User-Agent": userAgent,
	}, string(body), &document)
	if err != nil || status < 200 || status >= 300 {
		return "", "", fmt.Errorf("windsurf registration failed")
	}
	apiKey := jsonPath(document, []string{"apiKey"})
	if apiKey == "" {
		apiKey = jsonPath(document, []string{"api_key"})
	}
	if apiKey == "" {
		return "", "", fmt.Errorf("windsurf registration returned no API key")
	}
	serverURL := jsonPath(document, []string{"apiServerUrl"})
	if serverURL == "" {
		serverURL = jsonPath(document, []string{"api_server_url"})
	}
	if serverURL == "" {
		serverURL = "https://server.codeium.com"
	}
	return apiKey, serverURL, nil
}
