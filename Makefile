GO ?= /opt/homebrew/bin/go
GOFMT ?= $(shell $(GO) env GOROOT)/bin/gofmt
PREFIX ?= $(HOME)/.swarm
# Pinned: the newest staticcheck that reads Go 1.27 export data. CI runs the same version through `make lint`.
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@v0.8.2
DEFAULT_SIGN_IDENTITY := $(shell security find-identity -v -p codesigning 2>/dev/null | grep -q '"Swarm Dev"' && echo "Swarm Dev" || echo "-")
SWARM_SIGN_IDENTITY ?= $(DEFAULT_SIGN_IDENTITY)

.PHONY: build web-build vet fmt staticcheck web-lint lint test test-go test-web test-menubar e2e dev dev-seed install install-daemon install-app app skills-sync

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

staticcheck:
	$(GO) run $(STATICCHECK) ./...

web-lint:
	cd web && pnpm install --frozen-lockfile && pnpm lint

# Everything CI's lint jobs run: gofmt, go vet, staticcheck and Biome. Run it before committing.
lint: fmt vet staticcheck web-lint

# §23.1's order, minus `swarm doctor`, which only runs on a real machine (Task 23).
test: test-go test-web test-menubar

# -race is several times slower; Go's 10m default timeout is too tight for internal/runtime.
test-go: vet fmt web-build
	$(GO) test -race -timeout 30m ./...
	GO=$(GO) scripts/cover.sh

test-web:
	cd web && pnpm install --frozen-lockfile && pnpm test

# Generic Task.sleep(for:) / clock sleeps abort the menubar tests on Swift 6.3; use taskSleep(_:) (docs/ci.md).
test-menubar:
	@! grep -rnE '\.sleep\((for|until):' --exclude=TaskSleep.swift apps/menubar/Sources apps/menubar/Tests \
		|| { echo 'use taskSleep(_:), not Task.sleep(for:) or a clock sleep (docs/ci.md)'; exit 1; }
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

# Builds swarm into ~/.swarm/bin. `build` already signed bin/swarm; the signed bytes are copied to a
# temp file and renamed over the target, so the installed inode is never rewritten or re-signed in
# place (that left launchd's respawn failing with OS_REASON_CODESIGNING, BUG-74/76).
install-daemon: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/swarm $(PREFIX)/bin/.swarm.new
	mv -f $(PREFIX)/bin/.swarm.new $(PREFIX)/bin/swarm

# Deploys the tunnel relay to personal Railway (workspace "My Projects").
# The bootstrap is idempotent: it ensures the project, service, secret,
# config path and domain before uploading. No PATH argument: `railway up .`
# fails with "prefix not found" on CLI 5.62.1; the bare invocation uploads
# the current directory, which is the same thing.
relay-deploy:
	scripts/relay-bootstrap.sh
	RAILWAY_API_TOKEN="$$(cat $$HOME/.config/railway-personal/token)" railway up --project "$$(scripts/relay-bootstrap.sh --project-id)" --service relay --environment production --ci

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
