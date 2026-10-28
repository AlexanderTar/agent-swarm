#!/bin/sh
# rename-check.sh: fail when "Agent Swarm" or "AlexanderTar/agent-swarm" appears
# outside the must-stay-literal list (spec § Rename rules, class 3).
#
# Usage: scripts/rename-check.sh [--root DIR] [PATH ...]
#   No PATHs: scan every tracked file under the repo root.
#   PATHs are read relative to ROOT (default: the repo root).
# Prints offending path:line:content lines and exits 1 when any are found.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ "${1:-}" = "--root" ]; then
  ROOT="$2"
  shift 2
fi

is_allowed() {
  case "$1" in
    docs/plans/*|docs/specs/*|docs/investigations/*) return 0 ;;
    internal/db/schema/0001_init.sql) return 0 ;;
    internal/install/testdata/*) return 0 ;;
    internal/install/files_test.go|internal/install/agy_test.go|internal/install/codex_test.go) return 0 ;;
    internal/hook/testdata/*) return 0 ;;
    internal/adapter/testdata/*) return 0 ;;
    apps/menubar/Tests/Fixtures/*) return 0 ;;
    internal/mcpserver/tools_report_bug.go|internal/mcpserver/tools_report_bug_test.go) return 0 ;;
    scripts/move-checkout*) return 0 ;;
    scripts/rename-check*) return 0 ;;
  esac
  return 1
}

found=""
if [ "$#" -gt 0 ]; then
  files="$(printf '%s\n' "$@")"
else
  files="$(git -C "$ROOT" ls-files)"
fi

while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  is_allowed "$rel" && continue
  # Line-level allow: comments naming external spec files by filename.
  hits="$(grep -n -F -I -e 'Agent Swarm' -e 'AlexanderTar/agent-swarm' -- "$ROOT/$rel" 2>/dev/null \
    | grep -v 'superpowers/specs' || true)"
  if [ -n "$hits" ]; then
    found="$found$(printf '%s\n' "$hits" | sed "s|^|$rel:|")"
    found="$found
"
  fi
done <<EOF
$files
EOF

if [ -n "$found" ]; then
  printf '%s' "$found"
  exit 1
fi
