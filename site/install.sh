#!/bin/sh
# Swarm one-line installer: curl -fsSL https://swarmrun.dev/install.sh | sh
# POSIX sh. The whole body sits in main() and main "$@" is the last line, so
# a truncated download never runs a partial script. Every step prints one
# plain line; a failure names the step and how to retry.
set -eu

main() {
  step() {
    printf '%s\n' "$1"
  }

  fail() {
    # fail "step" "detail" "retry"
    printf '%s failed: %s Retry with: %s\n' "$1" "$2" "$3" >&2
    exit 1
  }

  guide_url="https://github.com/AlexanderTar/swarmrun#setup"

  tty_in="${SWARM_TTY:-/dev/tty}"
  if [ "$tty_in" != "/dev/null" ]; then
    if (: <"$tty_in") 2>/dev/null; then
      :
    else
      tty_in=/dev/null
    fi
  fi

  step "Checking system..."
  os="$(uname -s)"
  if [ "$os" != "Darwin" ]; then
    printf 'Unsupported system: %s. Swarm needs macOS 14 or later. Setup guide: %s\n' "$os" "$guide_url" >&2
    exit 1
  fi
  ver="$(sw_vers -productVersion)"
  major="${ver%%.*}"
  case "$major" in
    '' | *[!0-9]*)
      fail "System check" "could not parse macOS version '$ver'." "sw_vers -productVersion"
      ;;
  esac
  if [ "$major" -lt 14 ]; then
    printf 'macOS 14 or later is required (found %s). Setup guide: %s\n' "$ver" "$guide_url" >&2
    exit 1
  fi
  if ! command -v brew >/dev/null 2>&1; then
    # shellcheck disable=SC2016
    brew_cmd='/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
    printf 'Homebrew is missing. Install it with: %s and re-run this script.\n' "$brew_cmd" >&2
    exit 1
  fi
  if ! xcode-select -p >/dev/null 2>&1; then
    printf 'Xcode command line tools are missing. Run: xcode-select --install and re-run this script.\n' >&2
    exit 1
  fi
  step "System check passed."

  step "Installing tools..."
  missing=""
  for pkg in go tmux pnpm uv node; do
    if ! brew list --formula --versions "$pkg" >/dev/null 2>&1; then
      missing="$missing $pkg"
    fi
  done
  if [ -n "$missing" ]; then
    # shellcheck disable=SC2086
    if ! brew install $missing <"$tty_in"; then
      fail "Tool install" "brew install$missing failed." "brew install$missing"
    fi
  fi
  ghostty_app="${GHOSTTY_APP:-/Applications/Ghostty.app}"
  missing_casks=""
  if ! brew list --cask --versions ghostty >/dev/null 2>&1 \
    && [ ! -d "$ghostty_app" ]; then
    missing_casks="$missing_casks ghostty"
  fi
  if ! brew list --cask --versions ollama >/dev/null 2>&1 \
    && ! brew list --formula --versions ollama >/dev/null 2>&1 \
    && ! command -v ollama >/dev/null 2>&1; then
    missing_casks="$missing_casks ollama"
  fi
  if [ -n "$missing_casks" ]; then
    # shellcheck disable=SC2086
    if ! brew install --cask $missing_casks <"$tty_in"; then
      fail "Tool install" "brew install --cask$missing_casks failed." "brew install --cask$missing_casks"
    fi
  fi

  step "Downloading embedding model..."
  if ! ollama list <"$tty_in" >/dev/null 2>&1; then
    if command -v open >/dev/null 2>&1; then
      open -a Ollama <"$tty_in" >/dev/null 2>&1 || true
      # open returns before the app's server is listening; polling list
      # first avoids racing it with our own ollama serve on port 11434.
      tries=0
      while ! ollama list <"$tty_in" >/dev/null 2>&1; do
        tries=$((tries + 1))
        if [ "$tries" -ge "${SWARM_OLLAMA_TRIES:-30}" ]; then
          break
        fi
        sleep "${SWARM_OLLAMA_SLEEP:-2}"
      done
    fi
    if ! ollama list <"$tty_in" >/dev/null 2>&1; then
      ollama serve <"$tty_in" >/dev/null 2>&1 &
      tries=0
      while ! ollama list <"$tty_in" >/dev/null 2>&1; do
        tries=$((tries + 1))
        if [ "$tries" -ge "${SWARM_OLLAMA_TRIES:-30}" ]; then
          fail "Model download" "the Ollama server did not start." "ollama serve"
        fi
        sleep "${SWARM_OLLAMA_SLEEP:-2}"
      done
    fi
  fi
  if ! ollama pull qwen3-embedding:0.6b <"$tty_in"; then
    fail "Model download" "ollama pull qwen3-embedding:0.6b failed." "ollama pull qwen3-embedding:0.6b"
  fi

  step "Checking out Swarm..."
  repo_url="https://github.com/AlexanderTar/swarmrun.git"
  swarm_src="${SWARM_SRC:-$HOME/.swarm/src/swarmrun}"
  if [ ! -d "$swarm_src/.git" ]; then
    if ! git clone "$repo_url" "$swarm_src" <"$tty_in"; then
      fail "Source checkout" "git clone $repo_url failed." "git clone $repo_url $swarm_src"
    fi
  else
    dirty="$(git -C "$swarm_src" status --porcelain)" || fail "Source checkout" "git status in $swarm_src failed." "git -C $swarm_src status"
    if [ -n "$dirty" ]; then
      printf 'Checkout at %s has uncommitted changes. Commit or stash them, then re-run this script.\n' "$swarm_src" >&2
      exit 1
    fi
    if ! git -C "$swarm_src" pull --ff-only <"$tty_in"; then
      fail "Source update" "git pull --ff-only in $swarm_src failed." "git -C $swarm_src pull --ff-only"
    fi
  fi
  step "Building Swarm..."
  if ! make -C "$swarm_src" install <"$tty_in"; then
    fail "Build" "make install in $swarm_src failed." "make -C $swarm_src install"
  fi

  step "Setting up Swarm..."
  PATH="$HOME/.swarm/bin:$PATH"
  export PATH
  if ! swarm install --yes <"$tty_in"; then
    fail "Setup" "swarm install --yes failed." "swarm install --yes"
  fi

  step "Checking Swarm..."
  if swarm doctor <"$tty_in"; then
    :
  else
    printf 'Warning: swarm doctor reported issues. Run swarm doctor to see details.\n'
  fi

  found_agent=""
  for agent_bin in claude codex agy cursor-agent muse grok; do
    if command -v "$agent_bin" >/dev/null 2>&1; then
      found_agent="$agent_bin"
      break
    fi
  done
  if [ -z "$found_agent" ]; then
    printf 'Warning: no agent CLI found. Install one of claude, codex, agy, cursor-agent, muse or grok and sign in; Swarm needs an agent to work with.\n'
  fi
  printf 'Swarm is installed. Next step: open the Swarm menu bar app and the board.\n'
}

main "$@"
