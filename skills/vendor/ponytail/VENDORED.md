# Vendored: ponytail
- Source: https://github.com/DietrichGebert/ponytail@e3ba2aa6f1e6f0bc4d69eb09c9f0d0a93af56156, path skills/ponytail
- License: MIT (see LICENSE). Copyright: DietrichGebert.
- Vendored on: 2026-09-24
- Changes:
  - Replaced the "Persistence" section's interactive mode-switching text
    (off/on by saying "stop ponytail" / "normal mode", `/ponytail
    lite|full|ultra`) with a fixed note: swarm agents have no user to
    switch modes for, so swarm always runs ponytail at `full`.
  - `description` was a multi-line YAML folded scalar (`description: >`);
    reflowed it to one line, wording unchanged, since this repo's
    frontmatter parser (`internal/install/skills.go`) only reads flat,
    single-line `key: value` frontmatter.
