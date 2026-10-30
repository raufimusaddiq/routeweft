# Dynamic settings audit (#78 C05)

Baseline: `5dfddec`. This inventory was built before implementation from the
writable allowlist (`internal/runtime/settings_update.go:18`) and its consumers.
No setting is assumed functional merely because it appears in the settings API.

| Area / setting | Baseline running reader | Baseline change effect | C05 disposition |
| --- | --- | --- | --- |
| Observability: `enableObservability` | `internal/app/app.go:91,200,730` | Restart; disabled startup prevents worker launch | Wire live acceptance and always launch worker |
| `observabilityMaxRecords` | `internal/app/app.go:92`, `internal/telemetry/telemetry.go:127,184` | Restart; queue capacity only, not durable detail retention | Wire live queue admission; preserve accepted events when shrinking |
| `providerStrategies` `proxyPoolId` / `rotateStrategy` (#272) | `internal/runtime/noauth_proxy.go`, `internal/proxy/binder.go` | Live: compiled into the next snapshot; the rotation position is memory | Applied to the credential-free connections of the named provider; see PARITY 6.38 |
| `observabilityMaxDetailRecords` (#273, D17) | `internal/app/app.go` (`details.MaxRows`), `internal/telemetry/detail.go` | Live; read on each detail write | Newest N request details kept, pruned in the write transaction |
| `observabilityBatchSize` | `internal/app/app.go:95`, `internal/telemetry/telemetry.go:247,263` | Restart; batch upper bound, every enqueue flushes immediately | Wire live threshold and batch bound |
| `observabilityFlushIntervalMs` | `internal/app/app.go:98`, `internal/telemetry/telemetry.go:241` | Restart; ticker, immediate enqueue flush masks partial-batch delay | Wire interval on next accepted event |
| `observabilityMaxJsonSize` | `internal/app/app.go:107,201`, `internal/telemetry/detail.go:70` | Restart; byte cap replaces oversized detail with omission attribution | Wire next detail write |
| Recording / sampling / retention | `internal/app/app.go:265`, `internal/telemetry/detail.go:16,108` | Completion accounting only; no sampling setting; detail retention fixed at seven days | Keep policy; complete inference diagnostic capture remains R10 |
| Transforms: `rtkEnabled` | `internal/transforms/body/body.go:49` | Next request after compile/commit/publish | Already live |
| `headroomEnabled`, `headroomUrl`, `headroomTimeoutMs`, `headroomCompressUserMessages` | `internal/transforms/body/body.go` (`headroom.Transform`; `headroomUrl` is a base URL, `/v1/compress` is appended, #271) | Next request | Already live |
| `cavemanEnabled`, `cavemanLevel` | `internal/transforms/body/body.go:55-56` | Next request | Already live |
| `ponytailEnabled`, `ponytailLevel` | `internal/transforms/body/body.go:58-59` | Next request | Already live |
| `pxpipeEnabled`, `pxpipeUrl`, `pxpipeMinChars`, `pxpipeTimeoutMs` | `internal/transforms/body/body.go` (`pxpipe.Transform.Compress`, a body-level step after the text savers, #271) | Next request | Already live |
| `pxpipeAutoInstall` | None; allowlist/default only | Stored only, including after restart | Explicit storage-only: no managed installer/lifecycle subsystem (R09) |
| Prompt cache | `internal/ingress/transforms.go:30` | Always anchors final Messages body, including saver bypass | No writable cache toggle exists; do not invent one |
| Proxy: `outboundProxyEnabled`, `outboundProxyUrl`, `noProxy` | `internal/runtime/compiler.go:196-198`, `internal/proxy/binder.go:54,140` | URL/no-proxy are live, but `internal/proxy/policy.go:44-47` enables proxying from a saved URL even when the toggle is false | Fix shared policy to honor the global toggle; bound pools remain independent. Does not configure helper/quota/OAuth transports |
| Provider compatibility: `providerCompatibility` | None; JSON validation/storage only | Stored only, including after restart | Explicit storage-only: no defined flag schema or compatibility dispatcher |
| `dnsToolEnabled` | None; shape validation only | Stored only, including after restart | Explicit storage-only: no DNS tool surface; pinned oracle has no consumer either |
| Quota visibility: `quotaVisibility` | None; JSON validation/storage only | Stored only, including after restart | Explicit storage-only: Routeweft quota read model lacks the named per-window rows needed by the oracle hide/show workflow (U06) |
| Quota auto-ping: `claudeAutoPing`, `codexAutoPing` | `internal/quota/autoping.go`; `internal/runtime/settings_update.go`, `compiler.go` | Validated and stored, default `{"connections":{}}` (off); a minute worker reads them live | Opt-in generation warming (P05); UI selection remains U06; quota refresh alone never pings |
| Extra oracle controls: `observabilityMaxBuffered`, `headroomCodeAware`, `headroomKompress` | None; rejected by allowlist | Absent, not stored | Queue bound uses `observabilityMaxRecords`; managed helper command-line controls await R09 |

## Oracle evidence

Read with `git -C /opt/9router show 1c3b9f47:<path>`:

- `src/lib/db/repos/settingsRepo.js:16-54`: actual defaults, including no cache
  or sampling toggle. `providerCompatibility` is not a default or a consumer in
  this pin (whole-tree search); Routeweft's object is storage-only.
- `src/lib/db/repos/requestDetailsRepo.js:14-58`: observability refresh cache is
  five seconds; max JSON is KiB per field. Lines 141-146 cap durable detail
  count; lines 173-194 separate buffering, batch threshold and timed flushing.
  Routeweft's max-records queue bound and seven-day durable retention remain a
  documented difference, not a claim of count-retention parity.
- `src/app/(dashboard)/dashboard/usage/components/ProviderLimits/utils.js:288-321`:
  visibility hides provider quota rows by `modelKey`/`name`, not providers or
  routing eligibility. Applying it to whole Routeweft connections would be wrong.
- `src/lib/network/outboundProxy.js:20-36`: a disabled global proxy has no
  effective URL; C05 restores that distinction when a URL remains saved.
- `src/shared/services/quotaAutoPing.js:261-313`: per-provider opt-in connection
  maps select OAuth accounts; configuration starts/stops reset-window warming.
  `src/app/api/settings/route.js:93-101` reconfigures it on settings changes.

Storage-only dispositions record retained gaps; they are not product waivers.
Restart does not activate them. No production access or live provider calls are
needed for this audit or its tests.

## Final readers and verification

Observability values compile at `internal/runtime/observability.go:13-32`;
`RuntimeSnapshot.TelemetryOptions` and `DetailSizeLimit` return compiled values.
The app attaches those accessors to the worker and detail sink at
`internal/app/app.go:173-182`. `internal/telemetry/telemetry.go:148-160,180-190,263-281`
consumes live acceptance, queue and batch/timer limits;
`internal/telemetry/detail.go:66-72` consumes the live detail-size limit.
No request reparses settings or queries SQLite for configuration. Live completion
metadata (`internal/app/app.go:219-236`) and console logs remain independent of
this persistence gate.

`internal/app/dynamic_settings_test.go` changes settings on one initialized
instance, proving disabled startup, enable, detail-size change, redaction,
disable and draining accepted records. `internal/telemetry/dynamic_test.go`
proves queue shrink/growth and partial/full batch timing. Runtime tests cover
published transform toggles, round-tripped storage-only values and rejected
absent controls. Proxy tests prove a changed snapshot's enable/URL/policy and
disabled-global versus explicitly bound-pool selection. Existing app transform
and cache fixtures cover the remaining already-wired saver settings and bypass.

## Benchmark limits

Configured native serving (`BenchmarkConfiguredNativeServing`, three runs,
Go 1.22.12, linux/amd64) was measured against baseline `5dfddec` using a Go
source overlay and then the changed code. Both used a loopback mock upstream.
Baseline: 0.925-1.670 ms/op, 62,381-62,400 B/op, 301 allocs/op.
Changed: 1.611-3.019 ms/op, 62,278-62,507 B/op, 301 allocs/op.
Concurrent builds/tests on this shared host make latency comparison
inconclusive. Allocation count is unchanged; these are not release latency
budgets or evidence of a throughput improvement.

No schema migration or durable-format change is required. Queue changes retain
accepted accounting; detail redaction and upstream SSRF checks remain in place.
Disabling a saved global proxy now takes effect, which operators relying on the
previous bug should account for; explicitly bound pools remain selected.

## Delivery checks

- Copy/comments: PASS. Operator labels distinguish queue capacity from durable
  retention, and the restart instruction now matches the running consumers.
  Reader/disposition statements above cite code rather than inferred names.
- UI behavior/accessibility: PASS. Lockfile install, typecheck, 44 unit tests,
  production build and the full browser smoke script passed. Smoke ran against
  this worktree's preview on isolated port 4188 with local Chromium; 15 visual
  captures, light/dark/system checks and all interactive assertions passed with
  no browser errors. Theme checks now await resolved state instead of a fixed
  100 ms sleep. No visual system, navigation or interaction was added.
- Go: build, vet and touched-package tests passed. The repository race run
  passed every package except `internal/control/api`, which reached Go's
  ten-minute package timeout while hashing a test password in
  `TestStartNeedsNoPublicOriginUnlessTheFlowConsumesIt` setup. No race warning
  was reported. App, runtime, telemetry, proxy, ingress and security passed.
  The known unrelated `TestStaticUIUnknownPathsAre404` and
  `TestRunbookHealthAndAdminGate` were excluded from that invocation. This is
  not a claim that the complete repository race check passed. No live provider
  or production calls.
