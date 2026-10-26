Fixtures contain no credentials. Provenance is oracle commit `1c3b9f47`:

- `grok-cli.json`: `tests/unit/grok-cli-usage.test.js:38-51`, ACTIVE_BILLING.
- `kenari.json`: `open-sse/services/usage/kenari.js:36-45`, plan/window field shape with synthetic names, amounts and reset dates.

Tests pass these upstream envelopes directly to the production readers.

`groq.json` is pinned to oracle commit `264a26d0`: the header block is `tests/unit/groq-usage.test.js:24-31` (RATE_LIMIT_HEADERS) and the body is the `{ data: [] }` models envelope from the same test, both read by `open-sse/services/usage/groq.js:44-62,102-129`. Groq reports quota only in headers, so the fixture carries status, headers and body together.

`deepseek.json` is pinned to oracle commit `264a26d0`: it is ACTIVE_BALANCE from `tests/unit/deepseek-usage.test.js:24-40` (positive USD beside zero CNY), the `/user/balance` envelope read by `open-sse/services/usage/deepseek.js:11-37,77-108`.
