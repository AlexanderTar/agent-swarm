# P4 report: Support-role skills and kickoff

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p4`, branch `pkg/p4`.

## Units built

### 4.1 `swarm-debugger` and `swarm-mechanical`
- `skills/swarm-debugger/SKILL.md`: `superpowers:systematic-debugging` four
  phases, no fix without a root cause stated in a `progress` checkpoint,
  advisor consult before committing to a root cause, regression-test-first
  fix (red before green), then `swarm-coder`'s commit/verify/fix-round rules.
- `skills/swarm-mechanical/SKILL.md`: 23 lines (≤ 60 required), `ponytail`
  ladder, same-shape batching, exact-scope discipline, `blocked` for anything
  that turns out to need judgement.
- Extended `TestRoleSkillsReferenceTheirSkills` in
  `internal/install/skills_test.go` with both rows, and added
  `TestSwarmMechanicalSkillIsAtMost60Lines`.

**RED** (`go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills|TestSwarmMechanicalSkillIsAtMost60Lines' -v`):
```
--- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-debugger
    skills_test.go:1169: swarm-debugger: open swarm-debugger/SKILL.md: file does not exist
--- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-mechanical
    skills_test.go:1169: swarm-mechanical: open swarm-mechanical/SKILL.md: file does not exist
--- FAIL: TestSwarmMechanicalSkillIsAtMost60Lines
    skills_test.go:1187: open swarm-mechanical/SKILL.md: file does not exist
```

**GREEN**: same command, all PASS. Commit `7d42551`.

**Fix round (self-review via advisor consult, before reporting):** the advisor
flagged that `swarm-debugger`'s phase names ("reproduce, isolate, understand,
fix") were an invented paraphrase, not the real
`superpowers:systematic-debugging` phase names. Verified against the
installed plugin (`superpowers 6.3.0`):
```
$ grep -n '^### Phase' ~/.claude/plugins/cache/superpowers-marketplace/superpowers/6.3.0/skills/systematic-debugging/SKILL.md
48:### Phase 1: Root Cause Investigation
120:### Phase 2: Pattern Analysis
143:### Phase 3: Hypothesis and Testing
168:### Phase 4: Implementation
```
Rewrote the skill to name the real phases (Root Cause Investigation, Pattern
Analysis, Hypothesis and Testing, Implementation). Re-ran
`make skills-sync && go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills/swarm-debugger'`
— PASS. Commit `20c2fb7`.

### 4.2 `swarm-researcher`
- `skills/swarm-researcher/SKILL.md`: `superpowers:brainstorming` on its own
  sub-question ("no user dialogue"), the deep-research loop (short searches,
  primary sources, conflict recording, ~10–15 tool-call budget), the notes
  format at `~/.swarm/research/<SPIKE-KEY>/<ITEM-KEY>.md` with `### Takeaway`
  / `### Cited findings` / `### Inferences` / `### Gaps`, "Never invent".
- Extended the same table with a `swarm-researcher` row.

**RED**: `TestRoleSkillsReferenceTheirSkills/swarm-researcher` — `open swarm-researcher/SKILL.md: file does not exist`.
**GREEN**: all PASS. Commit `2036c7d`.

### 4.3 `swarm-advisor` and the advisor prompt
- `skills/swarm-advisor/SKILL.md`: original text (spec locked decision 9 —
  no text copied from scdenney/open-science-skills `codex/advisor`), when to
  consult, how to write the briefing, the five decision rules (one decision
  per consult / advice is evidence not authority / at most one focused
  follow-up / authoritative evidence wins / unresolved high-impact
  uncertainty goes up), how to record the outcome, the spec's mermaid
  diagram verbatim, and the exact CC BY-NC credit footer.
- `internal/advisor/run.go`: `Prompt` const updated to the same decision
  rules — one decision, grounded in what was actually read, not a menu.
- New tests: `TestAdvisorSkillHasMermaidAndCredit`
  (`internal/install/skills_test.go`) and
  `TestAdvisorSystemPromptDecisionRules` (`internal/advisor/run_test.go`).

