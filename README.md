# Routeweft

**Routeweft** is a standalone, local-first LLM routing gateway and control plane.

It presents stable LLM-compatible ingress, routes requests across providers/accounts/models, applies routing and prompt-efficiency policy, and exposes a compact operator UI. Routeweft is its own product: it is **not** a LiteRouter v2 branch, migration wrapper, or drop-in replacement.

## Product shape

```text
clients
  |
  v
Routeweft
  +-- OpenAI Chat Completions
  +-- OpenAI Responses
  +-- Anthropic Messages
  +-- compatible LLM ingress
  +-- System One
  |
  +-- routing / account selection / fallback
  +-- Combo / Fusion / capability-aware routing
  +-- RTK / Caveman / Ponytail / Headroom / PXPIPE
  +-- prompt-cache preservation
  |
  v
LLM providers
```

Normal single-instance deployment:

```text
Caddy / client
      |
      v
Routeweft Go service
  +-- immutable RuntimeSnapshot   <- hot config
  +-- mutable RuntimeState       <- RR/cooldown/health/in-flight
  +-- pooled upstream transports
  +-- bounded telemetry queue
  +-- versioned admin API
      |
      +-- SQLite WAL              <- durable source of truth
      +-- static React UI
```

SQLite is the default durable store. PostgreSQL and Redis are not part of the normal single-instance architecture.

## Documentation

- [Product Requirements](docs/PRD.md)
- [Technical Specification](docs/SPEC.md)
- [Build Decision Record](docs/BDR.md)
- [Provider Baseline](docs/PROVIDER_BASELINE.md)
- [UI Style Contract](docs/UI_STYLE.md)
- [Runbook](docs/RUNBOOK.md)
- [Sprint Plan](docs/SPRINT_PLAN.md)
- [Agent Instructions](AGENT.md)

## Foundation status

The repository is intentionally documentation-first. Runtime implementation begins only after the foundation PR is reviewed and merged.

The initial product/behavior scope was informed by proven LiteRouter behavior, while the visual baseline is informed by LiteRouter current UI at reference commit `2ffb7922954112b30425cd487d686758e519397e`. Routeweft does not import, share a database with, or depend on LiteRouter at runtime.
