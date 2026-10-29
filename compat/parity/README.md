# Differential gate against LiteRouter main

The oracle is `refs/heads/main` of `raufimusaddiq/literouter`, resolved **once per
run** by `resolve-oracle.mjs` and frozen: the resolver fetches that one commit into
`ORACLE_DIR`, exports it as `ORACLE_SHA`, and writes `oracle-run.json`
(`{sha, ref, remote, mode, resolvedAt}`); every gate in the run reads only that
commit, never a branch or `HEAD`, and none re-resolves. A failed resolve or fetch
fails the run with a clear message; there is no stale or default SHA to fall back
to, and a gate without `ORACLE_SHA` fails. CI uploads the record as the
`oracle-run` artifact and in the step summary.

```sh
cd <repo root>
eval "$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')"   # resolve main now
make parity-test
node compat/parity/resolve-oracle.mjs --sha <recorded sha>             # reproduce a recorded run
```

`ORACLE_REMOTE` overrides the remote (tests use a local one); the remote is only
read. Committed `*_pin.json` fixtures and `Source: ... at <sha>` headers record the
commit they were generated at and stay as they are until a generator is re-run on
purpose: that is how a moved oracle shows up, as a failing byte-for-byte check, not as
a silent change. Node 22 and Go from go.mod are required. No oracle working files
are imported.

`resolve-oracle.test.mjs` covers a moving main, a failed resolution and one
consistent SHA per run, against a local remote and no network.

## Change report (`report-changes.mjs`, #290)

After the run's snapshot is frozen, `report-changes.mjs` compares it with the
previous recorded snapshot and lists what moved upstream, so a changed LiteRouter
`main` becomes a reviewable list of candidate Routeweft gaps instead of a silent
update. It writes `oracle-change-report.json` and `.md` under `ORACLE_REPORT_DIR`
(default `$ORACLE_DIR/change-report`) and appends the Markdown to the CI step
summary; CI uploads both as the `oracle-change-report` artifact.

```sh
node compat/parity/report-changes.mjs --previous <sha | path to an earlier oracle-run.json>
```

`ORACLE_PREVIOUS` is the same value as `--previous`. CI takes it from the
`oracle-run` artifact of the last successful `main` run, so a PR is compared with
the snapshot `main` last recorded. Both SHAs are in the report. Surfaces compared:
API routes (added, removed, exported methods), dashboard pages, registry providers
(entry changed, activated, hidden), translator formats and conversion legs,
configuration and model-capability sources, and UI sources. Each entry carries the
lines of `docs/PARITY.md` and `docs/PROVIDER_BASELINE.md` that cite the same route,
provider or file, with the status word and issue numbers on those lines, or
`not-inventoried` when nothing cites it.

Rules the report keeps:

- Every entry is `unverified`. A difference is never classified as parity, an
  absent entry is not a parity claim, and inventory links are citations, not a
  verdict that the gap is real or closed. A person reads the source change and
  decides.
- First run (no previous record) reports `first-run` and no differences. A
  previous SHA the remote no longer has reports `previous-unavailable`. Identical
  snapshots report `unchanged`. A malformed `--previous` is an error.
