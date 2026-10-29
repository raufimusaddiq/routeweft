.PHONY: test build run ui-install ui-test ui-typecheck ui-build compat-test parity-test bench-router race check

test:
	go test ./...

build:
	go build -o dist/routeweft ./cmd/routeweft

run:
	go run ./cmd/routeweft serve

ui-install:
	cd ui && npm ci

ui-test:
	cd ui && npm test

ui-typecheck:
	cd ui && npm run typecheck

ui-build:
	cd ui && npm run build

compat-test:
	go test ./compat/...

parity-test:
	@test -n "$$ORACLE_DIR" -a -n "$$ORACLE_SHA" || { echo "set ORACLE_DIR and ORACLE_SHA, see compat/parity/README.md (eval \"\$$(node compat/parity/resolve-oracle.mjs | sed 's/^/export /')\")" >&2; exit 1; }
	node --test compat/parity/resolve-oracle.test.mjs compat/parity/report-changes.test.mjs
	node compat/parity/inventory.mjs
	node compat/parity/generate-combo-routing.mjs --check
	go test -race ./internal/app -run TestPinnedSystemOneDifferential -count=1
	go test -race ./internal/security ./internal/ingress ./internal/providers/shared ./internal/providers/wire ./internal/protocol/concerns
	go test -race ./internal/parity ./internal/protocol/hub -count=1

race:
	# 10m is also go test's default per-package timeout; it is spelled out so the
	# budget RUNBOOK 29 tracks (internal/app was 433s) is visible, not changed.
	go test -race -timeout 10m ./...

bench-router:
	go test ./bench/router -run '^$$' -bench . -benchmem -count=5

check: test compat-test ui-typecheck ui-test ui-build
