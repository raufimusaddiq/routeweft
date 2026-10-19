package codex

import (
	"strings"
)

// CLIVersion is the Codex CLI version Codex identifies as upstream. It is kept
// separate from the OAuth endpoints and pinned to the PROVIDER_BASELINE
// reference snapshot value.
const CLIVersion = "0.154.0"

// Originator labels Codex traffic to the ChatGPT backend.
const Originator = "codex_cli_rs"

// ReviewSuffix marks a Codex review-quota model variant.
const ReviewSuffix = "-review"

// AutoReviewModel is Codex CLI's virtual review model. Unlike derived review
// variants it is forwarded verbatim, so it must never have ReviewSuffix
// stripped.
const AutoReviewModel = "codex-auto-review"

// Headers returns the Codex CLI identity headers required on Codex requests.
func Headers() map[string]string {
	return map[string]string{
		"originator": Originator,
		"User-Agent": Originator + "/" + CLIVersion,
	}
}

// UpstreamModel maps a Routeweft Codex model id to the id Codex sends upstream.
// Review variants collapse to their base model for the review quota family,
// except the virtual auto-review model which is forwarded verbatim.
func UpstreamModel(model string) string {
	if model == AutoReviewModel {
		return model
	}
	return strings.TrimSuffix(model, ReviewSuffix)
}

// IsReviewModel reports whether a model consumes the Codex review quota family.
func IsReviewModel(model string) bool {
	return model == AutoReviewModel || strings.HasSuffix(model, ReviewSuffix)
}
