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
  unset STUB_MAKE_FAIL STUB_CAPTURE_STDIN STUB_NO_OLLAMA_BIN
  unset STUB_OLLAMA_NEEDS_SERVE STUB_DOCTOR_FAIL
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
  # GHOSTTY_APP points at a nonexistent dir unless the test creates it, so
  # the real /Applications/Ghostty.app on a Mac never leaks into tests.
  if RUN_OUT="$(SWARM_TTY=/dev/null HOME="$TEST_HOME" SWARM_SRC="$TEST_HOME/.swarm/src/swarmrun" GHOSTTY_APP="$TMP_ROOT/Ghostty.app" SWARM_OLLAMA_TRIES=30 SWARM_OLLAMA_SLEEP=0 PATH="$STUB_BIN:/usr/bin:/bin" sh "$INSTALL_SH" 2>&1)"; then
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
  # STUB_NO_OLLAMA_BIN=1 leaves out the ollama binary so the cask install
  # path is exercised; the brew stub then provides ollama when the cask
  # is installed, the way the real cask provides the CLI.
  CALL_LOG="$TMP_ROOT/calls.log"
  : >"$CALL_LOG"
  export CALL_LOG
  export STUB_BIN STUB_TMP="$TMP_ROOT"
  cat >"$TMP_ROOT/ollama-stub-template" <<'STUB_EOF'
#!/bin/sh
echo "ollama $*" >>"$CALL_LOG"
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-ollama"
fi
if [ "${1:-}" = "serve" ]; then
  : >"$STUB_TMP/ollama-serving"
  exit 0
fi
if [ "${1:-}" = "list" ] && [ -f "$STUB_TMP/ollama-list-fails-left" ]; then
  n="$(cat "$STUB_TMP/ollama-list-fails-left")"
  if [ "$n" -gt 0 ]; then
    printf '%s' "$((n - 1))" >"$STUB_TMP/ollama-list-fails-left"
    exit 1
  fi
fi
if [ "${STUB_OLLAMA_NEEDS_SERVE:-}" = "1" ] && [ ! -f "$STUB_TMP/ollama-serving" ]; then
  exit 1
fi
exit 0
STUB_EOF
  chmod +x "$TMP_ROOT/ollama-stub-template"
  if [ "${STUB_NO_OLLAMA_BIN:-}" != "1" ]; then
    cp "$TMP_ROOT/ollama-stub-template" "$STUB_BIN/ollama"
  fi
  cat >"$STUB_BIN/brew" <<'STUB_EOF'
#!/bin/sh
echo "brew $*" >>"$CALL_LOG"
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-brew"
fi
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
if [ "${1:-}" = "install" ]; then
  case "$*" in
    *--cask*ollama*|*ollama*--cask*)
      if [ ! -x "$STUB_BIN/ollama" ]; then
        cp "$STUB_TMP/ollama-stub-template" "$STUB_BIN/ollama"
      fi
      ;;
  esac
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/brew"
  cat >"$STUB_BIN/git" <<'STUB_EOF'
#!/bin/sh
echo "git $*" >>"$CALL_LOG"
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-git"
fi
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
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-make"
fi
if [ "${STUB_MAKE_FAIL:-}" = "1" ]; then
  exit 1
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/make"
  cat >"$STUB_BIN/swarm" <<'STUB_EOF'
#!/bin/sh
echo "swarm $*" >>"$CALL_LOG"
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-swarm"
fi
if [ "${1:-}" = "doctor" ] && [ "${STUB_DOCTOR_FAIL:-}" = "1" ]; then
  echo "✗ Agents: Install at least one agent CLI."
  exit 1
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/swarm"
  cat >"$STUB_BIN/open" <<'STUB_EOF'
#!/bin/sh
echo "open $*" >>"$CALL_LOG"
if [ "${STUB_CAPTURE_STDIN:-}" = "1" ]; then
  cat >"$CALL_LOG.stdin-open"
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/open"
}

