# Superpowers remediation: delivery, instructions, enforcement

## Context

Agent Swarm spawns four agent kinds (Claude, Codex, Cursor, Agy) to work on
software. Every spawned agent is supposed to follow the Superpowers
discipline (`superpowers:brainstorming` → `superpowers:writing-plans` for
features, `superpowers:systematic-debugging` for bugs) via
`skills/swarm-orchestrator/SKILL.md:36`. In practice this has not been
happening reliably, for three distinct and independently-fixable reasons
found during this investigation:

1. **Delivery is broken for at least one kind, and incomplete for another.**
   `internal/adapter/codex.go`'s `setupEnv` isolates `CODEX_HOME` to a fresh
   empty directory and symlinks only `auth.json` into it — none of
   `~/.codex/hooks.json` (swarm's hook wiring), `~/.codex/skills/` (the
   `swarm`/`swarm-orchestrator` skills `WriteSkills` installs), or
   `~/.codex/plugins/` (where the superpowers marketplace plugin lives) are
   carried over. A spawned Codex agent today has zero swarm protocol
   awareness and zero superpowers skills. This is the same bug class fixed
   for Agy yesterday (`eecbe6f`, `8f70743`), just never applied to Codex.
   Agy's own fix is itself incomplete: it symlinks `hooks.json` and the
   `antigravity-cli` directory (which holds the swarm skills), but never
   symlinks `~/.gemini/config/plugins/` — so the superpowers plugin itself
   (`brainstorming`, `systematic-debugging`, `writing-plans` skill
   definitions) still isn't reachable inside an isolated Agy session, even
   though `Agy.SuperpowersInstalled()` (which gates orchestrator spawning,
   `internal/runtime/agents.go:166`) checks the real home and reports "installed."

2. **The orchestrator skill's own instructions contradict this repo's
   standard.** `skills/swarm-orchestrator/SKILL.md:45` tells every swarm
   orchestrator to write specs/plans to `~/.superpowers/specs/` and
   `~/.superpowers/plans/` and "never write them into a repo." This repo's
   `CLAUDE.md` mandates the opposite for interactive work (`docs/specs/`,
   `docs/plans/`, in-repo, never `.superpowers/`). The two conventions serve
   different actors (a swarm orchestrator may work across repos it doesn't
   own; this spec itself, written interactively, correctly goes to
   `docs/specs/` per that same CLAUDE.md) — so this isn't strictly a
   contradiction, but the path itself is stale: it predates the existing
   decision to centralize swarm-owned state under `~/.swarm/` (worktrees
   already live at `~/.swarm/worktrees`, not as repo siblings).

