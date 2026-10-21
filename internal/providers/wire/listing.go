package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"

	clineprovider "github.com/raufimusaddiq/routeweft/internal/providers/cline"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Listing is how one provider's model listing differs from the shared bearer
// GET {base}/models. The reference lists models per provider in
// src/app/api/providers/[id]/models/route.js (PROVIDER_MODELS_CONFIG, cited per
// entry below) and in open-sse/services/*Models.js; a provider with no entry here
// is listed the shared way and is not hidden for lacking one.
//
// Nothing in a Listing comes from the client: the query and body are the
// provider's own constants and the connection's own settings. The reference
// route reads no client query at all (route.js:512 takes only the connection
// id), so a client's query is not forwarded upstream.
type Listing struct {
	// Method defaults to GET.
	Method string
	// URL replaces the route base and path when the reference lists from a host
	// that is not the serving host. It is validated by the caller's SSRF policy.
	URL string
	// Path replaces the catalog's DiscoveryPath under the route base.
	Path string
	// Query is the provider's fixed query, for example a client version gate.
	Query string
	// Headers are the provider's listing identity headers, applied over the
	// shared ones.
	Headers map[string]string
	// Body is the request body of a POST listing.
	Body []byte
	// OwnAuth means the credential travels in Headers (or in the signed headers),
	// so the caller must not add its own bearer or key header.
	OwnAuth bool
	// Signed means the headers come from the provider's RequestHeaders hook, over
	// the final URL and body.
	Signed bool
	// Override is applied after signing; an empty value removes the header.
	Override map[string]string
	// Shape names how the response is read.
	Shape Shape
}

// ListingError is a provider listing Routeweft refuses to make, with the reason.
// It is a tracked gap or a bad setting, never a hidden provider.
type ListingError struct {
	Err error
	// Unsupported marks a tracked gap (not a bad setting or an upstream failure).
	Unsupported bool
}

func (e *ListingError) Error() string { return e.Err.Error() }
func (e *ListingError) Unwrap() error { return e.Err }

func listingError(format string, args ...any) error {
	return &ListingError{Err: fmt.Errorf(format, args...)}
}

// listings maps a built-in provider to its listing request. Every entry cites the
// reference line it reproduces.
var listings = map[string]func(routing.ProviderRef) (Listing, error){
	// Perplexity is listed at /v1/models (route.js:247); its serving base has no /v1.
	"perplexity":  func(routing.ProviderRef) (Listing, error) { return Listing{Path: "v1/models"}, nil },
	"codex":       codexListing,
	"gemini-cli":  geminiCLIListing,
	"antigravity": antigravityListing,
	"github":      func(routing.ProviderRef) (Listing, error) { return Listing{Shape: ShapeGitHub}, nil },
	"kimchi":      kimchiListing,
	"kiro":        kiroListing,
	"cline":       clineListing,
	"clinepass":   clineListing,
	"grok-cli":    grokCLIListing,
	"qoder":       qoderListing,
	"cursor":      cursorListing,
}

// ListingFor reports the provider's own listing request. ok is false for a
// provider listed the shared way. The provider's APIToken must be set: a few
// listings (Cline, the signed ones) put the credential in their headers.
func ListingFor(provider routing.ProviderRef) (listing Listing, ok bool, err error) {
	build, ok := listings[provider.ProviderID]
	if !ok {
		return Listing{}, false, nil
	}
	listing, err = build(provider)
	return listing, true, err
}

// Codex (route.js:19-24,149-164). The endpoint gates each entry by
// minimal_client_version against client_version, so an old one comes back 200
// with newer models silently missing. The reference sends 0.144.6; Routeweft
// identifies as codexprovider.CLIVersion upstream, which is newer, and a newer
// version only widens the gate.
func codexListing(provider routing.ProviderRef) (Listing, error) {
	return Listing{
		Query: "client_version=" + url.QueryEscape(codexprovider.CLIVersion),
		Shape: ShapeCodex,
	}, nil
}

const (
	geminiCLIModelsURL   = "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels" // route.js:17
	antigravityModelsURL = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:models" // route.js:166
)

