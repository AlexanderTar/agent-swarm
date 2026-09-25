# SDD ledger — plan: docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md
Branch: claude/happy-ptolemy-fke4jx, integration worktree /Users/alexandertar/GitHub/agent-swarm--self-contained-tasks
Already done (prior session): P1, P5, P6 merged.
Baseline (Mac): go test ./... only TestBoardServedAtRoot fails (web bundle not built).
Ruling: workspace in scratchpad, not .superpowers/ — CLAUDE.md bans .superpowers/ — cost if wrong: none (scratch).
Ruling: merged local main (13 unpushed commits) into branch at 23b5368 — overlap in internal/install/codex.go, runtime/agents.go, limits.go — cost if wrong: PR diff includes main's unpushed commits until main is pushed.
Ruling: user direction — Opus reviewers, Sonnet implementers.
Ruling: packages run in own worktrees off the branch, merged back with --no-ff; P2 and P7 in parallel (file-disjoint); P3 waits for P2 (skills_test.go + mirror).
Dispatched (23b5368): P1 round-2 full review (opus), P6 f97734e review (opus), skilllink empirical check (sonnet, wt agent-swarm--skilllink), P2 impl (sonnet, wt agent-swarm--p2, base 23b5368), P7 impl (sonnet, wt agent-swarm--p7, base 23b5368).
User: one sub-agent at a time. Stopped P6 review, skilllink, P2, P7 (no commits, worktrees clean, no probe leftovers). P1 review still running.
Queue (serial): P1 review -> P6 f97734e review -> skilllink check -> P2 -> P3 -> P4 -> P7 -> P8 -> P9 -> P10 -> P11 -> P12 -> final review.
User: relaxed to 2-3 concurrent agents max.
Dispatched: P6 f97734e review (opus), P2 impl (sonnet, base 23b5368). Live: P1 review, P6 review, P2 impl. Next up when a slot frees: skilllink check, then P3 (after P2), P7.
P1 round-2 review (opus): Needs fixes — C1 daemon refresh touches real ~ roots (actual damage today from a cmd/swarm test run at 19:34; controller restored ~/.claude,.codex,.cursor,.config/muse,.gemini/antigravity-cli skills to main's install state at 19:45), I1 WriteIfChanged chmod leaks to all configs, I2 pre-A1 adoption too wide. Minors M1,M3,M4,M5 deferred.
Task P1: minor (deferred): M1 python3 check is OK:true (no warn level in Check); M3 user-owned dir without SKILL.md reported as "Missing…run install"; M4 rollback test doesn't cover skillsHome; M5 LinkSkills/SkillLinkMode thin wrappers.
Ruling: I2 fix = adopt pre-A1 dirs only on byte-match against shipped historical bodies, and never during daemon refresh — spec only names symlink/marker as ownership — cost if wrong: some pre-A1 installs with edited bodies stay "user-owned" until user deletes them.
Ruling: C1 fix = refresh only when cfg.Home == userHome/.swarm, marker records its skills home, TestMain sets HOME in cmd/swarm — cost if wrong: dev daemons never refresh live roots (intended).
P6 f97734e review (opus): Needs fixes — pinnedFrom stale filter re-spawns step 0 after resume. Minors folded into same fix round.
Ruling: resume retry after a stale-review escalation re-runs the fix step then review at fresh sha (spec ~737) — cost if wrong: one extra build run.
Instruction to all agents: never run go test ./... or ./cmd/... until P1 C1 fix lands.
Dispatched: P1 fix round 1 (sonnet, wt p1fix, base 23b5368), P6 fix round 1 (sonnet, wt p6fix, base 23b5368). Live: P2 impl, P1 fix, P6 fix.
P2 impl DONE_WITH_CONCERNS 23b5368..ccc20ce (7 commits). Concern: A1 frontmatter parser reads folded 'description: >' literally; 4 upstream frontmatters reflowed and documented.
Dispatched: P2 review r1 (opus) on core diff + mechanical diff -r vs upstream. Live: P1 fix, P6 fix, P2 review.
P2 review r1 (opus): Needs fixes — 1 Important (revert frontmatter reflows, yaml.v3 in test helper) + 4 minors folded into fix round 1. Provenance/licenses all verified.
P6 fix r1 DONE 23b5368..e5877d4 (7c75604,71bd100,8d43e54,e5877d4)
Task P2: fix round 1/5 (5 findings sent; commit b8b89ce) — awaiting re-review
Task P6: fix round 1/5 (5 addressed, 0 open; new Minor regression: phase2 stale outranks phase3 crash on resume; commits 23b5368..e5877d4)
Ruling: send the Minor regression to fix round 2 instead of deferring — introduced by the fix, P9 engine builds on Next's resume behaviour — cost if wrong: one extra small round.
Task P6: out-of-scope (deferred): round-1 failure loop hides a stale-review reason behind a crash reason; pinFromStale can't detect staleness vs a carried-forward Of step.
Task P2: fix round 1/5 (5 addressed, 0 open; commits ccc20ce..b8b89ce)
Task P2: complete (commits 23b5368..b8b89ce, review clean) — merged f47d944
Task P2: minor (deferred): skills_test.go:27-33,:753-754 comments narrate review history
Dispatched: P3 impl (sonnet, wt p3, base d53b689). Ruling: superpowers ref check uses the 15 names from muse's installed superpowers 6.4.1 package — cost if wrong: test too strict/lenient by one name. Live: P1 fix, P6 fix r2, P3 impl.
P6 fix r2 DONE e5877d4..4e66a0e
Task P6fix: fix round 2/5 (1 addressed, 0 open; e5877d4..4e66a0e)
Task P6fix: complete (commits 23b5368..4e66a0e, review clean) — merged into branch
Task P6fix: minor (deferred): next_test.go:1362,:1464 cite 'spec ~737' line numbers
Dispatched: P7 impl (sonnet, wt p7, base e344780). Live: P1 fix, P3 impl, P7 impl. Queue: skilllink check after P1 fix; P4 after P3; P8 after P7.
P1 fix r1 DONE_WITH_CONCERNS 23b5368..3ed946a. Concern: live roots are marker-less pre-A1 dirs (from controller repair) -> doctor says user-owned until explicit swarm install adopts them.
Ruling: accept — explicit swarm install adopts byte-matching pre-A1 dirs; the daemon refresh never adopts — cost if wrong: doctor warns until user runs swarm install once after upgrade (mention in PR notes).
P3 impl DONE d53b689..8414070 (6 commits). Note: moved one pre-existing test assertion (rule 11 -> swarm-coder), ticked plan boxes in pkg branch.
Task P1fix: fix round 1/5 (4 addressed, 1 new Important open — adoption test fixture uses current body, breaks when P3 lands; commits 23b5368..3ed946a). Folding minors: CheckSkills adopt=true, gate-only test, TestMain unset SWARM_HOME/URL.
Task P1fix: out-of-scope (deferred): foreign marker blocks explicit default install from reclaiming a dev-home-claimed root (per ruling).
P3 review r1 (opus): Needs fixes — Important: swarm rule 3/4 still relays completed to parent for workflow steps (breaks relay suppression). 7 minors folded into fix round 1.
Task P3: fix round 1 committed c949c0e — awaiting re-review
Task P3: fix round 1/5 (8 addressed, 1 new Important open — coder step 2 doesn't say green goes in a progress checkpoint; commits 8414070..c949c0e)
Task P1fix: fix round 2 committed 3ed946a..39febe7 — awaiting re-review
Task P1fix: fix round 2/5 (4 addressed, 0 open; 3ed946a..39febe7)
Task P1fix: complete (commits 23b5368..39febe7, review clean) — merged 8961a13. go test ./... on merged branch: only baseline TestBoardServedAtRoot fails; real home unchanged.
Note: worktrees based before 8961a13 (p3, p7) still carry the home-leak bug — keep the no ./cmd tests rule for them.
Task P1fix: minor (deferred): TestMainScrubs... only proves unset ran.
Merge note: when P3 lands, TestSkillBodyCarriesTheSpecFrontmatterAndLastRule takes P3's version.
Dispatched: skilllink check (sonnet, wt skilllink, base 0963ca1). Live: P3 fix r2, P7 impl, skilllink.
Ruling: tdd gate in fix rounds requires red→green only for units named by unit-tagged findings (at least one pair if any finding is untagged); unchanged units need none; verify gate still covers them — spec B5 literal reading demanded fake reds on untouched units — cost if wrong: an unnamed unit changed in a fix round without a red (reviewer + verify are backstop). File: ruling-tdd-fix-rounds.md. Carry to P8 dispatch.
Task P3: fix round 2 + 2b committed c949c0e..7981427 (36d90a4, b1bb0fd spec B5 amended, 7981427) — re-review dispatched (opus).
Task P3: fix round 2/5 (3 addressed + ruling applied, 0 open; c949c0e..7981427)
Task P3: complete (commits d53b689..7981427, review clean) — merged b2b876f; go test ./... only baseline fails.
Task P3: minor (deferred): 'new attempt' pin is a bare substring; fix-round TDD scope wording in swarm-coder:44 unpinned.
Ruling: tdd gate scope = this step's attempts in the current round (crash re-attempts keep evidence); package-wide fix-round miss uses non-batched copy; B6 'same session' → 'new session of same agent' — reviewer found crash-retry gap and B6 contradiction — cost if wrong: a crashed builder's pre-crash red counts after retry. File ruling-tdd-followups.md; carry into P8.
Ruling: P4 uses kinds.Role("designer") inline for the RoleSkills designer row if kinds.RoleDesigner (P7) isn't merged yet; switch to the constant at merge.
Dispatched: P4 impl (sonnet, wt p4, base b2b876f). Live: P7 impl, skilllink, P4 impl.
P7 impl DONE e344780..405543d (5 commits). Concern: invented copy 'Only an orchestrator or a plan can set workflow, steps, units, solo or verify.' (spec has none).
Dispatched: P7 review r1 (opus). Real-home skill roots verified intact after P7 run. Live: skilllink, P4 impl, P7 review.
P7 review r1 (opus): Approved, spec ✅. Ruling: fold minors 1 (task-only fields on non-task items), 4 (empty unit title/steps), invented copy into spec, 2 coverage tests into a follow-up round — P8/P9 rely on them — cost if wrong: one short extra round.
Task P7: minor (deferred): role_hint mismatch silently overwritten; error names task by title; deref/derefStr/jsonUnits helpers; web Item fields optional; swarm_read omits null workflow/solo (matches convention).
P4 impl DONE b2b876f..20c2fb7 (5 commits).
Dispatched: P4 review r1 (opus). P7 follow-up committed 405543d..e1c7762.
Task P7: fix round 1/5 (4 addressed, 0 open; 405543d..e1c7762)
Task P7: complete (commits e344780..e1c7762, review clean) — merged 0fa87d9. Conflict skills/swarm/SKILL.md rule 5: kept P3's version (already lists Workflow + swarm_workflow).
Task P7: minor (deferred): no test pins story after_tasks / root integration create after onlyTasksSet; spec lacks tdd_exempt refusal copy lines; chores can't declare verify; blank top-level steps accepted.
Web typecheck/test green on a0c3a34 after pnpm install (earlier FAIL was missing node_modules).
P4 review r1 (opus): Needs fixes — Important: advisor prompt lacks rule 5 (escalate unresolved high-stakes uncertainty). Minors 2-5 folded. Also merge PR branch into pkg/p4 and switch to kinds.RoleDesigner.
Task P4: minor (deferred): no spawn-path test for spike item-type lookup; agents.go:1013 swallows QueryRow error (pre-existing).
Task P4: fix round 1 committed 2c88cf8..42554cb — re-review dispatched (opus). Live: skilllink, P8 impl, P4 re-review.
Task P4: fix round 1/5 (6 addressed, 0 open; 2c88cf8..42554cb)
Task P4: complete (commits b2b876f..42554cb, review clean) — merged.
Task P4: minor (deferred): text.go:185 comment should point at agents.role CHECK, not agents.go; spec A4 advisor wording vs skill (native advisor sees full history).
skilllink DONE 863b894: all 5 CLIs follow symlinked skill dirs -> all Symlink. Probe cleanup verified by controller.
skilllink review (opus): codex/cursor/muse Symlink justified; agy NOT (agy reads ~/.gemini/config/skills; migration confound). CRITICAL: probe left live ~/.gemini/antigravity-cli/skills dangling -> controller restored it to original target ses_01M36H52AF.../agy-home/.gemini/config/skills at 21:05 (content matches main).
Ruling: agy stays Copy (unverified); agy SkillsDir path bug is pre-existing and out of scope — documented as a follow-up — cost if wrong: agy agents may not see swarm skills until the follow-up lands (same as before this PR).
skilllink fix r1 committed 863b894..165a1ff. Concern: prod agy setupEnv copy-and-repoints real ~/.gemini/antigravity-cli/skills each spawn (pre-existing live drift) -> follow-up.
skilllink: fix round 1/5 (items 1,2,4,5 addressed; 3 NOT — doc contradiction + overclaimed copy mechanism). Code+tests approved. Noted: real agy skills tree lives in ~/.swarm/run/launch/ses_01M36FPV68.../agy-home — session cleanup would delete it (pre-existing; tell user).
skilllink: fix round 2/5 (A,D,E addressed; B new self-contradiction :204-209 + 3 minor wording) -> fix round 3 sent with exact replacement text.
skilllink: fix round 3/5 (4 addressed, 0 open; 92f1618..b745613)
skilllink: complete (0963ca1..b745613) — merged.
skilllink: minor (deferred): probe doc :144-151 'The real risk' paragraph still single-risk framing; :107-108 nit about Resume.
Follow-up (out of PR scope, tell user): agy SkillsDir points at a path agy migrates away from; setupEnv repoints real ~/.gemini/antigravity-cli/skills per new session; real agy skills tree lives in ses_01M36FPV68 launch dir (cleanup would delete it).
skilllink merged 8eaf92f (conflict in skills_test.go: both sides appended tests; kept both). go test ./... baseline only. Live: P8 impl only.
P8 impl DONE_WITH_CONCERNS a0c3a34..af84a37 (6 commits). Concern: closeCompletedSiblings exempts orchestrator callers from same-role filter to keep legacy s11-tool-stubs behaviour.
P8 review r1 (opus): Needs fixes — I1 tdd fix-round detection wrong for multi-loop/first runs; I2 orchestrator exemption leaks to workflow tasks; I3 completedCurrent ignores designer/researcher (plan-mandated); I4 applyGates fails open. Legacy tests insertions-only ✅.
Ruling: R1 orchestrator sibling-close exemption only for legacy tasks — B5 literal text would break untouched legacy test — cost if wrong: legacy orchestrator keeps closing workers as today.
Ruling: R2 completedCurrent workflow branch counts any non-reviewer/non-orchestrator role — B5 'gated roles' breaks design-reviewed/research templates — cost if wrong: a designer/researcher completed could move a task toward InReview.
Ruling: R3 fix round = run of a loop's fix step after that loop's review asked for changes at round-1; zero findings → package-wide ≥1 pair; unit-tagged findings on non-batched task → package-wide — cost if wrong: tdd gate too strict/lenient in rare multi-loop specs.
Task P8: fix round 1 committed af84a37..953baea (11 commits)
Task P8: fix round 1/5 (11 + R1-R3 addressed; 1 new Important: story after_tasks reviewer refused by stepFor; commits af84a37..953baea)
Carry to P9: R2 means a designer completed in design→…→build moves task to InReview before build — P9 status handling must account for it.
2026-09-25: rate limit hit (account switched). P8 fix r2 interrupted after 033c1a9, 1fe8bd7 + uncommitted finding-4 RED; resumed same agent.
Task P8: fix round 2 committed 953baea..a79ec70
Task P8: fix round 2/5 (5 addressed, 0 open; 953baea..a79ec70)
Task P8: complete (commits a0c3a34..a79ec70, review clean) — merged da83e44; go test ./... baseline only.
Dispatched: P9 impl (sonnet, wt p9, base b36a5fd). Live: P9 impl.
P9 impl DONE_WITH_CONCERNS b36a5fd..b749c63 (7 commits incl. self fix round). Concerns: RetryFix fallback to fresh spawn if builder session not terminal; Resume note delivery when budget-blocked; SessionID/RequestID unused until P10; StartWorkflow story copy mismatch for P10.
User 2026-09-25: (a) RetryFix must never spawn a fresh builder while the old session is live — daemon closes the old session first, then retries (coordinate in daemon). Folded into P9 fix round 1.
User: keep everything local, no push to PR #20 for now.
User: agy follow-up goes in THIS PR — new package "PA" (agy skills root): move Config.SkillsDir(KindAgy) to ~/.gemini/config/skills; adapter setupEnv links agy-home/.gemini/config/skills straight to it (no copy-and-repoint of the real ~/.gemini/antigravity-cli/skills); migrate the existing chain safely (real tree currently in ~/.swarm/run/launch/ses_01M36FPV68.../agy-home/.gemini/config/skills); re-probe with a copy control in a sealed scratch HOME only. Runs after P9 merges (file-disjoint from P10, can run alongside it).
P9 review r1 (opus): Needs fixes — I1 stranded active runs; I2 spawn-before-claim double spawn/infinite respawn; I3 review worktree only on first insert; I4 findings lost on fresh spawn/resume; I5 resumeBumpsRound re-implements planner wrongly; I6 pause treated as crash; I7 FIFO per-workflow only; I8 resume/cancel ownership+race; I9 deliverNote dup. Legacy ✅. + user directive on RetryFix.
Dispatched: P9 fix r1 (resumed), PA impl (sonnet, wt pa, base b36a5fd). Controller snapshot of real agy dirs: scratchpad/agy-home-snapshot-before-pa.txt. Live: P9 fix, PA impl.
User 2026-09-25: muse spawns get full home/context/skills/MCPs — should be isolated like others; probe to validate. New package PM (wt pm, base b36a5fd). Phase 1 probes dispatched (sonnet), stops for ruling before implementing. Snapshot: scratchpad/muse-home-snapshot-before-pm.txt. Live: P9 fix, PA impl, PM probe (3/3).
PM phase 1 done: root cause = HOME not isolated (muse scans ~/.claude/.codex/.agents skills, loads ~/.claude/CLAUDE.md) + settings clone carries operator MCP creds.
Ruling: PM drops all operator MCP servers (user's request) — cost if wrong: user loses a wanted MCP in muse spawns.
Ruling: PM keeps real XDG_DATA_HOME shared (plugin store integrity rejects copies) — cost if wrong: muse session logs + non-superpowers plugins visible to spawns.
Ruling: PM phase 2 starts with a live tmux TUI probe for blocking dialogs. File pm-ruling.md.
PA impl DONE b36a5fd..1b78a27 (5 commits). Real agy dirs unchanged vs snapshot. Needs real swarm install after merge to repair live chain.
PA review r1 (opus): Needs fixes — Important: symlinked legacy entry aborts swarm install for agy. Minors 2-7 + one extra sealed probe with symlinked config/skills root folded in.
Note for user: an agy process ran with the REAL HOME from the primary checkout at 08:33:58-08:34:02 (log cli-20260925_083358.log, agy 1.2.11, 'Failed to resolve GeminiDir'), not from this session — likely the live daemon/menubar/usage probe.
PM impl DONE_WITH_CONCERNS b36a5fd..16d6634 (6 commits). Ruling: denylist the swarm home (m.d.Home) from the isolated HOME when it's inside UserHome — token file/worktree paths are absolute so nothing needs the link; removes the find -L cycle and casual token exposure — cost if wrong: a tool resolving ~/.swarm relative to HOME inside muse breaks.
PA fix r1 committed 1b78a27..51aebec
PM review r1 (opus): Needs fixes — I1 .swarm exclusion (ruling) not implemented; I2 excluding .config breaks gcloud/solana (fix: HOME/.config -> isolated XDG dir). Minors 3-6 folded. Leftover muse registry file runtime/muse/sessions/01a0d78b-b80a-78d2-a9e1-464013cc0eac.json (dead pid 69962) in real data dir — tell user, optional delete.
PA: fix round 1/5 (6 addressed, finding 2 partial, new Important: os.SameFile guard). -> round 2.
PM fix r1 committed 16d6634..516d4e4
PA fix r2 committed 51aebec..ca4b360
Task PM: fix round 1/5 (6 addressed, 0 open; 16d6634..516d4e4)
Task PM: complete (b36a5fd..516d4e4, review clean) — merged.
Task PM: minor (deferred): muse.go:63-87 setupEnv doc comment detached; probe doc :140 says SIGKILL (tmux sends SIGHUP); lexical swarm-home path compare misses /var vs /private/var spellings.
PA: fix round 2/5 (4 addressed; new Important x2 from copyTree symlink recreation: cursor contract + salvage dies on reap). Ruling: copyTree dereferences all links (cycle-guarded); salvage keeps top-level links pointing outside run/launch — cost if wrong: vendored cursor plugins with intentional symlinks get duplicated content.
PA fix r3 committed ca4b360..2519214
P9 fix r1 committed b749c63..9704206 (11 commits). Concerns: FIFO best-effort; post-close Retry failure leaves agent row active w/ dead session.
PA: fix round 3/5 (ruling + tests 3-5 addressed; tests 1-2 not committed; new Important: dangling link aborts install again). Ruling: copyTree skips dangling links and cycles with a log line instead of aborting — cost if wrong: a broken skill silently missing (logged). Round 4 → fresh sonnet implementer (user policy: sonnet implementers).
User 2026-09-25: pause all work; agents commit + report; controller commits progress; handoff at docs/plans/2026-09-25-self-contained-tasks-handoff.md.
P9 re-review of fix round 1 (opus, partial — paused): Needs fixes. (1) user directive not met: round-2 build row claimed for the old agent only after Retry returns → crash/Retry-after-tmux-start gap spawns a second live builder (probe zz_probe2_test.go); fix: claim the row for the old agent before closing its session and calling Retry. (2) addendum residual NOT harmless: orphaned active builder counted by liveDescendants (reconcile.go:913) → spurious stale alerts, blocks worktree reclaim; fix: on Retry failure mark row failed but keep the old agent id so the next retry reuses the builder. (3) I3 not fixed on the real replay path (Next returns Wait once reviewer rows exist; probe zz_probe3_test.go); I2 partial: checkpoint trigger (checkpoint.go:1403) still advances on the MCP request ctx. I1,I4-I9 ADDRESSED. Unconfirmed: removing toCloseFailedSelf may leave a failed agent's pane running after retries are spent (checkpoint.go:1217). Full notes: p9-rereview1-partial.md; probes in probes/.
PA fix round 4: paused before any edit; plan in pa-report.md "Fix round 4 — paused".
PAUSED 2026-09-25.

RESUMED 2026-09-25. PA fix round 4 committed 2519214..6d35ec4; scoped round-5 review found a nested-parent cycle left a partial destination entry.
Ruling: the round-5 nested-parent cycle is load-bearing for PA's skip-and-continue contract, so fix it before merge with a focused red/green correction and scoped review — cost if wrong: one extra correction dispatch after the review cap.
PA correction b65e1fb passes scoped review (nested cycle absent, sibling copied, top-level directory links preserved). PA complete; merged as 689302e. Integration go build ./... and go vet ./... passed; go test ./... failed only the pre-existing internal/httpapi/TestBoardServedAtRoot (GET /kanban = 503).
P9 fix round 2 committed 9704206..4820c88 (user directive gap, orphaned active builder, I3 replay review worktree, I2 ctx, failed pane).
P9 re-review of fix round 2: Needs fixes (1: atomic round bump + bound run, 2: orphan active agent cleanup on escalation, 3: coverage & naming). Notes: p9-fix2-findings.md.
P9 fix round 3 committed 4820c88..b6079f9 (atomically bind fix rounds, retire exhausted agents).
P9 re-review of fix round 3: Needs fixes (release worktree_reservations rows on escalation). Notes: p9-fix3-findings.md.
P9 fix round 4 committed b6079f9..edb6989 (release exhausted workflow agent reservations).
P9 scoped re-review of fix round 4 (pro): Approved (transactional atomicity, session safety, test coverage; no issues).
Task P9: complete (commits b36a5fd..edb6989, review clean).

