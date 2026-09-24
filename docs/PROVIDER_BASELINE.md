# Routeweft Provider Baseline

Status: **Bootstrap coverage manifest**

This file defines the initial provider breadth Routeweft must classify. It is intentionally stored in Routeweft so provider coverage does not depend on another repository at implementation time.

The bootstrap list was derived from the active LLM provider registry in LiteRouter around the Routeweft foundation reference window. Subsequent Routeweft changes own this list.

## Disposition values

Each provider must eventually be marked as one of:

- `shared-openai` — implemented primarily by shared OpenAI-compatible protocol/auth machinery;
- `shared-anthropic` — implemented primarily by shared Anthropic-compatible machinery;
- `shared-gemini` — implemented primarily by shared Gemini machinery;
- `specialized` — requires provider-specific auth/wire/quota behavior;
- `native` — first-party/native implementation;
- `deferred-explicitly` — intentionally not in a given release, with reason;
- `removed-by-product-decision` — only through an approved PRD/BDR change.

Do not silently omit a provider because implementation is inconvenient.

## Active bootstrap catalog

| Provider ID | Disposition | Notes |
|---|---|---|
| alicode-intl | TBD | |
| alicode | TBD | |
| anthropic | TBD | |
| antigravity | TBD | |
| azure | TBD | |
| blackbox | TBD | |
| byteplus | TBD | |
| cerebras | TBD | |
| chutes | TBD | |
| claude | TBD | |
| cline | TBD | |
| clinepass | TBD | |
| cloudflare-ai | TBD | |
| codebuddy-cn | TBD | |
| codex | TBD | |
| cohere | TBD | |
| commandcode | TBD | |
| cursor | TBD | |
| deepseek | TBD | |
| featherless | TBD | |
| fireworks | TBD | |
| gemini-cli | TBD | |
| gemini | TBD | |
| github | TBD | |
| gitlab | TBD | |
| glm-cn | TBD | |
| glm | TBD | |
| grok-cli | TBD | |
| grok-web | TBD | |
| groq | TBD | |
| hyperbolic | TBD | |
| iflow | TBD | |
| kilocode | TBD | |
| kimchi | TBD | |
| kimi | TBD | |
| kiro | TBD | |
| mimo-free | TBD | |
| minimax-cn | TBD | |
| minimax | TBD | |
| mistral | TBD | |
| mmf | TBD | |
| nebius | TBD | |
| nvidia | TBD | |
| ollama-local | TBD | |
| ollama | TBD | |
| openai | TBD | |
| opencode-go | TBD | |
| opencode | TBD | |
| openrouter | TBD | |
| perplexity-web | TBD | |
| perplexity | TBD | |
| perplexity-agent | TBD | |
| qoder | TBD | |
| siliconflow | TBD | |
| together | TBD | |
| venice | TBD | |
| vercel-ai-gateway | TBD | |
| vertex-partner | TBD | |
| vertex | TBD | |
| volcengine-ark | TBD | |
| xai | TBD | |
| xiaomi-mimo | TBD | |
| xiaomi-tokenplan | TBD | |
| alims-intl | TBD | |
| codebuddy-intl | TBD | |
| zed | TBD | |
| api-airforce | TBD | |
| baidu | TBD | |
| bazaarlink | TBD | |
| bluesminds | TBD | |
| kilo-gateway | TBD | |
| llm7 | TBD | |
| sambanova | TBD | |
| tencent | TBD | |
| morph | TBD | |
| poolside | TBD | |
| tokenrouter | TBD | |
| alitp-intl | TBD | |
| kenari | TBD | |
| typesafe | TBD | |

Bootstrap active count: **80**.

## Intentionally hidden/bootstrap-reference providers

These are not automatically user-visible Routeweft providers:

| Provider ID | Status |
|---|---|
| trae | hidden/reference only |
| devin-cli | hidden/reference only |
| windsurf | hidden/reference only |

They become user-visible only through an explicit Routeweft product decision.

## Provider acceptance checklist

A provider marked implemented must account for the applicable items:

- registry metadata;
- source/target protocol binding;
- API-key/OAuth/cookie/PAT/no-auth behavior;
- credential identity/dedup;
- rotating refresh-token semantics;
- model discovery;
- suggested models;
- provider-specific model validation;
- quota/reset parsing;
- error classification;
- proxy behavior;
- request header filtering;
- Usage extraction;
- prompt-cache quirks;
- request cancellation;
- tests/fixtures.

Shared adapters should be preferred when provider-specific behavior does not justify duplicate execution logic.

## Release policy

A release may intentionally ship before all 80 providers are implemented, but:

1. every provider remains classified;
2. release notes state which provider dispositions are production-ready;
3. providers required by the operator's daily-drive deployment must be green before that deployment is cut over;
4. broad-provider GA requires no unexplained `TBD` rows.
