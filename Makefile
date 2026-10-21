.PHONY: test build run ui-install ui-test ui-typecheck ui-build compat-test parity-test bench-router check

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
	node compat/parity/inventory.mjs
	go test -race ./internal/app -run TestPinnedSystemOneDifferential -count=1
	go test -race ./internal/security ./internal/ingress ./internal/providers/shared
	go test -race ./internal/parity ./internal/protocol/hub -count=1

race:
	go test -race -timeout 20m ./...

bench-router:
	go test ./bench/router -run '^$$' -bench . -benchmem -count=5

check: test compat-test ui-typecheck ui-test ui-build
