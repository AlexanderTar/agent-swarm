#!/bin/sh
# move-checkout.sh tests: runs scripts/move-checkout.sh against stub
# launchctl, sqlite3, git, make and swarm on PATH with a temp HOME.
# Never touches the real ~/.swarm, launchd, ~/GitHub or the DB.
# POSIX sh, set -eu.
set -eu

REPO_ROOT="$(dirname "$0")/.."
REPO_ROOT="$(cd "$REPO_ROOT" && pwd)"
MOVE_SH="$REPO_ROOT/scripts/move-checkout.sh"
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

slug_of() {
  printf '%s' "$1" | sed 's|/|-|g; s|\.|-|g'
}

move_env() {
  TMP_ROOT="$(mktemp -d)"
  STUB_BIN="$TMP_ROOT/bin"
  TEST_HOME="$TMP_ROOT/home"
  mkdir -p "$STUB_BIN" "$TEST_HOME"
  OLD_CHECKOUT="$TEST_HOME/GitHub/agent-swarm"
  NEW_CHECKOUT="$TEST_HOME/GitHub/swarmrun"
  mkdir -p "$OLD_CHECKOUT/.git"
  mkdir -p "$TEST_HOME/.swarm/worktrees/agent-swarm--abc"
  : >"$TEST_HOME/.swarm/swarm.db"
  printf '{"repoUrl": "https://github.com/AlexanderTar/agent-swarm.git"}\n' >"$TEST_HOME/.swarm/config.json"
  mkdir -p "$TEST_HOME/.codex"
  printf 'trust = ["%s", "%s-foo"]\n' "$OLD_CHECKOUT" "$OLD_CHECKOUT" >"$TEST_HOME/.codex/config.toml"
  printf '{"projects": {"%s": {}, "%s-foo": {}}}\n' "$OLD_CHECKOUT" "$OLD_CHECKOUT" >"$TEST_HOME/.claude.json"
  mkdir -p "$TEST_HOME/.claude/projects/$(slug_of "$OLD_CHECKOUT")"
  mkdir -p "$TEST_HOME/Library/LaunchAgents"
  : >"$TEST_HOME/Library/LaunchAgents/dev.swarm.daemon.plist"
  CALL_LOG="$TMP_ROOT/calls.log"
  : >"$CALL_LOG"
  export CALL_LOG
  export OLD_CHECKOUT NEW_CHECKOUT
  export STUB_STATE_DIR="$TMP_ROOT"
  cat >"$STUB_BIN/launchctl" <<'STUB_EOF'
#!/bin/sh
echo "launchctl $*" >>"$CALL_LOG"
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/launchctl"
  cat >"$STUB_BIN/sqlite3" <<'STUB_EOF'
#!/bin/sh
echo "sqlite3 $*" >>"$CALL_LOG"
if [ -n "${STUB_SQLITE_FAIL_ON:-}" ]; then
  case "$*" in
    *"$STUB_SQLITE_FAIL_ON"*)
      exit 1
      ;;
  esac
fi
case "${2:-}" in
  .backup*)
    dest="${2#.backup }"
    dest="${dest#\'}"
    dest="${dest%\'}"
    : >"$dest"
    ;;
esac
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/sqlite3"
  cat >"$STUB_BIN/git" <<'STUB_EOF'
#!/bin/sh
echo "git $*" >>"$CALL_LOG"
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/git"
  cat >"$STUB_BIN/make" <<'STUB_EOF'
#!/bin/sh
echo "make $*" >>"$CALL_LOG"
if [ "${STUB_MAKE_FAIL:-}" = "1" ]; then
  exit 1
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/make"
  cat >"$STUB_BIN/swarm" <<'STUB_EOF'
