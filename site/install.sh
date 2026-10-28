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
    if [ ! -r "$tty_in" ]; then
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
  # Unit 1 stops here; units 2 and 3 continue with tools, checkout and build.
  exit 0
}

main "$@"