// Gemini CLI (route.js:440-460): POST with the project when the connection has
// one, and the Google client identity headers.
func geminiCLIListing(provider routing.ProviderRef) (Listing, error) {
	body := []byte(`{}`)
	if project := strings.TrimSpace(provider.Connection.Settings.ProjectID); project != "" {
		encoded, err := json.Marshal(map[string]string{"project": project})
		if err != nil {
			return Listing{}, listingError("gemini-cli project could not be encoded")
		}
		body = encoded
	}
	return Listing{
		Method: "POST", URL: geminiCLIModelsURL, Body: body, Shape: ShapeGoogleModels,
		Headers: map[string]string{
			"User-Agent":        "google-api-nodejs-client/9.15.1",
			"X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
		},
	}, nil
}

// Antigravity (route.js:165-173): POST {} to the sandbox host. The reference
// returns data.models untouched, so the response shape is not proven by it; both
// the array and the keyed-object form Google uses are read.
func antigravityListing(routing.ProviderRef) (Listing, error) {
	return Listing{Method: "POST", URL: antigravityModelsURL, Body: []byte(`{}`), Shape: ShapeGoogleModels}, nil
}

const (
	kimchiModelsURL = "https://llm.kimchi.dev/v1/models/metadata" // kimchiModels.js:22-24
	kimchiUserAgent = "kimchi/0.1.40"                             // kimchiModels.js:6
)

// Kimchi lists a metadata catalog, filtered server-side to the models the CLI
// shows: include_in_cli=true (kimchiModels.js:22-24,106-135). Serving uses
// /openai/v1, which has no such listing.
func kimchiListing(routing.ProviderRef) (Listing, error) {
	return Listing{URL: kimchiModelsURL, Query: "include_in_cli=true", Headers: map[string]string{"User-Agent": kimchiUserAgent}, Shape: ShapeKimchi}, nil
}

// Kiro (kiroModels.js:55-60,67-90,159-199): ListAvailableModels on the region of the
// profile ARN, origin=AI_EDITOR and the profile ARN as query.
func kiroListing(provider routing.ProviderRef) (Listing, error) {
	arn := strings.TrimSpace(provider.Connection.Settings.ProfileARN)
	region := "us-east-1"
	if parts := strings.Split(arn, ":"); len(parts) >= 4 && parts[3] != "" {
		region = parts[3]
	}
	// The region becomes part of a host name: only an AWS region name may.
	if !kiroRegion.MatchString(region) {
		return Listing{}, listingError("kiro profile ARN names an invalid region %q", region)
	}
	query := url.Values{"origin": {"AI_EDITOR"}}
	if arn != "" {
		query.Set("profileArn", arn)
	}
	seed := firstNonEmpty(arn, provider.APIToken, "kiro-anonymous")
	sum := sha256.Sum256([]byte(seed))
	machine := hex.EncodeToString(sum[:])
	const sdk, version = "1.0.0", "0.10.32"
	return Listing{
		URL:   "https://q." + region + ".amazonaws.com/ListAvailableModels",
		Query: query.Encode(),
		Headers: map[string]string{
			"User-Agent": "aws-sdk-js/" + sdk + " ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/codewhispererruntime#" + sdk +
				" m/N,E KiroIDE-" + version + "-" + machine,
			"x-amz-user-agent":            "aws-sdk-js/" + sdk + " KiroIDE-" + version + "-" + machine,
			"x-amzn-kiro-agent-mode":      "vibe",
			"x-amzn-codewhisperer-optout": "true",
			"amz-sdk-request":             "attempt=1; max=1",
			"amz-sdk-invocation-id":       kiroInvocationID(machine),
		},
		Shape: ShapeKiro,
	}, nil
}

// authKindAPIKey is credentials.AuthAPIKey as ConnectionMetadata carries it.
const authKindAPIKey = "api-key"

// Cline and ClinePass share api.cline.bot/api/v1/models (clinepassModels.js:11-94).
// An API key is a plain bearer; an OAuth access token is a WorkOS JWT and must
// carry the workos: prefix, which the shared bearer does not add. ClinePass keeps
// only the cline-pass/ models.
func clineListing(provider routing.ProviderRef) (Listing, error) {
	shape := ShapeCline
	if provider.ProviderID == "clinepass" {
		shape = ShapeClinePass
	}
	token := strings.TrimSpace(provider.APIToken)
	if token == "" {
		return Listing{Shape: shape}, nil
	}
	headers := map[string]string{"Accept": "application/json", "Authorization": "Bearer " + token}
	if provider.Connection.AuthKind != authKindAPIKey {
		headers = clineprovider.Headers(token)
		headers["Accept"] = "application/json"
	}
	return Listing{Headers: headers, OwnAuth: true, Shape: shape}, nil
}