#!/bin/sh
echo "swarm $*" >>"$CALL_LOG"
if [ "${1:-}" = "doctor" ]; then
  if [ -f "$STUB_STATE_DIR/doctor-fails-left" ]; then
    n="$(cat "$STUB_STATE_DIR/doctor-fails-left")"
    if [ "$n" -gt 0 ]; then
      printf '%s' "$((n - 1))" >"$STUB_STATE_DIR/doctor-fails-left"
      exit 1
    fi
  fi
  if [ "${STUB_DOCTOR_FAIL:-}" = "1" ]; then
    exit 1
  fi
  exit 0
fi
if [ "${1:-}" = "repos" ]; then
  printf '%s\n' "${STUB_RESCAN_OUTPUT:-swarmrun $NEW_CHECKOUT}"
  if [ "${STUB_RESCAN_FAIL:-}" = "1" ]; then
    exit 1
  fi
fi
exit 0
STUB_EOF
  chmod +x "$STUB_BIN/swarm"
}

cleanup_tmp() {
  unset STUB_MAKE_FAIL STUB_SQLITE_FAIL_ON STUB_DOCTOR_FAIL
  unset STUB_RESCAN_FAIL STUB_RESCAN_OUTPUT STUB_STATE_DIR
  rm -rf "$TMP_ROOT"
}

run_move() {
  # run_move [--dry-run]
  if RUN_OUT="$(HOME="$TEST_HOME" MOVE_DAEMON_SLEEP=0 MOVE_DAEMON_TRIES=30 PATH="$STUB_BIN:/usr/bin:/bin" sh "$MOVE_SH" "$@" 2>&1)"; then
    RUN_STATUS=0
  else
    RUN_STATUS=$?
  fi
}

move_log() {
  ls "$TEST_HOME/.swarm/logs"/move-checkout-*.log
}

t_success_runs_steps_in_order() {
  move_env
  run_move
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "success path exits zero"
  else
    not_ok "success path exits zero" "$RUN_OUT"
  fi
  log_file="$(move_log 2>/dev/null || true)"
  prev=0
  ordered=1
  if [ -z "$log_file" ] || [ ! -f "$log_file" ]; then
    not_ok "success path runs steps in order" "no move log was written"
    ordered=0
  else
    for marker in "Stopping the daemon" "Backing up the database" \
      "Moving the checkout" "Updating the git remote" "Repairing worktrees" \
      "Updating the repo record" "Updating tool config" "Reinstalling Swarm" \
      "Checking the moved checkout" "Move complete"; do
      n="$(grep -n "$marker" "$log_file" | cut -d: -f1 | head -1)"
      if [ -z "$n" ] || [ "$n" -le "$prev" ]; then
        not_ok "success path runs steps in order" "missing or out of order: $marker"
        ordered=0
        break
      fi
      prev="$n"
    done
  fi
  if [ "$ordered" -eq 1 ]; then
    ok "success path runs steps in order"
  fi
  for expected in "bootout" ".backup" \
    "remote set-url origin ssh://git@github.com/AlexanderTar/swarmrun.git" \
    "fetch origin" "worktree repair" "UPDATE repos" \
    "make -C " "bootstrap" "repos --rescan" "swarm doctor"; do
    case "$(cat "$CALL_LOG")" in
      *"$expected"*)
        ok "success path runs: $expected"
        ;;
      *)
        not_ok "success path runs: $expected" "$(cat "$CALL_LOG")"
        ;;
    esac
  done
  n_updates="$(grep -c 'UPDATE repos' "$CALL_LOG" || true)"
  if [ "$n_updates" -eq 1 ]; then
    ok "repo record update is a single UPDATE"
  else
    not_ok "repo record update is a single UPDATE" "updates: $n_updates"
  fi
  if [ -d "$NEW_CHECKOUT/.git" ] && [ ! -e "$OLD_CHECKOUT" ]; then
    ok "checkout is moved"
  else
    not_ok "checkout is moved" "old or new checkout in the wrong place"
  fi
  if grep -q "$NEW_CHECKOUT" "$TEST_HOME/.codex/config.toml" \
    && grep -q "$NEW_CHECKOUT" "$TEST_HOME/.claude.json"; then
    ok "tool config points at the new checkout"
  else
    not_ok "tool config points at the new checkout" "config still names the old path"
  fi
  if grep -q "$OLD_CHECKOUT-foo" "$TEST_HOME/.codex/config.toml" \
    && grep -q "$OLD_CHECKOUT-foo" "$TEST_HOME/.claude.json"; then
    ok "sibling paths are left alone"
  else
    not_ok "sibling paths are left alone" "a sibling path was rewritten"
  fi
  if grep -F -q "https://github.com/AlexanderTar/swarmrun.git" "$TEST_HOME/.swarm/config.json" \
    && ! grep -q "agent-swarm" "$TEST_HOME/.swarm/config.json"; then
    ok "swarm config repoUrl names the swarmrun remote"
  else
    not_ok "swarm config repoUrl names the swarmrun remote" "$(cat "$TEST_HOME/.swarm/config.json")"
  fi
  if [ -d "$TEST_HOME/.claude/projects/$(slug_of "$NEW_CHECKOUT")" ] \
    && [ ! -e "$TEST_HOME/.claude/projects/$(slug_of "$OLD_CHECKOUT")" ]; then
    ok "Claude projects folder is renamed"
  else
    not_ok "Claude projects folder is renamed" "projects folder still has the old name"
  fi
  cleanup_tmp
}

