# Pinned differential gate

Run `ORACLE_DIR=/path/to/literouter make parity-test` from the repository root.
The oracle must contain commit `1c3b9f47837d42ea64992063309064af755a14a3`.
Node 22 and Go from go.mod are required. No oracle working files are imported.

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
