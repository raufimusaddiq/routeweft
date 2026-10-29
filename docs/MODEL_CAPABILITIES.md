# Pinned model capabilities (#125)

PRD §9, SPEC §5 and §15.3 require model metadata available without mutable
configuration reads. This implements the source decision for #125: generate
checked-in Go data from `1c3b9f47837d42ea64992063309064af755a14a3`, following
`compat/parity/generate-seeds.mjs`. Generation is a developer/build preparation
step; ordinary builds consume the checked-in file and need neither Node nor the
oracle. No models.dev fetch, file reader, runtime catalog-source setter or
outbound request is introduced. The pin is the parity reference. A live catalog
would add an SSRF surface and offline/upstream-change failures to immutable data.

Regenerate or check with:

```bash
# ORACLE_DIR / ORACLE_SHA from `node compat/parity/resolve-oracle.mjs`
node compat/parity/generate-capabilities.mjs
node compat/parity/generate-capabilities.mjs --check
```

`gofmt` must be on PATH. Both commands read only git objects at the pin.
`data_gen.go` contains values only: merged defaults, exact model and provider
entries, ordered glob entries, provider aliases, CommandCode's text-only list
and vision-name regex strings. Resolution lives in `capabilities.go` and is
covered separately from regeneration.

The pin's fields (`open-sse/providers/capabilities.js:46-68`) are `vision`, `pdf`,
`audioInput`, `videoInput`, `imageOutput`, `audioOutput`, `search`, `tools`,
`reasoning`, `thinkingFormat`, `thinkingCanDisable`, `thinkingRange`,
`thinkingEffortSupported`, `contextWindow`, `maxOutput`. Go uses a string with
empty meaning null for `thinkingFormat`, and a value `BudgetRange` whose `Set`
flag distinguishes null from `{min:0,max:0}`. Results contain no mutable references.
Media fields describe metadata only; they add no standalone media API.

Resolution follows the actual pin (`capabilities.js:703-748`), including its
exceptions:

1. Empty model returns defaults immediately. Normalize provider aliases
   (`:686-692`); base model is the last slash-separated component.
2. CommandCode (`:711-726`) uses provider exact full ID, then base ID, then its
   own reasoning/format/limits and text-only denylist (`:660-701`). It bypasses
   family matching and vision refinement.
3. Other providers: exact full ID, then base ID (`:728-733`). Return immediately;
   no name-based refinement on provider overrides.
4. Canonical model: base ID before full ID (`:735-738`). Exact keys are case
   sensitive.
5. Globs: preserve array order, first match on base or full ID wins
   (`:740-745`). Globs are anchored and case insensitive, with `*` the only
   wildcard (`providers/pricing.js`, `matchPattern`). Specific patterns precede
   generic ones as supplied by the pin (`capabilities.js:364-540`).
6. Otherwise defaults. Canonical/pattern/default paths apply additive vision
   name refinement (`:634-655`, `providers/visionPatterns.js:8-44`). The pin's
   optional synced models.dev overlay (`:637-651`) is deliberately absent.

Hand-written corrections live in `internal/providers/capabilities/overrides.go`.
Use canonical provider IDs and exact full/base model IDs, with a complete
`Capabilities` value (start from `defaults` when only correcting a field).
These entries replace generated provider entries at construction and therefore
win before canonical/glob/default resolution. They cannot be erased by
regeneration. An intentional divergence also needs an explicit parity/test
disposition; the regeneration check still compares the untouched generated data.
No operator-editable override API is added.

The compiler constructs a private `Catalog` in every `RuntimeSnapshot` before
publication. Construction copies model/provider values and ordered patterns,
applies corrections and compiles matchers. `ModelCapabilityDetails` resolves aliases
against that snapshot and returns a value. There are no setters or exposed maps;
old snapshots and previously returned values cannot change. Passthrough IDs use
the same immutable rules. There is no SQL, filesystem or network access during
resolution, and the existing validate/compile → commit → publish protocol is
unchanged. Storage/schema/migration impact: none.

The Chat-to-Messages request leg now supplies canonical `maxOutput` to
`AdjustMaxTokens`, matching `translator/request/openai-to-claude.js:25-28`.
For example, Opus 4.8 can send 128000 rather than being pre-clamped to 64000;
unknown IDs retain the 64000 floor. This leg has no provider identity, as at the
pin, and uses a private immutable package catalog with the same data and rules.

Still unwired under #119: provider-aware final/native Messages ceiling
(`translator/formats/claude.js:496-505`), thinking-budget reconciliation,
`captureThinking`/`applyThinking` gating by `reasoning`/`thinkingFormat`, and the
remaining Claude preparation pipeline. Gemini/Ollama reasoning mapping, Combo
capability consumption, existing custom/discovered `Capabilities` metadata and
catalog API projection are also unchanged; this table does not certify them.

`TestPinnedCapabilities` checks byte-for-byte regeneration and every field of the
resolved result for every served registry seed, retained compatibility seeds,
and selected alias/prefix/unknown-ID cases against execution of the pin.
Only the null representation described above is normalized. It runs when
`ORACLE_DIR` is set, following the existing differential-test convention.
