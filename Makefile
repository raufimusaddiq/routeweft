.PHONY: test build run ui-install ui-typecheck ui-build compat-test bench-router check

test:
	go test ./...

build:
	go build -o dist/routeweft ./cmd/routeweft

run:
	go run ./cmd/routeweft serve

ui-install:
	cd ui && npm ci

ui-typecheck:
	cd ui && npm run typecheck

ui-build:
	cd ui && npm run build

compat-test:
	go test ./compat/...

bench-router:
	go test ./bench/router -run '^$$' -bench . -benchmem -count=5

check: test compat-test ui-typecheck ui-build