**RED**:
```
=== RUN   TestAdvisorSkillHasMermaidAndCredit
    skills_test.go:1201: open swarm-advisor/SKILL.md: file does not exist
=== RUN   TestAdvisorSystemPromptDecisionRules
    run_test.go:253: advisor Prompt missing "one decision": ...
    run_test.go:253: advisor Prompt missing "actually read": ...
```
**GREEN**: `go test ./internal/install/... ./internal/advisor/...` — both `ok`. Commit `78f9812`.

### 4.4 Kickoff role-skill table
- `internal/runtime/text.go`: new `RoleSkills(role Role, itemType items.Type) []string`
  (spec A3's table exactly, including the orchestrator/spike row using
  `swarm-spike`), `joinSkillNames` for the prose join, `skills(role, itemType)`
  rewritten on top of it, and `mandateText` now applied to **every** role's
  `Kickoff`/`ResumeKickoff` (previously orchestrator-only — A3's "every role
  gets the mandate" is an intentional change from the old R3 form).
- `Kickoff`/`ResumeKickoff` signatures gained `itemType items.Type` (inserted
  after `role`, before `key`, per the brief's interface line). Updated every
  call site: `internal/runtime/agents.go` (now selects `type` alongside
  `key`/`title` from `items` and passes `items.Type(itemTypeStr)`) and the
  test call in `internal/hook/handler_test.go`.
- `skills/swarm-spike/SKILL.md` and `skills/swarm-workflows/SKILL.md`: valid
  frontmatter (`name` == dir, real `description`), body "Filled in by P12."
- `internal/runtime/text_test.go`: added `TestKickoffNamesRoleSkill` (table
  over every role + the orchestrator spike/non-spike cases, checking both
  `RoleSkills` and that `Kickoff` actually names each skill), `TestRoleSkillsExist`
  (every `RoleSkills` name exists in `install.Skills()`, across all six
  `items.Type` values), and `TestKickoffMandateForEveryRole`, which **replaces**
  `TestOrchestratorKickoffMandatesSkillWorkflows` — that test's "worker
  kickoff must not carry the orchestrator mandate" assertion no longer holds
  once every role carries the mandate; the brief names this as an intentional
  change. Updated every existing `Kickoff`/`ResumeKickoff` call in the file
  for the new signature.
- Updated the three kickoff/resume goldens
  (`internal/runtime/testdata/text/{kickoff-worker,kickoff-orchestrator,resume}.golden`)
  to the new text (new skill lists, universal mandate wording).

**RED, step 1** (`go vet ./internal/runtime/...` after adding the new/updated
tests and call-site edits, before touching `text.go`'s implementation):
```
vet: internal/runtime/agents.go:1018:62: too many arguments in call to ResumeKickoff
	have (string, Role, items.Type, string, string)
	want (string, Role, string, string)
```
(A compile-level red: the new signature was asserted by the test/call-site
edits before `text.go` implemented it.)

**RED, step 2** (after implementing `RoleSkills`/the new signatures, before
updating the goldens — `go test ./internal/runtime/... -run 'Kickoff|RoleSkills|Notices|IsDaemonPrompt' -v`):
```
--- FAIL: TestNoticesMatchGoldens
    text_test.go:39: kickoff-worker:
         got "You are swarm agent login-form-coder (coder) for TASK-101: Build the login form. Use the `swarm` and `swarm-coder` skill(s). You MUST follow the skill(s) above, including the superpowers skills they name — do not improvise around them. Call swarm_sync now to get your assignment. ..."
        want "You are swarm agent login-form-coder (coder) for TASK-101: Build the login form. Use the `swarm` skill(s). Call swarm_sync now to get your assignment. ..."
    text_test.go:39: kickoff-orchestrator:
         got "... Use the `swarm`, `swarm-orchestrator`, `swarm-workflows` and `swarm-batching` skill(s). You MUST follow the skill(s) above, including the superpowers skills they name — do not improvise around them. ..."
        want "... Use the `swarm` and `swarm-orchestrator` skill(s). You MUST follow the skill(s) above, including their superpowers workflows — do not improvise around them. ..."
    text_test.go:39: resume:
         got "... Use the `swarm` and `swarm-coder` skill(s). You MUST follow ... ..."
        want "... Use the `swarm` skill(s). Call swarm_sync now; ..."
--- FAIL: TestNoticesMatchGoldens
```
All other new/updated tests in this run (`TestKickoffMandateForEveryRole`,
`TestKickoffNamesRoleSkill`, `TestRoleSkillsExist`, `TestIsDaemonPrompt`) were
already green at this point — this diff is exactly the intended text change
(new skill lists, universal mandate wording), confirming the golden update
below is the deliberate, brief-named change and not a leftover bug.

**GREEN**: `go test ./internal/runtime/... -run 'Kickoff|RoleSkills|Notices|IsDaemonPrompt' -v` — all PASS, including the three golden comparisons. Commit `1fe7029`.

## Verify (package, from the brief)

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/... ./internal/advisor/... ./internal/runtime/ -run 'Kickoff|RoleSkills'
ok  	github.com/AlexanderTar/agent-swarm/internal/install	0.289s
ok  	github.com/AlexanderTar/agent-swarm/internal/advisor	0.748s [no tests to run]   # regex doesn't match Test*Advisor*; see below
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	0.569s

$ go build ./... && go vet ./...
(clean)
```

Note: the literal `-run 'Kickoff|RoleSkills'` filter (as given in the brief)
does not match `TestAdvisorSkillHasMermaidAndCredit` /
`TestAdvisorSystemPromptDecisionRules`; those were verified separately above
(unit 4.3 GREEN) and again in the full-suite run below.

## Additional verification

- `go test ./internal/runtime/...` (full package, since call sites changed): `ok`, 16.8s.
- `go test ./internal/hook/... ./internal/install/... ./internal/advisor/...` (full): all `ok`.
- `go test ./...` (whole repo, baseline-safe per the contract): every package
  `ok` except the pre-existing, documented baseline failure
  `internal/httpapi TestBoardServedAtRoot` (503, web bundle not built) —
  exactly the one failure the implementer contract names as pre-existing and
  unrelated. Nothing else is red.

## Files changed

- `skills/swarm-debugger/SKILL.md` (new)
- `skills/swarm-mechanical/SKILL.md` (new)
- `skills/swarm-researcher/SKILL.md` (new)
- `skills/swarm-advisor/SKILL.md` (new)
- `skills/swarm-spike/SKILL.md` (new stub)
- `skills/swarm-workflows/SKILL.md` (new stub)
- `internal/install/skills/**` mirrors of the six files above (via `make skills-sync`)
- `internal/install/skills_test.go` (new rows + `TestSwarmMechanicalSkillIsAtMost60Lines` + `TestAdvisorSkillHasMermaidAndCredit`)
- `internal/advisor/run.go` (`Prompt` const updated)
- `internal/advisor/run_test.go` (`TestAdvisorSystemPromptDecisionRules`)
- `internal/runtime/text.go` (`RoleSkills`, `joinSkillNames`, `skills`, `mandateText`, `Kickoff`/`ResumeKickoff` signatures)
- `internal/runtime/text_test.go` (new/updated tests, new imports, all call sites updated)
- `internal/runtime/agents.go` (spawn path selects `type`, passes `itemType` to `Kickoff`/`ResumeKickoff`)
- `internal/runtime/testdata/text/{kickoff-worker,kickoff-orchestrator,resume}.golden` (updated)
- `internal/hook/handler_test.go` (call-site update for the new `Kickoff` signature)

## Self-review notes

- Grepped every `Kickoff(`/`ResumeKickoff(` call site in the repo
  (`grep -rn 'Kickoff(' --include='*.go' .`) before and after the signature
  change; only `internal/runtime/agents.go` (production) and
  `internal/hook/handler_test.go` (test) called it besides `text.go`/`text_test.go`
  themselves. Both updated.
- `RoleSkills`/`Kickoff`/`ResumeKickoff` live in `internal/runtime`, which
  needed a new import of `internal/items`; confirmed no import cycle
  (`internal/items` and `internal/kinds` import nothing from `internal/runtime`).
  `internal/runtime`'s test file also imports `internal/install`; confirmed
  `internal/install` imports neither `internal/runtime` nor `internal/items`,
  so no cycle there either.
- `joinSkillNames` is a small new prose-join helper (single-item →
  `` `swarm` ``, two-item → `` `swarm` and `swarm-coder` ``, N-item →
  Oxford-less `a, b and c`); there was no existing "join with and" helper in
  the codebase to reuse (checked via `grep -rn '" and "'`).
- `swarm-mechanical/SKILL.md` is 23 lines; the ≤ 60 acceptance bullet is
  enforced by a real test (`TestSwarmMechanicalSkillIsAtMost60Lines`), not
  just eyeballed.

## Judgment calls

1. **`joinSkillNames` prose format.** The spec's A3 table gives the
   role→skill mapping but no exact kickoff copy for more-than-two skill
   lists (the "All user-facing copy" section doesn't cover kickoff text
   either). I extended the existing two-item "`x` and `y`" pattern to N
   items as "`a`, `b` and `c`" — kept it consistent with the prior format
   rather than inventing a different scheme (e.g. a bullet list) for the
   4-skill orchestrator case.
2. **`mandateText` wording.** Used A3's quoted sentence verbatim: "You MUST
   follow the skill(s) above, including the superpowers skills they name —
   do not improvise around them." (This differs slightly from the old R3
   text "including their superpowers workflows" — A3 is the newer, binding
   spec section, so I followed it exactly rather than the older R3 wording
   still sitting in the pre-P4 code comment.)
3. **`TestOrchestratorKickoffMandatesSkillWorkflows` → `TestKickoffMandateForEveryRole`.**
   The brief names this test by its new name and says the old assertions
   are "updated intentionally" without saying explicitly whether to rename
   or add a second test. I rewrote the test **in place**, not deleted it:
   the old test's positive assertions (orchestrator kickoff/resume carry
   the mandate) survive as a subset of the new table-driven version, which
   now covers every role's `Kickoff` *and* `ResumeKickoff`. Only the one
   assertion that became factually false with the spec change — "a worker
   kickoff must not carry the mandate" — was removed, because A3 now
   requires every role to carry it; keeping a test that asserts the
   opposite of the locked spec would be wrong, not conservative. No
   coverage was lost; coverage strictly increased (8 roles × 2 kickoff
   functions vs. 2 roles × 2 before).
4. **`items.Type` argument order in `RoleSkills`/test tables.** Brief's
   interface line only gives `RoleSkills(role Role, itemType items.Type)`;
   I used `items.Task` as the default/neutral item type in tests and
   call sites where the item type doesn't matter to the assertion (every
   non-orchestrator role), and `items.Epic`/`items.Spike` specifically for
   the orchestrator rows, matching the one place item type actually changes
   the answer.
5. **Designer role placeholder.** Used `Role("designer")` (the raw string,
   since `Role = kinds.Role` is a real Go alias, not a distinct type) at
   both the `RoleSkills` switch case and in `text_test.go`/route through
   `TestRoleSkillsExist`, per the brief's explicit instruction, with a
   one-line comment pointing at the P7 merge.

## Concerns

None outstanding. All four units are TDD'd (red captured before green),
committed individually, `make skills-sync` run and committed in the same
commit as each skill change, and the full package Verify set plus a
whole-repo `go test ./...` pass with only the documented pre-existing
baseline failure. One fix-round commit (`20c2fb7`) landed after an advisor
self-review caught a factual mismatch in `swarm-debugger`'s phase names
(see 4.1's fix-round note above); re-verified clean afterward.

## Commits

- `7d42551` feat(skills): swarm-debugger and swarm-mechanical role skills
- `2036c7d` feat(skills): swarm-researcher role skill
- `78f9812` feat(skills): swarm-advisor skill and aligned advisor prompt
- `1fe7029` feat(runtime): kickoff names each role's skill
- `20c2fb7` fix(skills): swarm-debugger names systematic-debugging's real phase names

# Fix round 1 (Opus review)

Findings file: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p4-fix1-findings.md`.

## Item 0: merge + kinds.RoleDesigner

`git merge --no-edit claude/happy-ptolemy-fke4jx` (P7, which added
`kinds.RoleDesigner` and `internal/runtime.RoleDesigner`) into `pkg/p4`.
Auto-merged clean, no conflicts (19 files, all P7-only paths except
`internal/hook/handler_test.go` and `internal/runtime/agents.go`, both of
which auto-merged without touching this package's own P4 hunks). Verified
`kinds.RoleDesigner` exists on the merged branch before merging
(`git show claude/happy-ptolemy-fke4jx:internal/kinds/kinds.go`), then
`go build ./...` — clean. Commit: merge commit `2c88cf8`.

Then replaced the inline `Role("designer")` placeholder (5 occurrences: 1 in
`internal/runtime/text.go`'s `RoleSkills` switch, 4 across
`internal/runtime/text_test.go`) with `RoleDesigner`, and dropped the
now-stale "kinds.RoleDesigner lands in a parallel package (P7)" comments.
This is a mechanical constant swap with no behavior change, so no new
red/green cycle — verified with the existing `RoleSkills`/`Kickoff` tests
instead:
```
$ go test ./internal/runtime/... -run 'Kickoff|RoleSkills' -v
--- PASS: TestKickoffMandateForEveryRole
--- PASS: TestKickoffNamesRoleSkill (incl. designer/task subtest)
--- PASS: TestRoleSkillsExist
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	0.480s
```
Commit `c795f31` (also covers item 4, see below — the two touch the same
function).

## Item 1 (Important): advisor prompt missing rule 5

`internal/advisor/run.go`'s `Prompt` gave "one decisive recommendation"
unconditionally, with no version of swarm-advisor's rule 5 (unresolved
high-impact uncertainty goes to the parent/user). Added the substring
`"parent or the user"` to `TestAdvisorSystemPromptDecisionRules`'s want-list
first.

**RED** (`go test ./internal/advisor/... -run TestAdvisorSystemPromptDecisionRules -v`):
```
run_test.go:256: advisor Prompt missing "parent or the user": You are the advisor for Swarm agent {name} ({role}) working on {KEY}. Read {context_path} and any files it lists. This is one decision, not a menu: give one decisive recommendation grounded in what you actually read there, not general assumptions — the main risk, and what to check next. At most 250 words. Do not edit files. This request was sent by the Swarm daemon on the agent's behalf, not typed by the user.
--- FAIL: TestAdvisorSystemPromptDecisionRules
```

Added to `Prompt`: "If what you read doesn't settle the decision and the
stakes are high, say so plainly and recommend the agent take it to its
parent or the user rather than guessing."

**GREEN**: `go test ./internal/advisor/... -run TestAdvisorSystemPromptDecisionRules -v` → PASS. Full `go test ./internal/advisor/...` → `ok`. Commit `a293c98`.

## Item 2 (Minor): swarm-advisor overclaims "never sees your conversation"

Verified the claim against the actual code: `BuildContext`
(`internal/advisor/context.go`) forwards up to `maxTranscriptTurns` (30)
truncated transcript turns, called from `Ask` in `internal/advisor/queue.go`
(`ReadTranscript(..., maxTranscriptTurns)` → `BuildContext(ContextInput{...,
Transcript: turns})`) — the "never sees your conversation" claim in
`skills/swarm-advisor/SKILL.md` is false for the simulated advisor path, and
also false for a native Claude advisor call, which runs inside the agent's
own conversation and sees full history.

Added two assertions to `TestAdvisorSkillHasMermaidAndCredit` first: the old
phrase must be **absent**, a "truncated slice" phrase must be **present**.

**RED**:
```
skills_test.go:1224: swarm-advisor: overclaims the advisor never sees any of the conversation
skills_test.go:1227: swarm-advisor: missing the corrected truncated-slice wording
--- FAIL: TestAdvisorSkillHasMermaidAndCredit
```

Reworded the "How to ask" section to state both shapes (native call sees full
history; every other case gets a daemon-built context file with at most a
truncated transcript slice) instead of claiming zero visibility.

**GREEN**: `go test ./internal/install/... -run TestAdvisorSkillHasMermaidAndCredit -v` → PASS. Commit `e43a0fc` (skill + test).

Also updated the plan-mandated spec text (A4's swarm-advisor description,
`docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` line ~397-398)
to the same corrected claim, in its own `docs(spec)` commit as the finding
asked: commit `df72c7f`.

## Item 3 (Minor): swarm-debugger red entry lacks cmd/unit

Compared against `swarm-coder`'s matching red entry
(`verification: [{"cmd": "...", "phase": "red", "ok": false, "note":
"<why it fails>", "unit": <n>}]`) and confirmed `swarm-debugger` step 1 had
neither `"cmd"` nor any unit-tagging guidance. Added both substrings
(`` `"cmd"` `` and `"## Units"`) to the `swarm-debugger` row in
`TestRoleSkillsReferenceTheirSkills` first.

**RED**:
```
skills_test.go:1188: swarm-debugger: missing "\"cmd\""
skills_test.go:1188: swarm-debugger: missing "## Units"
--- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-debugger
```

Rewrote step 1 to: `verification: [{"cmd": "...", "phase": "red", "ok":
false, "note": "<why it fails>"}]` — tag it `"unit": <n>` when your brief
has `## Units` (a batched debug package), the same as `swarm-coder`.

**GREEN**: `go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills/swarm-debugger' -v` → PASS. Commit `79d8f19`.

## Item 4: RoleSkills fallback comment

Folded into item 0's commit (`c795f31`, same function): the trailing
`return []string{"swarm"}` in `RoleSkills` now carries a comment that every
role is validated at spawn, so this default is a defensive fallback, never a
role inference. No behavior change (kept the existing return value), so no
new test — the existing `TestRoleSkillsExist`/`TestKickoffNamesRoleSkill`
already cover every role this function is ever called with.

## Item 5 (Nit): knownSuperpowersSkills version comment

Confirmed there's no internal contradiction within `skills_test.go` itself
(both mentions already said "v6.4.1" consistently) — the apparent mismatch
is against the rest of the repo, which pins `superpowers 6.3.0` as the
installed plugin-cache version for adapter/parity test fixtures
(`internal/adapter/*_test.go`, `internal/catalog/service_test.go`). Confirmed
`diagnosing-superpowers` (one of the 15 allow-listed names) does not exist
under the locally installed `superpowers/6.3.0/skills/` tree, i.e. the name
list genuinely does come from a newer catalog than the pinned 6.3.0 cache —
so 6.4.1 is not a typo to fix, but the comment read as if it might be.
Comment-only clarification (not independently testable as a behavior), so no
red/green cycle: reworded both comments to spell out that 6.4.1 (the upstream
skill catalog these names were verified against) and 6.3.0 (the pinned dev
plugin cache) are two different, intentional version numbers.

`go build ./... && go vet ./...` and `go test ./internal/install/...` clean
after the edit. Commit `42554cb`.

## Fix round 1 Verify (from the findings file)

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/... ./internal/advisor/... ./internal/runtime/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	(cached)
ok  	github.com/AlexanderTar/agent-swarm/internal/advisor	0.743s
ok  	github.com/AlexanderTar/agent-swarm/internal/runtime	16.838s

$ go build ./... && go vet ./...
(clean)

$ go test ./...
(every package ok, except the documented pre-existing baseline failure
internal/httpapi TestBoardServedAtRoot — 503, web bundle not built)
```

## Fix round 1 commits

- `2c88cf8` Merge branch 'claude/happy-ptolemy-fke4jx' into pkg/p4 (item 0)
- `c795f31` fix(runtime): RoleSkills uses kinds.RoleDesigner now P7 has merged (items 0 + 4)
- `a293c98` fix(advisor): prompt covers rule 5 (send high-stakes uncertainty up) (item 1)
- `e43a0fc` fix(skills): swarm-advisor no longer overclaims zero-context advice (item 2, skill)
- `df72c7f` docs(spec): advisor wording matches BuildContext's truncated-slice reality (item 2, spec)
- `79d8f19` fix(skills): swarm-debugger red entry carries cmd and unit tag (item 3)
- `42554cb` docs(install): clarify the knownSuperpowersSkills version comment (item 5)

## Fix round 1 concerns

None outstanding. All five numbered findings addressed; items 1 and 3 got a
genuine red-before-green test cycle, item 2 got one for the skill wording
plus a same-content spec doc commit, item 0/4 were a verified-safe mechanical
constant swap plus a comment (covered by pre-existing tests), and item 5 was
a comment-only clarification with no testable behavior. Full package Verify
set and whole-repo `go test ./...` both clean except the documented baseline
failure.
