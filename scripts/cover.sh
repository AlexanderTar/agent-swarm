#!/usr/bin/env bash
# Coverage gate (spec §23.3): 85% for items, runtime, hook, mcpserver, worktree, migrate;
# 75% for every other internal package that has tests.
set -euo pipefail
GO=${GO:-go}
strict=" items runtime hook mcpserver worktree migrate "
out=$("$GO" test -cover ./internal/... 2>&1) || { echo "$out"; exit 1; }
echo "$out"
fail=0
while read -r line; do
  pkg=$(awk '{print $2}' <<<"$line")
  pct=$(grep -oE 'coverage: [0-9.]+%' <<<"$line" | grep -oE '[0-9.]+' || true)
  [ -z "$pct" ] && continue
  name=${pkg#github.com/AlexanderTar/agent-swarm/internal/}
  top=${name%%/*}
  min=75
  [[ "$strict" == *" $top "* ]] && min=85
  if awk -v p="$pct" -v m="$min" 'BEGIN { exit !(p < m) }'; then
    echo "coverage gate: internal/$name is at $pct%, needs $min%"
    fail=1
  fi
done < <(grep -E '^ok' <<<"$out")
[ "$fail" = 0 ] && echo "coverage gate: passed"
exit "$fail"
