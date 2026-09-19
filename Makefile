GO ?= /opt/homebrew/bin/go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
PREFIX ?= $(HOME)/.swarm

.PHONY: build web-build vet fmt test test-go test-web test-menubar e2e dev dev-seed install app skills-sync

build: web-build
	$(GO) build -o bin/swarm ./cmd/swarm

web-build:
	cd web && pnpm install --frozen-lockfile && pnpm build

vet:
	$(GO) vet ./...

fmt:
	@out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# §23.1's order, minus `swarm doctor`, which only runs on a real machine (Task 23).
test: test-go test-web test-menubar

test-go: vet fmt web-build
	$(GO) test -race ./...
	GO=$(GO) scripts/cover.sh

test-web:
	cd web && pnpm install --frozen-lockfile && pnpm test

test-menubar:
	cd apps/menubar && swift build -c release && swift test
	apps/menubar/scripts/cover.sh

# Dev daemon: its own home and port, never ~/.swarm or :7777. --dev enables
# POST /api/dev/seed only; it gates nothing else (usage polling stays off
# whatever --dev is, S-4).
dev:
	$(GO) run ./cmd/swarm daemon --dev --home $(HOME)/.swarm-dev --port 17777

# Loads contracts §6's fixture keys into the running `make dev` daemon.
dev-seed:
	$(GO) run ./cmd/swarm dev-seed --home $(HOME)/.swarm-dev --url http://127.0.0.1:17777

# End-to-end run with the fake adapter, on its own port and tmux socket (never
# :7777 or -L swarm). Kept separate from `test`: it builds two binaries, spawns
# real tmux sessions and can take a while.
e2e:
	scripts/e2e.sh

# §19: builds swarm into ~/.swarm/bin and Swarm.app into ~/Applications.
install: build app
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/swarm $(PREFIX)/bin/swarm

app:
	apps/menubar/scripts/bundle.sh

# The embedded skill copies must match the canonical skills/ files (§18, §21.1).
skills-sync:
	cp skills/swarm/SKILL.md internal/install/skills/swarm/SKILL.md
	cp skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md