t_failure_after_db_update_rolls_back() {
  move_env
  STUB_MAKE_FAIL="1"
  export STUB_MAKE_FAIL
  run_move
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "failed move exits non-zero"
  else
    not_ok "failed move exits non-zero" "exit was 0"
  fi
  case "$(cat "$CALL_LOG")" in
    *"UPDATE repos"*)
      ok "failure happens after the DB update"
      ;;
    *)
      not_ok "failure happens after the DB update" "$(cat "$CALL_LOG")"
      ;;
  esac
  case "$(cat "$CALL_LOG")" in
    *".restore"*)
      ok "failed move restores the DB backup"
      ;;
    *)
      not_ok "failed move restores the DB backup" "$(cat "$CALL_LOG")"
      ;;
  esac
  if [ -d "$OLD_CHECKOUT" ] && [ ! -e "$NEW_CHECKOUT" ]; then
    ok "failed move moves the folder back"
  else
    not_ok "failed move moves the folder back" "checkout not restored"
  fi
  if grep -q "$OLD_CHECKOUT" "$TEST_HOME/.codex/config.toml" \
    && ! grep -q "$NEW_CHECKOUT" "$TEST_HOME/.codex/config.toml" \
    && grep -q "$OLD_CHECKOUT" "$TEST_HOME/.claude.json" \
    && grep -q "agent-swarm.git" "$TEST_HOME/.swarm/config.json"; then
    ok "failed move restores config backups"
  else
    not_ok "failed move restores config backups" "config still names the new path"
  fi
  if [ -d "$TEST_HOME/.claude/projects/$(slug_of "$OLD_CHECKOUT")" ] \
    && [ ! -e "$TEST_HOME/.claude/projects/$(slug_of "$NEW_CHECKOUT")" ]; then
    ok "failed move moves the Claude projects folder back"
  else
    not_ok "failed move moves the Claude projects folder back" "projects folder not restored"
  fi
  n_repairs="$(grep -c 'worktree repair' "$CALL_LOG" || true)"
  if [ "$n_repairs" -ge 2 ]; then
    ok "failed move repairs worktrees again after moving back"
  else
    not_ok "failed move repairs worktrees again after moving back" "repairs: $n_repairs"
  fi
  case "$(cat "$CALL_LOG")" in
    *"bootstrap"*)
      ok "failed move restarts the daemon"
      ;;
    *)
      not_ok "failed move restarts the daemon" "$(cat "$CALL_LOG")"
      ;;
  esac
  case "$RUN_OUT" in
    *"failed"* | *"FAILED"*)
      ok "failure names the failed step"
      ;;
    *)
      not_ok "failure names the failed step" "$RUN_OUT"
      ;;
  esac
  case "$RUN_OUT" in
    *"Retry with:"*)
      ok "failure tells how to retry"
      ;;
    *)
      not_ok "failure tells how to retry" "$RUN_OUT"
      ;;
  esac
  cleanup_tmp
}

