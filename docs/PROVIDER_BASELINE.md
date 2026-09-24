# Routeweft Built-in Provider Requirements

Status: **Implementation-ready product requirement**

Reference behavior source: LiteRouter `main` at `2ffb7922954112b30425cd487d686758e519397e`.

This document is Routeweft-owned. LiteRouter is provenance/behavioral reference only; Routeweft has no runtime dependency on it.

## 1. What "built-in / first-class" means

A Routeweft built-in provider:

- appears in the Providers catalog without requiring the operator to create a Generic Provider;
- exposes its supported connection/auth modes;
- uses the correct native/shared wire adapter;
- preserves provider-specific auth refresh, model discovery, quota/usage, headers, reset semantics and quirks where applicable;
- supports multiple Routeweft connections/accounts when the provider model allows it;
- participates in provider strategies, fallback, cooldown, quota-aware routing, aliases, Combo and Usage;
- can be configured/tested from the control plane.

"First-class" does **not** mean every provider gets a unique executor. Most providers should be declarative `ProviderSpec` entries backed by shared protocol adapters.

## 2. Transport class legend

- **OA-chat** — shared OpenAI Chat Completions adapter.
- **OA-responses** — shared OpenAI Responses adapter.
- **Anthropic** — shared Anthropic Messages adapter.
- **Gemini** — shared/native Gemini GenerateContent adapter.
- **Ollama** — shared Ollama chat adapter.
- **SystemOne** — Routeweft System One adapter.
- **Multi** — provider advertises several native LLM transports; choose the source-matching transport before translating.
- **Specialized** — provider requires a provider-specific wire adapter in addition to shared infrastructure.

Authentication is orthogonal to transport. A provider can reuse a shared protocol adapter while retaining specialized OAuth/import/quota code.

## 3. Initial built-in catalog — 80 active providers

