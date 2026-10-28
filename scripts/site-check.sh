#!/bin/sh
# site-check.sh: fail when the swarmrun.dev site is stale or broken.
# Checks: tokens fresh, the build runs, every local link and asset exists
# (including /install.sh and /social-preview.png), the tracked sources of the
# served images exist (site/assets/social-preview.png, board-kanban png/webp),
# no raw colours in site/*.css (hex or colour functions), no "Agent Swarm"
# text, and every Screens string present in dist/index.html.
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/site/dist"
PAGE="$DIST/index.html"

fail() {
  printf 'site-check: %s\n' "$1" >&2
  exit 1
}

node "$ROOT/design/scripts/gen-tokens-css.mjs" --check || fail "design/tokens.css is stale"
sh "$ROOT/site/build.sh" || fail "site/build.sh failed"
[ -f "$PAGE" ] || fail "dist/index.html missing after build"
[ -f "$DIST/install.sh" ] || fail "dist/install.sh missing (build must copy site/install.sh)"
[ -f "$DIST/social-preview.png" ] || fail "dist/social-preview.png missing (build must copy site/assets/social-preview.png for og:image)"
[ -f "$ROOT/site/assets/social-preview.png" ] || fail "site/assets/social-preview.png missing (tracked source of og:image; a card-only commit must touch site/ so Vercel deploys it)"
[ -f "$ROOT/site/assets/board-kanban.png" ] || fail "site/assets/board-kanban.png missing (hero board capture)"
[ -f "$ROOT/site/assets/board-kanban.webp" ] || fail "site/assets/board-kanban.webp missing (hero board capture)"

if grep -rEi '#[0-9a-f]{3,8}\b' "$ROOT"/site/*.css >/dev/null 2>&1; then
  grep -rEi '#[0-9a-f]{3,8}\b' "$ROOT"/site/*.css >&2
  fail "raw hex colour in site/*.css (use tokens)"
fi

if grep -rEi '(rgb|hsl)a?\(' "$ROOT"/site/*.css >/dev/null 2>&1; then
  grep -rEi '(rgb|hsl)a?\(' "$ROOT"/site/*.css >&2
  fail "raw colour function in site/*.css (use tokens)"
fi

if grep -r "Agent Swarm" "$DIST" >/dev/null 2>&1; then
  grep -r "Agent Swarm" "$DIST" >&2
  fail "Agent Swarm text in site/dist"
fi

# Local links and assets: relative href/src must resolve inside dist
# (fragment-only links skip the file check), every #fragment must match
# an id in the page, and every srcset candidate must resolve inside dist.
grep -oE '(href|src)="[^"]+"' "$PAGE" | sed -E 's/^(href|src)="([^"]*)"$/\2/' | while IFS= read -r link; do
  case "$link" in
    http*|mailto:*|data:*) continue ;;
  esac
  path="${link%%#*}"
  frag="${link#*#}"
  [ "$path" = "$link" ] && frag=""
  if [ -n "$path" ]; then
    [ -e "$DIST/$path" ] || fail "missing asset: $path"
  fi
  if [ -n "$frag" ]; then
    grep -q "id=\"$frag\"" "$PAGE" || fail "missing #$frag target"
  fi
done
grep -oE 'srcset="[^"]+"' "$PAGE" | sed -E 's/^srcset="//; s/"$//' | tr ',' '\n' | while IFS= read -r cand; do
  url=$(printf '%s' "$cand" | awk '{print $1}')
  case "$url" in
    ""|http*|data:*) continue ;;
  esac
  [ -e "$DIST/$url" ] || fail "missing srcset asset: $url"
done

# Every external link must open in a new tab with noopener.
tmp_tags="$(mktemp)"
grep -oE '<a[^>]*>' "$PAGE" > "$tmp_tags" || true
while IFS= read -r tag; do
  case "$tag" in
    *'href="http'*) ;;
    *) continue ;;
  esac
  case "$tag" in
    *'target="_blank"'*) ;;
    *) fail "external link without target=\"_blank\": $tag" ;;
  esac
  case "$tag" in
    *noopener*) ;;
    *) fail "external link without rel noopener: $tag" ;;
  esac
done < "$tmp_tags"
rm -f "$tmp_tags"

# The hero starts with the headline: no logo lockup (nav already has it).
if grep -qE 'hero-icon|hero-name' "$PAGE"; then
  grep -oE '.{0,40}hero-(icon|name).{0,40}' "$PAGE" >&2
  fail "hero logo lockup present in dist/index.html (hero starts with the h1)"
fi

# Every Screens string, word for word.
while IFS= read -r line; do
  [ -z "$line" ] && continue
  grep -qF "$line" "$PAGE" || fail "missing Screens string: $line"
done <<'STRINGS'
Swarm: run your coding agents as a team
Swarm plans the work with you, hands it to Claude Code, Codex and other agents, and has every change reviewed before it reaches you.
How it works
Agents
Install
Run your coding agents as a team.
Tell Swarm what you want built. It drafts a plan for you to approve, then gives each piece to an agent and has another agent review it.
Install Swarm
View on GitHub
Free and open source.
All your agents on one board
Your agents keep their own logins and models. Swarm keeps track of what each one is doing.
The board
Every task, which agent has it, and what's waiting on you. Plans and finished work come here for your approval.
The menu bar app
When an agent has a question, it shows up here. Open any agent's session or pause it from the same menu.
Use the agents you already pay for
Pick a different agent for planning, building and reviewing. Claude Code can write the code while Codex reviews it.
Claude Code
Codex
Cursor
Antigravity
Grok
Muse
Agent names and logos belong to their owners and are shown only to identify the tools Swarm works with.
From a request to a merged change
Describe it
Write a sentence or a page about what you want built or fixed.
Agree on a plan
Agents read the code and write a spec. You approve it one section at a time, and nothing gets built before that.
Agents build it
The plan becomes small packages. Agents build them in parallel, writing tests first, and a second agent reviews each one.
Step in when you like
Open any agent's session, answer its questions, or pause everything.
Accept the result
You get one change to accept, as a pull request or a merge.
Install in one line
Paste this into a terminal:
curl -fsSL https://swarmrun.dev/install.sh | sh
The script installs anything missing, builds Swarm and runs its setup. Rather do it by hand?
Read the setup guide.
Swarm runs your coding agents as a team.
Releases
Contributing
License
© 2026 Alexander Taraymovich. Apache-2.0.
Claude Code, Codex, Cursor, Antigravity, Grok and Muse are trademarks of their respective owners.
STRINGS

printf 'site-check: ok\n'
