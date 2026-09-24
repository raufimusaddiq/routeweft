# compat/fixtures

Deterministic wire fixtures — the compatibility oracle for Routeweft ingress and
provider adapters (SPEC §9–10, §28.3; PRD §5).

Each `*.json` file is one recorded exchange:

```json
{
  "description": "what this exchange proves",
  "request": {
    "method": "POST",
    "path": "/v1/chat/completions",
    "headers": {"content-type": "application/json"},
    "body": "<raw request bytes as a JSON string or file reference>"
  },
  "response": {
    "status": 200,
    "headers": {"content-type": "text/event-stream"},
    "body": "<raw response bytes>"
  },
  "usage": {"inputTokens": 1, "outputTokens": 2},
  "terminal": "[DONE]"
}
```

Requests/responses are raw bytes, not re-encoded JSON, so adapters can prove they
preserve unknown forward-compatible fields and stream framing exactly.

Fixtures are loaded by `compat/fixtures.Load` and replayed against the mock
upstream in `compat/mockupstream`.

> Fixtures must contain synthetic content only. Never record real prompts,
> credentials, cookies, or provider responses here.
