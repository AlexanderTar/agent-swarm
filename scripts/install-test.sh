#!/bin/sh
# install.sh stub tests: runs site/install.sh against stub commands on PATH.
# Never touches the real system: temp HOME, temp SWARM_SRC, stub bin first
# on PATH, prompts from /dev/null. POSIX sh, set -eu.
set -eu

REPO_ROOT="$(dirname "$0")/.."
REPO_ROOT="$(cd "$REPO_ROOT" && pwd)"
INSTALL_SH="$REPO_ROOT/site/install.sh"
PASS=0
FAIL=0

ok() {
  PASS=$((PASS + 1))
  printf 'ok: %s\n' "$1"
}

not_ok() {
  FAIL=$((FAIL + 1))
  printf 'FAIL: %s\n%s\n' "$1" "${2:-}" >&2
}

new_tmp() {
  TMP_ROOT="$(mktemp -d)"
  STUB_BIN="$TMP_ROOT/bin"
  TEST_HOME="$TMP_ROOT/home"
  mkdir -p "$STUB_BIN" "$TEST_HOME"
}

write_stub() {
  # write_stub name body...
  name="$1"
  body="$2"
  printf '%s\n' "$body" >"$STUB_BIN/$name"
  chmod +x "$STUB_BIN/$name"
}

cleanup_tmp() {
  rm -rf "$TMP_ROOT"
}

run_install() {
  # run_install: runs install.sh, sets RUN_OUT and RUN_STATUS.
  if RUN_OUT="$(SWARM_TTY=/dev/null HOME="$TEST_HOME" SWARM_SRC="$TEST_HOME/.swarm/src/swarmrun" PATH="$STUB_BIN:/usr/bin:/bin" sh "$INSTALL_SH" 2>&1)"; then
    RUN_STATUS=0
  else
    RUN_STATUS=$?
  fi
}

t_unsupported_os() {
  new_tmp
  write_stub uname '#!/bin/sh
echo Linux'
  write_stub sw_vers '#!/bin/sh
echo 14.5'
  write_stub brew '#!/bin/sh
exit 0'
  write_stub xcode-select '#!/bin/sh
exit 0'
  run_install
  case "$RUN_OUT" in
    *"Unsupported system: Linux. Swarm needs macOS 14 or later. Setup guide: https://github.com/AlexanderTar/swarmrun#setup"*)
      ok "unsupported OS stops with the exact message"
      ;;
    *)
      not_ok "unsupported OS message" "$RUN_OUT"
      ;;
  esac
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "unsupported OS exits non-zero"
  else
    not_ok "unsupported OS exits non-zero" "exit was 0"
  fi
  cleanup_tmp
}

t_old_macos() {
  new_tmp
  write_stub uname '#!/bin/sh
echo Darwin'
  write_stub sw_vers '#!/bin/sh
echo 13.6.0'
  write_stub brew '#!/bin/sh
exit 0'
  write_stub xcode-select '#!/bin/sh
exit 0'
  run_install
  case "$RUN_OUT" in
    *"macOS 14 or later is required (found 13.6.0). Setup guide: https://github.com/AlexanderTar/swarmrun#setup"*)
      ok "old macOS stops with the exact message"
      ;;
    *)
      not_ok "old macOS message" "$RUN_OUT"
      ;;
  esac
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "old macOS exits non-zero"
  else
    not_ok "old macOS exits non-zero" "exit was 0"
  fi
  cleanup_tmp
}

t_no_homebrew() {
  new_tmp
  write_stub uname '#!/bin/sh
echo Darwin'
  write_stub sw_vers '#!/bin/sh
echo 14.5'
  # no brew stub: brew is genuinely missing from PATH
  write_stub xcode-select '#!/bin/sh
exit 0'
  run_install
  # shellcheck disable=SC2016
  case "$RUN_OUT" in
    *'Homebrew is missing. Install it with: /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)" and re-run this script.'*)
      ok "missing Homebrew stops with the exact message"
      ;;
    *)
      not_ok "missing Homebrew message" "$RUN_OUT"
      ;;
  esac
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "missing Homebrew exits non-zero"
  else
    not_ok "missing Homebrew exits non-zero" "exit was 0"
  fi
  cleanup_tmp
}

t_no_xcode_tools() {
  new_tmp
  write_stub uname '#!/bin/sh
echo Darwin'
  write_stub sw_vers '#!/bin/sh
echo 14.5'
  write_stub brew '#!/bin/sh
exit 0'
  write_stub xcode-select '#!/bin/sh
exit 1'
  run_install
  case "$RUN_OUT" in
    *"Xcode command line tools are missing. Run: xcode-select --install and re-run this script."*)
      ok "missing Xcode tools stops with the exact message"
      ;;
    *)
      not_ok "missing Xcode tools message" "$RUN_OUT"
      ;;
  esac
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "missing Xcode tools exits non-zero"
  else
    not_ok "missing Xcode tools exits non-zero" "exit was 0"
  fi
  cleanup_tmp
}

t_unsupported_os
t_old_macos
t_no_homebrew
t_no_xcode_tools

printf 'install-test: %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