run_install_piped() {
  # run_install with SENTINEL-DATA on stdin: proves prompt-capable commands
  # read from the tty, not from the piped script stream.
  if RUN_OUT="$(printf 'SENTINEL-DATA\n' | SWARM_TTY=/dev/null HOME="$TEST_HOME" SWARM_SRC="$TEST_HOME/.swarm/src/swarmrun" GHOSTTY_APP="$TMP_ROOT/Ghostty.app" SWARM_OLLAMA_TRIES=30 SWARM_OLLAMA_SLEEP=0 PATH="$STUB_BIN:/usr/bin:/bin" sh "$INSTALL_SH" 2>&1)"; then
    RUN_STATUS=0
  else
    RUN_STATUS=$?
  fi
}

t_installs_only_missing_tools() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux"
  STUB_INSTALLED_CASKS=""
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  STUB_NO_OLLAMA_BIN="1"
  export STUB_GIT_DIRTY STUB_NO_OLLAMA_BIN
  std_stubs
  run_install
  unset STUB_NO_OLLAMA_BIN
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "tool install exits zero on the success path"
  else
    not_ok "tool install exits zero on the success path" "$RUN_OUT"
  fi
  if grep -qx 'brew install pnpm uv node' "$CALL_LOG"; then
    ok "only missing formulae are installed"
  else
    not_ok "only missing formulae are installed" "$(cat "$CALL_LOG")"
  fi
  if grep -qx 'brew install --cask ghostty ollama' "$CALL_LOG"; then
    ok "only missing casks are installed"
  else
    not_ok "only missing casks are installed" "$(cat "$CALL_LOG")"
  fi
  install_lines="$(grep '^brew install' "$CALL_LOG" || true)"
  case "$install_lines" in
    *"tmux"*)
      not_ok "already-installed tools are not reinstalled" "$install_lines"
      ;;
    *)
      ok "already-installed tools are not reinstalled"
      ;;
  esac
  cleanup_tmp
}

t_node_missing_gets_brew_install() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  run_install
  if grep '^brew install' "$CALL_LOG" 2>/dev/null | grep -qw 'node'; then
    ok "missing node is installed with brew"
  else
    not_ok "missing node is installed with brew" "$(grep '^brew install' "$CALL_LOG" || true)"
  fi
  cleanup_tmp
}

t_node_present_is_skipped() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  run_install
  if grep '^brew install' "$CALL_LOG" 2>/dev/null | grep -w 'node'; then
    not_ok "present node is not reinstalled" "$(grep '^brew install' "$CALL_LOG")"
  else
    ok "present node is not reinstalled"
  fi
  cleanup_tmp
}

t_ghostty_app_counts_as_installed() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS=""
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  mkdir -p "$TMP_ROOT/Ghostty.app"
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "Ghostty.app run exits zero"
  else
    not_ok "Ghostty.app run exits zero" "$RUN_OUT"
  fi
  if grep -q '^brew install --cask' "$CALL_LOG"; then
    not_ok "Ghostty.app skips the cask install" "$(grep '^brew install' "$CALL_LOG" || true)"
  else
    ok "Ghostty.app skips the cask install"
  fi
  cleanup_tmp
}

t_ollama_cli_counts_as_installed() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  # The default stubs put an ollama CLI on PATH with no ollama cask or
  # formula installed.
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "ollama CLI run exits zero"
  else
    not_ok "ollama CLI run exits zero" "$RUN_OUT"
  fi
  if grep -q '^brew install' "$CALL_LOG"; then
    not_ok "ollama CLI skips every brew install" "$(grep '^brew install' "$CALL_LOG" || true)"
  else
    ok "ollama CLI skips every brew install"
  fi
  cleanup_tmp
}

