# Pinned differential gate

Run `ORACLE_DIR=/path/to/literouter make parity-test` from the repository root.
The oracle must contain commit `1c3b9f47837d42ea64992063309064af755a14a3`.
Node 22 and Go from go.mod are required. No oracle working files are imported.

`inventory.mjs` verifies 97 route files, 153 exported operations, remote guard
behavior, key forms, 81 provider entries, 16 pages and every open-row disposition.
Its assertions are also the inventory check; a new unannotated gap fails CI.

`internal/app/parity_test.go` executes the pinned System One handler in Node
with synthetic dependency exports, then compares it with Routeweft's configured
app. No provider, credential store or host-operation dependency from v1 runs.
Only test upstreams receive traffic. System One status/prepared JSON/native
bytes/error-envelope/cancel cases are differential; admin shape descriptions
and other protocol fixtures are not a claim of complete differential coverage.
Pending coverage stays explicit in PARITY R11.

CI runs this as the `parity` PR job, alongside existing Go/UI/container jobs.
No test may update golden files from its actual output or turn a missing oracle
into a passing dedicated gate. Ordinary unit tests remain usable without v1.
