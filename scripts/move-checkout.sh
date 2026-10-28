#!/bin/sh
# Move the Swarm checkout from ~/GitHub/agent-swarm to ~/GitHub/swarmrun and
# update every record that names the old path (the spec's "Directory move and
# repo record" steps 1-9, in order). Run detached after the rebrand finishes;
# it restarts the daemon itself.
#
# Paths are overridable by env for tests (MOVE_OLD_CHECKOUT,
# MOVE_NEW_CHECKOUT, MOVE_SWARM_HOME, MOVE_WORKTREES_DIR, MOVE_CODEX_CONFIG,
# MOVE_CLAUDE_CONFIG, MOVE_CLAUDE_PROJECTS_DIR, MOVE_REPO_ID,
# MOVE_REMOTE_URL, MOVE_DAEMON_LABEL, MOVE_PLIST). --dry-run prints what
# would run and changes nothing.
#
# Logs to $SWARM_HOME/logs/move-checkout-<timestamp>.log. On any failure the
# DB backup is restored, the folder is moved back, the daemon is restarted
# and the script exits non-zero.
# POSIX sh, set -eu.
set -eu

main() {
  old_checkout="${MOVE_OLD_CHECKOUT:-$HOME/GitHub/agent-swarm}"
  new_checkout="${MOVE_NEW_CHECKOUT:-$HOME/GitHub/swarmrun}"
  swarm_home="${MOVE_SWARM_HOME:-$HOME/.swarm}"
  worktrees_dir="${MOVE_WORKTREES_DIR:-$swarm_home/worktrees}"
  codex_config="${MOVE_CODEX_CONFIG:-$HOME/.codex/config.toml}"
  claude_config="${MOVE_CLAUDE_CONFIG:-$HOME/.claude.json}"
  claude_projects="${MOVE_CLAUDE_PROJECTS_DIR:-$HOME/.claude/projects}"
  repo_id="${MOVE_REPO_ID:-repo_01M2WN0SM6ZEZBZ154JFYNG364}"
  remote_url="${MOVE_REMOTE_URL:-ssh://git@github.com/AlexanderTar/swarmrun.git}"
  daemon_label="${MOVE_DAEMON_LABEL:-dev.swarm.daemon}"
  plist="${MOVE_PLIST:-$HOME/Library/LaunchAgents/dev.swarm.daemon.plist}"
  db="$swarm_home/swarm.db"
  backups_dir="$swarm_home/backups"
  logs_dir="$swarm_home/logs"
  swarm_config="$swarm_home/config.json"

  dry=0
  if [ "${1:-}" = "--dry-run" ]; then
    dry=1
  fi

  log() {
    printf '%s\n' "$1"
    if [ "$dry" -eq 0 ]; then
      printf '%s\n' "$1" >>"$log_file"
    fi
  }

  rollback() {
    # rollback "step" "detail" "retry"
    log "$1 failed: $2 Retry with: $3"
    if [ "$backup_done" -eq 1 ] && [ -f "$backup" ]; then
      if [ "$dry" -eq 0 ]; then
        if sqlite3 "$db" ".restore '$backup'" >>"$log_file" 2>&1; then
          log "Database backup restored."
        else
          log "Warning: database restore failed."
        fi
      fi
    fi
    if [ "$moved" -eq 1 ]; then
      if [ "$dry" -eq 0 ]; then
        if mv "$new_checkout" "$old_checkout" >>"$log_file" 2>&1; then
          moved=0
          log "Checkout moved back."
        else
          log "Warning: moving the checkout back failed."
        fi
      fi
    fi
    if [ "$dry" -eq 0 ]; then
      if launchctl bootstrap "gui/$(id -u)" "$plist" >>"$log_file" 2>&1; then
        log "Daemon restarted."
      else
        log "Warning: daemon restart failed."
      fi
    fi
    exit 1
  }

  do_step() {
    # do_step "step" "retry-hint" command...
    name="$1"
    retry="$2"
    shift 2
    if [ "$dry" -eq 1 ]; then
      log "would run: $*"
      return 0
    fi
    "$@" >>"$log_file" 2>&1 || rollback "$name" "command failed: $*." "$retry"
  }

  rewrite_paths() {
    # rewrite_paths file: replace the old checkout path with the new one.
    file="$1"
    tmp_out="$(mktemp)" || rollback "Tool config" "could not create a temp file." "scripts/move-checkout.sh"
    sed "s|$old_checkout|$new_checkout|g" "$file" >"$tmp_out" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
    mv "$tmp_out" "$file" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
  }

  ts="$(date +%Y%m%d-%H%M%S)"
  backup="$backups_dir/swarm.db.pre-swarmrun-$ts"
  log_file="$logs_dir/move-checkout-$ts.log"
  moved=0
  backup_done=0

  if [ "$dry" -eq 0 ]; then
    mkdir -p "$logs_dir" "$backups_dir" \
      || { printf 'Move setup failed: could not create %s. Retry with: scripts/move-checkout.sh\n' "$logs_dir" >&2; exit 1; }
    : >"$log_file"
  fi

  if [ ! -e "$old_checkout" ] && [ -d "$new_checkout" ]; then
    log "Checkout already moved to $new_checkout; nothing to do."
    exit 0
  fi

  step() {
    log "$1"
  }

  step "Stopping the daemon..."
  do_step "Stop daemon" "launchctl bootout gui/$(id -u)/$daemon_label" \
    launchctl bootout "gui/$(id -u)/$daemon_label"

  step "Backing up the database..."
  do_step "Database backup" "sqlite3 $db \".backup $backup\"" \
    sqlite3 "$db" ".backup '$backup'"
  backup_done=1

  step "Moving the checkout..."
  do_step "Move checkout" "mv $old_checkout $new_checkout" \
    mv "$old_checkout" "$new_checkout"
  moved=1

  step "Updating the git remote..."
  do_step "Remote update" "git -C $new_checkout remote set-url origin $remote_url" \
    git -C "$new_checkout" remote set-url origin "$remote_url"
  do_step "Remote fetch" "git -C $new_checkout fetch origin" \
    git -C "$new_checkout" fetch origin

  step "Repairing worktrees..."
  if [ "$dry" -eq 1 ]; then
    log "would run: git worktree repair $worktrees_dir/agent-swarm--*"
  else
    for wt in "$worktrees_dir"/agent-swarm--*; do
      [ -e "$wt" ] || continue
      do_step "Worktree repair" "git worktree repair $wt" \
        git -C "$new_checkout" worktree repair "$wt"
      do_step "Worktree status" "git -C $wt status" \
        git -C "$wt" status
    done
  fi

  step "Updating the repo record..."
  update_sql="UPDATE repos SET path='$new_checkout', name='swarmrun', missing=0 WHERE id='$repo_id';"
  do_step "Repo record update" "sqlite3 $db \"$update_sql\"" \
    sqlite3 "$db" "$update_sql"

  step "Updating tool config..."
  if [ "$dry" -eq 1 ]; then
    log "would run: rewrite $old_checkout to $new_checkout in tool config"
  else
    for cfg in "$codex_config" "$claude_config" "$swarm_config"; do
      if [ -f "$cfg" ]; then
        rewrite_paths "$cfg"
      fi
    done
    old_slug="$(printf '%s' "$old_checkout" | sed 's|/|-|g; s|\.|-|g')"
    new_slug="$(printf '%s' "$new_checkout" | sed 's|/|-|g; s|\.|-|g')"
    if [ -e "$claude_projects/$old_slug" ]; then
      do_step "Claude projects move" "mv $claude_projects/$old_slug $claude_projects/$new_slug" \
        mv "$claude_projects/$old_slug" "$claude_projects/$new_slug"
    fi
  fi

  step "Reinstalling Swarm..."
  do_step "Reinstall" "make -C $new_checkout install" \
    make -C "$new_checkout" install
  do_step "Daemon start" "launchctl bootstrap gui/$(id -u) $plist" \
    launchctl bootstrap "gui/$(id -u)" "$plist"

  step "Checking the moved checkout..."
  do_step "Repo rescan" "swarm repos --rescan" \
    swarm repos --rescan
  do_step "Doctor check" "swarm doctor" \
    swarm doctor

  log "Move complete. The checkout now lives at $new_checkout."
}

main "$@"