t_ollama_server_starts_before_pull() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  STUB_OLLAMA_NEEDS_SERVE="1"
  export STUB_GIT_DIRTY STUB_OLLAMA_NEEDS_SERVE
  std_stubs
  run_install
  unset STUB_OLLAMA_NEEDS_SERVE
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "serve-then-pull run exits zero"
  else
    not_ok "serve-then-pull run exits zero" "$RUN_OUT"
  fi
  if grep -q '^ollama serve' "$CALL_LOG"; then
    ok "Ollama server is started before the pull"
  else
    not_ok "Ollama server is started before the pull" "$(cat "$CALL_LOG")"
  fi
  i_serve="$(grep -n '^ollama serve' "$CALL_LOG" | cut -d: -f1 | head -1)"
  i_pull="$(grep -n '^ollama pull qwen3-embedding:0.6b$' "$CALL_LOG" | cut -d: -f1 | head -1)"
  if [ -n "$i_serve" ] && [ -n "$i_pull" ] && [ "$i_serve" -lt "$i_pull" ]; then
    ok "server start runs before the model pull"
  else
    not_ok "server start runs before the model pull" "$(cat "$CALL_LOG")"
  fi
  cleanup_tmp
}

t_ollama_app_start_awaits_server() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  # The app takes two polls to listen: open must be followed by retries of
  # ollama list, not by an immediate private ollama serve.
  printf '2' >"$TMP_ROOT/ollama-list-fails-left"
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "app-start run exits zero"
  else
    not_ok "app-start run exits zero" "$RUN_OUT"
  fi
  case "$(cat "$CALL_LOG")" in
    *"ollama serve"*)
      not_ok "opened app is awaited instead of raced by serve" "$(cat "$CALL_LOG")"
      ;;
    *)
      ok "opened app is awaited instead of raced by serve"
      ;;
  esac
  if grep -q '^ollama pull qwen3-embedding:0.6b$' "$CALL_LOG"; then
    ok "model pull still runs after the app starts"
  else
    not_ok "model pull still runs after the app starts" "$(cat "$CALL_LOG")"
  fi
  cleanup_tmp
}

t_doctor_failure_warns() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  STUB_DOCTOR_FAIL="1"
  export STUB_GIT_DIRTY STUB_DOCTOR_FAIL
  std_stubs
  run_install
  unset STUB_DOCTOR_FAIL
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "doctor failure still exits zero"
  else
    not_ok "doctor failure still exits zero" "$RUN_OUT"
  fi
  case "$RUN_OUT" in
    *"swarm doctor reported issues"*)
      ok "doctor failure prints a warning"
      ;;
    *)
      not_ok "doctor failure prints a warning" "$RUN_OUT"
      ;;
  esac
  case "$RUN_OUT" in
    *"menu bar"*)
      ok "closing message follows a doctor failure"
      ;;
    *)
      not_ok "closing message follows a doctor failure" "$RUN_OUT"
      ;;
  esac
  cleanup_tmp
}

t_pulls_model_once() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
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
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
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
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
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
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
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

t_build_steps_run_in_order() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  run_install
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "build steps exit zero on the success path"
  else
    not_ok "build steps exit zero on the success path" "$RUN_OUT"
  fi
  i_make="$(grep -n '^make ' "$CALL_LOG" | cut -d: -f1 | head -1)"
  i_setup="$(grep -n '^swarm install --yes$' "$CALL_LOG" | cut -d: -f1 | head -1)"
  i_doctor="$(grep -n '^swarm doctor$' "$CALL_LOG" | cut -d: -f1 | head -1)"
  if [ -n "$i_make" ] && [ -n "$i_setup" ] && [ -n "$i_doctor" ] \
    && [ "$i_make" -lt "$i_setup" ] && [ "$i_setup" -lt "$i_doctor" ]; then
    ok "make install, swarm install and swarm doctor run in order"
  else
    not_ok "make install, swarm install and swarm doctor run in order" "$(cat "$CALL_LOG")"
  fi
  case "$RUN_OUT" in
    *"menu bar"*)
      ok "closing message names the next step"
      ;;
    *)
      not_ok "closing message names the next step" "$RUN_OUT"
      ;;
  esac
  cleanup_tmp
}

