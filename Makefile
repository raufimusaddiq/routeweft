.PHONY: test build run ui-install ui-typecheck ui-build check

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

check: test ui-typecheck ui-build
