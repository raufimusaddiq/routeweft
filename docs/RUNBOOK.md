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
```

`ROUTEWEFT_UI_DIR` (`--ui-dir`) points at the built Vite output; the Go binary
then serves the dashboard at `/`. The image sets it to `/ui`. UI routes are
hash-based, so only `/` returns `index.html`; `/assets/*` is immutable-cached and
unknown paths (and GET/HEAD under `/admin/`, `/v1/`, `/api/`) return 404. Leave it
unset to serve API only and let Caddy serve the assets. A directory without
`index.html` logs an error and disables the UI; the API keeps running.

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
`debug`, `warn`, and `error` are supported). Invalid levels stop startup before
opening data. There is no corresponding CLI flag.

## 7. Reverse proxy

Recommended topology:

```text
internet/private LAN
        |
      Caddy
        |
    Routeweft:21128
```

Routeweft currently ignores forwarded-IP headers from every peer. There is no
`ROUTEWEFT_TRUSTED_PROXIES` setting or trusted-proxy CLI flag. Login throttling
uses the socket remote address, so requests through a reverse proxy share its
throttle identity. Restrict direct access to the service at the network boundary.

Consequence: every client behind one reverse proxy is one socket peer and shares
one admin-login throttle bucket (8 failures per 5 minutes). One client's failed
logins can lock all operators out of the login route for up to 5 minutes
(`429` with `Retry-After`); it never grants access. A forged `X-Forwarded-For`,
`X-Real-Ip` or `Forwarded` value cannot move a request into another bucket or
out of a full one, and no client address is persisted in usage, request details
or any other table. Evidence: `TestForwardedHeadersNeverChangeTheThrottleBucket`
(`internal/control/api`) and `TestForwardedHeadersAreNotRecordedAnywhere`
(`internal/app`). Operators who need per-client buckets must terminate the
distinction before Routeweft (separate listeners or networks); a trusted-proxy
setting is not implemented (PARITY TRUST_PROXY).

Do not expose an unprotected admin API directly to the public network.

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
5. re-auth/import account if provider invalidated the refresh token;
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
live UI, or v1 migration/history continuity. Provider readiness is not fully
advertised by the current registry/UI; manually restrict evaluation to the
specific provider/account/protocol combinations the operator has validated.
An enabled provider or a successful model listing is not inference validation.

Standalone media, hidden providers, cloud/tunnel lifecycle, Redis and
multi-replica operation stay excluded by PRD 4.2 / BDR-005/016. Remote helper
install/start/stop/restart and host auto-import stay unavailable. Manage any
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
   v1 history in v1; do not combine accounting histories during evaluation.
   The full one-time policy (mapping, units, refusals, key reissue,
   reauthorization, history) is `docs/V1_MIGRATION.md`.
   Snapshot publication and an independent restart must preserve the new config.
4. Prefer separate provider identities/keys. If identities must be shared,
   designate exactly one refresh-token rotation owner and verify the provider's
   concurrency policy first. Shared identities also share upstream quotas;
   duplicated evaluation calls may consume tokens or incur charges twice.
5. Use the production image's CA trust bundle and existing SSRF policy. Keep
   `ROUTEWEFT_ALLOW_PRIVATE_UPSTREAMS=false` unless a trusted local operator
   explicitly needs LAN helpers/models. Permit only the required browser
   origins with `ROUTEWEFT_CORS_ORIGINS`; do not weaken redirect revalidation.
   Do not forward v1 session cookies, Authorization, proxy userinfo, tokens or
   `X-Sync-Secret` into an application/access log. #74 owns deployed ingress
   redaction and image-retention evidence; R11 has not verified that deployment.

### Evaluate without moving ordinary traffic

1. Record the independent image commit, schema/config revision and Routeweft
   backup. Check `/health/live` and `/health/ready` on the separate port. Leave
   ordinary clients pointed at v1; health alone does not prove provider parity.
2. Run `ORACLE_DIR=/path/to/pinned-oracle make parity-test` in a disposable
   development checkout, then review the `parity` PR CI result. The suite reads
   pinned source and synthetic inputs only. It never calls a live provider.
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
  keep their transport. Helper, quota and OAuth clients do not inherit these
  settings.
- Observability enable/disable applies when a record is accepted. Disabling
  stops new Usage/detail records; it does not erase stored records or discard
  already queued events. The background worker stays available for re-enabling.
  Live completion metadata and console logs continue independently of this
  persistence setting.
- `observabilityMaxRecords` bounds new queue admission, not durable row count.
  Shrinking preserves accepted events; admission resumes when the queue drains
  below the new bound. Details retain the fixed seven-day pruning policy.
- Batch size applies on the next worker wake. Its effective value is capped at
  the queue admission limit when settings compile, so a full queue can flush
  even if the saved batch size is larger. Full batches flush immediately;
  partial batches wait for the interval. A changed flush interval resets the
  timer on the next accepted event; queued events without another event can
  still wait for the previous interval. Shutdown drains accepted events.
- `observabilityMaxJsonSize` is bytes for the entire detail document and applies
  on the next detail write, including queued details. Oversized documents keep
  attribution with an omission marker. Normal inference diagnostic capture is
  still incomplete (R10); changing the cap cannot create missing diagnostics.

`quotaVisibility`, `providerCompatibility`, `dnsToolEnabled`, and
`pxpipeAutoInstall` are explicitly storage-only. No restart activates them.
Named-window visibility, a compatibility flag dispatcher, DNS tools, and managed
helper installation respectively are not implemented. `claudeAutoPing` and
`codexAutoPing` are absent and rejected; periodic quota refresh does not warm
reset windows. There is no writable cache or sampling toggle.
See [the setting audit](DYNAMIC_SETTINGS.md) and PARITY section 5.5 for evidence
and follow-up ownership. No accepted dynamic setting in these areas requires a
process restart.

## 28. Shutdown, drain and accounting evidence (C09)

On SIGTERM/SIGINT `routeweft serve` runs SPEC 24 in this order: readiness goes
false and the listener closes at once (new connections are refused); in-flight
requests and streams get a 10 second drain deadline; background schedulers and
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

A client that disconnects mid-request is accounted exactly once. If the upstream
response had started, usage is whatever had arrived. If the upstream request had
been issued but no response existed yet, the record has status `499`,
termination `client_abandoned` and unobserved usage (it is never written to the
client). A disconnect before any upstream request is issued records nothing
because nothing was billed.

Evidence (`internal/app/c09_ops_test.go`, run with `-race`):

| Claim | Test |
| --- | --- |
| Real SIGTERM mid-request: listener closes, readiness false, request completes with its body, one usage row flushed with the ticker disabled, clean exit | `TestSIGTERMDrainsInFlightRequestAndFlushesAccounting` |
| Drain deadline is bounded, the cut request is still recorded once as `client_abandoned` | `TestSIGTERMDrainDeadlineBoundsShutdownAndStillAccounts` |
| Client disconnect (stream and non-stream) leaves one record, cancels upstream, goroutines return to baseline | `TestClientDisconnectKeepsExactlyOneUsageRecordWithoutLeaks` |

Not covered: SIGKILL or power loss lose events still in the in-memory queue
(bounded by `observabilityMaxRecords`, flushed at `observabilityFlushIntervalMs`).

## 29. Race suite and CI budget

`go test -race ./...` is the intended gate, but it has **not been shown green**
in the C09 validation: the full run did not complete in the available budget
(only `cmd/routeweft`'s known `TestRunbookHealthAndAdminGate` failure had
reported). Treat the full-repository race suite as not completing inside a ten
minute package budget until a complete run proves otherwise.
`internal/control/api` previously exceeded Go's
ten minute package timeout under `-race` because every fixture that provisioned
an admin ran the production PBKDF2 work factor (600000 iterations, about 0.45 s
natively and 5-7 s under the race detector) once to create the account and again
per login. The package now finishes in about 90 s under `-race` (measured alone, `-v` per-test timings; it was killed past ten minutes before). Fixtures
provision the operator with a precomputed low-iteration hash
(`adminauth.BootstrapWithHash`); production paths (`Bootstrap`, `ChangePassword`,
`HashPassword`) are unchanged and keep 600000 iterations, and no test is skipped.
`make race` runs the full suite locally. `.github/workflows/ci.yml` still only
races a package subset; promoting it to the full run is a workflow change tracked
for a follow-up.
