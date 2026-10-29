# 9router-go as a supplementary implementation reference (#291)

Status: evidence document, read-only evaluation. It changes no product behavior and
creates no issue. Follow-ups below are proposals for the PM to file.

## 1. Role and rules

- The parity oracle is LiteRouter `refs/heads/main` (`raufimusaddiq/literouter`),
  resolved once per run (`compat/parity/resolve-oracle.mjs`, #289). 9router-go is
  **never the oracle**. Where it disagrees with LiteRouter, LiteRouter decides.
- 9router-go (`luqman-v1/9router-go`) is **read-only**: it was cloned into a scratch
  directory outside this repository (`git clone --depth=1`), nothing was written to
  it or to its GitHub project (no issues, comments, PRs, stars, forks), and no code
  from it is copied into Routeweft by this change.
- Ideas are reimplemented against Routeweft-owned interfaces (AGENTS.md section 2),
  and only after the proposal is accepted and checked against LiteRouter.

## 2. What was examined

| Item | Value |
| --- | --- |
| 9router-go | `luqman-v1/9router-go` `main` at `554469e091c0fb604043eeef50ccdcb7acba34cb` (commit date 2026-10-30, subject "fix(combo): never resend a combo entry to a connection it already tried (#774)"), `VERSION` `1.9.15-exp.1` |
| LiteRouter | `raufimusaddiq/literouter` `main` at `fc8dd1fc4f61383e7944dd880d94b6529fe92526` (resolved with `git ls-remote` during this work; the run snapshot of #289) |
| Routeweft | `main` at `7c928d3a22eaca41c2218935136a359c94db0251` |

### What 9router-go is

Its own `LICENSE` (lines 1-8) and `COMPARISON.md` (lines 1-3) state it is a Go + Svelte
port of `decolua/9router` ("historical upstream", declared sync target v0.5.85), not
of LiteRouter. LiteRouter is a different line of that codebase. Parity claims in
9router-go's docs are scoped to the historical upstream, so they say nothing about
LiteRouter main (`COMPARISON.md` itself says provider parity is not inferred from a
shared route). Its `TECHNICAL_DEBT.md` lists open High items (plaintext credentials
at rest DB-02, optional updater integrity OPS-01) that Routeweft must not inherit.

### Areas read (and only these)

Read in full or in the cited ranges; nothing here was run on 9router-go.

| Area | 9router-go paths | Routeweft paths | LiteRouter paths |
| --- | --- | --- | --- |
| Outbound address guard | `internal/netguard/netguard.go:30-86`; callers `internal/proxy/ssrf.go:59,210-214`, `internal/translator/image_prefetch.go:106-110` | `internal/transport/ssrf.go:70-116` | `src/shared/utils/ssrfGuard.js:41-48,119-137`; `open-sse/translator/concerns/image.js:20-34` |
| Login lockout and client IP | `internal/auth/session.go:226-345` (limiter), `:453-527` (`ResolveClientIP`, `LoginClientIP`) | `internal/control/api/throttle.go`, `internal/control/api/api.go:376-392,420-425` | `src/lib/auth/loginLimiter.js:4-42` |
| Codex reset-credit auth retry | `internal/codexquota/resetcredits_fetch.go:88-186` | see PARITY section 6.32 (#298) | (cited by #298) |

### Not examined (no claim made)

Translators (`internal/translator`, 43 files), providers (`internal/providers`),
proxy engine and pools (`internal/proxy`), Headroom/token saver
(`internal/headroom`, `internal/tokensaver`), combos/fusion, usage and request
details, semantic cache, guardrails, model health, vault, MITM, updater, Svelte UI.
Open issues that touch them (#270-#278) are therefore **not** covered by this
evaluation, and silence here is not a statement that 9router-go has nothing useful.

## 3. Proven gaps in Routeweft

"Proven" means reproduced by running a throwaway probe test against Routeweft `main`
(the probe files were deleted; none is committed) and, for the oracle side, by reading
LiteRouter's source at the SHA above.

### 3.1 Outbound public-address guard accepts reserved ranges (security)

Routeweft `isPublicIP` (`internal/transport/ssrf.go:82-89`) accepts anything that is
`IsGlobalUnicast` and not private/loopback/link-local/multicast/unspecified, plus two
exact metadata addresses (`:91-93`). Go's `IsGlobalUnicast` is true for ranges that
are not public. Probe of the dial-time predicate (`allowedIP(ip, false)`):

| Address | Routeweft result | Note |
| --- | --- | --- |
| `100.64.0.1` | **public** | RFC 6598 CGNAT; only the exact `100.100.100.200` is blocked |
| `192.0.0.192` | **public** | IETF assignments; Oracle metadata address |
| `198.18.0.1` | **public** | RFC 2544 benchmark range |
| `0.1.2.3` | **public** | `0.0.0.0/8` "this network" |
| `192.88.99.1`, `198.51.100.7`, `203.0.113.5` | **public** | deprecated 6to4 anycast, documentation |
| `64:ff9b::a00:1`, `64:ff9b::7f00:1`, `64:ff9b::a9fe:a9fe` | **public** | NAT64 well-known prefix wrapping `10.0.0.1`, `127.0.0.1`, `169.254.169.254` |
| `::10.0.0.1` | **public** | deprecated IPv4-compatible form |
| `2001:db8::1`, `64:ff9b:1::1` | **public** | documentation, local-use NAT64 |
| `10.0.0.1`, `169.254.169.254`, `100.100.100.200`, `::1`, `fd00:ec2::254`, `::ffff:10.0.0.1` | blocked | |

`ValidatePublicURL` (`:97-116`) also accepts `http://100.64.0.1/` and
`http://198.18.0.1/`. IPv6 zone literals (`[::1%25lo]`, `[fd00:ec2::254%25eth0]`) are
rejected, so the zone-stripping helper in 9router-go (`netguard.ParseIP`,
`netguard.go:30-35`) is **not** needed in Routeweft.

Reference behavior:

- LiteRouter blocks `0.0.0.0/8` and `100.64.0.0/10` (`ssrfGuard.js:41-48`) and checks
  the IPv4 embedded in IPv4-mapped, NAT64 `64:ff9b::/96` and IPv4-compatible IPv6
  addresses against the same list (`ssrfGuard.js:134-137`). Read, not run.
- 9router-go blocks 100.64/10, 192.0.0.0/24, 198.18/15, 192.88.99/24 and the
  documentation ranges, via one shared predicate (`netguard.go:41-60,66-86`). It does
  **not** list the well-known NAT64 prefix `64:ff9b::/96` or `::a.b.c.d`, so it is not a
  superset of LiteRouter. Read, not run.

AGENTS.md section 11 forbids weakening the SSRF policy; this proposal only tightens it
and keeps the trusted-local variant's explicit allowances.

### 3.2 Failed-login map is unbounded (resource exhaustion)

`throttler.windows` (`internal/control/api/throttle.go:24,51-62`) is only pruned when
the same key returns (`retryAfter`) or succeeds. The key is the socket host
(`api.go:420-425`). Probe: 200,000 distinct keys followed by 24 h of silence leave
200,000 entries. Many source addresses (an IPv6 prefix) grow the process without
bound.

Reference: 9router-go caps its buckets at `maxLoginBuckets = 5000`
(`session.go:272`), sweeps buckets silent for a full window and evicts the stalest
one when still full (`:276-345`). LiteRouter's `loginLimiter.js` has the same
progressive policy (5 failures, 30 s / 2 m / 10 m / 30 m, 1 h window; lines 4-6) but I
did not check it for a bucket cap. The bound itself is a Routeweft resource-safety
requirement and does not depend on either reference.

## 4. Reusable ideas that are not proven gaps

1. **Right-to-left forwarded-address walk with fail-closed parsing**
   (`session.go:453-494`): trust a hop only if its peer is in the trusted CIDR list,
   walk `X-Forwarded-For` from the right, stop at the first untrusted hop, and return
   the connecting peer when any hop is malformed. This is a candidate algorithm for
   the C10 trusted-proxy contract (#283) and the throttle key of #281. Its
   environment names (`TRUSTED_PROXIES`, `TRUST_PROXY`, `TRUST_CLOUDFLARE`, and the
   peer-token headers at `:499-509`) belong to the historical upstream, not to
   Routeweft (`ROUTEWEFT_TRUSTED_PROXIES`, which `docs/SPEC.md:1853` says is not implemented) and not
   shown to be LiteRouter's. A file search of LiteRouter main for `trustedProxy` or
   `clientIp` names found none; I did not search for the behavior under other names.
2. **One shared address predicate** instead of per-layer copies (`netguard.go:1-13`
   explains the earlier drift between `proxy` and `translator`). Routeweft has one
   predicate in `internal/transport`; keep it that way when 3.1 is fixed.
3. **Reset-credit redeem retried once with the same idempotency key**
   (`resetcredits_fetch.go:165-170`): matches the replay-once-with-same-redeem-id
   behavior Routeweft implemented in #298. It differs in the trigger: 9router-go retries
   on any HTTP 401 or 403 (`:102`) after a list-then-select step (`:153-157`), whereas
   Routeweft follows the oracle's refusal-text rule (PARITY section 6.32). This is the
   reason it cannot be the oracle; no change is proposed.

## 5. Differences and limitations

- Different lineage and baseline (section 2): any agreement or disagreement with
  9router-go is evidence about `decolua/9router` v0.5.85 plus 9router-go's own
  changes, not about LiteRouter.
- Different architecture: chi router, plaintext credentials in SQLite
  (`TECHNICAL_DEBT.md` DB-02), process-local caches, root-native route layout
  (`COMPARISON.md`). Routeweft's snapshot/state split, credential handling and
  `/admin/v1` layout are not to be reshaped to match.
- 9router-go retains media surfaces (embeddings, images, audio, video) that Routeweft's
  product boundary excludes (AGENTS.md section 2).
- Its own debt register states its documentation drifts from source (DOC-01); its
  prose is therefore not used as evidence anywhere in this document, only source.
- Nothing from 9router-go was executed. Its tests were not run; statements about it
  are from reading the source at the SHA above.

## 6. License and attribution

`LICENSE` of 9router-go is MIT with two notices: "Copyright (c) 2024-2026 decolua and
contributors" and "Copyright (c) 2026 Luqmanul Hakim" (it also says the decolua notice
is retained because it is a port). MIT requires the copyright notice and permission
text to be included in all copies or substantial portions of the Software.

- This change copies **no** code, tables or text from 9router-go, so no notice is
  owed for it. The address ranges in 3.1 are public IANA/RFC allocations
  (RFC 6598, 6890, 2544, 3068, 5737, 8215, 3849), taken from the RFCs, not from its
  source.
- If a later PR copies or closely adapts any 9router-go source (not just an idea),
  that PR must carry the two MIT notices and permission text for that code (for
  example in a `THIRD_PARTY_NOTICES` entry), name the source path and SHA, and be
  checked against the license of `decolua/9router` it derives from. An idea
  reimplemented from the RFC or from LiteRouter's behavior needs no notice.
- Routeweft's own license is not changed here.

## 7. Overlap with existing open issues

| Issue | Overlap |
| --- | --- |
| #281 password policy and trusted-proxy-aware login throttle | 3.2 (bucket bound) and 4.1 (client IP) belong in this issue's scope; 3.2 is not mentioned in its body |
| #283 trusted-proxy and verbose environment semantics | 4.1 is a candidate algorithm; semantics must come from LiteRouter/SPEC, not from 9router-go's env names |
| #76 providers gaps | no overlap: providers not examined |
| #270-#278, #271, #272, #273 | not examined; no overlap claimed |
| #297 quota warming toggle | 9router-go `internal/handlers/chat/*` mentions "warm"; not read, no claim |
| #226 (closed in PARITY 6.32) | 4.3 only |
| #304 flaky proxy-pool test | unrelated |

No existing open issue covers 3.1.

## 8. Proposed Routeweft-only follow-ups (not filed)

1. **[security] Block reserved IPv4/IPv6 ranges in the outbound public-address
   predicate** (`internal/transport/ssrf.go`, 3.1): `0.0.0.0/8`, `100.64.0.0/10`,
   `192.0.0.0/24`, `198.18.0.0/15`, `192.88.99.0/24`, the documentation ranges, and the
   IPv4 embedded in `64:ff9b::/96` and `::a.b.c.d`, evaluated against LiteRouter
   main (`ssrfGuard.js:41-48,134-137`) first. Keep trusted-local semantics, add
   table tests for each range in both modes, and check the same predicate is used for
   every outbound path (proxy pool tests, provider validation, image prefetch).
2. **[resource] Bound the failed-login map** (`internal/control/api/throttle.go`, 3.2):
   cap entries, sweep expired windows, evict the oldest when full, with a test that
   200,000 distinct keys stay under the cap. Fold into #281 or file separately.
3. **[docs, optional] Record 4.1 as input to #283** after the LiteRouter behavior for
   trusted proxies is read from source; do not adopt 9router-go's env names.

## 9. How this was checked

- Clone: `git clone --depth=1 https://github.com/luqman-v1/9router-go.git` into a
  scratch directory; `git rev-parse HEAD` gave the SHA in section 2. Read-only.
- LiteRouter: `git ls-remote https://github.com/raufimusaddiq/literouter.git
  refs/heads/main` and `git show fc8dd1fc4f61:<path>` for the cited files.
- Routeweft probes (temporary `_test.go` files, deleted before commit): an
  `allowedIP`/`ValidatePublicURL` table over the addresses in 3.1, and a throttler run
  with 200,000 keys. The outputs are the ones tabulated above.