| Provider | Routeweft transport class | Connection/auth | Model catalog | Usage/quota | Required provider-specific behavior |
|---|---|---|---|---|---|
| alicode-intl | OA-chat | API key | Static | — | Alibaba Coding Plan international; retain provider quirks/cache-control behavior. |
| alicode | OA-chat | API key | Static | — | Alibaba Coding Plan China; retain provider quirks/cache-control behavior. |
| anthropic | Anthropic | API key | Static | — | First-party Messages provider; Anthropic headers/beta behavior. |
| antigravity | Specialized | OAuth | Static | Usage/quota | Native Antigravity wire format, Google OAuth, per-model live quota/reset semantics. |
| azure | OA-chat | API key | Static | — | Azure-specific endpoint/auth configuration over shared OpenAI request semantics. |
| blackbox | OA-chat | API key | Static | — | Built-in first-class provider. |
| byteplus | OA-chat | Free-tier/API key | Static | — | Built-in free-tier provider. |
| cerebras | OA-chat | API key | Static | — | Retain provider-specific quirks. |
| chutes | OA-chat | API key | Static | — | Built-in first-class provider. |
| claude | Anthropic | OAuth | Static | Usage/quota | Claude OAuth, refresh, provider-specific headers/quirks and quota. |
| cline | OA-chat | OAuth | Static | — | Cline OAuth plus cline-envelope request behavior. |
| clinepass | OA-chat | API key + OAuth | Static | — | Dual-auth Cline pass workflow plus cline-envelope behavior. |
| cloudflare-ai | OA-chat | API key/free-tier | Static | — | Built-in Cloudflare AI provider. |
| codebuddy-cn | OA-chat | API key + OAuth | Static | Usage/quota | Tencent CodeBuddy CN auth/refresh and usage workflow. |
| codex | OA-responses | OAuth | Static | Usage/quota | Codex OAuth/PKCE, CLI fingerprint headers, review-model mapping, rotating refresh and usage/reset credits. |
| cohere | OA-chat | API key | Static | — | OpenAI-compatible surface used as built-in provider. |
| commandcode | Specialized | API key | Static | Usage | CommandCode custom wire format and usage extraction. |
| cursor | Specialized | OAuth | Static | — | Cursor-specific transport/auth and current import/auto-import workflows. |
| deepseek | Multi: chat+messages | API key | Static | Usage | Native Chat + Anthropic Messages transports; retain provider quirks. |
| featherless | OA-chat | API key | Static | — | Built-in first-class provider. |
| fireworks | OA-chat | API key | Static | — | Built-in first-class provider. |
| gemini-cli | Specialized | OAuth | Static | Usage/quota | Google OAuth and Gemini CLI internal wire format/quota. |
| gemini | Gemini | API key | Static | — | Native Gemini GenerateContent transport; current API-key flow. |
| github | Multi: chat+responses | OAuth | Static | Usage/quota | GitHub Copilot OAuth, native Chat/Responses endpoints and usage/reset behavior. |
| gitlab | OA-chat | OAuth/PAT | Static | — | Retain GitLab PAT helper/current credential workflow. |
| glm-cn | OA-chat | API key | Static | Usage | China GLM endpoint and usage. |
| glm | Multi: chat+messages | API key | Static | Usage | OpenAI Chat + Anthropic Messages native transports. |
| grok-cli | OA-responses | OAuth | Static | Usage/quota | xAI/Grok CLI OAuth refresh, Responses transport and usage. |
| grok-web | Specialized | Cookie/session | Passthrough | — | Web-cookie transport; arbitrary current model IDs remain routable. |
| groq | OA-chat | API key | Static | Usage | OpenAI-compatible transport plus usage. |
| hyperbolic | OA-chat | API key | Static | — | Built-in first-class provider. |
| iflow | OA-chat | OAuth | Static | — | iFlow OAuth plus current cookie/import helper workflow. |
| kilocode | OA-chat | OAuth | Dynamic + passthrough | — | Kilo model discovery through gateway/OpenRouter-style catalog. |
| kimchi | OA-chat | API key + OAuth | Passthrough | — | Dual auth and passthrough model IDs. |
| kimi | Multi: chat+messages | API key + OAuth | Static | Usage | Kimi native Chat + Messages transports, OAuth refresh and header hooks. |
| kiro | Specialized | OAuth/API key/imports | Static | Usage/quota | Kiro runtime wire format, device/social auth, API-key/import/auto-import/CLI-proxy flows. |
| mimo-free | OA-chat | No auth | Dynamic + passthrough | — | Free/no-auth provider with live model discovery. |
| minimax-cn | Multi: chat+messages | API key | Static | Usage | China MiniMax Chat + Messages, usage and provider quirks. |
| minimax | Multi: chat+messages | API key | Static | Usage | International MiniMax Chat + Messages, usage and provider quirks. |
| mistral | OA-chat | API key | Static | — | Retain current provider quirks. |
| mmf | OA-chat | No auth | Static | — | No-auth built-in provider. |
| nebius | OA-chat | API key | Static | — | Built-in first-class provider. |
| nvidia | OA-chat | API key/free-tier | Static | — | Built-in NVIDIA provider. |
| ollama-local | Ollama | Local/optional | Static | — | Local Ollama /api/chat transport; local-network operation is first-class. |
| ollama | Ollama | API key | Static | — | Hosted Ollama /api/chat compatibility. |
| openai | Multi: chat+responses | API key | Static | — | First-party OpenAI; Routeweft must use native Chat or Responses according to client source protocol. |
| opencode-go | Multi: chat+responses+messages | API key | Static | Usage | Three native transports; select matching client protocol where possible. |
| opencode | OA-chat | No auth | Dynamic + passthrough | — | Free/no-auth OpenCode gateway; retain quirks and arbitrary model IDs. |
| openrouter | OA-chat | API key | Dynamic + passthrough | — | Live model catalog and passthrough IDs. |
| perplexity-web | Specialized | Cookie/session | Static | — | Perplexity web SSE/session transport. |
| perplexity | OA-chat | API key | Static | — | Perplexity API built-in provider. |
| perplexity-agent | OA-responses | API key | Dynamic + passthrough | — | Responses API plus live model discovery. |
| qoder | Specialized | OAuth + PAT/API key | Static | Usage/quota | Qoder device/refresh/token workflow and provider-specific agent SSE transport. |
| siliconflow | OA-chat | API key | Static | — | Built-in first-class provider. |
| together | OA-chat | API key | Static | — | Built-in first-class provider. |
| venice | OA-chat | API key | Dynamic + passthrough | — | Live catalog and passthrough IDs. |
| vercel-ai-gateway | OA-chat | API key | Dynamic + passthrough | Usage | Vercel AI Gateway live catalog and usage. |
| vertex-partner | OA-chat | API key | Static | — | Partner endpoint support through shared OpenAI semantics. |
| vertex | Specialized | Google/Vertex credentials | Static | — | Native Vertex request/auth semantics. |
| volcengine-ark | OA-chat | API key | Static | — | Built-in Ark OpenAI-compatible provider. |
| xai | Multi: chat+responses | API key + OAuth | Static | — | xAI direct API with native Chat/Responses plus current dual-auth/OAuth refresh support. |
| xiaomi-mimo | Multi: chat+messages | API key + OAuth | Static | Usage | Chat + Messages, API-key/auto-import/OAuth workflows. |
| xiaomi-tokenplan | Multi: chat+messages | API key | Static | — | Token-plan endpoints for Chat + Messages. |
| alims-intl | OA-chat | API key | Static | — | Alibaba Model Studio international; retain current quirks. |
| codebuddy-intl | OA-chat | API key + OAuth | Static | Usage/quota | International CodeBuddy auth/refresh and usage. |
| zed | OA-chat | OAuth | Passthrough | Usage | Zed OAuth, passthrough model IDs and usage. |
| api-airforce | OA-chat | API key/free-tier | Dynamic + passthrough | — | Live model catalog and passthrough IDs. |
| baidu | OA-chat | API key | Static | — | Built-in Baidu provider. |
| bazaarlink | OA-chat | API key/free-tier | Static | — | Built-in first-class provider. |
| bluesminds | OA-chat | API key | Static | — | Built-in first-class provider. |
| kilo-gateway | OA-chat | API key/free-tier | Static | — | Built-in Kilo gateway provider. |
| llm7 | OA-chat | API key | Passthrough | — | Passthrough model IDs. |
| sambanova | OA-chat | API key | Static | — | Built-in SambaNova provider. |
| tencent | OA-chat | API key | Static | — | Tencent Hunyuan OpenAI-compatible endpoint. |
| morph | OA-chat | API key | Static | — | Morph OpenAI-compatible endpoint. |
| poolside | OA-chat | API key/free-tier | Static | — | Poolside built-in provider. |
| tokenrouter | OA-chat | API key | Dynamic + passthrough | — | Live catalog and passthrough IDs; retain thinking-format mapping. |
| alitp-intl | OA-chat | API key | Static | — | Alibaba Token Plan; preserve cache-control quirk. |
| kenari | Multi: chat+responses+messages | API key | Dynamic + passthrough | Usage/quota | Prefer source-matching native endpoint; retain Responses tool/system-role quirks and IDR pricing/usage. |
| typesafe | SystemOne | API key | Static | — | Native System One/Jev transport; service kind is systemone. |

