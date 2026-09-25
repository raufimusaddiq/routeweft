## Goal

Deliver Sprint 7 item 2: the Overview page for the static Routeweft UI
(PRD §14 Overview; PRD-PERF-002 single useful read model; UI_STYLE §6-7, §10-11).

## Non-goals

No Endpoint & Key, Providers, Combo, System One, Usage, Quota, Token Saver,
Console Log or Settings workflow (later Sprint 7 PRs, planned order). No new read
model, no Go change and no polling or live event subscription: this PR consumes
the existing session-gated `GET /admin/v1/overview` from PR44 exactly once per
load, so Overview stays a single request rather than several browser joins.

## Contract references

- PRD §14 Overview (runtime/version/health, configured/ready providers, API-key
  state, active/recent traffic, actionable errors), PRD-PERF-002 (one useful
  Overview read model, no Next runtime)
- SPEC §1-2 static UI consuming `/admin/v1/*`
- BDR-003/BDR-015 static React/Vite, Routeweft-owned implementation
- UI_STYLE §6 page heading, §7 cards, §10 tables/lists, §11 metrics (tabular
  numerals, no gradient KPI tiles), §12-15 forms/a11y/theme
- REQUIREMENTS_TRACEABILITY: UI shell/workflows → browser smoke/a11y/theme/mobile
- Sprint: Sprint 7 item 2 (Overview)

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

The shell now checks the admin session and, when unauthenticated, presents the
existing `POST /admin/v1/auth/login` form (masked password, inline error) before
showing operator data; a `401` from any read returns the user to sign-in, and
sign-out calls `POST /admin/v1/auth/logout`. Overview renders four summary
metrics (gateway state, enabled connections/nodes/catalog, active keys, recorded
requests), an actionable-attention panel for failed requests and degraded
telemetry, and Runtime/Recent activity/Usage telemetry detail panels with
monospace technical values, an explicit loading state, an error state with
Retry, and a manual Refresh. Later pages keep the placeholder workspace.

## Storage / migration impact

- [x] No schema/migration impact.

No Go, SQLite or API change.

## Security impact

- [x] Security-boundary impact documented and tested.

The UI sends only `username`/`password` to the existing login route, never
persists them, and uses `credentials: 'same-origin'` on the `HttpOnly`
`SameSite=Strict` session cookie. No provider credential is fetched or rendered.
No read model is requested before a session exists.

## Performance impact

- [x] No inference hot-path impact.

One `GET /admin/v1/overview` per load plus one session check; no polling. Client
bundle after this PR: 11.9 kB CSS and 157 kB JS (50.1 kB gzip).

## Tests

Passed: `npm run typecheck`; `npm run build`; `npm run smoke`. The Chromium
smoke run now stubs the control API and asserts the sign-in prompt, masked
password field, exactly one login call, exactly one initial overview call,
formatted metrics (`Ready`/`2`/`1`/`1,200`), the actionable panel text for
failed requests and degraded telemetry, the signed-in identity label, monospace
technical values, and no horizontal overflow, alongside the existing
shell/nav/theme/drawer/focus assertions.

## Rollback

Revert this PR. Overview and sign-in disappear; the shell returns to the PR46
placeholder page. No schema, storage or API state is affected.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
