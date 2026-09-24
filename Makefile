GO ?= /opt/homebrew/bin/go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
PREFIX ?= $(HOME)/.swarm
DEFAULT_SIGN_IDENTITY := $(shell security find-identity -v -p codesigning 2>/dev/null | grep -q '"Swarm Dev"' && echo "Swarm Dev" || echo "-")
SWARM_SIGN_IDENTITY ?= $(DEFAULT_SIGN_IDENTITY)

.PHONY: build web-build vet fmt test test-go test-web test-menubar e2e dev dev-seed install install-daemon install-app app skills-sync

APP_DIR ?= /Applications

build: web-build
	$(GO) build -o bin/swarm ./cmd/swarm
	codesign --force --sign "$(SWARM_SIGN_IDENTITY)" --identifier dev.swarm.daemon bin/swarm

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

# §19: builds and installs both. Kept as two independent targets below so
# reinstalling one never rebuilds or touches the other.
install: install-daemon install-app

# Builds swarm into ~/.swarm/bin.
install-daemon: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/swarm $(PREFIX)/bin/swarm
	codesign --force --sign "$(SWARM_SIGN_IDENTITY)" --identifier dev.swarm.daemon $(PREFIX)/bin/swarm

# Builds Swarm.app into /Applications.
install-app: app
	mkdir -p $(APP_DIR)
	-osascript -e 'quit app id "dev.swarm.menubar"'
	@for i in 1 2 3 4 5; do pgrep -x Swarm >/dev/null || break; sleep 1; done; pkill -x Swarm 2>/dev/null; true
	rm -rf $(APP_DIR)/Swarm.app
	cp -R apps/menubar/.build/Swarm.app $(APP_DIR)/Swarm.app
	open $(APP_DIR)/Swarm.app

app:
	apps/menubar/scripts/bundle.sh

# The embedded skill mirror must match the canonical skills/ tree byte-for-byte
# (§18, §21.1, A1). Plain cp -R rather than rsync: rsync is not guaranteed to be
# installed on every dev/CI machine.
skills-sync:
	rm -rf internal/install/skills
	cp -R skills internal/install/skills
