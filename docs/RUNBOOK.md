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
```

Expected first boot:

1. create data directory with restrictive permissions;
2. create/open SQLite;
3. run migrations;
4. create initial admin only when database has no admin;
5. compile RuntimeSnapshot;
6. start workers;
7. readiness becomes healthy.

Remove the bootstrap password from deployment environment after the admin credential exists if the implementation does not require it for subsequent boots.

## 4. Local development

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

## 5. Production container

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

## 6. Reverse proxy

Recommended topology:

```text
internet/private LAN
        |
      Caddy
        |
    Routeweft:21128
```

Forward only trusted proxy headers from known proxy peers.

Do not expose an unprotected admin API directly to the public network.

## 7. Smoke checks

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

## 8. Health interpretation

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

## 9. Logs

Production logging should be structured.

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

## 10. Backup

Preferred:

```bash
routeweft backup --output /backup/routeweft-$(date +%Y%m%d-%H%M%S).sqlite
```

Backup must use a SQLite-safe online backup/checkpoint procedure rather than copying an arbitrary live WAL state.

Store alongside metadata:

- Routeweft version/commit;
- schema version;
- timestamp;
- config revision.

Periodically rehearse restore on a disposable host/data directory.

## 11. Restore

Validation first:

```bash
routeweft restore --input /backup/file.sqlite --check
```

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

## 12. Upgrade

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

## 13. Rollback

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

## 14. SQLite maintenance

Monitor:

- DB size;
- WAL size;
- busy/lock errors;
- checkpoint duration;
- telemetry batch duration;
- disk free space.

Do not run manual VACUUM/checkpoint commands during heavy production traffic without understanding impact.

Use Routeweft-owned maintenance commands once implemented.

## 15. Incident: elevated 5xx

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

## 16. Incident: OAuth refresh failures

Actions:

1. identify provider/account;
2. stop repeated refresh storms;
3. inspect error without logging secrets;
4. verify current durable credential generation;
5. re-auth/import account if provider invalidated the refresh token;
6. confirm RuntimeState reflects new credential;
7. run a minimal provider test.

Never restore an older DB solely to recover an OAuth token unless you know the older refresh token remains valid.

## 17. Incident: SQLite busy/locked

Check:

- accidental second Routeweft writer;
- long transaction;
- telemetry batch behavior;
- backup/restore activity;
- disk/filesystem health.

A normal single-instance deployment should not repeatedly hit sustained writer lock failures.

Do not add Redis/Postgres as an emergency workaround; fix the contention/root cause.

## 18. Incident: telemetry pressure

Check:

- queue depth;
- diagnostic drops;
- critical backpressure;
- emergency flush count;
- lost-critical counter;
- SQLite batch latency;
- disk space.

Inference may remain available in degraded mode according to SPEC, but lost critical accounting must be visible.

## 19. Incident: prompt-cache regression

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

## 20. Incident: runaway Fusion cost/load

Actions:

- inspect Combo/Fusion concurrency;
- panel count;
- panel hard timeout;
- judge choice;
- repeated retries;
- response sizes.

Disable/change the affected Combo strategy if needed.

Do not globally disable unrelated routing.

## 21. Security rotation

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

## 22. Disaster recovery minimum

Maintain:

- current immutable image reference;
- at least one known-good previous image;
- current DB backup;
- previous pre-upgrade backup;
- documented environment/config;
- Caddy/reverse proxy config.

A fresh server should be recoverable using only Routeweft artifacts, without LiteRouter.
