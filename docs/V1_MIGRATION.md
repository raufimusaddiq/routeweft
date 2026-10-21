# LiteRouter v1 → Routeweft: gradual manual migration (#78 C01, #179)

Routeweft and LiteRouter v1 are different applications. Run them side by side with
independent state, and manually recreate configuration and move clients at your own pace.
The operator chooses which workloads to move and when, after validating their behavior
in Routeweft (RUNBOOK §25).

The absence of a migration command is a product decision. There is no
`routeweft v1-plan` command, apply step, admin migration route or migration runner.
`PlanV1Migration` stays a tested library, with no operator-facing entry point.

Oracle: `/opt/9router` @ `1c3b9f47`. Library reference: `PlanV1Migration` and
`V1Plan.ApplyCatalog` in `internal/runtime/v1_migration.go`; tests in
`v1_migration_test.go` run on a synthetic export with fake sentinel secrets.

Routeweft never opens, mounts or restores a v1 database (AGENTS §2, BDR-001/017).
The library accepts only the **JSON export** that v1 produces at `GET /api/settings/database`
(`src/app/api/settings/database/route.js:14-25`, built by `exportDb`,
`src/lib/db/index.js:74-95`). `routeweft restore` refuses a v1 `data.sqlite` by
name and points here (`internal/backup`, `TestStageRestoreNamesLiteRouterV1Database`).
`PlanV1Migration` refuses a non-JSON or non-object input the same way.

The following tables document the library's mappings as a reference for manual
recreation; a mapped value does not imply an automatic product migration. The library
builds a plan without storing it. Library callers persist its `Settings` output through
`SetSettings` and catalog entries through `ApplyCatalog`, using Routeweft's normal
validate → compile → commit → publish protocol.
Every v1 value is mapped, or named in the plan's `Refused`,
`Recreate`, `KeyReissue` or `Reauth` list. No list holds a secret: refusals and notes name a path and describe a value's JSON type, never its content (`TestV1PlanNoRefusalOrNoteEchoesInputValues`). Only identifiers are echoed: setting keys, field names, and combo, alias and node names (`TestV1PlanNoRefusalOrNoteEchoesStringValues` feeds a string sentinel to every accepted field). A combo with no models is refused by name, since it would route nowhere. A fusion combo with no `judgeModel` is carried with a note. A malformed value, including a malformed `capacityAdapter`, refuses only itself.

## 1. Mapping reference for manual recreation

| v1 export member | Result | Where |
|---|---|---|
| `settings`: `stickyRoundRobinLimit`, `comboStickyRoundRobinLimit`, `capacityAdapter`, `outboundNoProxy`, `observabilityMaxJsonSize`, `dnsToolEnabled`, `fallbackStrategy`, `comboStrategy`, `providerStrategies`, `comboStrategies`, `ccFilterNaming`, `headroomCodeAware`, `headroomKompress` | **Mapped by the library** using the existing `ImportPinSettings`; its notes are carried into the plan | `settings_pin.go` |
| `settings`: `rtkEnabled`, `headroom{Enabled,Url,CompressUserMessages,TimeoutMs}`, `caveman{Enabled,Level}`, `ponytail{Enabled,Level}`, `pxpipe{Enabled,AutoInstall,MinChars,TimeoutMs}`, `enableObservability`, `observability{MaxRecords,BatchSize,FlushIntervalMs}`, `outboundProxy{Enabled,Url}`, `requireApiKey`, `quotaVisibility` | **Mapped by the library** under the same key after `validateSettingValue` | `v1PassthroughSettings` |
| `combos` | **Mapped by the library** as Routeweft Combos, member order kept. `comboStrategies[name]` with `fallbackStrategy: "fusion"` sets `FusionEnabled`, `judgeModel`, `fusionTuning` on the Combo | `ApplyCatalog` |
| `modelAliases` | **Mapped by the library** when the target is `provider/model` | `ApplyCatalog` |
| `providerConnections`, `apiKeys` | **Not carried**; listed (§4, §5) | |
| `providerNodes`, `proxyPools`, `customModels`, `pricing` | **Recreate by hand**; listed | `Recreate` |

Combos and aliases need their providers and models to exist: Routeweft rejects an unknown
member (`compileCombos`). `ApplyCatalog` therefore commits entry by entry, returns each
failure by name, and is idempotent (Combo IDs are `v1-<name>`). For manual recreation,
create providers and models → authorize connections → recreate Combos and aliases →
reissue keys and update each client when ready. A `provider/`
prefix that is a v1 provider *alias* (for example `cc/…`) is kept verbatim and fails
until the operator fixes it; Routeweft does not guess.

## 2. Units

Only one stored unit differs; the rest were verified identical against the pin.

