## Goal

Deliver Sprint 7 item 1: the static Routeweft app shell, theme handling and
navigation (PRD §14 Operate/Route/Observe/System IA; UI_STYLE §4-5, §13-15;
BDR-003/BDR-015).

## Non-goals

No page workflows or data fetching; Overview, Endpoint & Key, Providers, Combo,
System One, Usage, Quota, Token Saver, Console Log and Settings land in later
Sprint 7 PRs in the planned order. No visual redesign and no new design system:
tokens come from `docs/UI_STYLE.md`. No API or Go changes. No Next runtime.

## Contract references

- PRD §14 (control-plane UI information architecture), PRD-PERF-002 (static UI,
  no Next server)
- SPEC §1-2 (static React UI consumes `/admin/v1/*` and `/admin/v1/events`),
  MODULAR monolith topology
- BDR-003 (static React/Vite control plane), BDR-015 (LiteRouter visual
  language, Routeweft-owned implementation)
- UI_STYLE §2 typography, §3 tokens, §4 app shell, §5 sidebar, §6 page heading,
  §13 theme, §14 motion, §15 accessibility, §16-17 implementation rules
- REQUIREMENTS_TRACEABILITY: UI shell/workflows → browser smoke/a11y/theme/mobile
- Sprint: Sprint 7 item 1 (app shell/theme/navigation)

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

The shell renders the exact four navigation groups and ten destinations from
PRD §14, with hash-routed active state (`aria-current="page"`), a 15.5rem
desktop sidebar, an off-canvas mobile drawer with translucent backdrop, a
reusable page-heading block, and light/dark/system theme selection persisted in
`localStorage`. Theme is applied by an inline pre-paint script so there is no
light/dark flash, and `system` follows a live `prefers-color-scheme` change.

## Storage / migration impact

- [x] No schema/migration impact.

No Go or SQLite change.

## Security impact

- [x] Security-boundary impact documented and tested.

The shell is static and makes no network calls in this PR; it never receives
provider credentials. Theme preference is a local UI preference only.

## Performance impact

- [x] No inference hot-path impact.

No request-path code changed. Client bundle after this PR: 6.5 kB CSS and
148 kB JS (47.8 kB gzip), with fonts loaded from Google Fonts over preconnect.

## Tests

Passed: `npm run typecheck`; `npm run build`; `npm run smoke` (Chromium via the
repo's playwright-core harness against `vite preview`). The smoke run asserts
nav count/groups, sidebar width and background, active row and page title after
navigation, skip-link target, first Tab focus, theme selection plus persistence
across reload, live system-theme following, mobile drawer open/close with focus
return on Escape and backdrop, and zero horizontal overflow at 390/1920/320 px.

## Rollback

Revert this PR. The shell returns to the PR45 placeholder page; no schema,
storage or API state is affected.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
