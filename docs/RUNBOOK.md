# Routeweft Runbook

Status: **Operational contract**

This runbook describes Routeweft as a standalone service.

Commands shown here are target commands. Implementation PRs must keep this document synchronized as the CLI/container becomes executable.

## 1. Runtime identity

Defaults:

```text
binary:     routeweft
listen:     :21128
data dir:   /var/lib/routeweft
database:   /var/lib/routeweft/routeweft.sqlite
health:     /health/live
readiness:  /health/ready
admin API:  /admin/v1/*
```

Environment prefix: `ROUTEWEFT_`.

## 2. Standalone/coexistence rule

Routeweft must use its own:

- container;
- port;
- data directory;
- SQLite database;
- writable provider credentials;
- reverse-proxy route.

Never mount a LiteRouter data volume into Routeweft.

Never run two Routeweft processes as writable owners of one SQLite file.

If Routeweft and another router use the same OAuth identity during evaluation, only one may own refresh-token rotation unless the provider explicitly guarantees safe concurrent refresh.

## 3. First boot

Example environment:

```bash
ROUTEWEFT_LISTEN=:21128
ROUTEWEFT_DATA_DIR=/var/lib/routeweft
ROUTEWEFT_LOG_LEVEL=info
ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD='use-a-secret-source'
# Reverse proxies allowed to vouch for a client address (login throttle only).
# Empty by default: no peer is trusted. See section 7 and SPEC 12.3.
ROUTEWEFT_TRUSTED_PROXIES=
ROUTEWEFT_MAX_BODY_BYTES=134217728
ROUTEWEFT_CORS_ORIGINS='https://console.example'
# Operator-facing origin used as the provider sign-in callback. Required only for
# the providers whose flow sends a redirect URI it takes from here (gemini-cli,
# antigravity, iflow, gitlab, cline, clinepass, kimchi, trae, windsurf,
# xiaomi-mimo). Flows that hardcode their registered redirect (claude, codex, xai),
# never use one (device flows, zed) or have no browser step (cursor) start without
# it. Never derived from request headers; sign-in for a flow that needs it fails
# with oauth_redirect_unconfigured when it is unset.
ROUTEWEFT_PUBLIC_ORIGIN='https://console.example'
ROUTEWEFT_UI_DIR=/ui
# Google-family sign-in (gemini-cli, antigravity). No built-in default: see
# docs/SPEC.md 19.1 "Deliberate divergence: Google OAuth client".
ROUTEWEFT_GOOGLE_OAUTH_CLIENT_ID='use-a-secret-source'
ROUTEWEFT_GOOGLE_OAUTH_CLIENT_SECRET='use-a-secret-source'
# MiMo Desktop discovery (#296, BDR-028). Leave false unless MiMo Desktop runs
# as the same OS user on this machine and the operator wants it imported.
ROUTEWEFT_ALLOW_HOST_CREDENTIAL_DISCOVERY=false
```

`ROUTEWEFT_ALLOW_HOST_CREDENTIAL_DISCOVERY` (`--allow-host-credential-discovery`)
is the host's consent to one read of host secrets: MiMoCode's `auth.json` and the
Xiaomi account cookies in MiMo Desktop's cookie store, under the home directory of
the user Routeweft runs as. Even when it is true, nothing is read until an admin
ticks the consent box on the Xiaomi MiMo connection panel ("Find on this server",
or "Also attach MiMo Desktop's account session" on the sign-in). What is read is
sealed into the connection and never shown in the dashboard. In the production
container there is no Desktop profile, so leave it false there; a discovery then
answers `host_discovery_disabled`. To renew a session, quit Desktop (it locks its
cookie store while it runs) and find it again.

