# P1 round-2 review findings (Opus) — fix these

## C1 (Critical). cmd/swarm/daemon.go:227 + internal/install/skills.go:440-481: startup refresh reaches the REAL user's skill roots
openDaemon passes UserHome: os.UserHomeDir() together with a custom cfg.Home. RefreshSkillLinks then uses roots built from UserHome (~/.claude/skills, ~/.codex/skills, ...). anyAlreadyInstalled/isSwarmOwned count any marker dir, or any marker-less pre-A1 swarm/swarm-orchestrator dir, as owned regardless of which swarm home wrote it. So a daemon with a temp/dev home relinks/recopies the operator's live roots against its own cfg.Home/skills.
This ACTUALLY HAPPENED on this machine today: running cmd/swarm tests (TestDaemonServesTheP2Routes) left ~/.claude/skills/{swarm,swarm-orchestrator,swarm-batching} as dangling symlinks into a deleted /var/folders/.../T/... dir and adopted/overwrote dirs in the codex/cursor/muse/agy roots. (The controller has already repaired the live dirs; do not touch ~ yourself.)
No test that opens the daemon sets HOME. TestOpenDaemonSyncsSkills claims to guard against leaking but only checks cfg.Home/skills exists.
`make dev` (--home ~/.swarm-dev) would repoint real roots at ~/.swarm-dev/skills and prod flips them back.
Fix (required):
 1. daemonConfig gets an injectable UserHome (default os.UserHomeDir()); every cmd/swarm test that opens the daemon passes a temp dir.
 2. Only refresh per-kind links when cfg.Home == filepath.Join(userHome, ".swarm"). Don't gate on Dev.
 3. Tie marker ownership to the home that wrote it: write the skills-home path into .swarm-managed and compare it in isSwarmOwned (a marker naming another home = not ours; an empty legacy marker: treat as ours only if the controller rules otherwise — default: not ours during the daemon refresh).
 4. Regression test: a daemon with a temp swarm home leaves a pre-seeded UserHome skills root byte-for-byte untouched.
 5. Add a test-suite-wide safety net in cmd/swarm tests (e.g. TestMain setting HOME to a temp dir) so no cmd/swarm test can ever touch the real home again.

## I1 (Important). internal/install/files.go:21-23: WriteIfChanged mode fix now applies to every caller
EditJSON (files.go:69), codex.go:109/:297, agy.go:96 call WriteIfChanged even when content is equal. User configs the operator set to 0600 (~/.cursor/mcp.json, ~/.codex/config.toml, muse settings.json, agy MCP config) get reset to 0644 on every install, and wrote=true is reported for a no-op.
Fix: restore the old no-op on equal content in WriteIfChanged; do the chmod only in the skills paths (syncSkills, copyTreeSynced). Add a test that WriteIfChanged leaves a 0600 file with equal content at 0600 and returns wrote=false. Keep TestWriteIfChangedFixesTheModeWhenOnlyThatDrifted's intent by moving it to the skills sync path.

## I2 (Important). skills.go:223, :235-250: adopting pre-A1 dirs widens "swarm-owned" beyond spec
A marker-less swarm/swarm-orchestrator dir with one SKILL.md whose name: matches is adopted -> a user's own same-named single-file skill gets RemoveAll'd (symlink mode), overwritten (copy mode) or deleted by uninstall.
Fix (controller ruling): adopt a pre-A1 dir only when its SKILL.md byte-matches a body swarm actually shipped before A1. Build the list from git history: every historical blob of skills/swarm/SKILL.md and skills/swarm-orchestrator/SKILL.md on main (`git log --format=%H --all -- skills/swarm/SKILL.md` then `git show <c>:path | shasum -a 256`), stored as sha256 hex constants with a comment saying how they were generated. Also, the daemon refresh never adopts — only explicit `swarm install` does. Test: a user-owned same-name skill whose body isn't a shipped body is left untouched and reported user-owned; a byte-matching pre-A1 body is adopted by install.

## M2 (Minor, fix it — one line). adapter/claude.go:141 comment says the cwd is "created fresh"; MkdirAll keeps it across attempts. Correct the comment.

Deferred (do NOT fix now): M1 python3 warn level, M3, M4, M5.