t_both_checkouts_refuse() {
  move_env
  mkdir -p "$NEW_CHECKOUT/.git"
  run_move
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "old and new checkouts together exit non-zero"
  else
    not_ok "old and new checkouts together exit non-zero" "exit was 0"
  fi
  case "$RUN_OUT" in
    *"already exists"*)
      ok "old and new checkouts together say so"
      ;;
    *)
      not_ok "old and new checkouts together say so" "$RUN_OUT"
      ;;
  esac
  if [ -d "$OLD_CHECKOUT/.git" ] && [ -d "$NEW_CHECKOUT/.git" ]; then
    ok "old and new checkouts together leave both folders alone"
  else
    not_ok "old and new checkouts together leave both folders alone" "a folder moved"
  fi
  if [ ! -s "$CALL_LOG" ]; then
    ok "old and new checkouts together run nothing"
  else
    not_ok "old and new checkouts together run nothing" "$(cat "$CALL_LOG")"
  fi
  cleanup_tmp
}

t_doctor_failure_is_warning() {
  move_env
  STUB_DOCTOR_FAIL="1"
  export STUB_DOCTOR_FAIL
  run_move
  unset STUB_DOCTOR_FAIL
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "doctor failure still exits zero"
  else
    not_ok "doctor failure still exits zero" "$RUN_OUT"
  fi
  case "$RUN_OUT" in
    *"doctor reported issues"*)
      ok "doctor failure prints a warning"
      ;;
    *)
      not_ok "doctor failure prints a warning" "$RUN_OUT"
      ;;
  esac
  if [ -d "$NEW_CHECKOUT/.git" ] && [ ! -e "$OLD_CHECKOUT" ]; then
    ok "doctor failure keeps the moved checkout"
  else
    not_ok "doctor failure keeps the moved checkout" "checkout was moved back"
  fi
  cleanup_tmp
}

t_daemon_health_wait() {
  move_env
  printf '2' >"$TMP_ROOT/doctor-fails-left"
  run_move
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "slow daemon still exits zero"
  else
    not_ok "slow daemon still exits zero" "$RUN_OUT"
  fi
  n_doctor="$(grep -c '^swarm doctor' "$CALL_LOG" || true)"
  if [ "$n_doctor" -ge 3 ]; then
    ok "move waits for the daemon to become healthy"
  else
    not_ok "move waits for the daemon to become healthy" "doctor runs: $n_doctor"
  fi
  cleanup_tmp
}

t_bad_rescan_rolls_back() {
  move_env
  STUB_RESCAN_OUTPUT="swarmrun /somewhere/else"
  export STUB_RESCAN_OUTPUT
  run_move
  unset STUB_RESCAN_OUTPUT
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "rescan without the new row exits non-zero"
  else
    not_ok "rescan without the new row exits non-zero" "exit was 0"
  fi
  case "$RUN_OUT" in
    *"Repo rescan failed"*)
      ok "rescan failure names the failed step"
      ;;
    *)
      not_ok "rescan failure names the failed step" "$RUN_OUT"
      ;;
  esac
  if [ -d "$OLD_CHECKOUT" ] && [ ! -e "$NEW_CHECKOUT" ]; then
    ok "rescan failure moves the folder back"
  else
    not_ok "rescan failure moves the folder back" "checkout not restored"
  fi
  cleanup_tmp
}

