# P3 report: Builder and reviewer skills

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p3`, branch `pkg/p3`.

## Summary

Rewrote `skills/swarm/SKILL.md` as protocol-only, wrote the four new role
skills (`swarm-coder`, `swarm-reviewer`, `swarm-ui-reviewer`,
`swarm-designer`), reviewed and fixed drift in the `swarm-batching` draft,
and added the two reference-checking tests. All 5 units committed
separately, TDD per unit (test red for the not-yet-written skills, green
once each skill lands).

## Units

### Unit 3.1: Reference-checking tests (commit 8205db0, combined with 3.2 per brief)
Added `TestRoleSkillsReferenceTheirSkills` (table: skill → required
substrings, covering all 5 package skills) and `TestSuperpowersReferencesAreKnown`
(scans every non-vendored skill for `superpowers:<x>` references against the
fixed 15-name v6.4.1 set) to `internal/install/skills_test.go`.

Design note: `install.SkillBody()` panics on an unknown skill name, which
would kill the whole test binary on the first missing skill and hide the
other rows' red/green status. Used `t.Run(name)` + `fs.ReadFile(install.SkillFS(), ...)`
with `t.Fatalf` inside the subtest instead, so each row reports
independently.

RED (with swarm-coder/reviewer/ui-reviewer/designer directories temporarily
moved aside, before they were written):
```
$ go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills|TestSuperpowersReferencesAreKnown|TestSkillBodyCarriesTheSpecFrontmatterAndLastRule' -v
--- PASS: TestSkillBodyCarriesTheSpecFrontmatterAndLastRule (0.00s)
--- FAIL: TestRoleSkillsReferenceTheirSkills (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm (0.00s)
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
        skills_test.go:918: swarm-coder: open swarm-coder/SKILL.md: file does not exist
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
        skills_test.go:918: swarm-reviewer: open swarm-reviewer/SKILL.md: file does not exist
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
        skills_test.go:918: swarm-ui-reviewer: open swarm-ui-reviewer/SKILL.md: file does not exist
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
        skills_test.go:918: swarm-designer: open swarm-designer/SKILL.md: file does not exist
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-batching (0.00s)
=== RUN   TestSuperpowersReferencesAreKnown
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
FAIL
```
(`swarm` and `swarm-batching` rows were already green because unit 3.2's
`swarm` rewrite and the `swarm-batching` drift fixes were done in the same
commit, per the brief's "Red. Commit with unit 3.2.")

### Unit 3.2: Core `swarm` rewrite (commit 8205db0)
Rewrote `skills/swarm/SKILL.md` as protocol only:
- Rule 11 (TDD) removed from `swarm`; the sentence it used to carry
  ("Reviewers: check that the tests cover each acceptance criterion and
  would fail without the change.") now lives in `swarm-reviewer` (unit 3.4).
- New rule 11: the workflow-step rule ("If your assignment has a `##
  Workflow` section, you are one step of a daemon-run workflow: do exactly
  your step, write `completed` when your step's gates are met, and don't
  coordinate the next step yourself...").
- Rule 5: added `Workflow` to the disabled native-tool list (spec A6),
  alongside `Agent`/`Task`/`Fork`/`invoke_subagent`, and named `swarm_workflow`
  as the orchestrator's own path.
- Rule 9a: appended a pointer to the `swarm-advisor` skill (spec A4 note:
  "rather than listed in every kickoff").
- Description tail changed from "...commits and TDD." to "...commits, and
  the workflow-step contract." (kept the byte-identical prefix
  `TestSkillBodyCarriesTheSpecFrontmatterAndLastRule` checks).

Also adopted `swarm-batching` from the draft with two drift fixes against
spec C4/C5:
- The mechanical row now spells out the explicit steps form (`[{run:
  mechanical}, {review: [reviewer]}]`) instead of paraphrasing it.
- The single-unit `solo` warning sentence gained a pointer to the P12
  plan-registration check and spec C2 (the draft already anticipated the
  warning copy; this makes explicit that it's not enforced yet).

Updated the pre-existing `TestSkillBodyCarriesTheSpecFrontmatterAndLastRule`
(intentional change, named by the brief: "TDD rule 11 moves to
`swarm-coder`/`swarm-debugger`"): dropped the "Reviewers: check..." assertion
from the `swarm` checks (that sentence no longer lives in `swarm`) and added
checks for `## Workflow` and `` `Workflow` `` instead. The assertion itself
was not deleted — its content is covered by the new `swarm-reviewer` row of
`TestRoleSkillsReferenceTheirSkills` (`"would fail without the change"`).

GREEN: see full Verify output below (all of `internal/install` passes).

### Unit 3.3: `swarm-coder` (commit 7ed824a)
Wrote `skills/swarm-coder/SKILL.md`: `## Units`/`## Steps`/`## Verify`/`##
Workflow` reading, unit-by-unit execution per `swarm-batching` ("own red →
green per unit... one commit per unit"), the `ponytail` ladder with the
precedence rule (task's tdd gate / acceptance govern tests; ponytail's
"question the requirement" becomes a `completed`-summary note, never a
skipped criterion), the `completed` git contract (`dirty:false` + HEAD sha),
fix rounds via `assignment_update` + `superpowers:receiving-code-review`,
and scope discipline.

```
$ go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills' -v
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
```
(swarm-coder green; reviewer/ui-reviewer/designer still red as expected — not
yet written.)

### Unit 3.4: `swarm-reviewer` and `swarm-ui-reviewer` (commit 46f2f3b)
Wrote `skills/swarm-reviewer/SKILL.md`: read-only review at a fixed sha, walk
the package unit by unit (missing unit = `major`), review order borrowed from
`superpowers:requesting-code-review` (spec/acceptance → tests-would-fail →
correctness → security → simplicity/`ponytail-review` → conventions), the
`completed` verdict/findings contract (`pass`/`changes_requested`/`blocked`;
`pass` allows only nit/minor), never edit files.

Wrote `skills/swarm-ui-reviewer/SKILL.md`: restates the verdict/findings
contract explicitly (judgment call below — see "ui_reviewer kickoff" note),
plus UI-specific checks (`web-design-guidelines`, `building-components`, the
task's design artifact, mobile skills + `ui-ux-pro-max` pre-delivery
checklist with 44pt/48dp touch targets), findings cite their rule source.

```
$ go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills|TestSuperpowersReferencesAreKnown' -v
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- FAIL: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
```

### Unit 3.5: `swarm-designer`; swarm-batching already adopted (commit 7b51040)
Wrote `skills/swarm-designer/SKILL.md` (new role): explore with
`superpowers:brainstorming` (questions to the parent only), tools
(`ui-ux-pro-max` with `--design-system`/`--stack`, `building-components`, the
mobile skills), artifact at `~/.swarm/designs/<ROOT-KEY>/<ITEM-KEY>-<slug>.md`
with the exact section list from spec A4 (Goals, Screens w/ per-screen
states, Components, Tokens, Interaction & motion, Accessibility, Mobile
specifics, Open questions), designers never write product code, `completed`
carries the artifact path in `artifacts`.

`swarm-batching` drift review/fixes against C4/C5 were done as part of unit
3.2's commit (needed to be in place before its test row could go green in
3.1); noted again here since the brief places the "review the draft" step in
3.5.

GREEN (all rows):
```
$ go test ./internal/install/... -v
... (full output below) ...
--- PASS: TestRoleSkillsReferenceTheirSkills (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-batching (0.00s)
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.683s
```

Additional commit `3ce94de docs(plan): tick P3 units` ticked the P3
checkboxes in `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md`
(matching the convention the P2 merge commit `d53b689` set). This file was
not in the brief's Files list — see judgment calls.

## Verify (final, on HEAD 3ce94de)

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	(cached)   [full -v run: all tests PASS, see above]

$ go build ./... && go vet ./...
(no output — success)

$ git status --porcelain
(empty — clean tree)
```

Per the implementer contract, `go test ./...` / `go test ./cmd/...` were
never run (known home-dir bug in cmd/swarm tests).

## Files changed

- `skills/swarm/SKILL.md` — rewritten, protocol only.
- `skills/swarm-batching/SKILL.md` — adopted from draft, two C4/C5 drift fixes.
- `skills/swarm-coder/SKILL.md` — new.
- `skills/swarm-reviewer/SKILL.md` — new.
- `skills/swarm-ui-reviewer/SKILL.md` — new.
- `skills/swarm-designer/SKILL.md` — new.
- `internal/install/skills/**` — mirror of the above (`make skills-sync`).
- `internal/install/skills_test.go` — `TestRoleSkillsReferenceTheirSkills`,
  `TestSuperpowersReferencesAreKnown`, and an intentional update to
  `TestSkillBodyCarriesTheSpecFrontmatterAndLastRule`.
- `docs/plans/2026-09-24-self-contained-tasks-and-role-skills.md` — P3
  checkboxes ticked (judgment call, see below).

## Self-review notes

- Every new SKILL.md opens with "Follow the `swarm` skill first; this adds
  to it." per spec A4, and has non-empty frontmatter `name`/`description`
  matching its directory (enforced by `TestSkillsRegistryMatchesTree`, which
  still passes).
- Checked all `superpowers:` references by hand against the 15-name list:
  `test-driven-development`, `verification-before-completion`,
  `receiving-code-review` (swarm-coder), `requesting-code-review`
  (swarm-reviewer), `brainstorming` (swarm-designer) — all valid; test also
  confirms this mechanically.
- `swarm-ui-reviewer` does not merely say "see swarm-reviewer" — it restates
  the verdict/findings contract in full, since B5 makes `verdict` required on
  `ui_reviewer`'s `completed` too and A3's kickoff table gives `ui_reviewer`
  only `swarm` + `swarm-ui-reviewer` (not `swarm-reviewer`). Still tells the
  agent to read `swarm-reviewer` in full for the review order and unit-walk
  mechanics, since kickoff wiring (A3) is out of this package's scope
  (mentioned in P4, not P3's Files list).
- Kept `frontmatterField`'s line-based parser happy: every new skill's
  `description:` is a single line.
- No skill exceeds ~100 lines (well under the ~200 line guidance in A4).

## Concerns / judgment calls

1. **`TestSkillBodyCarriesTheSpecFrontmatterAndLastRule` edit.** Not in the
   brief's unit list, but its old assertion ("Reviewers: check that the
   tests...") directly contradicts unit 3.2's explicit instruction that rule
   11 moves out of `swarm`. Treated this as the brief naming the change (via
   3.2's text), moved the assertion's content into the new
   `swarm-reviewer` row of `TestRoleSkillsReferenceTheirSkills` rather than
   deleting it, and called this out in its own commit message per the
   implementer contract ("otherwise stop and report" — I judged this
   unambiguous enough not to block on, since A4 explicitly places that exact
   sentence in `swarm-reviewer`'s spec text).
2. **Ticking P3 checkboxes in the plan doc.** Not in the brief's Files list.
   Did it anyway, in its own commit, because P2's merge left a precedent
   (`d53b689 docs(plan): tick P2 and P5 units`) and it's a low-risk,
   easily-reverted addition. If this should instead happen at merge time
   (by whoever merges P3), that commit (3ce94de) can be dropped or the
   orchestrator can ignore it.
3. **`swarm-batching`'s P12 pointer wording.** The brief says it "gains a
   pointer to the plan-registration warnings once P12 lands" — the draft
   already had warning-shaped language ("plan registration warns"). Added an
   explicit "(once P12 lands...)" parenthetical rather than restructuring the
   section, to keep the diff minimal and the existing size-bounds/role table
   (already correct per advisor review against C4/C5) untouched.
4. Did not touch `internal/runtime/text.go`'s `skills(role)` kickoff table
   (spec A3) — that's outside this package's Files list and, per spec A4's
   own framing, looks like P4's job (P4's brief mentions `RoleSkills`).

## Fix round 0 (self-review via advisor, before first report)

Advisor caught two issues by diffing the whole `swarm` rewrite against its
pre-P3 version rather than trusting the reference-test coverage:

1. **Dropped sentence in `swarm` rule 5.** The rewrite (`Write`, not `Edit`)
   silently lost "Top-level agents may use their native question tool or
   `swarm_ask`" from bullet 1 while restructuring. No test caught this
   because nothing asserted its presence. Verified with:
   ```
   $ git diff d53b689..HEAD -- skills/swarm/SKILL.md
   ```
   and confirmed every remaining `-` line pairs with an intended `+` change
   (description tail, rule 5 bullet 4's Workflow addition, rule 9a's
   swarm-advisor pointer, rule 11's replacement) — the ask-tool sentence was
   the only unpaired loss. Restored verbatim.
2. **`swarm-designer` overstepped into the daemon's job.** "Register the
   artifact and write `completed`..." would have a designer call
   `swarm_artifact register` itself; spec B5 says the daemon does this
   (`registerArtifactAsDaemon`) off the `artifacts` field on `completed`.
   Reworded to "the daemon registers it, not you."

Fix commit `8414070`. Re-verified:
```
$ make skills-sync
$ go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	5.899s
$ go build ./... && go vet ./...
(no output — success)
$ git diff d53b689..HEAD -- skills/swarm/SKILL.md | grep '^-' | grep -v '^---'
(only the four intended lines, no unpaired losses)
```

## Fix round 1 (Opus review, all 8 findings)

Findings file: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p3-fix1-findings.md`

1. **Important — swarm rules 3/4 missing workflow-relay carve-out.** Spec B4
   says `WriteCheckpoint` does not relay `accepted`/`progress`/`completed`
   checkpoints to the parent for agents with a `workflow_runs` row (the
   engine owns those); `blocked`/`failed`/`handoff`/questions still go
   through. Rule 3's completed-finding-send and rule 4's resume-finding-send
   had no such carve-out, so a workflow-step agent would still send a
   redundant `swarm_send` on every `completed` and every resume. Fixed:
   both rules now end with "— unless your assignment has a `## Workflow`
   section: ... (rule 11)", and rule 11 states the full carve-out once
   ("only questions, `blocked` and `failed` checkpoints still reach your
   orchestrator from you directly"). Pinned with a new required substring
   on the `swarm` row of `TestRoleSkillsReferenceTheirSkills`.
2. **swarm-coder — TDD detail lost.** Restored: the red run is recorded in
   a `progress` checkpoint with `note: "<why it fails>"` at the moment it
   happens (step 1), and the refactor step is back after green (step 2).
3. **swarm-coder — wording/omission.** Unified on "every rw worktree shared
   with you" (matches B5's commit-gate wording) in both the Verify section
   and the completed git contract section (previously "worktree you
   touched" / "worktree your brief shares with you" — two different
   phrasings). Added: a single-unit `## Steps` task has no unit number, so
   its verification entries omit `unit`.
4. **skills_test.go — loose pins.** Replaced the individual-word checks
   (`"verdict"`, `"changes_requested"`, `"critical"`, `"major"`, `"minor"`,
   `"nit"`) with the exact literal contract strings the skills actually use:
   `` "`pass` | `changes_requested` | `blocked`" `` and
   `"critical|major|minor|nit"`, plus `"for each acceptance criterion"`.
   Added the same two contract-string pins to the `swarm-ui-reviewer` row
   (it already carried the identical literal text, just unpinned).
   `TestSuperpowersReferencesAreKnown` now also fails if it finds zero
   `superpowers:` references across the whole non-vendored tree.
5. **skills_test.go designer row — under-pinned.** Added `"Interaction &
   motion"`, `"Mobile specifics"`, `"Open questions"`, `"Mermaid"` — all
   already present in `swarm-designer/SKILL.md`'s text, just not
   previously asserted.
6. **swarm-batching — forward-looking pointer.** Replaced "(once P12
   lands... see spec C2)" with the actual behaviour it will have: "plan
   registration returns these as `warnings[]` (registration still
   succeeds; the warning shows on the board's plan review screen)".
7. **swarm-designer — invalid `--stack` examples.** The skill said "web,
   React Native, iOS, Android", none of which are real
   `ui-ux-pro-max` stack values. Checked
   `skills/vendor/ui-ux-pro-max/scripts/core.py`'s `STACK_CONFIG` dict
   (22 real keys: `react`, `nextjs`, `vue`, `svelte`, `astro`, `swiftui`,
   `react-native`, `flutter`, `nuxtjs`, `nuxt-ui`, `html-tailwind`,
   `shadcn`, `jetpack-compose`, `threejs`, `angular`, `laravel`, `javafx`,
   `wpf`, `winui`, `avalonia`, `uno`, `uwp`) and swapped in real examples:
   `react`, `react-native`, `swiftui`, `jetpack-compose`, `html-tailwind`.
8. **swarm-designer — brainstorming relationship unclear.** Added a
   sentence: the design artifact replaces brainstorming's usual spec/plan
   steps (brainstorming alone ends in a user-approved spec plus
   `superpowers:writing-plans`; here the artifact itself is the spec a
   coder builds from) — don't also write a separate spec or plan.

Fix commit: `c949c0e fix(skills): P3 review round 1 — relay suppression,
TDD detail, verdict pinning`.

### Covering tests

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills|TestSuperpowersReferencesAreKnown|TestSkillBodyCarriesTheSpecFrontmatterAndLastRule' -v
--- PASS: TestSkillBodyCarriesTheSpecFrontmatterAndLastRule (0.00s)
--- PASS: TestRoleSkillsReferenceTheirSkills (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-batching (0.00s)
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
```

### Full Verify (per coordinator's instruction, HEAD c949c0e)

```
$ go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.855s
(full -v run: every test PASS, no skips)

$ go build ./...
(no output — success)

$ go vet ./...
(no output — success)

$ git status --porcelain
(empty — clean tree)
```

No `go test ./...` or `go test ./cmd/...` run, per the implementer contract
and the coordinator's reminder.

## Fix round 2 (Opus re-review: all 8 round-1 findings ADDRESSED; 3 new follow-ups)

Findings file: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p3-fix2-findings.md`

1. **Important — swarm-coder step 2 didn't say "checkpoint".** Step 1 said
   "Immediately write a `progress` checkpoint recording that run"; step 2
   only said "Run the test again. Record `{phase: green, ok: true, unit:
   <n>}`" with no checkpoint verb, while the Verify section already claimed
   both were "recorded as `progress` checkpoints (step 1 and 2 above)" — a
   real inconsistency the `tdd` gate cares about (it reads checkpoint
   `verification` entries, not narrative text). Fixed: step 2 now reads
   "Run the test again; record it too, in a `progress` checkpoint:
   `verification: [{"phase": "green", "ok": true, "unit": <n>}]`." Pinned
   with a new swarm-coder row substring: `"record it too, in a
   \`progress\` checkpoint"`.

2. **Minor — swarm rules 3/11 had the relay reason backwards.** I had
   written "the daemon relays your `completed` to your orchestrator
   itself" — but spec B4 says the opposite: `WriteCheckpoint` does **not**
   relay `accepted`/`progress`/`completed` for a workflow step at all; the
   *engine* separately reports the whole workflow's outcome
   (`workflow_succeeded`/`workflow_escalated`) to the orchestrator once
   the package resolves, on its own schedule — not as a live per-checkpoint
   relay. Reworded rule 11: "Your `accepted`, `progress` and `completed`
   checkpoints are not individually relayed to your orchestrator while
   you're a workflow step — the engine reports the workflow's outcome
   (success or escalation) to your orchestrator itself, on its own
   schedule, once the whole package resolves..." Also added `handoff` to
   the list of checkpoint kinds that still reach the orchestrator directly
   (spec B4's actual list: "`blocked`, `failed`, `handoff`, and
   `swarm_send` questions still reach the orchestrator" — I'd previously
   only listed `blocked`/`failed`/questions and dropped `handoff`). Pinned
   both the new reason phrase and the corrected list in the `swarm` row.

3. **Check and state — does a fix round start a new attempt?** Read spec
   B4 (`RetryFix: bump workflows.round; Retry(builder, note)`), B5 (the
   `tdd` gate is explicitly "across this attempt's entries"), and B6 (the
   example rendered brief calls it "this same session"), then cross-checked
   against the actual `Retry` implementation already in the codebase
   (`internal/runtime/agents.go:1454-1531`, pre-existing, not part of P3):
   it requires the session to already be in a terminal state
   (`Completed`/`Failed`/`Crashed`/`Interrupted`), unconditionally computes
   `nextAttempt := ses.Attempt + 1`, and delivers `note` as an
   `assignment_update` message to the new session. **Confirmed: a fix
   round is a new attempt** — B6's "this same session" is agent-identity
   continuity language (you're not re-spawned as a stranger with a fresh
   brief), not literal DB-session-row continuity. Consequence for the
   `tdd` gate, read literally: since the gate only sees this attempt's
   checkpoint entries, and the spec's own error copy is "the error names
   the units missing evidence" (checked across the whole `units[]` list,
   not just touched ones), every unit needs a fresh red→green pair
   recorded in the new attempt, not only the unit(s) a finding names.

   Rewrote swarm-coder's "Fix rounds" section to state this plainly and to
   name the **unresolved spec gap** honestly rather than inventing a
   comfortable exception: the spec gives no mechanism for producing a
   *genuine* new red on a unit whose code is already correct and
   unchanged (re-running an already-passing test can't legitimately fail).
   The skill tells the coder: don't fabricate a red to satisfy the gate's
   letter; give a real red→green pair for the unit(s) actually touched,
   record a real green for unchanged units, and if the gate then refuses
   `completed` over an unchanged unit's missing red, say so plainly
   (`blocked`, or ask the advisor/parent) rather than paper over it. Pinned
   with `"new attempt"` on the swarm-coder row.

Fix commit: `36d90a4 fix(skills): P3 review round 2 — fix-round attempt
semantics, relay wording`.

### Covering tests

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/... -run 'TestRoleSkillsReferenceTheirSkills|TestSuperpowersReferencesAreKnown|TestSkillBodyCarriesTheSpecFrontmatterAndLastRule' -v
--- PASS: TestSkillBodyCarriesTheSpecFrontmatterAndLastRule (0.00s)
--- PASS: TestRoleSkillsReferenceTheirSkills (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-batching (0.00s)
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
```

Also re-ran the unpaired-diff sanity check from fix round 0 against the
whole `swarm` rewrite (`git diff d53b689..HEAD -- skills/swarm/SKILL.md |
grep '^-' | grep -v '^---'`): every remaining removed line corresponds to
an intended replacement (description tail, rules 3/4 reworded, rule 5's
`Workflow` addition, rule 9a's swarm-advisor pointer, rule 11 replaced) —
no unpaired content loss.

### Full Verify (HEAD 36d90a4)

```
$ go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.214s
(full -v run: every test PASS)

$ go build ./...
(no output — success)

$ go vet ./...
(no output — success)

$ git status --porcelain
(empty — clean tree)
```

No `go test ./...` or `go test ./cmd/...` run, per the implementer contract.

## Fix round 2b (controller ruling on the fix-round tdd-gate gap)

Ruling file: `/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/ruling-tdd-fix-rounds.md`

The controller resolved the spec gap flagged in fix round 2 (every unit
needing fresh red/green evidence in every fix-round attempt, including
unchanged units, was impossible to satisfy honestly). Ruling: a fix-round
attempt (round > 1) only needs red-before-green for the units its
unit-tagged findings actually name; a package-wide (untagged) finding
needs at least one red-before-green pair somewhere in the attempt; units
no finding touches need no new `tdd` evidence that round (the `verify`
gate's full command run still covers them); a non-testable finding still
counts via a note or the actual failing check used. Round 1 (the step's
first attempt) is unchanged — every unit still needs its own pair.

### 1. Spec amendment

Amended B5's `tdd` gate bullet in
`docs/specs/2026-09-24-self-contained-tasks-and-role-skills.md` to state
the ruling precisely: split the bullet into "first attempt (round 1)"
(unchanged, every unit) and "fix-round attempts (round > 1)" (only the
units named by that round's findings, or one pair for an untagged
finding), plus the non-testable-finding allowance and the narrowed error
copy ("names only the units still missing required evidence for the
current attempt").

Commit: `b1bb0fd docs(spec): tdd gate in fix rounds covers only the units
the findings name`.

### 2. swarm-coder rewrite

Rewrote the "Fix rounds" section to follow the amended spec instead of
the fix-round-2 "unresolved spec gap / write `blocked` or ask the
advisor" guidance, which the ruling makes obsolete:
- unit-tagged findings → red-before-green for their unit, in this attempt;
- an untagged (package-wide) finding → at least one red-before-green pair
  somewhere in the attempt;
- units no finding touches → no new `tdd` evidence needed that round (the
  `verify` gate's full command run still covers them);
- a non-testable finding (wording/docs) still counts toward its unit: the
  red entry's `note` explains why it's the new/updated test, or, when no
  test can express it, records the actual failing check used (grep/lint).

Checked whether the pinned test substring needed updating: `"new
attempt"` (added in fix round 2) still appears verbatim in the rewritten
section's first paragraph, and nothing was pinning the now-removed "spec
gap"/`blocked` language, so `internal/install/skills_test.go` needed no
changes.

Commit: `7981427 docs(skills): swarm-coder fix rounds follow the
tdd-gate ruling`.

### Covering tests

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go test ./internal/install/... -v
... (full run)
--- PASS: TestRoleSkillsReferenceTheirSkills (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-coder (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-ui-reviewer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-designer (0.00s)
    --- PASS: TestRoleSkillsReferenceTheirSkills/swarm-batching (0.00s)
--- PASS: TestSuperpowersReferencesAreKnown (0.00s)
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.471s
```

### Full Verify (HEAD 7981427)

```
$ go test ./internal/install/...
ok  	github.com/AlexanderTar/agent-swarm/internal/install	5.621s

$ go build ./... && go vet ./...
(no output — success)

$ git status --porcelain
(empty — clean tree)
```

No `go test ./...` or `go test ./cmd/...` run, per the implementer contract.
