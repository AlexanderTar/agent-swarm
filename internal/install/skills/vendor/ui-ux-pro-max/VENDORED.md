# Vendored: ui-ux-pro-max
- Source: https://github.com/nextlevelbuilder/ui-ux-pro-max-skill@4d140cf8ff6842de13213c7214eff3810371beb2 (tag v2.13.0), path .claude/skills/ui-ux-pro-max
- License: MIT (see LICENSE). Copyright: Next Level Builder.
- Vendored on: 2026-09-24
- Changes:
  - Dropped `scripts/tests/` (upstream's own test suite; not needed at
    runtime).
  - `SKILL.md`: every `scripts/search.py` invocation used a Claude Code
    plugin-root environment variable expansion in its path, which only
    resolves under a Claude Code plugin install. Rewrote every occurrence
    to `scripts/search.py`, a path relative to this skill's own directory,
    and reworded the surrounding note to say "relative to the directory
    containing this SKILL.md" rather than naming this repo's own path
    (which is wrong once the skill is installed elsewhere, e.g. under
    `<cwd>/.claude/skills/ui-ux-pro-max/` or `~/.swarm/skills/ui-ux-pro-max/`).
  - Kept `data/` in full, including each CSV's own source/provenance
    columns.