t_rollback_after_bootstrap_boots_out() {
  move_env
  STUB_RESCAN_FAIL="1"
  export STUB_RESCAN_FAIL
  run_move
  unset STUB_RESCAN_FAIL
  if [ "$RUN_STATUS" -ne 0 ]; then
    ok "post-bootstrap failure exits non-zero"
  else
    not_ok "post-bootstrap failure exits non-zero" "exit was 0"
  fi
  n_bootout="$(grep -c '^launchctl bootout' "$CALL_LOG" || true)"
  n_bootstrap="$(grep -c '^launchctl bootstrap' "$CALL_LOG" || true)"
  if [ "$n_bootout" -ge 2 ] && [ "$n_bootstrap" -ge 2 ]; then
    ok "post-bootstrap rollback boots out before restarting the daemon"
  else
    not_ok "post-bootstrap rollback boots out before restarting the daemon" "$(cat "$CALL_LOG")"
  fi
  if [ -d "$OLD_CHECKOUT" ] && [ ! -e "$NEW_CHECKOUT" ]; then
    ok "post-bootstrap failure moves the folder back"
  else
    not_ok "post-bootstrap failure moves the folder back" "checkout not restored"
  fi
  cleanup_tmp
}

t_already_moved() {
  TMP_ROOT="$(mktemp -d)"
  STUB_BIN="$TMP_ROOT/bin"
  TEST_HOME="$TMP_ROOT/home"
  mkdir -p "$STUB_BIN" "$TEST_HOME/GitHub/swarmrun/.git"
  NEW_CHECKOUT="$TEST_HOME/GitHub/swarmrun"
  export NEW_CHECKOUT
  CALL_LOG="$TMP_ROOT/calls.log"
  : >"$CALL_LOG"
  export CALL_LOG
  run_move
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "already-moved checkout exits cleanly"
  else
    not_ok "already-moved checkout exits cleanly" "$RUN_OUT"
  fi
  case "$RUN_OUT" in
    *"already moved"*)
      ok "already-moved checkout says so"
      ;;
    *)
      not_ok "already-moved checkout says so" "$RUN_OUT"
      ;;
  esac
  if [ ! -s "$CALL_LOG" ]; then
    ok "already-moved checkout runs nothing"
  else
    not_ok "already-moved checkout runs nothing" "$(cat "$CALL_LOG")"
  fi
  cleanup_tmp
}

t_dry_run_changes_nothing() {
  move_env
  codex_before="$(cat "$TEST_HOME/.codex/config.toml")"
  run_move --dry-run
  if [ "$RUN_STATUS" -eq 0 ]; then
    ok "dry run exits zero"
  else
    not_ok "dry run exits zero" "$RUN_OUT"
  fi
  if [ -d "$OLD_CHECKOUT" ] && [ ! -e "$NEW_CHECKOUT" ]; then
    ok "dry run leaves the folders alone"
  else
    not_ok "dry run leaves the folders alone" "folders changed"
  fi
  if [ ! -s "$CALL_LOG" ]; then
    ok "dry run calls no system commands"
  else
    not_ok "dry run calls no system commands" "$(cat "$CALL_LOG")"
  fi
  if [ "$(cat "$TEST_HOME/.codex/config.toml")" = "$codex_before" ] \
    && grep -q "$OLD_CHECKOUT" "$TEST_HOME/.claude.json"; then
    ok "dry run leaves config files alone"
  else
    not_ok "dry run leaves config files alone" "config changed"
  fi
  if [ -d "$TEST_HOME/.swarm/worktrees/agent-swarm--abc" ]; then
    ok "dry run leaves worktrees alone"
  else
    not_ok "dry run leaves worktrees alone" "worktrees changed"
  fi
  cleanup_tmp
}

t_success_runs_steps_in_order
t_failure_after_db_update_rolls_back
t_both_checkouts_refuse
t_doctor_failure_is_warning
t_daemon_health_wait
t_bad_rescan_rolls_back
t_rollback_after_bootstrap_boots_out
t_already_moved
t_dry_run_changes_nothing

printf 'move-checkout-test: %s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
