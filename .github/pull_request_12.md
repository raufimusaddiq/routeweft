## Goal

Implement Sprint 3 PR 12: full Combo.

Deliver:

- Combo CRUD/store/compiler with ordered members, selection flag and strategy metadata;
- ordered fallback over Combo members;
- Combo round-robin and sticky round-robin using Combo-local RuntimeState cursors;
- `comboStrategy`, `comboStrategies` per-Combo override and `comboStickyRoundRobinLimit`;
- capability detection/reorder and capacity adapters with the enabled-empty-pool no-op contract;
- context trimming for adapter targets with smaller context.

## Non-goals

No Fusion panel/judge/quorum/grace/hard-timeout semantics (Sprint 3 PR 13). No Combo admin API or UI, no RTK/Caveman/Ponytail/Headroom/PXPIPE/cache work (Sprint 4), no provider-credential persistence (Sprint 5). The plan/inspect primitives here are request-time code consumed by ingress; the control-plane API and UI surface arrive in their own sprints.

## Contract references

- PRD: §11 (PRD-COMBO-001, PRD-COMBO-002, PRD-COMBO-004), §4
- SPEC: §15 (15.1–15.4), §4, §6
- BDR: BDR-006, BDR-009
- Sprint: Sprint 3, PR #12

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

New behavior:

- `internal/runtime/combo.go`: `Combo`/`ComboMember`, `Resolve` (selected, non-empty members in position order), `compileCombos` validation (non-empty id/name, known member models, no duplicate members, supported strategy, default sticky limit 1), `ComboByName`/`Combos` defensive copies, plus `ComboStrategy` (per-name `comboStrategies` -> Combo strategy -> global `comboStrategy`) and `ComboStickyLimit`.
- `internal/runtime/combo_store.go`: `SetCombos`, `PutCombo` (generates a UUID when absent, matches by id or name), `DeleteCombo` (cascades member rows via FK), and transactional `persistCombos`/`loadCombos` that preserve member order via `position`.
- `internal/runtime/compiler.go`/`manager.go`/`snapshot.go`: Combos flow through the same validate/compile/commit/publish protocol as catalog and settings; `loadConfig` reloads Combos so they survive restart.
- `internal/store/migrations/v3.go`: adds `idx_combo_models_ordered` for deterministic ordered loads. `combo_models.selected` already exists in v1, so it is not re-added.
- `internal/routing/capability.go`: `OrderCombo` stable capability reorder that keeps every original fallback candidate; `AdapterCandidates` that returns nothing for disabled adapters or an enabled empty pool; `TrimHistoryForContext` that preserves the instruction head and active tail and drops older middle turns first (tail wins when both cannot fit).
- `internal/ingress/policy.go`: `PlanCombo` resolves a named Combo to ordered members, applies capability reorder and adapter prepend, then Combo-local RR/sticky rotation; capability ordering is re-applied after rotation so a required capability is never rotated behind a non-capable fallback candidate.

## Storage / migration impact

- [x] Storage impact is documented below and migration/rollback is tested.

Details: no new tables or columns; v3 adds one covering index over the existing `combo_models` table (`combo_id, position, id`) to make ordered member loads deterministic and index-backed. Combos persist inside the existing single configuration transaction, so commit-before-publish ordering is unchanged. Rollback: revert the code; the index is additive and harmless if left in place.

## Security impact

- [x] No security-boundary impact.

Details: no auth, secret, network or SSRF behavior changes. Combo metadata contains only provider/model identifiers and strategy values; no credential material is added or logged.

## Performance impact

- [x] No inference hot-path impact.

Details: `PlanCombo` reads only the already-loaded `RuntimeSnapshot` plus narrow-lock RuntimeState cursors; it performs no SQLite read, filesystem read or config recomputation on the request path.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`. New coverage: Combo CRUD persistence and restart round-trip with member order/deselect preserved; unknown-member compile rejection; `comboStrategies` override and default resolution; capability reorder keeping original fallback order; adapter prepend plus disabled/empty-pool no-op; context trimming head/tail preservation; `PlanCombo` capability reorder, adapter prepend, sticky rotation and unknown-Combo handling.

## Rollback

Revert this PR to remove Combo compilation, persistence, planning and capability utilities. Combos are inert configuration data; no other feature depends on them until PR 13.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
