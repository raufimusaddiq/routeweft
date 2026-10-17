package cloudflare

import (
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
)

const (
	Clef      = "@cf/cloudflare/clef"
	ClefFlash = "@cf/cloudflare/clef-flash"
)

func ValidAccountID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Endpoint keeps typed Clef models out of the chat transport and vice versa.
func Endpoint(protocol, accountID, model string) (string, bool) {
	if !ValidAccountID(accountID) {
		return "", false
	}
	typed := model == Clef || model == ClefFlash
	switch protocol {
	case systemone.Protocol:
		if typed {
			return accountID + "/ai/run/" + model, true
		}
	case openai.ChatProtocol:
		if !typed {
			return accountID + "/ai/v1/chat/completions", true
		}
	}
	return "", false
}