- An unavailable *current* upstream is not handled here: `resolve-oracle.mjs`
  already fails the run without a fallback (#289).
- The report is informational: no upstream state (first run, unavailable or
  unchanged snapshots) fails the job, and it does not create or comment on
  issues, post anywhere or change anything outside its own files. The remote is
  only read.

`report-changes.test.mjs` covers added, removed and modified routes, pages,
providers (including activation and hiding), formats and legs, configuration and
capability sources, inventory links, the first run, an unavailable previous
snapshot, unchanged snapshots and bad input, against a local remote.

`inventory.mjs` verifies 97 route files, 153 exported operations, remote guard
behavior, key forms, 81 provider entries, 16 pages and every open-row disposition.
Its assertions are also the inventory check; a new unannotated gap fails CI.

`internal/app/parity_test.go` executes the pinned System One handler in Node
with synthetic dependency exports, then compares it with Routeweft's configured
app. No provider, credential store or host-operation dependency from v1 runs.
Only test upstreams receive traffic. System One status/prepared JSON/native
bytes/error-envelope/cancel cases are differential; admin shape descriptions
and other protocol fixtures are not a claim of complete differential coverage.
Pending coverage stays explicit in PARITY R11.

`internal/providers/parity` (build tag `oracle`) compares the whole provider
catalog, the generated model seeds, the usage-reader set and the onboarding flow
set against the oracle in both directions, and fails when a reference flow has no
implementation. A provider the reference hides may have a flow without a catalog
entry; a visible one may not.

`compat/parity/generate-seeds.mjs` regenerates `internal/providers/registry/seeds_gen.go`
from the pinned registry. Seeds are never hand-edited: running it with a new pin
is the only way that file changes.

`compat/parity/generate-antigravity-retry.mjs` evaluates the pinned `base.js` and
`antigravity.js` retry path, `computeRetryDelay` and the system-prompt rewrites with a
scripted network, clock and timers (a wait moves the clock), the IDE request id, V8's `Date.parse`, and writes `internal/providers/wire/testdata/antigravity_pin.json`
(PARITY 6.21). It needs `ORACLE_DIR` and `TZ=UTC`; the Go tests only read the file.

`compat/parity/generate-antigravity-image.mjs` evaluates the pinned `antigravity.js` image path
(`isImageModel`, `parseImageConfig`, `buildUrl`, `transformRequest`, `buildIdeRequestId`) and the
image test of `chatCore.js` and the buffered answer path (`translateNonStreamingResponse`) for
Chat, Responses and Messages clients, with the clock and session fixed, and writes
`internal/protocol/gemini/testdata/antigravity_image_pin.json` (PARITY 6.26). It needs `ORACLE_DIR`.

`compat/parity/generate-gemini-image-answers.mjs` evaluates the pinned buffered answer path
(`translateNonStreamingResponse`) and the stream (`translateResponse` over `gemini-to-openai.js`,
`openai-responses.js` and `openai-to-claude.js`) for an image answer from each Gemini-family wire
to Chat, Responses and Messages clients, with the clock fixed, and writes
`internal/ingress/testdata/gemini_image_answers_pin.json` (PARITY 6.27). It needs `ORACLE_DIR`.

`compat/parity/generate-scoped-quota.mjs` evaluates the pinned `open-sse/services/usage/{shared,codex,claude,github}.js`
with a scripted network and clock (endpoint URLs and the GitHub identity headers from the pinned registry entries) over
zero, empty, expired, multiple-window, alias, auth, error, timeout and invalid-JSON answers and two Claude multi-read
sequences, and writes `internal/providers/usage/testdata/scoped_quota_pin.json` (PARITY 6.28). It needs `ORACLE_DIR`.

`compat/parity/generate-regional-quota.mjs` evaluates the pinned `open-sse/services/usage/{shared,glm,kimi,minimax,codebuddy-cn}.js`
and `buildKimiHeaders` with a scripted network and clock, in UTC (endpoint URLs and the CodeBuddy identity headers from
the pinned registry entries), over zero, empty, expired, multiple-window, alias, fallback, auth, error, timeout and
invalid-JSON answers for every region, and writes `internal/providers/usage/testdata/regional_quota_pin.json`
(PARITY 6.29). It needs `ORACLE_DIR`.

`compat/parity/generate-account-quota.mjs` evaluates the pinned `open-sse/services/usage/{shared,grok-cli,grokCliQuotaFrame,kiro,misc,zed}.js`,
`shared/zedAuth.js`, the Qoder PAT exchange in `services/qoderModels.js` behind the dispatcher's qoder handler, and the
usage page's `parseQuotaData`, with a scripted network and clock, over zero, empty, expired, multiple-window, alias,
fallback, gRPC-frame, PAT-exchange, auth, error, timeout and invalid-JSON answers, and writes
`internal/providers/usage/testdata/account_quota_pin.json` (PARITY 6.30). It needs `ORACLE_DIR`.

`compat/parity/generate-billing-quota.mjs` evaluates the pinned `open-sse/services/usage/{shared,commandcode,kenari,
opencode-go,misc,xiaomi-mimo}.js` and `open-sse/shared/mimoAccount.js` with a scripted network, clock and file system
(endpoint URLs from the pinned registry entries; MiMo Desktop's cookie store is never read), over zero, empty, expired,
multiple-window, alias, order, auth, error, timeout and invalid-JSON answers, MiMo's account and key paths and its
session reads in sequence, and writes `internal/providers/usage/testdata/billing_quota_pin.json` (PARITY 6.31). It
needs `ORACLE_DIR`.

CI runs this as the `parity` PR job, alongside existing Go/UI/container jobs.
No test may update golden files from its actual output or turn a missing oracle
into a passing dedicated gate. Ordinary unit tests remain usable without v1.

## Drift guards (`internal/parity`)

`go test ./internal/parity` derives, from the pinned commit's own files read
with `git show`, the active/hidden provider ids (`open-sse/providers/registry/index.js`
and each entry's `id`), the format identifiers (`open-sse/translator/formats.js`)
and every conversion leg (`register(...)` calls in `translator/request` and
`translator/response`). It fails when LiteRouter has a provider, format or leg
that Routeweft lacks and that is not in the tracked list. Tracked lists only
shrink: a closed gap that is still listed also fails. Nothing in the tests is a
copy of the oracle's lists. The `formatNames` map only translates identifiers to
Routeweft protocol names, and the test fails when LiteRouter adds a format the
map does not cover.

`TestPinnedCapabilities` in `internal/parity` also checks capability-data
regeneration and compares every resolved field for all served seed models to
the pin. It runs in the existing parity CI job. Regenerate with
`ORACLE_DIR=/path/to/literouter node compat/parity/generate-capabilities.mjs`;
`gofmt` must be on PATH. See `docs/MODEL_CAPABILITIES.md` for the source decision
and resolution/override rules.

`selectable_test.go` also looks every pinned active provider up in the built-in
catalog the product serves, pins `muse` to its registry entry, and makes every
provider-table key in the pin's capability/pricing files either a provider, an
alias, or a named table-only entry (never a provider to seed).
