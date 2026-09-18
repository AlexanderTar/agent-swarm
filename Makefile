.PHONY: build test web-build web-test

build: web-build
	go build -o bin/swarm ./cmd/swarm

test: web-test
	go test ./...

web-build:
	cd web && pnpm install --frozen-lockfile && pnpm build

web-test:
	cd web && pnpm install --frozen-lockfile && pnpm test