Broad-provider GA requires all 80 rows to be implemented and green, not merely displayed in UI.

## 4. Hidden reference providers

The following LiteRouter entries are intentionally **not** initial Routeweft user-visible providers:

| Provider | Initial Routeweft status | Reason/reference |
|---|---|---|
| trae | hidden | Current reference has tool-calling limitations. |
| devin-cli | hidden | Current reference can spawn a local agent with shell/filesystem authority; not part of Routeweft gateway product. |
| windsurf | hidden | Current reference has tool-calling/transport limitations. |

They become visible only through an explicit Product Requirements + BDR change.

## 5. Required provider connection workflows

Built-in providers must preserve the applicable connection modes and operator workflows:

- API key;
- OAuth authorization/callback/device flows;
- rotating refresh tokens;
- PAT/token import;
- cookie/session credential;
- no-auth/free connection;
- dual API-key + OAuth providers;
- provider-specific bulk/auto import where explicitly listed below.

Current reference workflows that are product requirements where their provider is implemented:

- Codex token import and bulk import;
- Cursor import and auto-import;
- GitLab PAT;
- Grok CLI bulk import;
- iFlow cookie import;
- Kiro API-key, standard import, auto-import, CLI-proxy import, social authorize/exchange;
- Xiaomi Mimo API-key and auto-import;
- generic `/oauth/{provider}/{action}` equivalent flow.