// Grok CLI (grokCliModels.js:11,56-70; config/grokCli.js:1-9): /v1/models under
// the cli-chat-proxy, with the grok-shell identity (older ones get HTTP 426) and
// the account's email and user id.
const grokCLIVersion = "1.0.44"

func grokCLIListing(provider routing.ProviderRef) (Listing, error) {
	settings := provider.Connection.Settings
	headers := map[string]string{
		"User-Agent":               "grok-shell/" + grokCLIVersion + " (linux; x86_64)",
		"x-xai-token-auth":         "xai-grok-cli",
		"x-grok-client-version":    grokCLIVersion,
		"x-grok-client-identifier": "grok-shell",
		"x-grok-client-mode":       "headless",
	}
	if settings.Email != "" {
		headers["x-email"] = settings.Email
	}
	if settings.UserID != "" {
		headers["x-userid"] = settings.UserID
	}
	return Listing{Path: "v1/models", Headers: headers, Shape: ShapeGrokCLI}, nil
}

const (
	qoderModelListURL    = "https://api3.qoder.sh/algo/api/v2/model/list" // qoder/constants.js:32
	qoderModelListAltURL = "https://api2.qoder.sh/algo/api/v2/model/list" // constants.js:15; job tokens
)

// Qoder (qoderModels.js:195-262): a COSY-signed GET. A job token (jt-) is served
// from api2, where api3 answers "Login expired". A personal access token (pt-) is
// exchanged for a job token first (qoderModels.js:130-145); Routeweft does not make
// that exchange yet, so it says so instead of listing with a token the gateway
// does not accept.
func qoderListing(provider routing.ProviderRef) (Listing, error) {
	token := strings.TrimSpace(provider.APIToken)
	if strings.HasPrefix(token, "pt-") {
		return Listing{}, &ListingError{Err: errors.New("qoder personal access tokens need the job-token exchange before listing, which is not implemented"), Unsupported: true}
	}
	listing := Listing{URL: qoderModelListURL, OwnAuth: true, Signed: true, Shape: ShapeQoder,
		Headers: map[string]string{"Accept": "application/json", "Accept-Encoding": "identity"}}
	if strings.HasPrefix(token, "jt-") {
		listing.URL = qoderModelListAltURL
	}
	return listing, nil
}

// Cursor (cursorModels.js:146-170; registry/cursor.js:47-49): the AgentService
// GetUsableModels unary RPC with an empty protobuf body, signed like a Run. It is
// not a streaming Connect call, so the Connect framing headers are dropped.
func cursorListing(routing.ProviderRef) (Listing, error) {
	return Listing{
		Method: "POST", URL: cursorAgentBase + "/agent.v1.AgentService/GetUsableModels", OwnAuth: true, Signed: true, Shape: ShapeCursor,
		Override: map[string]string{
			"Accept": "application/proto", "Content-Type": "application/proto",
			"Connect-Accept-Encoding": "", "Connect-Protocol-Version": "",
		},
	}, nil
}

// ListingURL is the one place a model-listing URL is assembled. validate is the
// caller's outbound policy. absolute is a provider's own listing URL; otherwise
// path (default "models") goes under base. A query on either is dropped and only
// the provider's own query is set.
func ListingURL(validate func(string) (*url.URL, error), base, absolute, path, query string) (string, error) {
	if absolute != "" {
		target, err := validate(absolute)
		if err != nil {
			return "", err
		}
		target.RawQuery, target.Fragment = query, ""
		return target.String(), nil
	}
	target, err := validate(base)
	if err != nil {
		return "", err
	}
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		path = "models"
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("discovery path must not traverse paths")
		}
	}
	target.Path = strings.TrimRight(target.Path, "/")
	switch {
	case path == "models" && strings.HasSuffix(target.Path, "/models"):
	case strings.HasSuffix(target.Path, "/v1") && strings.HasPrefix(path, "v1/"):
		// The base already carries the version the path names.
		target.Path += "/" + strings.TrimPrefix(path, "v1/")
	default:
		target.Path += "/" + path
	}
	target.RawPath, target.RawQuery, target.Fragment = "", query, ""
	return target.String(), nil
}

// kiroInvocationID is a stable id per account (the reference uses a fresh uuid per
// call; the header only has to be a uuid), so a listing is reproducible.
func kiroInvocationID(machine string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("kiro-listing:"+machine)).String()
}
