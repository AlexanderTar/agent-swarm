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
  repo_url_cfg="${MOVE_REPO_URL:-https://github.com/AlexanderTar/swarmrun.git}"
  daemon_label="${MOVE_DAEMON_LABEL:-dev.swarm.daemon}"
  plist="${MOVE_PLIST:-$HOME/Library/LaunchAgents/dev.swarm.daemon.plist}"
  daemon_tries="${MOVE_DAEMON_TRIES:-30}"
  daemon_sleep="${MOVE_DAEMON_SLEEP:-2}"
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
          moved_back=1
          log "Checkout moved back."
        else
          log "Warning: moving the checkout back failed."
        fi
      fi
    fi
    if [ "$config_backup_done" -eq 1 ]; then
      if [ "$dry" -eq 0 ]; then
        for cfg in "$codex_config" "$claude_config" "$swarm_config"; do
          cfg_backup="$backups_dir/$(basename "$cfg").pre-swarmrun-$ts"
          if [ -f "$cfg_backup" ] && ! cp "$cfg_backup" "$cfg" >>"$log_file" 2>&1; then
            log "Warning: restoring $cfg failed."
          fi
        done
        log "Config backups restored."
      fi
    fi
    if [ "$projects_moved" -eq 1 ]; then
      if [ "$dry" -eq 0 ]; then
        if mv "$claude_projects/$new_slug" "$claude_projects/$old_slug" >>"$log_file" 2>&1; then
          projects_moved=0
          log "Claude projects folder moved back."
        else
          log "Warning: moving the Claude projects folder back failed."
        fi
      fi
    fi
    if [ "$moved_back" -eq 1 ] && [ "$old_checkout_repaired" -eq 0 ]; then
      if [ "$dry" -eq 0 ]; then
        for wt in "$worktrees_dir"/agent-swarm--*; do
          [ -e "$wt" ] || continue
          git -C "$old_checkout" worktree repair "$wt" >>"$log_file" 2>&1 \
            || log "Warning: repairing $wt after moving back failed."
        done
        old_checkout_repaired=1
        log "Worktrees repaired against the restored checkout."
      fi
    fi
    if [ "$dry" -eq 0 ]; then
      if [ "$daemon_started" -eq 1 ]; then
        launchctl bootout "gui/$(id -u)/$daemon_label" >>"$log_file" 2>&1 \
          || log "Warning: stopping the restarted daemon failed."
      fi
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
    # The match is anchored on a following quote, slash, space or
    # end-of-line, so sibling paths such as <old>-foo are left alone.
    file="$1"
    tmp_out="$(mktemp)" || rollback "Tool config" "could not create a temp file." "scripts/move-checkout.sh"
    old_re="$(printf '%s' "$old_checkout" | sed 's|[][\\.*^$]|\\&|g')"
    new_rep="$(printf '%s' "$new_checkout" | sed 's|[\\&]|\\&|g')"
    sed -e "s|$old_re\\([\"'/ ]\\)|$new_rep\\1|g" -e "s|$old_re\$|$new_rep|g" "$file" >"$tmp_out" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
    mv "$tmp_out" "$file" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
  }

  set_repo_url() {
    # set_repo_url file: point the repoUrl key at the swarmrun remote.
    # The real config value is a GitHub URL, not a checkout path, so the
    # path rewrite above leaves it unchanged; set it explicitly.
    file="$1"
    tmp_out="$(mktemp)" || rollback "Tool config" "could not create a temp file." "scripts/move-checkout.sh"
    url_rep="$(printf '%s' "$repo_url_cfg" | sed 's|[\\&]|\\&|g')"
    sed "s|\"repoUrl\":[[:space:]]*\"[^\"]*\"|\"repoUrl\": \"$url_rep\"|" "$file" >"$tmp_out" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
    mv "$tmp_out" "$file" \
      || rollback "Tool config" "rewriting $file failed." "scripts/move-checkout.sh"
    if ! grep -F -q "$repo_url_cfg" "$file"; then
      rollback "Tool config" "setting repoUrl in $file failed." "scripts/move-checkout.sh"
    fi
  }

  ts="$(date +%Y%m%d-%H%M%S)"
  backup="$backups_dir/swarm.db.pre-swarmrun-$ts"
  log_file="$logs_dir/move-checkout-$ts.log"
  moved=0
  moved_back=0
  backup_done=0
  config_backup_done=0
  projects_moved=0
  old_checkout_repaired=0
  daemon_started=0
  old_slug=""
  new_slug=""

  if [ "$dry" -eq 0 ]; then
    mkdir -p "$logs_dir" "$backups_dir" \
      || { printf 'Move setup failed: could not create %s. Retry with: scripts/move-checkout.sh\n' "$logs_dir" >&2; exit 1; }
    : >"$log_file"
  fi

  if [ -e "$old_checkout" ] && [ -e "$new_checkout" ]; then
    printf 'Move setup failed: %s already exists. Remove it or move it aside, then re-run scripts/move-checkout.sh\n' "$new_checkout" >&2
    exit 1
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
        do_step "Config backup" "cp $cfg $backups_dir/" \
          cp "$cfg" "$backups_dir/$(basename "$cfg").pre-swarmrun-$ts"
      fi
    done
    config_backup_done=1
    for cfg in "$codex_config" "$claude_config"; do
      if [ -f "$cfg" ]; then
        rewrite_paths "$cfg"
      fi
    done
    if [ -f "$swarm_config" ]; then
      rewrite_paths "$swarm_config"
      set_repo_url "$swarm_config"
    fi
    old_slug="$(printf '%s' "$old_checkout" | sed 's|/|-|g; s|\.|-|g')"
    new_slug="$(printf '%s' "$new_checkout" | sed 's|/|-|g; s|\.|-|g')"
    if [ -e "$claude_projects/$old_slug" ]; then
      do_step "Claude projects move" "mv $claude_projects/$old_slug $claude_projects/$new_slug" \
        mv "$claude_projects/$old_slug" "$claude_projects/$new_slug"
      projects_moved=1
    fi
  fi

  step "Reinstalling Swarm..."
  do_step "Reinstall" "make -C $new_checkout install" \
    make -C "$new_checkout" install
  do_step "Daemon start" "launchctl bootstrap gui/$(id -u) $plist" \
    launchctl bootstrap "gui/$(id -u)" "$plist"
  daemon_started=1

  step "Checking the moved checkout..."
  if [ "$dry" -eq 1 ]; then
    log "would run: wait for daemon health, then swarm repos --rescan and swarm doctor"
  else
    tries=0
    while ! swarm doctor >>"$log_file" 2>&1; do
      tries=$((tries + 1))
      if [ "$tries" -ge "$daemon_tries" ]; then
        log "Warning: the daemon is not healthy yet. Continuing anyway; run swarm doctor to see details."
        break
      fi
      sleep "$daemon_sleep"
    done
    rescan_out="$(mktemp)" || rollback "Repo rescan" "could not create a temp file." "swarm repos --rescan"
    if swarm repos --rescan >"$rescan_out" 2>&1; then
      cat "$rescan_out" >>"$log_file"
    else
      cat "$rescan_out" >>"$log_file"
      rm -f "$rescan_out"
      rollback "Repo rescan" "swarm repos --rescan failed." "swarm repos --rescan"
    fi
    n_rows="$(grep -F "$new_checkout" "$rescan_out" | grep -c "swarmrun" || true)"
    rm -f "$rescan_out"
    if [ "$n_rows" -ne 1 ]; then
      rollback "Repo rescan" "rescan did not list exactly one '$new_checkout' row." "swarm repos --rescan"
    fi
    log "Repo rescan lists the moved checkout."
    if swarm doctor >>"$log_file" 2>&1; then
      :
    else
      log "Warning: swarm doctor reported issues. Run swarm doctor to see details."
    fi
  fi

  log "Move complete. The checkout now lives at $new_checkout."
}

main "$@"
