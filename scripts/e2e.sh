#!/usr/bin/env bash
# End-to-end run with the fake adapter (spec §23.1, §23.2).
# It never touches the live daemon, ~/.swarm, the real tmux server or a real agent.
set -euo pipefail
export PATH=/opt/homebrew/bin:$PATH
root="$(cd "$(dirname "$0")/.." && pwd)"
export SWARM_HOME="$(mktemp -d)"
export SWARM_TMUX_SOCKET="swarm-e2e"
export SWARM_E2E_SCENARIOS_DIR="$root/scripts/e2e/scenarios"
# 17778, not 17777: that is `make dev`'s port, and the two must be able to run at
# the same time (D84).
port=17778

# R10 / S-3: a throwaway GPG keyring, created before the daemon starts so
# spawn.BaseEnv (Task 4) copies GNUPGHOME into every agent's tmux pane. It is
# inside $SWARM_HOME, which cleanup below removes, so the key is gone when the
# run ends whatever happens. A machine without gpg just skips scenario 20 (the
# harness checks and t.Skips there, not here).
export GNUPGHOME="$SWARM_HOME/gnupg"
mkdir -p "$GNUPGHOME" && chmod 700 "$GNUPGHOME"
cat >"$GNUPGHOME/batch" <<'KEY'
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Name-Real: Swarm E2E
Name-Email: e2e@example.invalid
Expire-Date: 0
%commit
KEY
gpg --batch --gen-key "$GNUPGHOME/batch" >/dev/null 2>&1 || echo "e2e: gpg unavailable; scenario 20 will skip" >&2

# `kill "${daemon_pid:-0}"` would run `kill 0` when the daemon never started, and
# `kill 0` signals the whole process group — under `make e2e` that is make, this
# shell and anything sharing the group (S5). Guard on the variable instead.
cleanup() {
  tmux -L "$SWARM_TMUX_SOCKET" kill-server 2>/dev/null || true
  if [ -n "${daemon_pid:-}" ]; then kill "$daemon_pid" 2>/dev/null || true; fi
  rm -rf "$SWARM_HOME"
}
trap cleanup EXIT

# S-4 is a default, not a flag: with SWARM_USAGE unset this daemon builds no usage
# sources, so it reads no keychain and calls no vendor endpoint. Unset it explicitly
# in case the operator's shell has it — `make e2e` must be inert whatever the
# environment. Scenario 17 therefore asserts the read side only; see Task 37.
unset SWARM_USAGE

go build -o "$SWARM_HOME/swarm" ./cmd/swarm
go build -o "$SWARM_HOME/swarm-fake-agent" ./cmd/swarm-fake-agent
"$SWARM_HOME/swarm" daemon --home "$SWARM_HOME" --port "$port" >"$SWARM_HOME/daemon.log" 2>&1 &
daemon_pid=$!
for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$port/api/health" >/dev/null && break
  sleep 0.1
done
token="$(cat "$SWARM_HOME/run/daemon.token")"
export SWARM_E2E_TOKEN="$token" SWARM_E2E_URL="http://127.0.0.1:$port" SWARM_E2E_HOME="$SWARM_HOME"
go test -tags e2e -count=1 -v ./scripts/e2e/...