Quota reads can also renew it (#299): "Renew session from this server" on a
Xiaomi MiMo connection row (`PUT /admin/v1/connections/{id}/desktop-session-renewal`
with `{"enabled":true,"consent":true}`) asks for confirmation and then lets that
connection's quota reads read Desktop's session when the connection has none or
its session stops working, at most every 30 minutes, sealing what they read.
Every attempt is recorded in the connection before the store is opened, so
restarting Routeweft does not reset the 30 minutes, and a read that found
nothing counts. The row shows when a session was last read. "Stop session renewal"
(`{"enabled":false}`) turns it off at once and works even after the host flag
is turned off; it leaves the session already sealed in place. Restarting
without the host flag stops every such read.

`ROUTEWEFT_UI_DIR` (`--ui-dir`) points at the built Vite output; the Go binary
then serves the dashboard at `/`. The image sets it to `/ui`. UI routes are
hash-based, so only `/` returns `index.html`; `/assets/*` is immutable-cached and
unknown paths (and GET/HEAD under `/admin/`, `/v1/`, `/api/`) return 404. A path
with `.`/`..` segments or repeated slashes gets Go's `ServeMux` canonical redirect
(301 on go1.22, 307 on later toolchains) to the cleaned same-origin path (for
every route, not only the UI), which is then
404 unless it names a built file. Symlinks inside the UI directory are followed
only while they resolve inside it (the directory may itself be a symlink); a link
to anything outside is a 404, so nothing outside the UI directory is read. Leave it
unset to serve API only and let Caddy serve the assets, all of them: the dashboard
also loads its language catalogs from `/i18n/literals/<locale>.json` (34 files from
`ui/public/i18n/literals/`, copied to the UI root by the build); a proxy that serves
the assets but not that path leaves the dashboard in English. The language is the
`locale` cookie in the operator's browser (path `/`, one year); the server never reads
it. A directory without
`index.html` logs an error and disables the UI; the API keeps running.

The dashboard keeps one Server-Sent Events connection open on `/admin/v1/events` (SPEC 22.1). A reverse
proxy in front must pass it through unbuffered and must not close it for being idle sooner than the 15 s
heartbeat. When it cannot, the pages say "Live updates unavailable · refreshing every 15 s" and re-read on a
timer, so nothing is lost, only delayed.

The default request-body limit is 128 MiB. Cross-origin requests are denied
unless `ROUTEWEFT_CORS_ORIGINS` has a comma-separated allowlist. Routeweft does
not enable credentialed CORS cookies.

Expected first boot:

1. create data directory with restrictive permissions;
2. create/open SQLite;
3. run migrations;
4. create initial admin only when database has no admin;
5. compile RuntimeSnapshot;
6. start workers;
7. readiness becomes healthy.

Remove the bootstrap password from deployment environment after the admin credential exists if the implementation does not require it for subsequent boots.

The bootstrap variables are read only while the database has no admin account and
are ignored on every later boot, so removing them is safe and recommended. There
is no default password and no reset endpoint. If the password is lost: stop the
service, delete the `admin_users` row in the SQLite file, set both bootstrap
variables, start, then remove them again (a backup restore does not help, it
carries the same hash). Login cannot be disabled; `requireApiKey=false` is the
only supported open mode and covers inference, never `/admin`. Policy and
rationale: SPEC 12.2.

Evidence (#282, C09): concurrent first boots create exactly one admin
(`TestConcurrentBootstrapProvisionsExactlyOneAdmin`); the password exists only as
a salted PBKDF2-SHA256 hash at 600000 iterations and is absent from log output,
the console-log API, the login response and the SQLite and WAL bytes
(`TestC09BootstrapPasswordNeverLeavesTheHash`); a later boot with different
bootstrap values neither adds an admin nor changes the password, and a boot with
none still logs in (`TestC09BootstrapActsOnlyWhileNoAdminExists`); a
whitespace-only username fails startup before readiness while surrounding
whitespace is trimmed (`TestC09BootstrapRefusalLeavesTheInstallLockedAndNotReady`);
a fresh data directory is created `0700` with the database `0600`
(`TestC09FreshDataDirectoryIsCreatedPrivate`).

## 4. Git worktree operating model

Routeweft development uses one dedicated worktree per feature/PR.

The primary checkout is coordination-only. Do not implement sprint features directly there.

Invariant:

```text
1 feature / PR = 1 branch = 1 worktree
```

### Create a feature worktree

```bash
git fetch origin
git switch main
git pull --ff-only origin main
git worktree list

mkdir -p ../routeweft-wt
git worktree add ../routeweft-wt/s<S>-<feature> \
  -b sprint/<S>-<feature> origin/main
```

Example:

```bash
git worktree add ../routeweft-wt/s1-repo-scaffold \
  -b sprint/1-repo-scaffold origin/main
```

Then work only from that worktree:

```bash
cd ../routeweft-wt/s1-repo-scaffold
git status --short
git branch --show-current
git rev-parse --show-toplevel
```

All edits, builds, tests, commits, pushes, and review fixes for that PR occur from this directory.

### Resume an existing PR

Run:

```bash
git worktree list
```

Reuse the worktree already bound to the PR branch. Never maintain two mutable worktrees for the same feature branch.

### Parallel features

Parallel work is permitted only when the work is explicitly independent.

Each parallel PR gets its own sibling worktree and branch:

```text
../routeweft-wt/s3-routing-core
../routeweft-wt/s3-combo
../routeweft-wt/s7-ui-overview
```

Each worktree must use separate disposable runtime data when running Routeweft locally.

### Dependent features

Default flow:

```text
PR A approved/merged
 -> update primary checkout main
 -> create PR B worktree from new origin/main
```

Stacked branches/worktrees are allowed only when `docs/SPRINT_PLAN.md` explicitly declares the dependency.

### Review fixes

All review fixes are made in the original PR worktree. Do not patch a PR from another feature's worktree or the coordination checkout.

### Cleanup

After merge/close and after verifying no local work remains:

```bash
cd <primary-routeweft-checkout>
git fetch origin
git switch main
git pull --ff-only origin main
git worktree remove ../routeweft-wt/s<S>-<feature>
git branch -d sprint/<S>-<feature>
git worktree prune
```

Never force-remove a worktree containing uncommitted or unpushed work.


## 5. Local development

Target commands:

```bash
go test ./...
go run ./cmd/routeweft serve
```

UI:

```bash
cd ui
npm ci
npm run dev
```

Development UI may proxy `/admin`, `/v1`, and health paths to the Go service.

Use a disposable data directory:

```bash
export ROUTEWEFT_DATA_DIR="$PWD/.data-dev"
```

Never point development at the production DB.

## 6. Production container

Target Compose shape:

```yaml
services:
  routeweft:
    image: ghcr.io/raufimusaddiq/routeweft:<immutable-tag>
    restart: unless-stopped
    environment:
      ROUTEWEFT_LISTEN: ":21128"
      ROUTEWEFT_DATA_DIR: /var/lib/routeweft
      ROUTEWEFT_LOG_LEVEL: info
    volumes:
      - routeweft-data:/var/lib/routeweft
    expose:
      - "21128"
```

The production image should be referenced by an immutable version/SHA-derived tag for daily-drive deployments.

The final scratch filesystem contains `/routeweft`, the static UI under `/ui`,
and `/etc/ssl/certs/ca-certificates.crt` from Alpine's `ca-certificates` package.
It contains no shell, Node runtime, or Go toolchain. The build loads the bundle
with Go's system trust loader in a scratch stage and rejects an empty or
unloadable store. The final stage copies that validated bundle; the test binary
is not shipped. Outbound TLS uses system roots with certificate verification.

`ROUTEWEFT_LOG_LEVEL` sets the JSON service log threshold (default `info`;
`debug`, `warn`, and `error` are supported; the flag `--log-level` overrides it).
Invalid levels stop startup before opening data. There is no separate "verbose"
switch: the per-request `request completed` record always carries the token and
cache counts at `info` (SPEC 12.3).

## 7. Reverse proxy

Recommended topology:

```text
internet/private LAN
        |
      Caddy
        |
    Routeweft:21128
```

Routeweft ignores forwarded-IP headers from every peer unless the peer is listed in
`ROUTEWEFT_TRUSTED_PROXIES` (flag `--trusted-proxies`; comma- or space-separated IPs
and CIDRs, empty by default, a `/0` prefix is refused). The setting changes one
thing: which address keys the admin login and password-change throttle (SPEC 12.3).
Set it to the address of the proxy as Routeweft sees it, for example
`ROUTEWEFT_TRUSTED_PROXIES=127.0.0.1,::1` for the Caddy on the same host above, or
the Docker network's gateway/subnet when the proxy is another container. Check the
proxy appends the connecting address to `X-Forwarded-For` (Caddy, nginx
`proxy_add_x_forwarded_for` and Traefik do); the client is the first hop from the
right that is not itself a listed proxy, so a client cannot choose its bucket by
prepending identities. Do not list a network that clients can reach directly: a
listed peer can name any client.

`X-Real-IP` is read only from a listed proxy and only when the request carries no
`X-Forwarded-For` at all; it then must hold exactly one valid address. That is safe
only if the proxy overwrites `X-Real-IP` with the connecting address (nginx
`proxy_set_header X-Real-IP $remote_addr` does; Caddy's `reverse_proxy` does not set
it, so a client-supplied value would pass through). Prefer a proxy that appends to
`X-Forwarded-For`, which always wins over `X-Real-IP`.

Consequence without the setting: every client behind one reverse proxy is one socket
peer and shares one admin-login throttle bucket (8 failures per 5 minutes). One
client's failed logins can lock all operators out of the login route for up to 5
minutes (`429` with `Retry-After`); it never grants access. With it, each client
behind a listed proxy has its own bucket. In both cases a forged `X-Forwarded-For`,
`X-Real-Ip` or `Forwarded` value from a peer that is not a listed proxy cannot move a
request into another bucket or out of a full one, and no client address is persisted
in usage, request details or any other table. Evidence:
`TestForwardedHeadersNeverChangeTheThrottleBucket`,
`TestUntrustedPeerCannotChooseItsThrottleBucket` and
`TestTrustedProxyGivesEachClientItsOwnLoginBucket` (`internal/control/api`),
`TestC10TrustedProxyKeysTheLoginThrottleOnTheRealClient` and
`TestC10UntrustedPeerCannotEscapeTheLoginLockoutByForging` (`internal/app`, real
listener) and `TestForwardedHeadersAreNotRecordedAnywhere` (`internal/app`).

Do not expose an unprotected admin API directly to the public network.

### Ingress limits and deployment evidence (#78 C08)

Routeweft has no general inference request-rate limiter. Its admin login and
password-change handlers share a process-local failed-attempt throttle:

- Key: the socket remote IP with the ephemeral port removed, or, when that peer is
  listed in `ROUTEWEFT_TRUSTED_PROXIES`, the client it names in `X-Forwarded-For`
  (SPEC 12.3, `internal/clientip`); no username, API key or `Forwarded` component.
- Policy: eight failed credential checks in a fixed five-minute window starting
  at the first failure. The eighth failure still returns 401; subsequent checks
  return 429 with JSON `error.code=too_many_attempts` and `Retry-After` equal to
  remaining seconds rounded up. A successful check clears the bucket; expiry
  or process restart also clears it (`internal/control/api/throttle.go:11-67`,
  `api.go:337-359`, `password.go:32-52`). This is a failure throttle, not a
  requests-per-second or concurrent-password-check budget.
- Behind Caddy, users reaching Routeweft from the same proxy IP share that
  bucket. Forwarding a different client-IP header does not separate them.
  Provider quota/cooldown handling and upstream 429 responses are separate from
  client admission limits (`internal/routing/cooldown.go:38`).

The pinned oracle `1c3b9f47` has an application login limiter
(`src/app/api/auth/login/route.js:14-20,72-82`). It contains no Caddyfile or
Caddy rate-limit policy. Its `scripts/edge-swap-rehearsal.sh:9,17-19` references
an external Caddyfile; that reference proves neither its contents nor a deployed
limit. No production config was inspected for C08.

The [#74 deployment report](https://github.com/raufimusaddiq/routeweft/issues/74#issuecomment-6259837234)
records a Caddy vhost, credential-safe logging, and a synthetic Authorization
redaction check on 2026-10-19. It does not record a Caddy rate limiter or an
`X-Sync-Secret` redaction test. Its recorded image
`sha-10ce86680675a309a2452188d2bc62ff80430935` already includes the eight-failure,
five-minute login throttle (`internal/control/api/throttle.go:11-14` and
`api.go:303-324` at that commit), so #74's "no rate limit" gap note is incorrect
and superseded. Caddy rate limiting remains unverified.

Deployment ownership stays with #74: establish the Caddy build/modules and
effective vhost policy, then choose and test edge rate/burst/concurrency budgets
for admin authentication and inference separately. Key edge limits on the
verified client address at that boundary, accepting forwarded identities only
from explicitly trusted peers. Preserve streaming connections and health checks;
verify both allowed traffic and 429/Retry-After behavior with synthetic requests.
These budgets depend on deployment capacity, client population and proxy trust,
so this repository ships no guessed Caddy directive or application limiter.
The #74 owner decision to omit Caddy basic auth remains in effect; see its
[scope amendment](https://github.com/raufimusaddiq/routeweft/issues/74#issuecomment-6259751314).

### Client token-list ownership (#78 C08)

No token-sync route, handler, upstream call or `X-Sync-Secret` implementation was
found in oracle `1c3b9f47`. Reproduce the absence check against tracked source:

```bash
git -C /opt/9router grep -n -i -E 'token.?sync|sync.?token|X-Sync-Secret' 1c3b9f47 -- .
git -C /opt/9router ls-tree -r --name-only 1c3b9f47
```

The grep returns no matches; the tree contains no token-sync route or Caddyfile.
The oracle does have local API-key listing/creation
(`src/app/api/keys/route.js:7-11,18-37`) and provider OAuth refresh
(`open-sse/services/tokenRefresh.js:3-21`), which are different operations.

Routeweft has no service that pushes a token list into inference clients. Clients
keep the Routeweft key supplied by their operator; no list synchronization is
needed for the standalone gateway. Endpoint & Key reads key metadata from
`GET /admin/v1/keys` on mount/page changes, reloads after creation and updates
local state after edits/deletion (reloading if deletion changes the page;
`ui/src/EndpointAndKey.tsx:45-74,119-212`).
Another open dashboard does not receive automatic key-list updates; reload it
to see changes made elsewhere. Full key secrets are returned only at creation.
Key mutations compile, commit and atomically publish the serving snapshot
(`internal/runtime/catalog_update.go:20-107`, `manager.go:57-125`); inference
checks that snapshot (`internal/ingress/ingress.go:843-862`). No external sync
is required for new requests to observe revocation when API-key enforcement is
enabled. Provider credential refresh is separate from client API-key management
(PRD §8).

If a separately deployed token-sync service exists, #74 owns its inventory,
source/config pin, callers, authentication, rate limits and log-redaction
evidence. C08 establishes an application-source absence, not verified deployment
parity or permission to create another endpoint.

## 8. Smoke checks

After start/upgrade:

```bash
curl -fsS http://127.0.0.1:21128/health/live
curl -fsS http://127.0.0.1:21128/health/ready
```

Then verify through normal client auth:

- model list;
- one native streaming request;
- one translated request if used;
- one Combo route if configured;
- Usage append;
- admin login;
- provider health/quota;
- config mutation updates active snapshot revision.

Do not use a destructive OAuth refresh smoke test casually on production credentials.

TLS trust can be checked without contacting an upstream:

```bash
go test ./internal/transport -run '^TestSystemTLSRoots$' -count=1 -v
```

For an optional public HTTPS check, run this from a source checkout on the
operator host:

```bash
ROUTEWEFT_TLS_SMOKE=1 go test ./internal/transport -run '^TestPublicHTTPSReachability$' -count=1 -v
```

To use the deployed image's filesystem and trust store, compile the test binary
for the image's OS/architecture and mount it into a disposable container:

```bash
CGO_ENABLED=0 GOOS=linux go test -c -o /tmp/routeweft-tls-check ./internal/transport
docker run --rm --entrypoint /tls-check \
  --mount type=bind,src=/tmp/routeweft-tls-check,dst=/tls-check,readonly \
  -e ROUTEWEFT_TLS_SMOKE=1 \
  ghcr.io/raufimusaddiq/routeweft:<immutable-tag> \
  -test.run='^(TestSystemTLSRoots|TestPublicHTTPSReachability)$' -test.v
```

The opt-in check sends an unauthenticated HEAD to `https://example.com` using
the direct pooled transport and verifies the TLS chain. It does not use provider
credentials, configured outbound proxies, or the service's database. CI and
image builds only load the trust store; they make no public HTTPS probe and do
not prove DNS, network egress, provider authentication, or inference success.

## 9. Health interpretation

### Liveness unhealthy

Treat as process failure.

Actions:

1. inspect process/container logs;
2. check crash/OOM;
3. restart only after capturing failure context when practical.

### Readiness unhealthy but live

Likely causes:

- DB/migration failure;
- invalid configuration;
- snapshot compile failure;
- restore/reload in progress;
- critical degraded state chosen by implementation.

Do not route new inference traffic until readiness recovers.

## 10. Logs

Production logging should be structured.

`GET /admin/v1/logs` exposes a bounded, newest-first console view of recent
process log messages for authenticated operators. Only the message text is
retained; structured attributes are intentionally omitted so a secret cannot be
exposed by the read model. Credential-shaped message text is redacted or
dropped before it becomes visible.

Minimum fields where relevant:

- timestamp;
- level;
- component;
- request ID;
- provider;
- model;
- account/connection ID (non-secret identifier);
- route mode;
- attempt;
- status;
- duration.

Never log:

- Authorization value;
- raw API key;
- refresh token;
- provider cookie;
- admin password;
- full sensitive headers.

Prompt/request bodies are captured only through explicitly enabled request-detail observability with redaction/retention limits.

## 11. Backup

Preferred:

```bash
routeweft backup --output /backup/routeweft-$(date +%Y%m%d-%H%M%S).sqlite
```

Backup uses SQLite's online backup API, validates the staged database, switches the artifact to rollback-journal mode, then publishes the database and metadata without overwriting existing files. It writes a sibling `<file>.meta.json` containing Routeweft version/commit, schema version, UTC timestamp, and config revision. Keep the database and metadata together.

The metadata includes Routeweft version/commit, schema version, timestamp, and config revision.

The standalone `routeweft backup` command opens the database as a separate
process and may fail busy/locked while the server is running; use the admin
backup API for an online backup from the active process.

```text
GET  /admin/v1/backup               -> zip {routeweft.sqlite, routeweft.sqlite.meta.json}
POST /admin/v1/backup/restore/check -> validate an uploaded archive without activating it
POST /admin/v1/backup/restore       -> validate and activate an uploaded archive
```

The admin backup API uses the same online backup API as the CLI, so it is safe
while serving inference. It returns a zip stream of the validated artifact and
its metadata and never overwrites an existing file. The restore endpoints are
session-gated and same-origin protected.

Periodically rehearse restore on a disposable host/data directory.

## 12. Restore

```bash
curl -fsS -b <session-cookie> -o backup.zip https://host/admin/v1/backup
curl -fsS -b <session-cookie> --data-binary @backup.zip https://host/admin/v1/backup/restore/check
curl -fsS -b <session-cookie> --data-binary @backup.zip https://host/admin/v1/backup/restore
```

Validation first:

```bash
routeweft restore --input /backup/file.sqlite --check
```

`--check` copies the candidate into a private temporary staging directory, checks integrity and Routeweft schema compatibility, applies supported migrations to that copy, and compiles a candidate RuntimeSnapshot. The supplied file remains unchanged.

The admin restore API activates a validated candidate: it sets readiness false,
drains in-flight requests for at most 30 seconds (then closes remaining
connections), stops the background schedulers, flushes accepted
telemetry, preserves a pre-restore rollback database (`routeweft.sqlite.pre-restore-<timestamp>`),
atomically replaces the live database, recompiles state, and returns to ready.
Admin sessions are invalidated, so operators log in again after restore.
Invalid candidates never touch the live database. If neither candidate
activation nor rollback recovery can be validated, readiness remains false and
the process exits with an error. The CLI `restore` command still validates
only (`--check`); it refuses to activate so two writers never contend for one
SQLite file. Archive upload/expanded size is capped at 8 GiB.

Activation restore should be performed during a controlled window.

Required behavior:

1. stage candidate;
2. integrity check;
3. migrate candidate if supported;
4. compile candidate snapshot;
5. quiesce mutations/telemetry;
6. preserve current rollback DB;
7. activate;
8. publish snapshot;
9. readiness healthy;
10. resume traffic.

If validation fails, leave the current DB untouched.

## 13. Upgrade

For an immutable image upgrade:

1. read release notes/schema impact;
2. verify current backup;
3. run a fresh backup;
4. record current image SHA/tag;
5. pull new image;
6. stop Routeweft cleanly;
7. start new image against the same Routeweft data volume;
8. wait for readiness;
9. run smoke checks;
10. monitor error/fallback/cache/telemetry signals.

Do not combine an upgrade with unrelated database surgery.

### Deploy-time image retention (#78 C08)

For each Routeweft-owned service, retain the running immutable release and one
distinct, recorded rollback release, locally and in the registry. Routeweft's
binary and static UI are one image: `ghcr.io/raufimusaddiq/routeweft:sha-<full-commit>`.
Record the full tag and resolved digest for both releases in deployment records.
External helpers need their own service-specific pair; do not treat unrelated
services or LiteRouter images as Routeweft cleanup candidates.

Before dispatching an upgrade or rollback, verify the target tag and the recorded
rollback tag exist in the registry. Example read-only checks on the operator
host, with both variables set to full immutable image references:

```bash
docker manifest inspect "${ROUTEWEFT_TARGET_IMAGE:?set full target image reference}" >/dev/null
docker manifest inspect "${ROUTEWEFT_ROLLBACK_IMAGE:?set full rollback image reference}" >/dev/null
docker manifest inspect --verbose "$ROUTEWEFT_TARGET_IMAGE"
docker manifest inspect --verbose "$ROUTEWEFT_ROLLBACK_IMAGE"
docker image inspect "$ROUTEWEFT_ROLLBACK_IMAGE" --format '{{.Id}} {{json .RepoDigests}}'
```

Compare resolved digests with the release record; a missing tag, registry error,
digest mismatch or missing local rollback image blocks dispatch. Prepare the
local rollback image before proceeding, and recheck it. A first deployment has
no previous release: record that limitation and skip cleanup until a distinct
rollback release exists. Two tags for the same image do not provide a rollback.

Only after readiness and smoke checks pass may the successful deployment update
the retained pair and consider cleanup. Cleanup belongs to that deployment,
never a timer or broad prune. Enumerate only the exact Routeweft-owned image
repository and remove only individually reviewed obsolete release references
outside the retained pair. Check all containers, including stopped ones, before
removal; never force-remove an image or remove one still referenced by a
container. If ownership or the retained pair is uncertain, skip cleanup.

Never run `docker system prune`, `docker image prune`, `docker volume prune`,
`docker compose down -v`, or registry-wide retention rules as release cleanup.
Never prune production volumes, Routeweft's data directory, SQLite/WAL/SHM,
credential state, backups, or another service's images/data. Image rollback
does not reverse a database migration; §14 still governs schema compatibility.

Richmod's
[`infra/deploy/post-deploy.sh:12-35`](https://github.com/raufimusaddiq/richmod/blob/acb8d1de5f348739a697712449bd60a5c22a821c/infra/deploy/post-deploy.sh#L12-L35)
is a shape reference: health gates, keep current/previous tags, and scoped image
removal. Its four repositories and cleanup loop are not Routeweft deployment
code and do not supply the registry/digest preflight above. #74 owns wiring and
rehearsing these gates in the actual deployment and registry policy. C08 adds
the operating contract only; it performs no deploy, reload or cleanup.

## 14. Rollback

If the new Routeweft release fails:

1. stop edge traffic/new requests;
2. drain/stop new Routeweft;
3. capture logs/version/config revision;
4. determine whether DB schema is backward-compatible;
5. if compatible, start previous immutable Routeweft image on current DB;
6. if incompatible, restore the pre-upgrade Routeweft backup;
7. wait for readiness;
8. run smoke checks;
9. restore edge traffic.

Release policy should prefer additive/backward-compatible migrations during the daily-drive stabilization window.

Schema v12 (#241) is forward-only. It drops `UNIQUE(node_id, identity)` from
`provider_connections` so the built-in Codex node can hold several connections
with one email, as LiteRouter does, and keeps the rule for every other node by
trigger. A release built before v12 refuses to start on a v12 database (schema
newer than supported), and once two Codex connections share an identity the
pre-v12 schema cannot hold them, so rolling back past v12 always means restoring
the pre-upgrade backup (step 6) and losing connections added since.

The same release records how each new Codex connection was signed in (SPEC
§19.1). A Codex connection stored before it, whose credential says
`authMethod: access_token`, is never updated by a later sign-in or bulk item for
the same account: a new connection is added beside it. If the operator knows it
was a bulk re-import, not a token import, they can delete it after the new
connection works.

Schema v13 (#220) only adds columns to `provider_connections` (validation
status, last test, last error, model count, auth origin, credential expiry and
credential revision and a keyed credential digest;
SPEC §33.2). Existing connections start `untested`. No column holds credential
material. It is forward-only like every migration: a release built before v13
refuses to start on a v13 database (schema newer than supported), so rolling
back past v13 means restoring the pre-upgrade backup (step 6); connections
added since are lost, and the recorded statuses with them.

## 15. SQLite maintenance

Monitor:

- DB size;
- WAL size;
- busy/lock errors;
- checkpoint duration;
- telemetry batch duration;
- disk free space.

Do not run manual VACUUM/checkpoint commands during heavy production traffic without understanding impact.

Use Routeweft-owned maintenance commands once implemented.

## 16. Incident: elevated 5xx

Check:

1. whether errors are one provider/model or all;
2. upstream status;
3. auth refresh failures;
4. cooldown/fallback decisions;
5. proxy failures;
6. SQLite/readiness only if control state is affected;
7. recent config revision/change;
8. resource pressure.

If one provider is broken, disable/reroute that provider rather than restart the entire router unless required.

## 17. Incident: OAuth refresh failures

Actions:

1. identify provider/account;
2. stop repeated refresh storms;
3. inspect error without logging secrets;
4. verify current durable credential generation;
5. re-auth/import account if provider invalidated the refresh token: on the
   connection's row use **Reauthenticate** (browser sign-in providers), which
   renews that connection in place and refuses a different account, saving
   nothing; a Codex sign-in or bulk item for an already connected account also
   updates its row rather than adding one (SPEC §19.1). A Gemini CLI or
   Antigravity connection whose requests fail with "has no projectId" needs its
   Code Assist project: name it on the sign-in result, or Reauthenticate once
   the account has one (SPEC §19.1, #216). A device-code provider (GitHub,
   Kimi, Kilo Code, CodeBuddy, Kiro, Qoder, Grok CLI) has no Reauthenticate:
   approve a new device code as the same account, which renews its row in
   place (SPEC §19.2, #217). A GitHub result that says no Copilot token was
   issued means the account has no Copilot entitlement;
6. confirm RuntimeState reflects new credential;
7. run a minimal provider test.

Never restore an older DB solely to recover an OAuth token unless you know the older refresh token remains valid.

## 18. Incident: SQLite busy/locked

Check:

- accidental second Routeweft writer;
- long transaction;
- telemetry batch behavior;
- backup/restore activity;
- disk/filesystem health.

A normal single-instance deployment should not repeatedly hit sustained writer lock failures.

Do not add Redis/Postgres as an emergency workaround; fix the contention/root cause.

## 19. Incident: telemetry pressure

Check:

- queue depth;
- diagnostic drops;
- critical backpressure;
- emergency flush count;
- lost-critical counter;
- SQLite batch latency;
- disk space.

Inference may remain available in degraded mode according to SPEC, but lost critical accounting must be visible.

## 20. Incident: prompt-cache regression

Symptoms:

- cache-read tokens drop sharply on otherwise stable conversations;
- cache-create tokens spike;
- Claude costs/TTFT change unexpectedly.

Actions:

1. isolate provider/model;
2. compare request-transform flags;
3. inspect outbound normalized request in sanitized diagnostics;
4. verify cache anchoring is after all savers;
5. compare N/N+1 fixture behavior;
6. roll back a transform/cache change if regression is confirmed.

## 21. Incident: runaway Fusion cost/load

Actions:

- inspect Combo/Fusion concurrency;
- panel count;
- panel hard timeout;
- judge choice;
- repeated retries;
- response sizes.

Disable/change the affected Combo strategy if needed.

Do not globally disable unrelated routing.

## 22. Security rotation

For compromised client API key:

- revoke it in control plane;
- snapshot publication makes new requests reject it immediately.

For compromised provider credential:

- disable connection;
- rotate/re-auth provider;
- verify old credential cannot be selected.

For compromised admin credential:

- rotate password/session state;
- invalidate active admin sessions as supported.

## 23. Incident: failed restore activation

An admin restore sets readiness false, drains requests, and preserves the prior
database as `routeweft.sqlite.pre-restore-<timestamp>` before swapping files. If
activation fails after file replacement, the process stops instead of resuming
against an unknown database.

Actions:

1. read the startup/restore error and current schema/config revision;
2. confirm the preserved `routeweft.sqlite.pre-restore-*` artifact is present;
3. stop the process and copy that artifact aside as the rollback database;
4. replace `routeweft.sqlite` with the rollback copy (and remove stale
   `routeweft.sqlite-wal`/`-shm` sidecars);
5. start Routeweft, wait for readiness, and run smoke checks;
6. keep the failed restored database for diagnosis, then re-stage the restore
   from a validated backup once the cause is fixed.

Never leave a stale `-wal`/`-shm` beside a replaced database file.

## 24. Disaster recovery minimum

Maintain:

- current immutable image reference;
- at least one known-good previous image;
- current DB backup;
- previous pre-upgrade backup;
- documented environment/config;
- Caddy/reverse proxy config.

A fresh server should be recoverable using only Routeweft artifacts, without LiteRouter.

## 25. Parallel-run evaluation (R11, not a cutover)

Read the current per-row dispositions in `docs/PARITY.md` first. R11 permits a
limited, opt-in evaluation alongside LiteRouter v1. It does not certify full
feature parity, authorize production deployment, or retire v1. Keep v1 running
with its existing state and client endpoints throughout the evaluation.

### Readiness reached and still required

The configured app serves native Chat, Responses/Compact, Messages, Gemini,
Ollama and System One, with bounded account fallback and snapshot-backed
configuration. Chat/Responses translation, ordered savers/final cache, typed
System One transports, access-aware aliases/tags/info, catalog workflows and
first-token probes have tests. External helper diagnostics, proxy-pool tests,
API-key usage attribution and console clear are implemented.

Do not select a workload requiring the unresolved rows: actual Combo/Fusion/
capacity serving, Messages/other protocol translators, specialized OAuth/wire/
quota modules, native Headroom/PXPIPE compatibility, complete cost/TTFT/details/
live UI. Migration is manual and gradual; v1 history stays in v1 permanently.
Provider readiness is not fully advertised by the current registry/UI; manually
restrict evaluation to the specific provider/account/protocol combinations the
operator has validated.
An enabled provider or a successful model listing is not inference validation.

Standalone media, hidden providers, cloud/tunnel lifecycle, Redis and
multi-replica operation stay excluded by PRD 4.2 / BDR-005/016. Remote helper
install/start/stop/restart and host auto-import stay unavailable, except MiMo
Desktop discovery, which needs `ROUTEWEFT_ALLOW_HOST_CREDENTIAL_DISCOVERY=true`
and per-request consent (§3, BDR-028); keep it false for the evaluation. Manage any
external helper locally under a separate host policy. Never substitute an
arbitrary remote command endpoint.

### Prepare independent state

1. Choose a separate Routeweft container/process, listen port (default 21128),
   private data volume, browser origin and reverse-proxy route. Do not mount or
   copy a live v1 database, credential cache, writable helper state or WAL/SHM.
   One Routeweft writer owns its SQLite file (BDR-001/017).
2. Generate a fresh 32-byte credential sealing key using a trusted secret
   source. Set `ROUTEWEFT_CREDENTIAL_KEY`, bootstrap username/password and
   `ROUTEWEFT_DATA_DIR` for Routeweft only. Preserve that key with Routeweft's
   recovery material; never reuse v1's master key. Do not paste values into
   commands recorded in shell history, fixtures, issue comments or logs.
3. Recreate provider nodes, independently provisioned accounts, manual models,
   aliases and settings through Routeweft's own UI/API. Reissue client API keys;
   do not import v1 plaintext keys or assume digest/schema compatibility. Leave
   v1 history in v1 permanently; never combine the two accounting bases.
   The gradual manual migration reference (mapping, units, refusals, key reissue,
   reauthorization, history) is `docs/V1_MIGRATION.md`.
   Snapshot publication and an independent restart must preserve the new config.
4. Prefer separate provider identities/keys. If identities must be shared,
   designate exactly one refresh-token rotation owner and verify the provider's
   concurrency policy first. Shared identities also share upstream quotas;
   duplicated evaluation calls may consume tokens or incur charges twice.
5. Use the production image's CA trust bundle and existing SSRF policy. Keep
   `ROUTEWEFT_ALLOW_PRIVATE_UPSTREAMS=false` unless a trusted local operator
   explicitly needs LAN helpers/models (a local Ollama host, or an Azure or Kimchi
   endpoint on a private address; saving such a URL grants nothing, requests still
   need this flag). Permit only the required browser
   origins with `ROUTEWEFT_CORS_ORIGINS`; do not weaken redirect revalidation.
   Do not forward v1 session cookies, Authorization, proxy userinfo, tokens or
   `X-Sync-Secret` into an application/access log. #74 owns deployed ingress
   redaction and image-retention evidence; R11 has not verified that deployment.

### Evaluate without moving ordinary traffic

1. Record the independent image commit, schema/config revision and Routeweft
   backup. Check `/health/live` and `/health/ready` on the separate port. Leave
   ordinary clients pointed at v1; health alone does not prove provider parity.
2. Resolve the oracle once (`node compat/parity/resolve-oracle.mjs`: LiteRouter
   `main`, frozen for the run, recorded in `oracle-run.json`; `--sha <recorded>`
   repeats a run), export its `ORACLE_DIR` and `ORACLE_SHA`, and run
   `make parity-test` in a disposable development checkout, then review the
   `parity` PR CI result and its `oracle-run` artifact. The suite reads the
   resolved oracle commit's source and synthetic inputs only. It never calls a
   live provider. Read the `oracle-change-report` artifact (or run
   `node compat/parity/report-changes.mjs --previous <sha|oracle-run.json>`) for
   what changed in LiteRouter since the previous recorded snapshot: its entries are
   unverified candidates for follow-up, not findings, and nothing is filed from
   them automatically.
3. Use a dedicated test client/key to send sanitized, consented inputs to each
   selected router independently. Do not mirror production prompts by default.
   Compare status, model/account attribution, JSON shape, native unknown fields,
   stream termination, first-token latency and upstream cancellation. Explicitly
   exercise account fallback, client-key denial and disabled models.
4. For savers, compare the outbound N/N+1 cache-prefix fixtures with each saver
   enabled and bypassed, including fallback. Helper failures must still serve
   valid requests. Read cache-token accounting through the existing shared
   parser; do not equate token reduction with verified provider cache reuse.
5. Check Usage append/key attribution, quota state, logs for secret leakage,
   config publication/restart, and an isolated Routeweft backup/restore rehearsal.
   Record missing cost/TTFT/request details as known gaps, not zero values or
   evidence that those workflows passed. Record all discrepancies against the
   specific PARITY row and area issue (#75-#78).

Before any workload is chosen, read `docs/DIVERGENCE_LEDGER.md`. It lists every difference
from LiteRouter that is known to the pinned differential (conversion legs between the protocols,
combo, key and alias workflows) and every decision the owner has not yet approved. A workload
that depends on a behavior with a decision outstanding is not covered by this evaluation, however
well it runs. `make parity-test` regenerates the evidence; the `parity` CI job uploads the sheet as the
`divergence-ledger` artifact.

### Stop conditions and handoff

Stop the affected evaluation workload for an auth leak, model mismatch, broken
cancellation, incorrect accounting, cache regression or unsupported transport.
Point that test client back at its unchanged v1 endpoint; leave v1 running.
Preserve sanitized diagnostics and the independent Routeweft backup. No v1
database restore, service stop or decommission is part of this procedure.

An operator may continue only the explicitly validated native/configuration
subset. Full parity and broad-provider GA require the remaining PARITY rows,
provider readiness and complete differential/browser evidence to close through
reviewed follow-ups. A future traffic change needs its own approved plan.

## 26. Incident: unreadable sealed connection settings

Startup fails with `open provider settings for connection "<id>"` or
`decode provider settings for connection "<id>"` when a sealed payload cannot
be authenticated or decoded. The service stays unready; do not direct traffic
to it or replace the settings with defaults.

1. Stop Routeweft and preserve the data directory, including any SQLite WAL/SHM
   files, before attempting recovery. Keep the copy private; it contains sealed
   credentials and operator configuration.
2. Check that the deployment supplies the original `ROUTEWEFT_CREDENTIAL_KEY`
   (a base64-encoded 32-byte key) for this database. Restore that key from the
   deployment's secret store; generating a new key cannot decrypt existing rows.
   Do not print the key or credential payloads into a terminal transcript or logs.
3. Restart with the matching key. Require `/health/ready` to succeed before
   restoring traffic. A missing/wrong key is not a reason to delete connections.
4. If the key matches but a row is corrupt, restore an intact database backup
   paired with its matching key, following §12. Rehearse against a private copy
   first. CLI structural `restore --check` does not authenticate sealed provider
   settings; starting Routeweft with the matching key performs that check.
5. If no decryptable copy/key exists, preserve the damaged database for recovery
   and provision a separate empty Routeweft data directory with a securely stored
   credential key. Recreate provider connections through supported onboarding
   and reapply configuration from the operator's records. Reauthorize OAuth
   accounts rather than assuming an older backup's refresh tokens remain valid.
   Verify inference and readiness before directing traffic to the replacement.

Do not clear `secret_blob`, disable authentication checks, or hand-edit encrypted
payloads to force startup. A successful startup must compile the configured
provider settings, including endpoint and account identity fields.

## 27. Applying settings while serving

Successful settings mutations validate and compile before commit, then publish
one immutable snapshot. No separate reload command is needed.

- Token savers use the snapshot captured by each new request. Requests already
  running keep their transform settings through retries. Prompt-cache anchoring
  remains enabled for Messages even when the client bypasses token savers.
- Global outbound proxy enable/URL/no-proxy changes apply at the next inference
  client selection. Disabling the global proxy preserves the saved URL but stops
  using it. Explicitly bound pools remain independent. Existing upstream requests
  keep their transport. Work made for a connection (token refresh, usage reads,
  auto-ping, reset credits, connection checks, model tests, discovery) uses that
  connection's proxy policy, global proxy included. Sign-in and helper clients do
  not inherit these settings.
- A connection's own proxy (imported from LiteRouter, or set with
  `POST /admin/v1/connections/{id}/proxy`) applies at the next client selection.
  A usable bound pool takes precedence; the dashboard connection row says
  "Connection proxy …" when one is set. To stop using it, post
  `{"connectionProxyEnabled": false}`, which clears the URL too, or bind a pool.
  If its proxy is down or its URL is unusable, that connection's requests fail
  and routing moves to the next account; they never go out directly (SPEC §20).
- Observability enable/disable applies when a record is accepted. Disabling
  stops new Usage/detail records; it does not erase stored records or discard
  already queued events. The background worker stays available for re-enabling.
  Live completion metadata and console logs continue independently of this
  persistence setting.
- `observabilityMaxRecords` bounds new queue admission, not durable row count.
  Shrinking preserves accepted events; admission resumes when the queue drains
  below the new bound.
- Request details are kept by count and by age: `observabilityMaxDetailRecords`
  (default 1000, at least 1) is how many of the newest details stay; older ones
  are deleted in the same transaction that writes a new one, on the next write
  after the setting changes. Details older than seven days are always pruned.
  Usage rows and the daily rollup are not pruned by either.
- Batch size applies on the next worker wake. Its effective value is capped at
  the queue admission limit when settings compile, so a full queue can flush
  even if the saved batch size is larger. Full batches flush immediately;
  partial batches wait for the interval. A changed flush interval resets the
  timer on the next accepted event; queued events without another event can
  still wait for the previous interval. Shutdown drains accepted events.
- `observabilityMaxJsonSize` is bytes for the entire detail document and applies
  on the next detail write, including queued details. Oversized documents keep
  attribution with an omission marker, and each of the four captured fields
  (request, provider request, provider response, response) is cut to a
  `{_truncated,_originalSize,_preview}` marker (first 200 characters) at the cap.
  Capture happens on the normal inference path while observability is enabled,
  held in memory at a few times the cap per in-flight request; credentials are
  redacted before anything is stored. Changing the cap applies to the next request.
- The live view is `GET /admin/v1/usage/active` (in-flight per account and model,
  the last 20 finished requests, the provider that last failed for ten seconds)
  and the `usage.pending` event on `/admin/v1/events`; it is memory only and empty
  after a restart. The `request completed` console log line carries the requested,
  selected and upstream model of every request.

- Proxy pools have a type: `http` (forward proxies) or a relay (`vercel`, `cloudflare`,
  `deno`, `netlify`), whose members are the addresses of relay functions. A connection
  bound to a relay pool sends its provider requests to the relay, which forwards them; the
  relay therefore sees the request and its credential, so run it yourself. A pool marked
  `strictProxy` never lets a bound connection leave over the server's own address: if the
  pool is switched off or has no usable member, or its `noProxy` list excludes a host and no
  global proxy takes over, the request fails instead (a pool that is not strict falls back
  to the connection's own proxy, then the global one, then direct). Testing a pool records
  the result and switches the pool on (passed) or off (failed), so a failed test takes the
  pool out of use.
- `POST /admin/v1/proxy-pools/netlify-deploy {netlifyToken, projectName?}` creates a Netlify
  site, deploys the relay function and adds a `netlify` pool. It runs inside the request and
  can take about two minutes; the token is used for that request only and is neither stored
  nor logged. If the request is cut short the site may exist at Netlify without a pool: add
  it by hand with the relay URL `https://<site>.netlify.app/.netlify/functions/relay`.
  Deleting a pool is refused while a connection is bound to it or a `providerStrategies` entry
  names it.
- A `providerStrategies` entry for a provider that needs no credential may set `proxyPoolId`
  and `rotateStrategy` (`none`, `round-robin`, `random`): the provider's requests then go
  through that pool, or through the active pools in turn (the rotation position is memory and
  starts over on restart); with no active pool they go direct. It replaces the proxy settings
  of such a connection and does nothing for a provider that has credentials.
- `headroomUrl` is the Headroom helper's **base** URL: Routeweft calls
  `<headroomUrl>/v1/compress` and `<headroomUrl>/health`. A value that was a full
  endpoint under an earlier build (one that ends in `/compress`) must be reduced to
  the base, or the call goes to `.../compress/v1/compress` and fails open, which the
  request log shows as `headroom skipped ... proxy returned HTTP 404`. It cannot carry
  user info or a query. Headroom is skipped for Gemini requests and for Responses
  input that holds tool or reasoning items; this is the reference's behavior.
- PXPIPE needs a helper that implements the contract in SPEC 17.4 at `pxpipeUrl`;
  Routeweft does not install, start or supervise one, and does not render images
  itself. With no `pxpipeUrl`, every attempt is `not_installed`. It images Anthropic
  Messages bodies only. `GET /admin/v1/pxpipe/logs` and `/stats` carry the reference's
  summary for each attempt (reason, characters, images, estimated tokens saved).
  The helper's answer is bounded to 32 MiB; a slow helper costs at most
  `pxpipeTimeoutMs` per attempt, once per provider/account attempt.

`quotaVisibility`, `providerCompatibility`, `dnsToolEnabled`, and
`pxpipeAutoInstall` are explicitly storage-only. No restart activates them.
Named-window visibility, a compatibility flag dispatcher, DNS tools, and managed
helper installation respectively are not implemented. `claudeAutoPing` and
`codexAutoPing` now have opt-in backend scheduling (see §30); periodic quota
refresh alone does not warm reset windows. There is no writable cache or sampling toggle.
See [the setting audit](DYNAMIC_SETTINGS.md) and PARITY section 5.5 for evidence
and follow-up ownership. No accepted dynamic setting in these areas requires a
process restart.

## 28. Shutdown, drain and accounting evidence (C09)

On SIGTERM/SIGINT `routeweft serve` runs SPEC 24 in this order: readiness goes
false and the listener closes at once (new connections are refused); in-flight
requests and streams get a fixed 10 second drain deadline (not configurable; the
`drainGrace` field exists only so tests can shorten it); background schedulers and
the telemetry batcher keep running through the drain and stop only after it, so a
request that finishes during the drain still has its usage record written; the
batcher then performs its final flush, and SQLite closes. Exit status is 0 on a
clean drain.

If a request outlives the 10 second deadline, its connection is cut, which
cancels the upstream call. Handlers get up to 5 more seconds to publish their
record, which is stored as `client_abandoned`, before the final flush. Serve then
returns the deadline error (non-zero exit). Records published after the final
flush are not accepted; a container stop grace period shorter than about 16
seconds can SIGKILL the process before this finishes, so set it to at least 30
seconds.

A client that disconnects mid-request is accounted exactly once on every relay:
native, translated (Chat, Responses, rewritten/unwrapped bodies), Messages and the
Gemini/System One/Ollama compat paths, stream and non-stream. The record is
`client_abandoned` and carries whatever usage the relay had measured. Its
usage-observed flag is derived exactly as for a completed request (usage parsed or
reported by the upstream is kept and observed; nothing measured is zero and
unobserved), and its duration starts at the request's start like every other
record. If no upstream response existed yet, its status is a
synthetic `499`; otherwise it keeps the upstream's status (a translator error
caused by the client leaving is not rewritten to 502). Only client abandonment is
`Unanswered`: persisted to usage, but publishing neither the live
`request.completed` event nor a usage-feed frame, because nothing completed. An
upstream that itself answers 499 is an ordinary completed request. A translated
non-stream body that cannot be read or translated while the client is still
connected is also an ordinary completion: it is recorded once with status `502`
and announced on the live event bus and usage feed, so the operator sees the
gateway failure. A disconnect before any upstream request is issued records
nothing because nothing was billed.

Evidence (`internal/app/c09_ops_test.go`, run with `-race`):

| Claim | Test |
| --- | --- |
| Real SIGTERM mid-request: listener closes, readiness false, request completes with its body, one usage row flushed with the ticker disabled, clean exit | `TestSIGTERMDrainsInFlightRequestAndFlushesAccounting` |
| Drain deadline is bounded, the cut request is still recorded once as `client_abandoned` | `TestSIGTERMDrainDeadlineBoundsShutdownAndStillAccounts` |
| Client disconnect leaves one record, cancels upstream, goroutines return to baseline, on chat (native and translated), responses, messages and Gemini/Ollama compat paths, stream and non-stream | `TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks` |

Not covered: SIGKILL or power loss lose events still in the in-memory queue
(bounded by `observabilityMaxRecords`, flushed at `observabilityFlushIntervalMs`).

## 29. Race suite and CI budget

`go test -race -count=1 -timeout 10m ./...` is the gate. It runs locally as
`make race` and in CI as the `race` job of `.github/workflows/ci.yml` (#282),
beside the other jobs (the parity job still races its selected packages too).
Go's default ten minute per-package timeout is kept on purpose and spelled out:
a package that outgrows it fails in the gate instead of being masked by a larger
number. The job's `timeout-minutes: 45` only stops a wedged runner.

Measured on a 2-core host, the whole suite takes about 12 minutes wall clock.
Per package, under that full parallel load (alone in parentheses): `internal/app`
398 s (233 s), `internal/ingress` 273 s (159 s), `internal/control/api` 145 s,
`internal/runtime` 128 s, `internal/credentials` 119 s, `internal/adminauth`
69 s; every other package is under 60 s. `internal/app` is the next package to
approach the budget: at about two thirds of it, treat a new slow fixture there as
a regression to fix, not a number to raise.

Three costs dominated and are fixed in the test fixtures, with no production
change and no test skipped:

- **Migrating a fresh database per test.** Applying every migration made each
  test that opened a store spend 0.3-0.5 s under `-race` (the SQLite driver's
  pointer instrumentation makes each schema reload slow); `internal/ingress`
  took 829 s alone and tripped the ten minute timeout on CI at
  `TestIngressPostBurstRSS`, which was only the test running when the timer
  fired (it had run 2 s), not a hang or a memory-measurement flaw. Fixtures now
  copy a database migrated once per test binary (`internal/store/storetest`:
  `Seed`, `DataDir`); the runner still runs against it and finds nothing to do.
  Only tests that exercise migration itself (`internal/store/migrations`,
  `internal/app` upgrade tests, `internal/backup`) start from an empty file.
- **The production PBKDF2 work factor in fixtures.** 600000 iterations take
  about 0.45 s natively and 5-7 s under the race detector, once per bootstrap and
  per login. `internal/control/api` fixtures insert a precomputed 1000-iteration
  hash for `operator`; `internal/app` convenience fixtures do the same through
  `storetest.DataDirWithAdmin`. Production paths (`Bootstrap`, `ChangePassword`,
  `HashPassword`) are unchanged and keep 600000 iterations, and the tests that
  prove bootstrap (`c09_bootstrap_test.go`, `internal/adminauth`) use them.
  `storetest` checks the fixture hash against the production verifier.
- **Serial execution.** Nothing in `internal/ingress` is parallel; adding
  `t.Parallel()` is the next lever if the budget tightens, but those tests share
  helpers that have not been audited for it.

Before the fixes the full run had `internal/app` at 1030 s and
`internal/control/api` at 872 s (both past the timeout) and `internal/ingress` at
829 s alone.

### 29.1 CI jobs and required checks (#280)

`.github/workflows/ci.yml` runs on every push and pull request: `parity`, `go`, `race`
(section 29), `ui` (typecheck, unit tests, build), `ui-smoke` and `container`.

`ui-smoke` runs `npm run smoke` in `ui/` (`make ui-smoke` locally): the shell smoke
with its accessibility checks (semantic labels, contrast, landmarks, heading order,
focus and skip link, reduced motion, 390px and 320px overflow) and the OAuth,
device, import-masking, connection-status, Kiro, quota-actions and quota-warming
scripts. `smoke.sh` builds, starts `vite preview` in its own process group and stops
it by PID on every exit path; the job then checks the port is free and runs
`smoke-cleanup-check.sh fail interrupt term` to show the failing, interrupted and
terminated paths stop it as well. Waits are on page conditions, never sleeps.
Playwright is pinned to `playwright-core` 1.49.1, installed with
`npm install --no-save --no-package-lock` (the manifest is untouched); Chromium and
Firefox are cached under `~/.cache/ms-playwright` by that version and the system
libraries are installed with `playwright-core install-deps`. The scripts launch the
full Chromium that install provides (`chromium.executablePath()`), not Playwright's
headless-shell default, because the OAuth journeys script asserts there are no console
errors and the headless shell reports a blocked popup navigation the full browser does
not; `ROUTEWEFT_SMOKE_CHROME` still overrides the binary. The evidence directory and
the preview log are uploaded as the `ui-smoke-evidence` artifact.

**Required checks are branch-protection settings, not repository code.** When #280 was
written, `main` required `Hermes Review` and `parity` only (`strict`, administrators
included); `go`, `race`, `ui`, `ui-smoke` and `container` were not required. List them
with:

```bash
gh api repos/raufimusaddiq/routeweft/branches/main/protection/required_status_checks --jq '.contexts'
```

To require the smoke, add its check name (`ui-smoke`, the job id, which is what GitHub
reports) without touching the others:

```bash
echo '["ui-smoke"]' | gh api -X POST repos/raufimusaddiq/routeweft/branches/main/protection/required_status_checks/contexts --input -
```

Only do this after `ui-smoke` has passed on the PRs that carry it: a required check
that never reports blocks every merge.

## 30. Quota credits and opt-in warming

Use session-gated GET `/admin/v1/quota/{connection-id}/codex-reset-credits` to
inspect credits. POST at the same path with `{"confirm":true}` spends one credit.
A 409 means no credit. After any 5xx, or a request that did not complete, the
outcome is unknown: read the credit list again before issuing another POST. An
ambiguous action is not retried automatically; the Quota page re-reads the list
itself. Every spend on the page reads the credit list from Codex first and
spends nothing while that read fails, including after leaving the page or
reloading it. A refused credential (Codex 401/403) is the one outcome the server
replays, once: nothing was spent, so it refreshes the token and sends the same
redeem id again; each of the three upstream calls has its own 15s budget, and a token refresh's save is bounded separately at 30s, so a spend that needs a refresh can take about 75 seconds before it answers. A 4xx whose body reads as an expired credential is replayed the same way and, if it still fails, keeps Codex's own status (for example 400): reconnect the account if it persists. Another 4xx means Codex
refused and nothing was spent; 403 `reauth_required` means reconnect the account.
On the Quota page, Credit expiry lists the credits and Use 1 reset credit asks
before spending one.

POST `/admin/v1/quota/refresh` returns 202 with a job, not an observation. Follow
it with GET `/admin/v1/quota/refresh` until `status` is `succeeded` or `failed`,
then read `/admin/v1/quota`; a second POST while it runs joins the same job.
A job that passes its 60-second deadline is `failed`; if its provider reads are
still unwinding, a POST is 409 `refresh_busy` until they stop. Retry later.

Quota warming is off by default. Set `claudeAutoPing` or `codexAutoPing` through
PATCH `/admin/v1/settings` with a JSON string value, for example:

```json
{"set":{"codexAutoPing":"{\"connections\":{\"connection-id\":true}}"}}
```

Only enabled OAuth accounts for that provider run. The next minute tick sees
changes. Set an empty connections object or remove the setting to disable it.
Warming sends a tiny generation request and can consume provider quota. Reset
suppression persists in the existing settings table and survives restarts.
A crash between accepted generation and local persistence can repeat a ping.
Background quota polling alone does not enable warming. Provider-specific
remaining gaps are listed in `P05_QUOTA_AUDIT.md`.

On the Quota page every enabled-or-not OAuth Claude and Codex account row has a
**Quota warming** switch (#297); API-key accounts and other providers have none. It
reads "Warming off" or "Warming on" in words, shows the provider's explanation
beside it (Codex's says it consumes a small amount of quota), and every account
starts off. A click saves at once through the settings call above: the page reads the
stored map first and changes only that account's entry, so an opt-in made elsewhere
is kept, and a refused or failed save leaves the switch as it was with an error
shown. If the settings cannot be read the switches are hidden rather than shown as
off. The switch can be operated with the keyboard (Tab, then Space), is a 44px target
and does not scroll sideways at 320px. The next minute tick sees the change.
