# Package PA: agy skills root (user asked for this in PR #20, 2026-09-25)

## Problem (evidence: docs/plans/2026-09-24-skill-symlink-probe.md, "Follow-up")
- agy 1.2.10 reads skills from `$HOME/.gemini/config/skills`. Swarm installs agy skills to `~/.gemini/antigravity-cli/skills` (`Config.SkillsDir(KindAgy)`, internal/install/config.go:~90).
- `adapter/agy.go setupEnv` gives each session a fresh `HOME=<launchDir>/agy-home`, symlinks `agy-home/.gemini/antigravity-cli` → the REAL `~/.gemini/antigravity-cli`, and creates an empty `agy-home/.gemini/config/` (no `skills`, no `.migrated`). On first run agy migrates `antigravity-cli/skills` into `$HOME/.gemini/config/skills` and leaves a link behind — and because `antigravity-cli` is the real dir, the REAL `~/.gemini/antigravity-cli/skills` gets repointed into that session's launch dir. Every new session adds a hop. On this machine the real skills tree now lives in `~/.swarm/run/launch/ses_01M36FPV68…/agy-home/.gemini/config/skills`; cleaning that session up deletes it.

## Controller decisions (locked)
1. `Config.SkillsDir(KindAgy)` = `~/.gemini/config/skills`. Link mode for agy decided by the re-probe (step 5); default Copy until proven.
2. `setupEnv`: create the real `~/.gemini/config/skills` if missing (0o755) and symlink `agy-home/.gemini/config/skills` → it. Also make agy-home not trigger migration: symlink (or create) `agy-home/.gemini/config/.migrated` mirroring the real `~/.gemini/config/.migrated` (create an empty marker in agy-home if the real one is missing). Keep the existing antigravity-cli / hooks.json / plugins links. Result: a spawned agy never rewrites the real `~/.gemini/antigravity-cli/skills`.
3. Repair of existing installs, done ONLY by explicit `swarm install` (never the daemon, never tests against the real home): if the real `~/.gemini/antigravity-cli/skills` is a symlink whose chain resolves under `<swarm home>/run/launch/`, then (a) copy the resolved tree's entries into `~/.gemini/config/skills` (don't overwrite an existing same-named entry there that is user-owned per the P1 ownership rules; swarm-owned/pre-A1-shipped `swarm*` entries are re-written by WriteSkills anyway), then (b) repoint `~/.gemini/antigravity-cli/skills` → `~/.gemini/config/skills` (agy's own post-migration shape). Never delete anything under `run/launch`.
4. Doctor: the agy skills check looks at the new root; add a warn-level check when `~/.gemini/antigravity-cli/skills` resolves into `<swarm home>/run/launch/` ("agy skills live inside a swarm session folder; run swarm install to move them" — add to spec copy).
5. Re-probe, sealed: a scratch HOME under the scratchpad with NO symlink to any real directory. Copy (not link) only the auth/onboarding files agy needs from `~/.gemini/antigravity-cli` into it (e.g. antigravity-oauth-token, antigravity_state.pbtxt, installation_id, settings.json — check what's needed). Create `$HOME/.gemini/config/.migrated`. Put the probe skill at `$HOME/.gemini/config/skills/zz-swarm-symlink-probe` as a symlink to a dir outside HOME; run agy headless; then control with a real copied dir. Before and after, record `ls -la ~/.gemini/antigravity-cli/skills ~/.gemini/config` — must be identical; if not, STOP and report BLOCKED. Set agy's link mode from the result and update docs/plans/2026-09-24-skill-symlink-probe.md with the evidence (version, exact command, prompt, output lines).

## Docs
- Add spec section "A7. agy skills root" (after A6) in docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md with decisions 1-4 and copy; add a "### PA: agy skills root" package to docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md (units below, Verify set) and tick it at the end.

## Units (one commit each, test-first; all tests use temp homes via the injected Config.UserHome / adapter UserHome)
- PA.1 SkillsDir(KindAgy) → ~/.gemini/config/skills; doctor/uninstall/WriteSkills follow; tests.
- PA.2 setupEnv links config/skills + .migrated; test that a simulated agy migration step can't reach the real antigravity-cli (assert agy-home/.gemini/config/skills is a link to UserHome/.gemini/config/skills and .migrated exists).
- PA.3 install repair (decision 3) + doctor warn (decision 4); tests with a fake chain in a temp home (link → temp swarm home run/launch/ses_x/agy-home/.gemini/config/skills with content).
- PA.4 sealed re-probe + link mode + probe doc.

## Verify
go test ./internal/install/... ./internal/adapter/... ./cmd/... ; go build ./... && go vet ./... ; go test ./... once.
