# Release-gate evidence: OAuth scenarios, accessibility, resource baseline (#231)

Gates O03, O04, O07 and O08 from the provider onboarding audit. This file records what was run and
what it printed on 2026-11-01. It claims nothing the commands below did not do. Nothing here contacted
a live provider; every OAuth upstream is an in-process or stubbed mock.

## O03: mock OAuth fixtures

Every way a sign-in attempt can end has a test that runs the real handler, the real sealed store and a
scripted provider. One command runs them all:

```
go test -race -count=1 -v ./internal/control/api ./internal/providers/oauthrefresh ./internal/providers/onboarding ./internal/credentials -run 'TestOAuthState|TestOAuthPasted|TestOAuthCancel|TestOAuthBrowserSaveFailure|TestOAuthDeviceSaveFailure|TestOAuthDeviceDenied|TestOAuthDeviceAttemptExpires|TestOAuthDeviceExpiry|TestOAuthSignInRefreshes|TestOAuthTokenRotation|TestOAuthPending|TestOAuthGlobal|TestRefreshKeeps|TestRefreshWithout|TestExchangersRotate|TestRotatedCredential|TestFailedPersist|TestConcurrentRefreshRotations'
```

Result on this commit: 25 tests, all PASS, 4 packages ok (`control/api` 5.8 s, `oauthrefresh` 3.9 s, `onboarding` 1.1 s, `credentials` 3.1 s). The same tests run on every push under the `race` CI job (`go test -race ./...`).

| Scenario | Test (file:line) |
|---|---|
| Wrong state: another session's, another provider's, another attempt's pasted state | `control/api/oauth_test.go:275` `TestOAuthStateIsBoundToStartingSession`, `:296` `...BoundToProvider`, `:305` `TestOAuthPastedStateFromAnotherAttemptIsRefusedWithoutConsuming`; browser: `ui/scripts/oauth-journeys-smoke.cjs` (wrong-state paste, then the right one) |
| Replay: a used state is refused and reaches no provider | `oauth_test.go:234` `TestOAuthStateCannotBeReplayed` |
| Expiry: an expired state reaches no provider and is discarded; device codes expire with the flow | `oauth_test.go:253` `TestOAuthStateExpires`; `oauth_device_test.go:261,288,302` |
| Cancel: a cancelled attempt cannot be finished or cancelled again; one being saved is not torn down | `oauth_capabilities_test.go:91` `TestOAuthCancelEndsTheAttempt`, `:127` `TestOAuthCancelLeavesASavingDeviceAttempt` |
| Failed save, browser flow (**new**): nothing half-saved, no secret in the answer, a fresh attempt saves | `control/api/oauth_e2e_test.go` `TestOAuthBrowserSaveFailureLeavesNoConnectionAndNoSecret` |
| Failed save, device flow: the attempt stays open and the retry saves | `oauth_device_test.go:313` `TestOAuthDeviceSaveFailureKeepsAttemptOpen`; `oauth_capabilities_test.go:226` |
| Token rotation, end to end (**new**): sign-in through the real handler and sealed store, two consecutive refreshes present the token each previous one rotated to, and a fresh registry over the same store continues from the durable rotation | `control/api/oauth_rotation_e2e_test.go` `TestOAuthTokenRotationIsUsedAndSurvivesARestart` |
| Token rotation, pieces: a rotated refresh token is kept, an unrotated one is not lost, the rotated credential is durable, a failed persist leaves no half-rotated connection, concurrent rotations survive a restart | `onboarding/flow_test.go:469,496`; `oauthrefresh/oauthrefresh_test.go:59`; `oauthrefresh/rotation_durability_test.go:124,150`; `credentials/status_test.go:149`; `control/api/oauth_test.go:413` |
| Bounded pending attempts | `oauth_test.go:318` (per session), `:396` (global) |

Two gaps found and closed: the browser-flow failed save (only the device flow had one) and a handler-level token rotation (rotation was proven piecewise, not through the sign-in handler); both are new tests.
Not covered: a companion or broker callback, which needs an approved threat model first (the ticket says so).

## O04: keyboard, focus, labels, light and dark

Both guards run in the `ui-smoke` job on every push (`npm run smoke` → `ui/scripts/smoke.sh`).

`ui/scripts/shell-smoke.cjs` `captureVisuals` drives each page at **1440 and 390** in **light and dark**
(and system at 1440) and asserts, per view/theme/width: no low-contrast text, control boundaries at 3:1
or more, no shadow outside the hard-offset tokens, every status dot has a word beside it, no 11 px text
outside chart axes; at 1440 the keyboard focus ring (offset 2 px, solid, accent colour, at least 3:1
against what is behind it) after Tab. The sign-in panels are covered by `oauth-journeys-smoke.cjs` and
`device-approval-smoke.cjs`.

