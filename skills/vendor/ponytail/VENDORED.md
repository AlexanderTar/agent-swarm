# Vendored: ponytail
- Source: https://github.com/DietrichGebert/ponytail@e3ba2aa6f1e6f0bc4d69eb09c9f0d0a93af56156, path skills/ponytail
- License: MIT (see LICENSE). Copyright: DietrichGebert.
- Vendored on: 2026-09-24
- Changes:
  - Replaced the "Persistence" section's interactive mode-switching text
    (off/on by saying "stop ponytail" / "normal mode", `/ponytail
    lite|full|ultra`) with a fixed note: swarm agents have no user to
    switch modes for, so swarm always runs ponytail at `full`.
  - Removed the same "stop ponytail" / "normal mode" / session-persistence
    sentence from the closing "Boundaries" section for the same reason (a
    second copy of the mode-switch instruction the spec calls out).
  - Dropped the `argument-hint: "[lite|full|ultra]"` frontmatter field: it
    is the removed slash-command mode switch's own argument hint, so it
    goes with the rest of that removal.
  - Everything else, including the multi-line `description: >` frontmatter,
    is byte-verbatim.
