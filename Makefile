GO ?= /opt/homebrew/bin/go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt

.PHONY: build vet fmt test dev e2e

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

# End-to-end run with the fake adapter, on its own port and tmux socket (never
# :7777 or -L swarm). Kept separate from `test`: it builds two binaries, spawns
# real tmux sessions and can take a while.
e2e:
	scripts/e2e.sh
