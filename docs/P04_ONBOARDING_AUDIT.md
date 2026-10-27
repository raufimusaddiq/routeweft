# #76 P04 — OAuth, import and credential-rotation audit

Oracle: `1c3b9f47` (`/opt/9router` was absent on this host; the pin was read with
`git show` from a clone of the same commit). Pin paths are written relative to the
pin root: `R/` is `src/app/api/oauth/`, `L/` is `src/lib/oauth/`.

The ticket's list was verified route by route. It is **complete and correct**: the
pin has exactly fifteen import/onboarding routes under `R/`, and every name on the
ticket maps to one. Two things the ticket does not say:

- The three `auto-import` routes (`R/cursor/auto-import/route.js:168-249`,
  `R/kiro/auto-import/route.js:12-132`, `R/xiaomi-mimo/auto-import/route.js:52-140`)
  read the server's own home directory and **return the raw credential to the
  browser** (`refreshToken`, `clientSecret`, `apiKey`, `mimoPassToken`). Routeweft's
  rule is that raw provider credentials never reach the browser (AGENTS.md §11), so
  these are absent by design, not by omission. Section 4 says what replaces them.
- The dispatcher `R/[provider]/[action]/route.js` carries host actions the ticket
  does not list: loopback listeners (`start-proxy` `:139`, `stop-proxy` `:218`,
  `poll-status` `:182`), `ide-status` `:229`, `register-session` `:303` (which calls
  `readDesktopPassToken`), and `manual-code` `:557`. Routeweft's equivalent is the
  paste-back completion on `/admin/v1/oauth/{provider}/callback`.

## 1. Per provider: what the pin does, what Routeweft does

"Stores" is where the pin writes the result: `createProviderConnection`, i.e. one
`providerConnections` row with plaintext credentials (`src/lib/db/repos/connectionsRepo.js:84-115`).
Routeweft stores the same fields in the sealed secret payload.