Routeweft does not have to preserve LiteRouter's internal route names; it must preserve the user capability.

## 6. Credential identity and refresh requirements

Provider connections have stable identity distinct from display name.

Requirements:

- do not collapse distinct OAuth identities/workspaces that the provider treats separately;
- refresh is singleflight per provider identity;
- access token updates become immediately visible in RuntimeState;
- rotated refresh token is durably committed before old credentials can be lost;
- failed refresh does not overwrite the last known durable credential with an empty/partial credential;
- account selection never exposes raw credentials in logs/UI;
- provider-specific refresh lead/max-age semantics can be represented in `ProviderSpec`/auth modules.

## 7. Model catalog requirements

A provider can declare:

- static seed models;
- dynamic model discovery;
- arbitrary/passthrough model IDs;
- aliases;
- upstream-model remapping;
- quota family;
- model capabilities/context window;
- thinking/reasoning options.

For **Dynamic + passthrough** providers:

- the UI refreshes the live list on demand;
- the static seed remains useful offline;
- unknown IDs remain routable when provider policy allows passthrough;
- live discovery failure must not delete a previously configured model.

## 8. Usage/quota requirements

Where the matrix marks Usage/quota:

- expose provider/account current limit state;
- preserve known reset timestamps/credit windows;
- feed routing eligibility where current behavior uses quota;
- distinguish `available`, `exhausted`, `cooldown`, `unknown`, and `error`;
- quota read failure is not automatically exhaustion;
- support provider-specific actions such as Codex reset-credit operations when the current product exposes them.

## 9. Shared-adapter requirement

Provider breadth must not create 80 duplicated executors.

Preferred implementation:

```text
ProviderSpec
  -> protocol adapter
  -> auth module
  -> optional model-discovery module
  -> optional usage/quota module
  -> optional provider-specific hooks/quirks
```

Examples:

- Groq/OpenRouter/Together can share OpenAI Chat machinery;
- Anthropic/Claude-compatible providers share Messages machinery;
- Codex/Grok CLI/Perplexity Agent share Responses machinery while keeping provider-specific auth/usage;
- DeepSeek/GLM/Kimi/MiniMax/Xiaomi Mimo choose native Chat or Messages according to incoming protocol;
- Kenari chooses Chat, Responses or Messages natively before translation.

## 10. Generic Provider is separate from built-in catalog

The built-in catalog does not replace Generic Provider.

Generic Provider requirements live in PRD/SPEC and must allow an operator to create a custom provider without source changes.

## 11. Acceptance gate

A provider row can be marked production-ready only when applicable tests cover:

- connection creation/edit/disable/delete;
- authentication;
- refresh/import;
- model listing/discovery/passthrough;
- request headers and base endpoint;
- protocol route selection;
- streaming/non-streaming;
- tool/reasoning behavior where supported;
- error classification/fallback;
- Usage/quota extraction;
- proxy behavior;
- cancellation;
- redaction/security.

The provider matrix must never regress back to unexplained `TBD` entries.