3. **The bypass is real: nothing stops a top-level item from being created
   and completed without ever touching brainstorming.**
   `POST /api/items` (`internal/httpapi/items.go:164`, the board UI's
   "create item") explicitly blocks `type: spike` ("Spikes start with an
   intent. Use New spike.") but freely creates an `epic`, `bug`, or `chore`
   directly from a human-typed title/brief — no spike, no orchestrator, no
   plan artifact. Once an orchestrator is spawned against such an item,
   nothing in the checkpoint path requires it to have produced a spec/plan
   before marking the item `completed` — `superpowers:brainstorming` usage
   is advisory text only. This matches the observed pattern: the last ~20
   commits on `main` (agy-skills fixes, native-wake, repo-proposal schema)
   all carry the `Co-Authored-By: Claude Sonnet 5` trailer, which the
   `swarm` skill explicitly forbids swarm-spawned agents from adding
   (`skills/swarm/SKILL.md` rule 10) — meaning this recent work was done by
   an interactive Claude Code session, not a swarm-spawned one, and
   therefore was never subject to any swarm-side gate at all.

**Two different populations, two different fixes.** Swarm-spawned agents can
be gated in Go code (items 1 and 3 above). Interactive Claude Code sessions
(including the one that wrote this spec) never touch swarm's database, so no
amount of agent-swarm code can gate them — that population is governed by
this repo's `CLAUDE.md` plus Claude Code's own hook configuration
(`settings.json`), which is out of this spec's scope and tracked as a
follow-up task in the companion plan instead.

**Collision warning:** `internal/adapter/*.go`, `internal/runtime/checkpoint.go`,
and `skills/swarm-orchestrator/SKILL.md` are hot files (5+ commits touching
adapters in the last 24h). Rebase before merging if `main` has moved.

## Locked decisions

- Fix Codex to full parity with Agy's post-fix state: symlink hooks config,
  skills directory, and plugins directory into the isolated `CODEX_HOME`.
- Fix Agy's remaining gap: symlink `~/.gemini/config/plugins/` into the
  isolated home (in addition to the hooks.json symlink it already has).
- Cursor: the working assumption (from the existing code comment at
  `internal/adapter/cursor.go:49`, "cursor has no HOME-wide config dir to
  isolate, only CURSOR_DATA_DIR") is that `CURSOR_DATA_DIR` does not affect
  where `cursor-agent` resolves plugins from, so no fix is needed there. This
  assumption is verified empirically (Verification section) before being
  trusted; if wrong, the fix mirrors Codex/Agy's pattern.
- Claude gets no changes — it has no HOME isolation and already gets the
  real home's plugin cache directly.
- No new abstraction layer (no shared cross-kind path registry). Each
  adapter's `setupEnv` gets the missing `symlinkIfExists` calls directly,
  the same way Agy's fix did it. The regression risk (a future kind's
  `SuperpowersInstalled()` glob and its `setupEnv` symlink drifting apart
  again, as just happened twice) is covered by a table-driven parity test,
  not by production code indirection.
- Spec/plan/debug-report location for swarm orchestrators moves from
  `~/.superpowers/specs|plans/` to `~/.swarm/specs|plans/`, consistent with
  the existing `~/.swarm/worktrees` centralization decision. Debug reports
  move from `~/.superpowers/specs/<...>-debug-<slug>.md` to
  `~/.swarm/specs/<...>-debug-<slug>.md` (same directory as specs, matching
  current behavior — only the root changes).
- Enforcement gate applies only to root items of type `epic` or `bug` (the
  two non-chore root types `validateTreeShape` allows) that are not
  `tdd_exempt`. `chore` roots are intentionally excluded — chore is already
  the codebase's designated "lightweight, no spec/plan process" root type
  (mirrored by `tdd_exempt`'s existing values: `docs`, `config`,
  `mechanical-rename`, `spike-research`). `story` and `task` are never
  gated here — they aren't roots and already derive/require their own
  completion rules.
- The gate fires on the `CompletedCkp` checkpoint the orchestrator writes
  for its own root item (the existing path described at
  `internal/runtime/checkpoint.go:378-393`), not on a new endpoint.
- Interactive-session enforcement (a Claude Code hook) is explicitly out of
  this spec; it's a distinct, smaller follow-up task in the companion plan.

## DB models

No schema changes. The `artifacts` table and its `kind` column
(`"spec" | "plan" | "debug_report" | "note"`, per
`internal/runtime/artifacts.go:337` and `RegisterArtifact`) already support
everything the enforcement gate needs. `ArtifactsFor(ctx, itemID)`
(`internal/runtime/artifacts.go:459`) already returns every artifact
registered against an item — the gate is a filter over its existing result,
not new storage.

## Model / API types

New helper in `internal/runtime/checkpoint.go`, adjacent to the existing
`priorVerify`/`verifyOK` helpers used by the current TDD gate:

```go
// requiredArtifactKind returns the artifact kind a root item's completed
// checkpoint must have on record before it's accepted, or "" if none is
// required (not a gated root type, or tdd_exempt).
func requiredArtifactKind(it items.Item) string {
	if it.TddExempt != "" {
		return ""
	}
	switch it.Type {
	case items.Epic:
		return "plan"
	case items.Bug:
		return "debug_report"
	default:
		return ""
	}
}

// hasArtifact reports whether an artifact of the given kind is registered
// against this item.
func (s *Store) hasArtifact(ctx context.Context, itemID, kind string) (bool, error) {
	arts, err := s.ArtifactsFor(ctx, itemID)
	if err != nil {
		return false, err
	}
	for _, a := range arts {
		if a.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}
```

Call site: inside the existing `if in.Kind == CompletedCkp { ... }` block in
`checkpoint.go` (the same block containing the current line-395 TDD-verify
check), before the `INSERT INTO checkpoints` at line ~411:

```go
if kind := requiredArtifactKind(it); kind != "" {
	ok, err := s.hasArtifact(ctx, tx, it.ID, kind)
	if err != nil {
		return err
	}
	if !ok {
		return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			"completed requires a registered %s for this %s. Register one with "+
				"swarm_artifact register, or set tdd_exempt if this genuinely needs neither.",
			kind, it.Type)}
	}
}
```

(`hasArtifact` takes the open `tx` rather than `ctx` alone, matching every
other helper called from inside this transaction — `ArtifactsFor` needs a
`*sql.Tx`-taking sibling, or an inline query; exact plumbing is an
implementation-time call, not a design one, since `ArtifactsFor`'s current
signature takes no tx and this is inside one.)

`internal/adapter/codex.go`, `setupEnv` — add after the existing
`auth.json` symlink block:

```go
for _, rel := range []string{"hooks.json", "skills", "plugins"} {
	if err := symlinkIfExists(
		filepath.Join(c.d.UserHome, ".codex", rel),
		filepath.Join(codexHome, rel),
	); err != nil {
		return nil, err
	}
}
```

(`symlinkIfExists` already exists in `internal/adapter/agy.go` as a
package-level function in `package adapter` — reused as-is, not duplicated.)

`internal/adapter/agy.go`, `setupEnv` — add alongside the existing
`hooks.json` symlink (line ~70-73):

```go
if err := symlinkIfExists(filepath.Join(a.d.UserHome, ".gemini", "config", "plugins"),
	filepath.Join(agyHome, ".gemini", "config", "plugins")); err != nil {
	return nil, err
}
```

`skills/swarm-orchestrator/SKILL.md` line 45 — text change only, no code
type involved:

```
- Write specs to `~/.swarm/specs/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, plans to `~/.swarm/plans/<YYYY-MM-DD>-<SPIKE-KEY>-<slug>.md`, debug reports to `~/.swarm/specs/<YYYY-MM-DD>-<SPIKE-KEY>-debug-<slug>.md`. Never write them into a repo. Use `## ` headings for sections.
```

## Screens

Not applicable — this is a backend/process change. No UI surfaces change.
(The board's "create item" flow, `POST /api/items`, keeps behaving exactly
as it does today; only what happens *after* an orchestrator tries to
complete the resulting item changes.)

## All user-facing copy

- New checkpoint-rejection error (returned to the orchestrator agent, shown
  in its tool result, not to the human user directly):
  `"completed requires a registered plan for this epic. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither."`
  and the bug variant:
  `"completed requires a registered debug_report for this bug. Register one with swarm_artifact register, or set tdd_exempt if this genuinely needs neither."`
  (Generated by the `fmt.Sprintf` above; `it.Type` renders as `epic`/`bug`
  directly, matching this repo's existing lowercase-type error style, e.g.
  `internal/items/store.go`'s `"Only an orchestrator or a plan can set tdd_exempt."`.)
- No other new strings. Doctor output, the board UI, and existing skill
  copy outside the one line 45 edit are unchanged.

## File list

**Changed:**
- `internal/adapter/codex.go` — `setupEnv`: symlink `hooks.json`, `skills`, `plugins` from real `~/.codex/` into isolated `CODEX_HOME`.
- `internal/adapter/agy.go` — `setupEnv`: symlink `~/.gemini/config/plugins` into the isolated home.
- `internal/runtime/checkpoint.go` — add `requiredArtifactKind`, `hasArtifact`, and the gate call inside the existing `CompletedCkp` handling block.
- `skills/swarm-orchestrator/SKILL.md` — line 45: `~/.superpowers/` → `~/.swarm/` for specs, plans, and debug reports.

**New tests:**
- `internal/adapter/codex_test.go` — `setupEnv` symlinks hooks/skills/plugins when present, no-ops when absent (mirrors existing `symlinkIfExists` no-op tests in `agy_test.go`).
- `internal/adapter/agy_test.go` — `setupEnv` symlinks `config/plugins`.
- `internal/adapter/adapter_test.go` (or a new `superpowers_parity_test.go` in the same package) — table-driven: for every isolating kind (Codex, Agy, and Cursor only if verification finds it needs the fix), assert `setupEnv`'s symlink targets cover every root `SuperpowersInstalled()` globs under.
- `internal/runtime/checkpoint_test.go` — `completed` on an `epic` with no registered `plan` artifact is rejected with the exact message above; passes once one is registered; `bug` variant with `debug_report`; `chore` root is never gated; `tdd_exempt` root is never gated; `story`/`task` items are unaffected (existing TDD-verify gate still applies to them unchanged).

**Reused unchanged:**
- `internal/runtime/artifacts.go` (`ArtifactsFor`, `RegisterArtifact`) — no changes, only called from the new gate.
- `internal/adapter/agy.go`'s `symlinkIfExists` — reused by `codex.go`, not duplicated.
- `internal/items/model.go` (`Type` constants) — no changes.

**Not touched (explicitly, so a reviewer doesn't wonder):**
- `internal/adapter/claude.go` — no isolation, no gap.
- `internal/adapter/cursor.go` — pending the empirical verification in this plan; only touched if that verification finds a real gap.
- `internal/install/*.go` (doctor, plugins) — the existing `SuperpowersInstalled`/`SuperpowersOK` checks correctly answer "is the plugin installed on this machine"; that predicate isn't wrong, only `setupEnv`'s carry-over was incomplete, so doctor needs no new check for this fix.
- `internal/httpapi/items.go` — the board's direct-create flow stays as-is; the gate is at completion time, not creation time, so a human can still legitimately create a chore or a quick bug report without going through a spike.

## Verification

Command order:

1. `cd internal/adapter && go test ./...` — new and existing adapter tests pass, including the parity table test.
2. `cd internal/runtime && go test ./... -run TestCheckpoint` — new gate tests pass alongside the existing TDD-verify tests (confirms the new gate doesn't regress the line-395 behavior for tasks).
3. `go build ./...` and `go vet ./...` from repo root — no breakage elsewhere.
4. Empirical Cursor check (spike-style, before deciding whether Cursor needs the fix): spawn a real `cursor-agent` process with `CURSOR_DATA_DIR` pointed at a fresh empty directory (no `swarm install` output present) and check whether its reported plugin/skill list is empty. If empty, Cursor has the same bug class and needs the Codex/Agy-style fix (falls back to this same spec's pattern, filed as a fast-follow task rather than reopening brainstorming). If non-empty (plugins resolve from a fixed path regardless of `CURSOR_DATA_DIR`), the locked assumption holds and no further Cursor change is made.
5. End-to-end scenario, happy path: `swarm install` for codex → spawn a Codex orchestrator on a test epic → confirm (via the spawned process's own environment, or a debug log) that `hooks.json`, `skills/swarm/SKILL.md`, and a `superpowers/*/skills/brainstorming/SKILL.md` are all visible inside its isolated `CODEX_HOME`.
6. End-to-end scenario, gate — reject path: create an epic directly via `POST /api/items` (mirroring the real bypass), spawn an orchestrator on it, have it attempt `swarm_checkpoint kind: "completed"` on the epic with no artifact registered → expect the new rejection message.
7. End-to-end scenario, gate — accept path: same setup, orchestrator calls `swarm_artifact register` with `kind: "plan"` first → `completed` now succeeds.
8. End-to-end scenario, gate — exempt path: same setup but the epic has `tdd_exempt` set → `completed` succeeds without any artifact.
9. End-to-end scenario, chore skip path: a `chore` root with no artifact → `completed` succeeds (chore is never gated).

## Explicitly out of scope

- Retroactive audit or backfill of the ~20 spec-less commits already on
  `main` — the user explicitly scoped this remediation to fixing the
  process going forward, not re-litigating past commits.
- Interactive Claude Code session enforcement (a `settings.json` hook
  blocking commits without a same-session `docs/specs`/`docs/plans` touch).
  Tracked as a distinct follow-up task in the companion plan; it's
  `update-config`/hook territory, not agent-swarm Go code, and deserves its
  own design pass (false-positive risk for legitimate no-spec commits like
  typo fixes needs thought).
- Any change to `POST /api/items` itself (still allows direct creation of
  epics/bugs/chores without a spike) — the gate is deliberately placed at
  completion time so quick, legitimate direct-creation use (a human typing
  a one-line chore) isn't blocked, only skipping the process for anything
  substantial enough to reach `completed` on a root.
- Building the Cursor fix now — only its empirical verification is in
  scope; the fix itself (if needed) is a fast-follow using this same spec's
  pattern.
- Any change to `swarm_materialize`, the Spike artifact-approval flow, or
  `tdd_exempt`'s existing task-level semantics — all already correct and
  unchanged by this work.
