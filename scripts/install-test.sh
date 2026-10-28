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

std_stubs() {
  # Full success-path stubs with call logging. Caller sets
  # STUB_INSTALLED_FORMULAE / STUB_INSTALLED_CASKS / STUB_GIT_DIRTY first.
  CALL_LOG="$TMP_ROOT/calls.log"
  : >"$CALL_LOG"
  export CALL_LOG
  cat >"$STUB_BIN/brew" <<'STUB_EOF'
#!/bin/sh
echo "brew $*" >>"$CALL_LOG"
if [ "${1:-}" = "list" ]; then
  kind="${2:-}"
  name="${4:-}"
  case "$kind" in
    --formula) list="${STUB_INSTALLED_FORMULAE:-}" ;;
    --cask) list="${STUB_INSTALLED_CASKS:-}" ;;
    *) exit 0 ;;
  esac
  case " $list " in
    *" $name "*) exit 0 ;;
    *) exit 1 ;;
  esac
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/brew"
  cat >"$STUB_BIN/git" <<'STUB_EOF'
#!/bin/sh
echo "git $*" >>"$CALL_LOG"
if [ "${1:-}" = "clone" ]; then
  mkdir -p "$3/.git"
  exit 0
fi
if [ "${1:-}" = "-C" ]; then
  shift 2
  if [ "${1:-}" = "status" ]; then
    printf '%s' "${STUB_GIT_DIRTY:-}"
    exit 0
  fi
  exit 0
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/git"
  cat >"$STUB_BIN/make" <<'STUB_EOF'
#!/bin/sh
echo "make $*" >>"$CALL_LOG"
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/make"
  cat >"$STUB_BIN/swarm" <<'STUB_EOF'
#!/bin/sh
echo "swarm $*" >>"$CALL_LOG"
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/swarm"
  cat >"$STUB_BIN/ollama" <<'STUB_EOF'
#!/bin/sh
echo "ollama $*" >>"$CALL_LOG"
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/ollama"
}

t_installs_only_missing_tools() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux"
  STUB_INSTALLED_CASKS="ghostty"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "tool install exits zero on the success path"
  else
    not_ok "tool install exits zero on the success path" "$RUN_OUT"
  fi
  if grep -qx 'brew install pnpm uv' "$CALL_LOG"; then
    ok "only missing formulae are installed"
  else
    not_ok "only missing formulae are installed" "$(cat "$CALL_LOG")"
  fi
  if grep -qx 'brew install --cask ollama' "$CALL_LOG"; then
    ok "only missing casks are installed"
  else
    not_ok "only missing casks are installed" "$(cat "$CALL_LOG")"
  fi
  install_lines="$(grep '^brew install' "$CALL_LOG" || true)"
  case "$install_lines" in
    *"tmux"* | *"ghostty"*)
      not_ok "already-installed tools are not reinstalled" "$install_lines"
      ;;
    *)
      ok "already-installed tools are not reinstalled"
      ;;
  esac
  cleanup_tmp
}

t_pulls_model_once() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  run_install
  n_pulls="$(grep -c '^ollama pull qwen3-embedding:0.6b$' "$CALL_LOG" || true)"
  if [ "$n_pulls" -eq 1 ]; then
    ok "embedding model is pulled exactly once"
  else
    not_ok "embedding model is pulled exactly once" "pulls: $n_pulls"
  fi
  cleanup_tmp
}

t_fresh_clone() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  # SWARM_SRC does not exist: fresh clone expected.
  run_install
  if grep -q '^git clone https://github.com/AlexanderTar/swarmrun.git' "$CALL_LOG"; then
    ok "fresh checkout is cloned"
  else
    not_ok "fresh checkout is cloned" "$(cat "$CALL_LOG")"
  fi
  cleanup_tmp
}

t_fast_forward_update() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  mkdir -p "$TEST_HOME/.swarm/src/swarmrun/.git"
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "clean checkout update exits zero"
  else
    not_ok "clean checkout update exits zero" "$RUN_OUT"
  fi
  if grep -q 'pull --ff-only' "$CALL_LOG"; then
    ok "clean checkout is fast-forwarded"
  else
    not_ok "clean checkout is fast-forwarded" "$(cat "$CALL_LOG")"
  fi
  case "$(cat "$CALL_LOG")" in
    *"clone"*)
      not_ok "clean checkout is not cloned again" "$(cat "$CALL_LOG")"
      ;;
    *)
      ok "clean checkout is not cloned again"
      ;;
  esac
  cleanup_tmp
}

t_dirty_checkout_stops() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=" M dirty-file"
  export STUB_GIT_DIRTY
  std_stubs
  mkdir -p "$TEST_HOME/.swarm/src/swarmrun/.git"
  run_install
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "dirty checkout exits non-zero"
  else
    not_ok "dirty checkout exits non-zero" "exit was 0"
  fi
  case "$RUN_OUT" in
    *"has uncommitted changes"*)
      ok "dirty checkout stops with the exact message"
      ;;
    *)
      not_ok "dirty checkout stops with the exact message" "$RUN_OUT"
      ;;
  esac
  case "$(cat "$CALL_LOG")" in
    *"pull --ff-only"*)
      not_ok "dirty checkout is never updated" "$(cat "$CALL_LOG")"
      ;;
    *)
      ok "dirty checkout is never updated"
      ;;
  esac
  cleanup_tmp
}

t_unsupported_os
t_old_macos
t_no_homebrew
t_no_xcode_tools
t_installs_only_missing_tools
t_pulls_model_once
t_fresh_clone
t_fast_forward_update
t_dirty_checkout_stops

printf 'install-test: %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
