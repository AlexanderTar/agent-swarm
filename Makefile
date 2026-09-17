GO ?= /opt/homebrew/bin/go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt

.PHONY: build vet fmt test dev

build:
	$(GO) build -o bin/swarm ./cmd/swarm

vet:
	$(GO) vet ./...

fmt:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

test: vet fmt
	$(GO) test -race ./...
	GO=$(GO) scripts/cover.sh

# Dev daemon: its own home and port, never ~/.swarm or :7777.
dev:
	$(GO) run ./cmd/swarm daemon --home $(HOME)/.swarm-dev --port 17777
