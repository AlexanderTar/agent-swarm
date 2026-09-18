#!/usr/bin/env bash
# Mock-mode smoke run: builds SwarmBar, starts it against the fixture daemon, and checks that it
# stays up for 5 s and shows the fixture state. It never touches the live daemon, ~/.swarm,
# Notification Center or real preferences (SWARM_URL points at a closed port; SWARM_HOME and
# CFFIXED_USER_HOME point at a temp folder).
set -euo pipefail
pkg="$(cd "$(dirname "$0")/.." && pwd)"
swift build --package-path "$pkg" --product SwarmBar
bin="$(swift build --package-path "$pkg" --show-bin-path)/SwarmBar"
home="$(mktemp -d)"
log="$home/smoke.log"
CFFIXED_USER_HOME="$home" SWARM_MOCK_FIXTURES="$pkg/Tests/Fixtures" SWARM_URL=http://127.0.0.1:9 SWARM_HOME="$home" SWARM_SMOKE=1 "$bin" >"$log" 2>&1 &
pid=$!
sleep 5
if ! kill -0 "$pid" 2>/dev/null; then
  cat "$log"
  echo "smoke: SwarmBar exited early"
  exit 1
fi
kill "$pid"
wait "$pid" 2>/dev/null || true
grep -q '^smoke: state agents=5 requests=4 active=4$' "$log" || { cat "$log"; echo "smoke: fixture state not loaded"; exit 1; }
echo "smoke: ok"
