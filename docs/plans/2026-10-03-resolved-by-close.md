# Plan: resolved-by close (TASK-505)
Spec: `docs/specs/2026-10-03-resolved-by-close.md`. One commit per unit, red then green each.

1. Store: migration `0027_resolved_by.sql`, `Patch.ResolvedBy`, rule in `UpdateTx`/`check`, scope
   exemption for this transition only, cancel-style side effects, `item_resolved` event. Tests in
   `internal/items/transition_test.go`.
2. HTTP `PATCH /api/items/{key}` and MCP `swarm_items` `resolved_by`, echoed as `resolved_by` in output.
3. CLI `swarm resolve --by KEY2 KEY...` (GET revision, then PATCH per key).
4. Skill bullet in swarm-orchestrator; `make skills-sync`.