| Provider | Pin path | Validates | Pin stores | Routeweft |
|---|---|---|---|---|
| codex token | `R/codex/import-token/route.js:12-96` | non-empty string; JWT claims for email, account id, plan, `exp` | `authType: access_token`, no refresh token | **implemented** (`manual.go:codexImportFlow`) |
| codex bulk | `R/codex/bulk-import/route.js:19-121` | object with `accessToken`; backfills email/account/plan from the JWT *only when missing*; `expiresAt` or `expiresIn` | whole item spread; `testStatus` active | **implemented; two differences fixed here** (expiry, exported `providerSpecificData`, `idToken`) |
| grok-cli bulk | `R/grok-cli/bulk-import/route.js:21-116` | `access_token`/`accessToken`; email from id token then access token | `authMethod: device_code`, `idToken`, `email`, `providerSpecificData` | **implemented; same fixes as codex bulk** |
| cursor import | `R/cursor/import/route.js:13-71`, `L/services/cursor.js:95-125` | token ≥ 50 chars; machine id ≥ 32 hex digits once dashes are removed; **no provider call** (`:116-117`) | `machineId`, `authMethod: imported`, `userId`, 24 h expiry, no refresh token | **was wrong, fixed here**: it POSTed JSON to a Connect-protocol endpoint |
| cursor auto-import | `R/cursor/auto-import/route.js:168-249` | reads `state.vscdb` from the host | returns token + machine id to the browser | **deliberately absent** (§4) |
| gitlab PAT | `R/gitlab/pat/route.js:10-62` | `GET {base}/api/v4/user` with `Private-Token` | `authKind: personal_access_token`, `baseUrl`, username/name/email | **implemented** (`providers.go:gitlabPAT`), SSRF-guarded client |
| iflow cookie | `R/iflow/cookie/route.js:9-137` | `BXAuth=`; GET then POST `platform.iflow.cn/api/openapi/apikey` | `apiKey`, only `BXAuth`, `expireTime` | **implemented; `expireTime` was dropped, kept here** |
| kiro API key | `R/kiro/api-key/route.js:11-67`, `L/services/kiro.js:299-350` | `GET q.{region}/ListAvailableModels?origin=AI_EDITOR` with `TokenType: API_KEY`, **at least one model** | `authMethod: api_key`, 365-day horizon | **was wrong, fixed here**: it POSTed to a lower-case path and accepted any 2xx |
| kiro token import | `R/kiro/import/route.js:11-66` | proves the refresh token by refreshing it; IDC when client id and secret are present | rotated pair, `authMethod` `idc`/`imported`, client registration, `profileArn` | **implemented here** |
| kiro CLI-proxy | `R/kiro/import-cli-proxy/route.js:9-40`, `L/kiroExternalIdp.js:77-128` | `external_idp` only; Microsoft token endpoint allowlist; client id, scope, profile ARN required | `authMethod: external_idp`, `tokenEndpoint`, `scope`, `clientId` | **implemented here** |
| kiro social | `R/kiro/social-authorize/route.js:10-43`, `R/kiro/social-exchange/route.js:10-67` | PKCE, `kiro://` redirect, code exchange at `prod.us-east-1.auth.desktop.kiro.dev/oauth/token` | `authMethod: google`/`github`, `provider`, `profileArn` | **implemented** (#219, §6): `kiro.go:kiroSocialFlow`, offered beside the device flow |
| kiro auto-import | `R/kiro/auto-import/route.js:12-132` | scans `~/.aws/sso/cache` for `aorAAAAAG…` | returns token + client secret to the browser | **deliberately absent** (§4) |
| kiro device | `L/providers/kiro.js:6`, `L/providers/index.js:175-179` | AWS SSO OIDC registration + device authorization; a missing profile ARN is looked up | client registration kept for refresh | already implemented (`custom.go:kiroFlow`); the ARN lookup added in #219 |
| xiaomi-mimo key | `R/xiaomi-mimo/api-key/route.js:11-136` | `sk-` prefix; `GET {base}/models` is *soft* (`:49-56`) | key, `uid`, `baseUrl`, `modelCount`, `mimoPassToken`/`UserId`/`CUserId` | **key and sealed account-field import implemented** (#181, §5) |
| xiaomi-mimo auto-import | `R/xiaomi-mimo/auto-import/route.js:52-140` | reads `~/.local/share/mimocode/auth.json` and Desktop's cookie store | returns key + `passToken` to the browser | **deliberately absent** (§4); the file's *contents* are now accepted pasted |
| refresh (all) | `open-sse/services/tokenRefresh.js:131-160`, `…/providers.js:326-455` | per-provider grant | rotated pair written back | table already mirrored; **kiro now covers its three shapes** |

## 2. What changed, and why each is a real difference

1. **Kiro refresh** (`internal/providers/onboarding/kiro.go`). Routeweft refused any
   Kiro credential without a registered AWS client (`kiro refresh needs the registered
   client credentials`). The pin has three branches (`providers.js:334-455`):
   `external_idp` (form post to a stored Microsoft endpoint), AWS SSO OIDC, and social
   (`prod.us-east-1.auth.desktop.kiro.dev/refreshToken`, default expiry 3600 s). A
   social or Microsoft login therefore worked until its first expiry and then died.
   The Microsoft endpoint is re-validated on every refresh because it was persisted
   from operator input and the request carries the refresh token.
2. **Kiro import modes** share the one admin `/import` endpoint. A bare string is an
   API key; a JSON object is a refresh-token import; a JSON object naming an access
   token or token endpoint is a CLIProxyAPI export. JSON travels in the existing masked
   credential field, so no new plain-text secret field appears in the dashboard.
3. **Kiro API key** now matches `listAvailableApiKeyModels`.
4. **Cursor import** now validates shape and makes no provider call, like the pin.
5. **Bulk import expiry** (codex, grok-cli). An export without a lifetime stored
   `now+0`, marking a fresh token expired on arrival. An absent expiry now stays
   unknown (zero), which the registry reads as "no proactive refresh" — the rule #173
   left behind. `expiresAt` is honored, the exported `idToken` and
   `providerSpecificData` are kept, and the JWT only backfills what is missing.
6. **Xiaomi key import** accepts the key, a JSON body with `apiKey`/`uid`/`baseUrl`, or
   the contents of MiMo Code's `auth.json`. The pin's account-session fields are
   accepted in JSON and sealed with the key (#181, §5). An unread model catalog records no `modelCount` rather than `0`.
7. **iFlow** keeps the key's `expireTime`.

## 3. Open, per provider

| Item | State | Why |
|---|---|---|
| Kiro Google/GitHub sign-in | **done in #219** | See §6. |
| Kiro `resolveKiroProfileArnPatch` network fallback | **done in #219** | A refresh, device approval or social sign-in without an ARN calls `ListAvailableProfiles` (`providerHelpers.js:53-71`); see §6. |
| Cursor token refresh | none exists | The pin has no public refresh endpoint (`R/cursor/import/route.js:47`); a Cursor token is re-imported at expiry. |
| Pin `testStatus` (`untested`) | implemented (#220) | v13 persists `test_status` per connection (SPEC §33.2). Xiaomi's soft validation (`Result.Unvalidated`) stores a new key `untested` and leaves a stored duplicate's status as it was (`xiaomi-mimo/api-key/route.js:79,115`); every other onboarding save records `active`. |
| Dispatcher loopback listeners, `ide-status`, `register-session` | deliberately absent | Host listeners and host reads; paste-back completion replaces them. |

## 4. Host-secret access

Routeweft does not read the host's home directory for any import, and no route
returns a stored credential. The three pin `auto-import` routes are replaced by a
paste of the *same file contents*: Kiro's `kiro-auth-token.json` as written (its
`accessToken` is ignored; the refresh token is proven by refreshing) plus the client
registration's `clientId`/`clientSecret` (or `client_id`/`client_secret`) go in the
token-import JSON, and MiMo
Code's `auth.json` goes in the Xiaomi import as-is. Both pass through the
admin-session-gated, size-bounded `/import` endpoint, are sealed at rest, and are
never echoed.

#218 makes this the dashboard's own journey: each import names where its file or
value lives on the operator's computer (Cursor's `state.vscdb` keys, Kiro's
`~/.aws/sso/cache`, MiMo's `auth.json`, iFlow's `BXAuth` cookie, GitLab's PAT
scopes), takes a file the operator chooses in the browser or a masked paste, and
checks its shape before sending it. The browser reads only the file the operator
picked; the server still reads no host path.

What is **not** on this host, and left named rather than invented:

- **MiMo Desktop's account-session `passToken`.** It lives in Desktop's Chromium cookie
  database (`open-sse/shared/mimoAccount.js`), which only exists on the machine where
  Desktop runs. The import accepts pasted `mimoPassToken`/`mimoUserId`/
  `mimoCUserId` (#181); host discovery stays open; see §5.
- Cursor's `state.vscdb` (token and machine id) and Kiro's `~/.aws/sso/cache`, for the
  same reason.

## 5. Xiaomi and #115's last row

The owner approved account credential storage in #181. Import JSON now accepts
`mimoPassToken`, `mimoUserId`, and `mimoCUserId`, sealed alongside the API key.
The quota reader derives the five-step SSO session and reads the account usage
service (`open-sse/shared/mimoAccount.js:119-175,246-260` at `1c3b9f47`).

Preview inference remains unwired: its route switch and defaults are separate
executor work (`open-sse/executors/xiaomi-mimo.js:30-94`). Host auto-import remains
open because neither the MiMoCode auth file nor Desktop cookie database exists
on this host. See PARITY’s MiMo account quota section for evidence and remaining
gaps; #181 changes neither provider readiness nor inference dispatch.

## 6. Durable rotation

`internal/credentials/registry.go:refresh` already commits the rotated payload under
the connection's install lock before publishing it. What was missing was a test that
proves it end to end with a real provider refresher and a real database:

- `TestRotatedCredentialIsDurableAndKeepsProviderData` — the rotated pair is read back
  from SQLite, and the fields the exchange did not mention survive.
- `TestFailedPersistLeavesNoHalfRotatedConnection` — a trigger makes the write fail;
  neither storage nor the served credential moves, and the next attempt presents the
  **durable** refresh token, never the unsaved one.

Residual risk, stated plainly: if the provider rotated the refresh token and the write
then fails, the new token exists only in the failed call. The retry presents the old
one. Providers with refresh-token reuse detection will reject it and the connection
needs reauthorization. Closing that needs the registry to hold the unsaved pair for a
persist-only retry, which changes the SPEC §19 commit protocol and is not done here.

## 6. Kiro Google and GitHub sign-in (#219)

Revalidated against main after #201/#208/#215-#217 (`2e04783`): the social *refresh*
existed (`kiro.go:refreshKiroSocial`), but no endpoint could start or finish a social
sign-in, because `providerFor` gave each provider one flow and Kiro's was the device
flow. The pin's flow and what Routeweft now does, step by step:

| Pin (`1c3b9f47`; identical at `264a26d0`) | Routeweft |
|---|---|
| `R/kiro/social-authorize/route.js:13-20`: `provider` must be `google` or `github`, else 400 | `POST /oauth/kiro/start` with `meta.authMethod`; `Flow.AuthMethods` is the closed set and `startAuthMethod` (`api/oauth.go`) refuses anything else with `oauth_auth_method_invalid` before any network call. The device start applies the same rule to `builder-id`/`idc`, so `google` no longer silently became a Builder ID device code (`custom.go:kiroFlow` read any non-`idc` value as `builder-id`). |
| `L/services/kiro.js:133-138`: `…/login?idp=Google\|Github&redirect_uri=kiro://kiro.kiroAgent/authenticate-success&code_challenge…S256&state&prompt=select_account` | `kiro.go:kiroSocialFlow` Authorize, same URL. The verifier stays server-side on the attempt; the pin returned it to the browser (`social-authorize/route.js:32-38`). |
| `KiroSocialOAuthModal.js:64-83`: paste the callback address; `error` param is a failure; no `code` is an error | The shared callback (`api/oauth.go:handleOAuthCallback`, `parseOAuthInput`) takes the `kiro://` address: `error=` → `oauth_denied` (attempt ended), no code → `oauth_code_missing` (attempt kept), another attempt's `state` → `oauth_state_mismatch` (kept). The pin never checked `state`. |
| `L/services/kiro.js:144-171`: JSON `{code, code_verifier, redirect_uri}` to `/oauth/token`; `expiresIn` defaults to 3600 | Exchange, same body and default. |
| `social-exchange/route.js:36-53`: email from the JWT; `providerSpecificData {profileArn, authMethod, provider}` | Same fields. Kiro tokens are opaque, so the JWT rarely names the account; the identity is asked up front as for the device flow. |
| `connectionsRepo.js:228-283`: an oauth sign-in with a stored email renews that row | `Flow.RenewsSameAccount` → the device rule (`deviceMatch`): same identity is `updated` in place, another is `added`. |
| `KiroAuthModal.js:243-273`: the Google/GitHub buttons are rendered with `hidden` | Shown, as "Sign in with Google" and "Sign in with GitHub", because every step behind them is wired and tested (`p219_kiro_social_test.go`). Each is fixed to its method; there is no field to retype it. |
| no pin equivalent | Reauthenticate renews a Google/GitHub connection with the method that made it (`signInMethod` on the connection read model) and refuses any other Kiro connection (`oauth_reauth_unsupported`), so a Builder ID row is never rewritten as a social one. |
| `tokenRefresh/providers.js:316-324`, `providers/index.js:175-179`: look up a missing ARN (`fetchKiroProfileArn`) | `kiro.go:kiroProfilePatch` / `kiroProfileARN` on refresh, device approval, token import and social sign-in; a stored ARN is never replaced or looked up; best effort, as in the pin. The pin's social exchange stores a missing ARN as missing (`social-exchange/route.js:40-52`, `testStatus: "active"`), serves without the profile header (`open-sse/executors/kiro.js:276-279`) and fills it at the first refresh; Routeweft looks it up at sign-in too, because `wire/kiro.go:kiroTarget` refuses to serve a Kiro connection without one. When the lookup also finds none, the credential is still saved (as in the pin, so the refresh can retry), but the result carries `profileArnMissing` and the dashboard reports **Not usable yet** with the recovery: name the ARN (`PATCH /connections/{id}` `secret.providerData.profileArn`), or Reauthenticate (`TestKiroSocialMissingProfileARNIsSurfacedAndRecoverable`). |

AWS SSO (Builder ID and IDC), API-key, token import, CLIProxyAPI (`external_idp`)
and their refresh branches are unchanged; the parity gate still reads `device_code`
for kiro (`Registry.OnboardingFlows`), with the browser sign-in carried as
`Flow.Browser`.

