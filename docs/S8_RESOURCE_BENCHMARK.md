# Sprint 8 — RSS/CPU evidence

Measured September 26, 2026, Linux amd64, Intel Xeon E5-2680 v4, 4 visible CPUs. Local mock upstream; no external provider time. Numbers are directional, not a production latency SLA.

| Workload | Command / method | Result |
|---|---|---|
| Native Chat | `go test ./internal/ingress -run '^$' -bench '^BenchmarkChatCompletionsNative$' -benchtime=2s -benchmem -count=3` | 306–322 µs/op; ~59.8–60.0 KiB/op; 181 allocs/op |
| Streamed Chat | `go test ./internal/ingress -run '^$' -bench '^BenchmarkStreamedChatCompletion$' -benchtime=2s -benchmem -count=3` | 317–356 µs/op; ~57.2 KiB/op; 185 allocs/op; 32 SSE data chunks plus DONE |
| Streamed Chat CPU profile | `go test ./internal/ingress -run '^$' -bench '^BenchmarkStreamedChatCompletion$' -benchtime=3s -benchmem -cpuprofile=/tmp/rw-s8-ingress.cpu` | 297 µs/op; 56,949 B/op, 185 allocs/op; top flat samples in syscall (20.8%), futex (9.0%), GC scan (6.5%). Profile includes both mock server and gateway. |
| Streamed Chat RSS | `go test ./internal/ingress -run '^TestIngressPostBurstRSS$' -v` | 20 streams: 16,652 KiB test-process RSS after burst, +7,256 KiB from test start. Under `-race`: 56,628 KiB, +31,136 KiB. These values include the Go test harness and mock server; not standalone service RSS. |
| Standalone service RSS | Build with `go build -trimpath -ldflags='-s -w'`, start `routeweft serve` with disposable data, serve 100 `/health/ready` requests, read `/proc/<pid>/smaps_rollup` | 17,736 KiB resident after requests. Idle service plus health checks; no provider traffic. |

PRD §19 targets <=64 MiB post-burst service RSS and native-router p50 <=7 ms / p95 <=12 ms. The streamed test records test-process RSS, not service RSS; do not treat it as a production post-burst gate. Running the entire ingress test package under race instrumentation produced 137,760 KiB because prior tests share that process. The service measurement is a fresh release-style binary but covers only health traffic. Full-service post-inference RSS and percentile latency require a dedicated external load run on representative configured providers before claiming those targets met. Existing `TestStreamConcurrencyLevels` separately verifies 1/10/50/100 intact streams.
