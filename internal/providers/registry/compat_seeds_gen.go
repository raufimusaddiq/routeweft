// Code generated from the seed lists Routeweft shipped before the seeds were
// generated from the oracle. DO NOT EDIT.
//
// These IDs were routable on the previous release. Parity is about what Routeweft
// can serve, not about deleting a model an operator already routed to, so every
// one of them stays seeded even when the reference no longer lists it. Without
// this floor an upgrade fails to compile an existing alias or Combo member and the
// install does not boot.
package registry

// compatSeedIDs is the union floor: previously shipped IDs, by provider.
var compatSeedIDs = map[string][]string{
	"anthropic":      {"claude-sonnet-4-20250514", "claude-opus-4-20250514", "claude-3-5-sonnet-20241022"},
	"antigravity":    {"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.1-pro-preview"},
	"claude":         {"claude-opus-5", "claude-fable-5-1", "claude-fable-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"},
	"cline":          {"anthropic/claude-opus-4.7", "anthropic/claude-sonnet-4.6", "anthropic/claude-opus-4.6", "openai/gpt-5.3-codex", "openai/gpt-5.4", "google/gemini-3.1-pro-preview", "google/gemini-3.1-flash-lite-preview", "kwaipilot/kat-coder-pro"},
	"clinepass":      {"cline-pass/glm-5.2", "cline-pass/kimi-k2.7-code", "cline-pass/kimi-k2.6", "cline-pass/deepseek-v4-pro", "cline-pass/deepseek-v4-flash", "cline-pass/mimo-v2.5", "cline-pass/mimo-v2.5-pro", "cline-pass/minimax-m3", "cline-pass/qwen3.7-max", "cline-pass/qwen3.7-plus"},
	"codebuddy-cn":   {"glm-5.2", "glm-5.1", "glm-5v-turbo", "minimax-m3", "kimi-k2.7", "kimi-k2.6", "hy3", "hy4-preview", "glm-5.3", "glm-5.3-flash", "kimi-k3-1", "deepseek-v4-pro", "deepseek-v4.1-flash"},
	"codebuddy-intl": {"glm-5.2", "glm-5.1", "glm-5.0", "glm-5.0-turbo", "glm-5v-turbo", "glm-4.7", "minimax-m3", "minimax-m2.7", "kimi-k2.7", "kimi-k2.6", "kimi-k2.5", "hy3-preview", "deepseek-v4-pro", "deepseek-v4.1-flash", "deepseek-v3-2-volc"},
	"codex":          {"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-sol-review", "gpt-5.6-terra", "gpt-5.6-terra-review", "gpt-5.6-luna", "gpt-5.6-luna-review", "gpt-5.5", "gpt-5.5-review", "gpt-5.4", "gpt-5.4-review", "gpt-5.4-mini", "gpt-5.4-mini-review", "gpt-5.3-codex-spark", "gpt-5.3-codex-spark-review", "codex-auto-review"},
	"commandcode":    {"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash", "moonshotai/Kimi-K2.6", "moonshotai/Kimi-K2.5", "zai-org/GLM-5.1", "zai-org/GLM-5", "MiniMaxAI/MiniMax-M2.7", "MiniMaxAI/MiniMax-M2.5", "Qwen/Qwen3.6-Max-Preview", "Qwen/Qwen3.6-Plus", "stepfun/Step-3.5-Flash"},
	"cursor":         {"default", "claude-4.5-opus-high-thinking", "claude-4.5-opus-high", "claude-4.5-sonnet-thinking", "claude-4.5-sonnet", "claude-4.5-haiku", "claude-4.5-opus", "gpt-5.2-codex", "claude-4.6-opus-max", "claude-4.6-sonnet-medium-thinking", "kimi-k2.5", "gemini-3-flash-preview", "gpt-5.2", "gpt-5.3-codex"},
	"gemini":         {"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite", "gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemma-4-31b-it"},
	"gemini-cli":     {"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-3-flash-preview", "gemini-3.1-flash-lite-preview", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"},
	"github":         {"gpt-5.2", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini", "claude-haiku-4.5", "claude-opus-4.5", "claude-sonnet-4.5", "claude-sonnet-4.6", "claude-opus-4.6", "claude-opus-4.7", "gemini-2.5-pro", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "grok-code-fast-1", "oswe-vscode-prime", "goldeneye-free-auto"},
	"iflow":          {"qwen3-coder-plus", "qwen3-max", "qwen3-vl-plus", "qwen3-max-preview", "qwen3-235b", "qwen3-235b-a22b-instruct", "qwen3-235b-a22b-thinking-2507", "qwen3-32b", "kimi-k2", "deepseek-v3.2", "deepseek-v3.1", "deepseek-v3", "deepseek-r1", "glm-4.7", "iflow-rome-30ba3b"},
	"kimi":           {"kimi-k3", "k3", "kimi-for-coding", "kimi-for-coding-highspeed", "kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.6", "kimi-k2.5", "kimi-k2.5-thinking", "kimi-latest"},
	"kiro":           {"claude-opus-5", "claude-opus-5-thinking", "claude-opus-4.8", "claude-opus-4.7", "claude-sonnet-4.6", "claude-sonnet-4.5"},
	"openai":         {"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "o3", "o3-mini", "o3-pro", "o4-mini", "o1", "o1-mini"},
	"perplexity-web": {"pplx-auto", "pplx-sonar", "pplx-gpt", "pplx-gemini", "pplx-sonnet", "pplx-opus", "pplx-nemotron"},
	"qoder":          {"ultimate", "auto", "performance", "efficient", "lite", "qmodel_38max", "qmodel_latest", "qmodel", "qfmodel", "kmodel_latest", "kmodel", "gmodel", "gfmodel", "dmodel", "dfmodel", "mmodel"},
	"typesafe":       {"jev-latest"},
	"vertex":         {"gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-2.5-flash"},
	"xai":            {"grok-4.6", "grok-4.5", "grok-4", "grok-4-fast-reasoning", "grok-code-fast-1", "grok-3"},
	"xiaomi-mimo":    {"mimo-x-pro-preview", "mimo-x-flash-preview", "mimo-v2.5-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash"},
}