| Value | v1 | Routeweft | Handling |
|---|---|---|---|
| `observabilityMaxJsonSize` | KiB (`requestDetailsRepo.js:27,44` ×1024) | bytes | ×1024 (existing importer; fixed in #158) |
| `observabilityMaxRecords`, `observabilityBatchSize`, `observabilityFlushIntervalMs` | count / count / ms; `0` → default (`requestDetailsRepo.js:24-26`) | same | carried; `0` kept as the Routeweft default with a note |
| `headroomTimeoutMs`, `pxpipeTimeoutMs`, `pxpipeMinChars` | ms / ms / chars | same | carried; Routeweft rejects ≤ 0, which is then refused by name (v1's UI clamp is not applied) |
| `fusionTuning.stragglerGraceMs`, `panelHardTimeoutMs` | ms | ms | carried; grace > 10 min or hard timeout ≤ 0 / > 10 min refused by name (`routing.FusionTuning.Validate`) |
| `fusionTuning.minPanel` | count; < 2 behaves as 2 (`combo.js:562`) | count | stored as `max(v, 2)` |
| `stickyRoundRobinLimit`, `comboStickyRoundRobinLimit` | count; `0` → default | same | existing importer |
| `pricing[…]` | $ / 1M tokens | $ / 1M tokens | no conversion, but fields rename: `cached`→cache read, `cache_creation`→cache write, `reasoning` has no Routeweft rate |

There is no percent-valued setting in the v1 export.

## 3. Unsupported fields (named and refused)

Each appears in `Refused` as `settings.<key>` with a reason, never with its value:

- `password` — hash not carried; set the Routeweft bootstrap password.
- `cloudEnabled: true` — no cloud sync (PRD 4.2). `false` is the Routeweft behavior and is not reported.
- `requireLogin: false` — Routeweft always requires a session (C03). `true` is not reported.
- `tunnel*`, `tailscale*`, `mitm*` — v1 deletes these itself on import (`index.js:116`).
- `outboundProxyUrl` containing user info — the credential would be copied; enter the proxy by hand.
- Wrong-typed or Routeweft-invalid values (`"yes"` for a boolean, `cavemanLevel: "max"`).
- Any other key (for example `claudeAutoPing`) — "no Routeweft equivalent, not stored".
- Top-level export members that are not v1 tables are refused by name.
- The C04 importer's own refusals (unknown capability, `*.host` no-proxy entry, unsupported strategy fields, `ccFilterNaming`, `headroomCodeAware`, `headroomKompress`) arrive as plan notes unchanged. Its fusion notes for a Combo that the plan carries are dropped, since the value was carried.

## 4. Key reissue

v1 stores client keys in plaintext (`apiKeys.key`, `apiKeysRepo.js:54-72`); Routeweft keeps only a
digest and shows a key once. No v1 key is read into the plan. `KeyReissue` lists, per key, its
**name, active flag and access restriction/allow-list** so the operator can recreate it
(`CreateAPIKeyWithAccess`) and give each client the new value. Inactive v1 keys are listed as
inactive: reissue them only if wanted. Clients keep working against v1 until they receive the new key.

## 5. Credential reauthorization

All connections are listed in `Reauth` (id, provider, name, auth type, active flag, priority) and none is carried:

- **`oauth`** — authorize again through the Routeweft OAuth flow. v1 refresh tokens are plaintext in the
  export; Routeweft seals credentials under its own key and never reuses v1's. Copying a refresh token
  would also leave two routers rotating one token, which providers may invalidate; use a separate provider
  identity, or designate one rotation owner (RUNBOOK §25).
- **`apikey`** — enter the provider key again. Reading it from the export would put a plaintext secret
  through logs, shell history and fixtures for no benefit.
- **Any other auth type** — listed with the action "authorize again".

Priority is listed so the operator can restore the account order by hand (Routeweft orders by
`priority, name, id`). Cooldown, health and last-error state are volatile in Routeweft and start empty.

## 6. Recreate by hand

`providerNodes` (compatible/custom nodes), `proxyPools` (pool URLs carry credentials, so none is
read), `customModels` (v1 keys them by provider alias, not a Routeweft provider ID) and `pricing`
(§2). Per-connection proxy fields are not part of the mapping at all (PARITY §6 C06 row 3).

## 7. History

**Decision: v1 usage and request history stays in v1 forever. Routeweft does not import it.**

This includes aggregate `usageDaily` totals: there is no labelled, read-only imported
series. v1 and Routeweft use two accounting bases, so retaining history in its original
application keeps their usage and cost totals separate.

- The JSON export contains no history: `exportDb` selects neither `usageHistory`, `usageDaily` nor `requestDetails`
  (`index.js:78-88`). History exists only in `data.sqlite`, which Routeweft never opens.
- v1 itself excludes `requestDetails` from its safety backups (`backup.js:16-17`).
- Routeweft usage rows are keyed to Routeweft key IDs and connection IDs and costed with Routeweft pricing;
  v1 rows reference v1 `apiKey` and `connectionId` values that do not exist in Routeweft, and a v1 `cost` was computed with
  v1 prices. Merging them would silently mix two accounting bases in one total (RUNBOOK §25.3: "do not combine").
- A history field in an export is refused by name: `usageHistory`, `usageDaily`, `requestDetails`.

Operator action: keep history in the independent v1 instance and off any Routeweft
volume. When archiving v1, preserve its `data.sqlite` read-only and read it with v1 or
`sqlite3`. Routeweft never deletes it.

## 8. Operator decisions

Choose which providers, settings, models, Combos and clients to recreate, and when to
move each workload. Resolve provider aliases and unsupported values using the mappings
above. Decide which inactive accounts and client keys you still need, restore account
priority as needed, and arrange provider identities or token-rotation ownership (§5).

Keep v1 available for its history and any clients you have not moved. Recreate and
validate each workload independently; there is no shared database, credential state or
accounting history between the applications.
