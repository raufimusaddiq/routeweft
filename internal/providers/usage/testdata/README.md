Fixtures contain no credentials. Provenance is oracle commit `1c3b9f47`:

- `grok-cli.json`: `tests/unit/grok-cli-usage.test.js:38-51`, ACTIVE_BILLING.
- `kenari.json`: `open-sse/services/usage/kenari.js:36-45`, plan/window field shape with synthetic names, amounts and reset dates.

Tests pass these upstream envelopes directly to the production readers.

`groq.json` is pinned to oracle commit `264a26d0`: the header block is `tests/unit/groq-usage.test.js:24-31` (RATE_LIMIT_HEADERS) and the body is the `{ data: [] }` models envelope from the same test, both read by `open-sse/services/usage/groq.js:44-62,102-129`. Groq reports quota only in headers, so the fixture carries status, headers and body together.

`deepseek.json` is pinned to oracle commit `264a26d0`: it is ACTIVE_BALANCE from `tests/unit/deepseek-usage.test.js:24-40` (positive USD beside zero CNY), the `/user/balance` envelope read by `open-sse/services/usage/deepseek.js:11-37,77-108`.

`antigravity.json` and `gemini-cli.json` are pinned to oracle commit `264a26d0` and keyed by the Cloud Code method each body answers:

- `antigravity.json`: `loadCodeAssist` is the paid-tier subscription from `tests/unit/antigravity-weekly-quota.test.js:301-305`; `fetchAvailableModels` extends the model map at `:311-318` with the `claude-opus-4-6-thinking` and `gemini-pro-agent` rows from `tests/unit/antigravity-weekly-dashboard.test.js:7-20` (the latter without `displayName`, to exercise the `info.displayName || modelKey` fallback at `open-sse/services/usage/google.js:218`) and two synthetic rows, an internal model and one outside the allowlist, that `google.js:192-201` drops; `retrieveUserQuotaSummary` is FULL_RESPONSE from `antigravity-weekly-quota.test.js:16-46`.
- `gemini-cli.json`: `retrieveUserQuota` is the bucket envelope from `tests/unit/gemini-usage-projectid.test.js:23-31`; `loadCodeAssist` is the project-object envelope from `:55-58`.

Tests vary these envelopes in place for the zero, empty, expired, multiple-window, auth, timeout and error cases.

`scoped_quota_pin.json` is generated, not hand-written: `compat/parity/generate-scoped-quota.mjs` evaluates the pin `1c3b9f47` Codex, Claude and GitHub Copilot readers (`open-sse/services/usage/{codex,claude,github}.js`) over synthetic upstream answers and records each answer, the pin's result and the requests it sent, with the token redacted (PARITY 6.28).

`regional_quota_pin.json` is generated the same way: `compat/parity/generate-regional-quota.mjs` evaluates the pin `1c3b9f47` GLM, Kimi, MiniMax and CodeBuddy readers (`open-sse/services/usage/{glm,kimi,minimax,codebuddy-cn}.js`) for every region over synthetic upstream answers and records each answer, the pin's result and the requests it sent, with the token redacted (PARITY 6.29).

`account_quota_pin.json` is generated the same way: `compat/parity/generate-account-quota.mjs` evaluates the pin `1c3b9f47` Grok CLI, Kiro, Qoder and Zed readers (`open-sse/services/usage/{grok-cli,grokCliQuotaFrame,kiro,misc,zed}.js`, the Qoder PAT exchange and `shared/zedAuth.js`) and the usage page's normalizer over synthetic upstream answers, and records each answer, the pin's result, the page's rows and the requests it sent, with the connection's credential redacted (PARITY 6.30).

`billing_quota_pin.json` is generated the same way: `compat/parity/generate-billing-quota.mjs` evaluates the pin `1c3b9f47` Command Code, Kenari, OpenCode Go, Ollama, Vercel AI Gateway and Xiaomi MiMo readers (`open-sse/services/usage/{commandcode,kenari,opencode-go,misc,xiaomi-mimo}.js`, `open-sse/shared/mimoAccount.js`) over synthetic upstream answers, including MiMo's SSO exchange and reads in sequence, and records each answer, the pin's result and the requests it sent, with the key redacted (PARITY 6.31).