`ui/scripts/a11y-keyboard-labels-smoke.cjs` (**#231 item 2**) loads Overview, Endpoint & Key, Providers,
Combo & Capability Adapter, System One, Settings and Usage, plus the provider connection form (provider
selected, Add connection opened), at **1440 and 390 in light and dark**: 32 views and 664 interactive
controls. Per view it asserts:

- every control is reached by Tab, exactly once, in DOM order (a native radio group is one Tab stop,
  the Usage tabs' roving `tabindex` is respected, and no positive `tabindex` exists);
- every control has a visible focus ring: a solid outline of at least 2 px, at least 3:1 against what is
  behind it (the ring is measured against its backdrop, not against its own fill);
- Enter activates every button and link, Space every button and checkbox, and Space selects an unchecked
  radio in the first group; clicks are counted with their default action prevented, so nothing
  navigates, submits or saves;
- every input, select, textarea, button and link has an accessible name from `aria-labelledby`,
  `aria-label`, `label[for]`, a wrapping label, or the button/link text, read from the rendered DOM;
- the language dialog is the only focus trap, and Escape returns focus to its button;
- nothing overflows horizontally at 390, and 16 views at **320** (the seven pages and the form, light and
  dark) do not either;
- at 390 with the viewport shortened to 420 px as an on-screen keyboard would, the focused new-key field
  is fully inside the visual viewport (188..232) and no fixed bar overlaps it.

Result on this head:

```
node scripts/a11y-keyboard-labels-smoke.cjs
a11y keyboard/labels: 32 views at 1440/390 light+dark, 664 interactive controls, 0 without a name, 0 without a focus ring, 16 views at 320, 0 problems
keyboard-open 390: field 188..232 in 420, fixed bars 0; modal trap true, focus return true
```

What it found and this PR fixed, both the same class — a ring that inherits a token meant for the
contents of a highlight block:

- the radio inside a **chosen access-mode label** inherited the page focus colour and measured
  **1.29:1 in dark**; its ring is now `--highlight-ink` (13:1 on the block in both themes);
- a **selected Provider row** (phase 3b's highlight block) re-points `--focus` to its ink for its
  children, so its own ring was ink on the page's ink — **1.00:1 in dark**. It now uses `--focus-page`,
  the page focus colour, which is drawn on the page behind the block.

No control lacked an accessible name, no focusable control lacked a 2 px outline, and no Tab-order,
activation or overflow failure was found elsewhere on these views. The whole `smoke.sh` list (16 scripts) ran green on this tree on 2026-11-01.

**Not done, and not claimed:** a real phone on-screen keyboard (the check shrinks the viewport and lets
the browser scroll the field into view, which is a proxy, not a device), a screen-reader announcement
audit, and screenshots of the onboarding forms at 390 — their labels, order and focus are measured,
their pixels are not captured.

## O08: resource baseline

Measured by the new tests; the machine was shared (load average about 6 to 7), so read times as upper bounds.
Nothing below is a budget; the tests assert bounds that are true by construction and print the numbers.

| What | Result | Test |
|---|---|---|
| Quota refresh of 1000 accounts over 4 providers (fake client, no network) | peak concurrent reads **1**, 1000 reads, one observation per account, 7 ms, heap +544 KiB after GC | `internal/quota/fanout_test.go` |
| Why it is one at a time | `RefreshProvider` loops its connections in order (`quota/service.go:85`); `RefreshAll` loops providers; the admin job takes a single slot, so a second refresh gets 409 `refresh_busy` and a deadline ends the job (`control/api/quota_refresh.go:143-189`) | read, not a benchmark |
| 1000 connections through the list API | 10 pages of 100; a request for 100000 is clamped to 100; each account on exactly one page; no stored credential in any page; slowest page 21 ms, largest page 28 KiB, heap -552 KiB after GC | `control/api/pagination_scale_test.go` |
| Pending sign-in attempts | bounded per session and globally (tests at `oauth_test.go:318,396`) | O03 table |

Reproduce both, one command each; the printed line is the measurement and the assertions beside it are
true by construction, not budgets:

```
go test ./internal/quota -run TestARefreshOfAThousandAccountsReadsOneAtATime -count=1 -v
go test ./internal/control/api -run TestAThousandConnectionsPageWithinTheBoundAndLeakNothing -count=1 -v
```

Both run in CI: `go test ./...` (job `go`) and `go test -race ./...` (job `race`) include every package
above, so these fixtures and these numbers are produced on every push; `-v` is only what prints them.


**Not measured, and not claimed:** cold and warm OAuth start or callback p95 (the mock has no network, so a
number would say nothing about a provider); service RSS under load (`docs/S8_RESOURCE_BENCHMARK.md` states its
own limits); the real network cost of 1000 quota reads. No live provider was called.

## O07: SSRF, input bounds, sealed credentials, secret-free events

Covered by existing tests named in the O03 table (`TestOAuthCallbackNeverEchoesProviderText`, `assertNoSecrets`
on every failure), `TestDefaultOAuthClientUsesTheSSRFPolicy` (`oauth_test.go:738`), `internal/security`
(`TestProtectedClientsDoNotFollowRedirects`) and, for the events, #277 (SPEC 22.1: payloads are redacted). This PR
adds no new SSRF or event test.
