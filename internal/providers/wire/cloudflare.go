package wire

import (
	"errors"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudflare"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// cloudflareTarget: Workers AI addresses the account in the path. Chat posts to
// {base}/{accountId}/ai/v1/chat/completions and the typed Clef models (System
// One) to {base}/{accountId}/ai/run/{model} (open-sse/providers/registry/
// cloudflare-ai.js:26,30). The base is the catalog's
// https://api.cloudflare.com/client/v4/accounts; the pin's templated baseUrl
// puts {accountId} inside it, and substitutes it from the connection's
// providerSpecificData.accountId, throwing when it is absent
// (executors/default.js:158-161). Auth is the default Bearer
// (default.js resolveAuthDescriptor), so the credential needs no hook.
//
// Divergences from the pin, which only checks the account id is non-empty and
// splices it and the model into the URL raw: the account id must be a plain
// identifier (cloudflare.ValidAccountID), and each model is served on one wire
// only (Clef on System One, every other model on Chat), the same split the pin
// draws with systemOneTransport and transport. A missing or unusable account id
// is refused naming the setting; no URL is ever invented.
func cloudflareTarget(provider routing.ProviderRef) (Target, error) {
	account := strings.TrimSpace(provider.Connection.AccountID)
	if account == "" {
		return Target{}, errors.New("cloudflare-ai connection has no account ID: add the Cloudflare Account ID to the connection (Providers > New connection) or set accountId when creating it through the API")
	}
	if !cloudflare.ValidAccountID(account) {
		return Target{}, errors.New("cloudflare-ai account ID is not valid")
	}
	typed := provider.UpstreamModel == cloudflare.Clef || provider.UpstreamModel == cloudflare.ClefFlash
	switch {
	case provider.Protocol == systemone.Protocol && typed:
		return Target{Endpoint: account + "/ai/run/" + provider.UpstreamModel}, nil
	case provider.Protocol == openai.ChatProtocol && !typed:
		return Target{Endpoint: account + "/ai/v1/chat/completions"}, nil
	}
	return Target{}, errors.New("cloudflare-ai model " + provider.UpstreamModel + " is not served on the " + provider.Protocol + " wire: Clef models use System One, every other model Chat Completions")
}
