# CHORE-34 known debt

CHORE-34 shipped in-session model/effort recording for all agent kinds,
a cheaper Muse usage probe, and Remote Control for Claude orchestrators.
Research notes: `~/.swarm/research/CHORE-34/TASK-521.md` (model/effort
signals) and `TASK-522.md` (Muse probe cost).

## Open items

- **Muse effort change shape is a guess.** `internal/adapter/muse.go`
  (`ponytail:` near ObserveModel) parses
  `runtime.reasoning_effort_reconfigure.completed` from `session.jsonl`, but
  no local sample exists. A miss never overwrites the stored effort. Confirm
  with one live `/effort` change in a Muse session and fix the parser.
- **Cursor mid-session `/model` behaviour unverified.** The hook `model`
  field is recorded, but whether it reflects a mid-session change (and what
  Auto/`default` routes to) was not observed live.
- **Stale effort on agy/cursor model change without effort.** A hook that
  reports a new model with no effort keeps the old model's effort
  (`internal/runtime/observedmodel.go`). Both kinds usually encode effort in
  the slug, so this is rare.
- **Muse observer reset check.** The offset reset uses SameFile plus
  `size < offset`; an in-place truncate-and-rewrite to a larger size on the
  same inode would be missed. Muse only appends, so this is theoretical.
- **Reminder-agent env flags are experimental.** The Muse probe host sets
  `MUSE_EXPERIMENTAL_*_REMINDER=0`; a Muse release may rename them, which
  would silently bring back ~3.8 requests per probe.