t_commands_ignore_script_stream() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  STUB_CAPTURE_STDIN="1"
  export STUB_GIT_DIRTY STUB_CAPTURE_STDIN
  std_stubs
  run_install_piped
  unset STUB_CAPTURE_STDIN
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "piped run exits zero"
  else
    not_ok "piped run exits zero" "$RUN_OUT"
  fi
  for cmd_name in brew git make swarm ollama; do
    cap="$CALL_LOG.stdin-$cmd_name"
    if [ ! -f "$cap" ]; then
      not_ok "$cmd_name does not read the script stream" "missing $cap"
    elif grep -q 'SENTINEL' "$cap"; then
      not_ok "$cmd_name does not read the script stream" "$(cat "$cap")"
    else
      ok "$cmd_name does not read the script stream"
    fi
  done
  cleanup_tmp
}

t_failing_step_names_itself() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  STUB_MAKE_FAIL="1"
  export STUB_GIT_DIRTY STUB_MAKE_FAIL
  std_stubs
  run_install
  unset STUB_MAKE_FAIL
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "failing build exits non-zero"
  else
    not_ok "failing build exits non-zero" "exit was 0"
  fi
  case "$RUN_OUT" in
    *"Build failed"* | *"Build: "*)
      ok "failing step names itself"
      ;;
    *)
      not_ok "failing step names itself" "$RUN_OUT"
      ;;
  esac
  case "$RUN_OUT" in
    *"Retry with: "*make*)
      ok "failing step tells how to retry"
      ;;
    *)
      not_ok "failing step tells how to retry" "$RUN_OUT"
      ;;
  esac
  case "$(cat "$CALL_LOG")" in
    *"swarm install"*)
      not_ok "failed build stops before setup" "$(cat "$CALL_LOG")"
      ;;
    *)
      ok "failed build stops before setup"
      ;;
  esac
  cleanup_tmp
}

t_no_agent_cli_warns() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  # No agent CLI on PATH: only stubs plus /usr/bin:/bin.
  run_install
  case "$RUN_OUT" in
    *"no agent CLI found"*)
      ok "missing agent CLI prints a warning"
      ;;
    *)
      not_ok "missing agent CLI prints a warning" "$RUN_OUT"
      ;;
  esac
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "missing agent CLI still exits zero"
  else
    not_ok "missing agent CLI still exits zero" "$RUN_OUT"
  fi
  cleanup_tmp
}

t_agent_cli_present_ok() {
  new_tmp
  STUB_INSTALLED_FORMULAE="go tmux pnpm uv node"
  STUB_INSTALLED_CASKS="ghostty ollama"
  export STUB_INSTALLED_FORMULAE STUB_INSTALLED_CASKS
  STUB_GIT_DIRTY=""
  export STUB_GIT_DIRTY
  std_stubs
  write_stub claude '#!/bin/sh
exit 0'
  run_install
  case "$RUN_OUT" in
    *"no agent CLI found"*)
      not_ok "present agent CLI prints no warning" "$RUN_OUT"
      ;;
    *)
      ok "present agent CLI prints no warning"
      ;;
  esac
  cleanup_tmp
}

t_unsupported_os
t_old_macos
t_no_homebrew
t_no_xcode_tools
t_installs_only_missing_tools
t_node_missing_gets_brew_install
t_node_present_is_skipped
t_ghostty_app_counts_as_installed
t_ollama_cli_counts_as_installed
t_ollama_server_starts_before_pull
t_ollama_app_start_awaits_server
t_doctor_failure_warns
t_pulls_model_once
t_fresh_clone
t_fast_forward_update
t_dirty_checkout_stops
t_build_steps_run_in_order
t_commands_ignore_script_stream
t_failing_step_names_itself
t_no_agent_cli_warns
t_agent_cli_present_ok

printf 'install-test: %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
