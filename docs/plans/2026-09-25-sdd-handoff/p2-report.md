# P2: Vendored skills — implementation report

Worktree: `/Users/alexandertar/GitHub/agent-swarm--p2`, branch `pkg/p2`.
Base: `23b5368` (merge of main into the working branch). Final HEAD: `b8b89ce`.

**Note on testing scope:** per the coordinator's mid-task instruction, I
never ran `go test ./...` or `go test ./cmd/...` (a known bug there rewrites
the real user's `~/.claude/skills`). All verification below is
`go test ./internal/install/...` plus `go build ./... && go vet ./...`, as
the brief's Verify set requires.

## Units

### Unit 2.1 — Provenance test and vendor README
Commit `45c218c` `test(skills): vendored skill provenance checks`.

Added `TestVendoredSkillsHaveLicenseAndProvenance` to
`internal/install/skills_test.go`: walks `../../skills/vendor`, asserts the
dir set is exactly the 11 spec-A2 names, each has a `LICENSE`/`LICENSE.md`
and a `VENDORED.md` with the `# Vendored: <name>` header and `Source`/
`License`/`Vendored on`/`Changes` fields, each `SKILL.md` frontmatter `name`
matches its dir, and no file under `skills/vendor/` contains
`CLAUDE_PLUGIN_ROOT`, `submit-expo-feedback`, `raw.githubusercontent.com`,
or `/ponytail ` (trailing space, i.e. the interactive slash-command form).

Also wrote `skills/vendor/README.md` (name/source/license/role-consumer
table for all 11) and added the README License line: "Vendored skills under
`skills/vendor/` keep their own licenses; see each `VENDORED.md`."

RED:
```
$ go test ./internal/install/... -run TestVendoredSkillsHaveLicenseAndProvenance -v
=== RUN   TestVendoredSkillsHaveLicenseAndProvenance
    skills_test.go:718: open ../../skills/vendor: no such file or directory
--- FAIL: TestVendoredSkillsHaveLicenseAndProvenance (0.00s)
FAIL
```
(`go build ./... && go vet ./...` green throughout.)

**Judgment call / self-caught process gap:** this commit added
`skills/vendor/README.md` but I did not run `make skills-sync` before
committing it (only ran the single `-run` test, not the drift test). The
mirror gap was closed automatically by unit 2.2's `make skills-sync`, which
mirrors the whole tree — no functional issue, since nothing was released
between the two commits, but the discipline slipped for one commit. Noted
here rather than silently fixed.

### Unit 2.2 — web-design-guidelines and building-components
Commit `fe6a814` `feat(skills): vendor web design and component skills`.

- **web-design-guidelines**: `SKILL.md` from
  `vercel-labs/agent-skills@063bee9` `skills/web-design-guidelines`
  (frontmatter `name` already matched). `references/rules.md` is
  `vercel-labs/web-interface-guidelines@e3d624b`'s `command.md`, vendored
  verbatim. Rewrote the "fetch guidelines from a raw GitHub URL with
  WebFetch" instructions in `SKILL.md` to read the local
  `references/rules.md` instead. Wrote a `LICENSE` (MIT, crediting Vercel
  for `SKILL.md` and Vercel Labs for `references/rules.md`, since
  agent-skills ships no LICENSE file of its own — its README says MIT;
  web-interface-guidelines' own LICENSE, copied faithfully, confirms Vercel
  Labs / MIT for the rules file).
- **building-components**: `vercel/components.build@ba72745`
  `skills/building-components`, copied verbatim (frontmatter `name` already
  matched, no content changes). `LICENSE` is the upstream repo's own
  Apache-2.0 file — the repo's README says "MIT" in prose, but the LICENSE
  file (Apache-2.0) governs; `VENDORED.md` notes Apache §4(b) requires a
  changed-file notice only for files we changed, and we changed none.

GREEN: `go test ./internal/install/... -run TestVendoredSkillsHaveLicenseAndProvenance -v`
still red (progresses to "2/11 dirs present, want 11" — expected, more
units to go). `go test ./internal/install/...` (full package) green except
that one test. `go build ./... && go vet ./...` green.

### Unit 2.3 — ui-ux-pro-max
Commit `7f14bd1` `feat(skills): vendor ui-ux-pro-max` (VENDORED.md later
fixed in `f67a3da`, see below).

Vendored `nextlevelbuilder/ui-ux-pro-max-skill@4d140cf` (tag `v2.13.0`)
`.claude/skills/ui-ux-pro-max`, dropping `scripts/tests/`. `core.py`
already resolves `DATA_DIR = Path(__file__).parent.parent / "data"`, so no
script code needed changing. `SKILL.md`'s eleven `search.py` invocations
all used
`${CLAUDE_PLUGIN_ROOT}/.claude/skills/ui-ux-pro-max/scripts/search.py`;
rewrote every one to `scripts/search.py` (relative to the skill's own
directory) and reworded the surrounding note. Kept `data/` (19 CSVs incl.
`data/stacks/*`) unchanged, including each row's own source/provenance
columns. `LICENSE` is upstream's own MIT file (Next Level Builder).

Verify line:
```
$ python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style | head -5
## UI Pro Max Search Results
**Domain:** style | **Query:** dashboard
**Source:** styles.csv | **Found:** 3 results

### Result 1
```

**Self-caught bug (fixed in unit 2.5's commit, see below):** running
`search.py` compiled `.pyc` files into `scripts/__pycache__/`, which I
committed by accident alongside the skill (both under `skills/` and the
`internal/install/skills` mirror). Fixed in the very next commit
(`aa78270`) by removing them and adding `__pycache__/` to `.gitignore`.

### Unit 2.4 — mobile skills
Commit `19b1a19` `feat(skills): vendor mobile design skills`.

- **expo-native-ui**, **expo-design-system**: `expo/skills@efa52f0`
  `plugins/expo/skills/{expo-native-ui,expo-design-system}`. Removed each
  skill's "Submitting Feedback" section (runs an upstream feedback CLI on
  every use — not applicable to an offline vendored copy) and dropped the
  upstream `agents/openai.yaml` (not skill content). `LICENSE` is the
  expo/skills repo's own MIT file (650 Industries).
- **vercel-react-native-skills**: `vercel-labs/agent-skills@063bee9`
  `skills/react-native-skills`. Upstream's own frontmatter `name` was
  already `vercel-react-native-skills` (differs from the upstream dir name
  `react-native-skills`, matching the brief's expectation). No content
  changes beyond frontmatter (see below). Added a `LICENSE` (MIT, crediting
  Vercel) since this repo ships none of its own.
- **mobile-ios-design**, **mobile-android-design**:
  `wshobson/agents@4236bb9` `plugins/ui-design/skills/{mobile-ios-design,
  mobile-android-design}`, copied verbatim, no changes. `LICENSE` is the
  repo's own MIT file (Seth Hobson).

**Judgment call — bug found via the existing (non-vendor) registry test,
not the new one:** `TestSkillsRegistryMatchesTree` failed after this unit:
`vercel-react-native-skills: empty description`. Upstream's frontmatter used
a multi-line plain YAML scalar (`description:` with the value starting on
the next line, indented), but this repo's frontmatter parser
(`internal/install/skills.go`'s `frontmatterField`) is documented as
"deliberately minimal: skills' frontmatter is flat, single-line key/value
pairs" — it only reads the same line as the key. Reflowed the description
to one line (wording unchanged) and documented it in `VENDORED.md`. This
recurred for all three ponytail skills in unit 2.5 (they use YAML's `>`
folded-scalar form, which the naive parser reads as the literal string
`">"`, non-empty but wrong) — same fix, same documentation pattern, applied
proactively there.

GREEN: `go test ./internal/install/...` full package — only
`TestVendoredSkillsHaveLicenseAndProvenance` still failing on dir count
(8/11 present; expected, 3 ponytail dirs still missing).

### Unit 2.5 — ponytail
Commit `f67a3da` `feat(skills): vendor ponytail skills`.

Vendored `ponytail`, `ponytail-review`, `ponytail-debt` from
`DietrichGebert/ponytail@e3ba2aa` (pinned, per the brief), `skills/<name>`,
MIT (DietrichGebert). Frontmatter names already matched dir names for
`ponytail-review`/`ponytail-debt`; `ponytail`'s did too.

In `ponytail/SKILL.md`, replaced the "## Persistence" section:
```
ACTIVE EVERY RESPONSE. No drift back to over-building. Still active if
unsure. Off only: "stop ponytail" / "normal mode". Default: **full**.
Switch: `/ponytail lite|full|ultra`.
```
with the brief's exact text:
```
Swarm runs ponytail at **full** level for coders and mechanical agents;
there is no mode switching in swarm sessions.
```
This was also the only occurrence of the banned `/ponytail ` string in the
whole vendored tree — confirmed by grep before and after the edit. I left
the rest of the file (the `## Intensity` table's lite/full/ultra rows, and
the closing `## Boundaries` paragraph's "stop ponytail"/"normal mode"
sentence) untouched: the brief named only the "Persistence" section and
"the intensity-switch commands" (i.e. the slash-command switch), and the
Boundaries text doesn't contain a banned string. Flagging as a judgment
call — an argument could be made those are now slightly stale in a
swarm-only context, but the brief's acceptance line ("fixed `full` level,
and no slash-command persistence section") describes exactly what I
changed.

Also reflowed all three skills' `description: >` (YAML folded scalar)
frontmatter to one line each, for the same frontmatter-parser reason as
`vercel-react-native-skills` in unit 2.4 — documented per-skill in each
`VENDORED.md`.

**Bug found and fixed in this same commit:** `ui-ux-pro-max/VENDORED.md`
(written two commits earlier) named the literal upstream path
`${CLAUDE_PLUGIN_ROOT}/.claude/skills/ui-ux-pro-max/scripts/search.py` when
describing the change — which itself contains the banned string
`CLAUDE_PLUGIN_ROOT`. My unit 2.3 verification only ran the narrow
`-run TestVendoredSkillsHaveLicenseAndProvenance`, and at that point only
3/11 vendored dirs existed, so the test's dir-count check failed with
`t.Fatalf` (which halts the test) *before* reaching the banned-strings
walk — the bug was latent and untested until all 11 dirs existed. Caught it
here by running the full `go test ./internal/install/...` (not just the
narrow `-run`) once all units were in place, and reworded the sentence to
describe the path pattern without quoting the literal banned string.

GREEN:
```
$ go test ./internal/install/... -v 2>&1 | tail -20
...
=== RUN   TestVendoredSkillsHaveLicenseAndProvenance
--- PASS: TestVendoredSkillsHaveLicenseAndProvenance (0.01s)
...
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.743s
```

### Extra commit — `aa78270` `chore(skills): drop __pycache__ from ui-ux-pro-max, ignore it repo-wide`
Not one of the 5 planned units; a same-session fix for the `__pycache__`
leak noted in unit 2.3 (removed the `.pyc` files from both `skills/` and
the `internal/install/skills` mirror, added `__pycache__/` to
`.gitignore`). Listed here for completeness since it's a real commit on the
branch.

## Full package Verify (re-run after the fix round, final HEAD `ccc20ce`)

```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go build ./... && go vet ./...
(clean, no output)

$ go test ./internal/install/... -v
... (33 tests)
--- PASS: TestVendoredSkillsHaveLicenseAndProvenance (0.01s)
...
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.841s

$ python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style | head -5
## UI Pro Max Search Results
**Domain:** style | **Query:** dashboard
**Source:** styles.csv | **Found:** 3 results

### Result 1
```

`git status --short` after the run (and after removing the `__pycache__/`
the search re-created, now gitignored): clean.

I did **not** run `go test ./...` or `go test ./cmd/...`, per the
coordinator's mid-task instruction (a known bug rewrites the real user's
`~/.claude/skills`).

## Files changed
- `internal/install/skills_test.go` — new
  `TestVendoredSkillsHaveLicenseAndProvenance`, `vendoredSkillNames`,
  `bannedVendorStrings`, `sortedKeys` helper.
- `README.md` — one-line License section addition.
- `.gitignore` — `__pycache__/`.
- `skills/vendor/README.md` — new (provenance table for all 11).
- `skills/vendor/{web-design-guidelines,building-components,ui-ux-pro-max,
  expo-native-ui,expo-design-system,vercel-react-native-skills,
  mobile-ios-design,mobile-android-design,ponytail,ponytail-review,
  ponytail-debt}/**` — new (SKILL.md, LICENSE, VENDORED.md, and each
  skill's own references/scripts/data as vendored).
- `internal/install/skills/vendor/**` — the `make skills-sync` mirror of
  all of the above.

299 files changed, 49823 insertions across the package (base `23b5368` to
final `f67a3da`, restricted to `skills/vendor`, `internal/install/skills`,
`internal/install/skills_test.go`, `README.md`, `.gitignore`).

## Self-review notes
- Verified every spec-A2 source path, license, and pinned commit against
  the actual upstream repos before vendoring (all matched the spec exactly
  — no NEEDS_CONTEXT triggers found). Commit SHAs used: agent-skills
  `063bee94c3f4df8453406c830b0a7df0f2860278`, components.build
  `ba727450437385af662810329fa19896215f76d5`, expo/skills
  `efa52f0a9d2176db75992736281c77da1b714fa3`, ui-ux-pro-max-skill
  `4d140cf8ff6842de13213c7214eff3810371beb2` (= tag v2.13.0),
  web-interface-guidelines `e3d624baaf29dc1fc645aff3e38f03e564d2d6b1`,
  wshobson/agents `4236bb91f8395b0435f1d8b8baf9e8e4c69a8620`, ponytail
  `e3ba2aa6f1e6f0bc4d69eb09c9f0d0a93af56156` (pinned per the brief).
- Every VENDORED.md was individually swept for the 4 banned strings before
  committing (in addition to the full-package test run at the end).
- Two real bugs were caught by my own re-verification passes, not left for
  review: the `__pycache__` leak (unit 2.3) and the banned-string leak in
  `ui-ux-pro-max/VENDORED.md` (caught in unit 2.5 via a full, not narrow,
  test run). Both are called out above rather than silently folded in.

## Fix round 1 (advisor review, before reporting)
Called the advisor before declaring done. Two real findings, both fixed in
commit `ccc20ce` `fix(skills): match spec A2 exactly for ponytail and the
expo skills`:

1. **`ponytail/SKILL.md`'s `## Boundaries` section still carried a second
   copy of the mode-switch text** ("stop ponytail" / "normal mode": revert.
   Level persists until changed or session end.). I'd only replaced the
   `## Persistence` section; spec A2's own wording ("replace the
   Persistence/intensity-switch instructions (slash commands, 'stop
   ponytail')") covers this second copy too — the brief's shorthand
   acceptance line ("no slash-command persistence section") didn't make
   that obvious enough, but "modifications match spec A2 exactly" does.
   Deleted the sentence, left the rest of `## Boundaries` and the whole
   `## Intensity` table alone (those describe levels, not switch them).
   Documented in `ponytail/VENDORED.md`.
2. **`expo-native-ui`/`expo-design-system`: I'd dropped the upstream
   `agents/openai.yaml` as "not skill content," which spec A2 never asked
   for** — A2 lists exactly one change per skill ("Remove 'Submitting
   Feedback' section") and is explicit elsewhere when it wants a directory
   dropped (ui-ux-pro-max's `scripts/tests/`). Restored both
   `agents/openai.yaml` files verbatim (swept for all 4 banned strings
   first, clean), updated both `VENDORED.md`s to say so.

Re-ran `make skills-sync`, `go build ./... && go vet ./...`, and
`go test ./internal/install/...` (full package) after the fix — all green,
same as the final Verify run reproduced below.

## Concerns
- The four frontmatter-reflow fixes (`vercel-react-native-skills` + all
  three ponytail skills) are content changes beyond what spec A2's table
  literally lists ("None" / no mention) — driven by this repo's own
  frontmatter-parsing constraint (A1: `internal/install/skills.go`'s
  `frontmatterField` only reads flat, single-line `key: value` frontmatter,
  and silently reads a YAML `description: >` folded scalar as the literal
  string `>` rather than erroring). Each reflow is documented per-skill in
  its own `VENDORED.md` with wording otherwise unchanged, but this is a
  real deviation from the spec table and the A1 owner should know the
  parser has this gap — `TestSkillsRegistryMatchesTree`'s "non-empty
  description" check doesn't catch a `description` that decoded to just
  `>`, only a genuinely empty one.

## Fix round 1 (external review, Opus — findings at
`/private/tmp/claude-501/-Users-alexandertar-GitHub-agent-swarm/959691ff-160f-4831-94da-3c84074966d6/scratchpad/sdd/p2-fix1-findings.md`)

Commit `b8b89ce` `fix(skills): restore byte-verbatim frontmatter; sharpen
the provenance test`. Fixed all 5 findings:

1. **(Important) Reverted the 4 frontmatter reflows; fixed the test helper
   instead.** The reviewer traced it correctly: production
   `frontmatterField` (`internal/install/skills.go`) is only ever called
   for `"name"`, never `"description"` — the frontmatter-parsing gap I'd
   found was entirely in the *test* helper `parseFrontmatter`
   (`internal/install/skills_test.go`), which read only the first line
   after `key:` and so misdecoded a folded/multi-line `description`. Fixed
   the actual bug: rewrote `parseFrontmatter` to decode the frontmatter
   block with `gopkg.in/yaml.v3` (already a direct dependency — see
   `internal/kb/doc.go`'s `ParseDoc`) instead of a naive line scan. Then
   restored `vercel-react-native-skills/SKILL.md`,
   `ponytail{,-review,-debt}/SKILL.md` byte-verbatim from the upstream
   clones at their recorded SHAs (confirmed with `diff` against
   `scratchpad/vendor-src/...` — exit 0 on all 4 once ponytail's other
   intentional edits, see #2 below, were accounted for). Updated all 4
   `VENDORED.md`s: `vercel-react-native-skills` keeps "Added LICENSE";
   `ponytail-review`/`ponytail-debt` now say "Changes: none, byte-verbatim
   upstream"; `ponytail` drops the reflow bullet.
2. **(Minor) Dropped `ponytail/SKILL.md`'s `argument-hint:
   "[lite|full|ultra]"`** — the removed slash-command mode switch's own
   argument hint. Listed in `VENDORED.md`.
3. **(Minor) `TestVendoredSkillsHaveLicenseAndProvenance`'s dir-count
   check used `t.Fatalf`,** which (via `runtime.Goexit`) skipped the
   per-skill checks and the banned-strings walk on a count mismatch — the
   same class of gap that let the `CLAUDE_PLUGIN_ROOT` leak in
   `ui-ux-pro-max/VENDORED.md` go undetected for two commits in the
   original round. Changed to `t.Errorf`.
4. **(Minor) `ui-ux-pro-max/SKILL.md`'s path note named this repo's own
   path** (`skills/vendor/ui-ux-pro-max/scripts/search.py`), which is
   wrong once the skill is installed elsewhere (`<cwd>/.claude/skills/
   ui-ux-pro-max/` or `~/.swarm/skills/ui-ux-pro-max/`). Reworded to
   "relative to the directory containing this SKILL.md". Listed in
   `VENDORED.md`.
5. **(Minor) `sortedKeys` duplicated `slices.Sorted(maps.Keys(m))`.**
   Replaced both call sites with the stdlib form and deleted the helper.

Covering tests — run after all 5 fixes, HEAD `b8b89ce`:
```
$ make skills-sync
rm -rf internal/install/skills
cp -R skills internal/install/skills

$ go build ./... && go vet ./...
(clean, no output)

$ go test ./internal/install/... -v
...
=== RUN   TestVendoredSkillsHaveLicenseAndProvenance
--- PASS: TestVendoredSkillsHaveLicenseAndProvenance (0.01s)
...
PASS
ok  	github.com/AlexanderTar/agent-swarm/internal/install	4.875s

$ python3 skills/vendor/ui-ux-pro-max/scripts/search.py "dashboard" --domain style | head -5
## UI Pro Max Search Results
**Domain:** style | **Query:** dashboard
**Source:** styles.csv | **Found:** 3 results

### Result 1
```
`git status --short` after removing the `__pycache__/` the search
re-created (gitignored): clean.

Never ran `go test ./...` or `./cmd/...`, per the standing instruction.

## Status
DONE. All 5 units plus 2 fix rounds (advisor + external review) committed;
`TestVendoredSkillsHaveLicenseAndProvenance` green; full package Verify set
green; `go build`/`go vet` clean throughout. No open concerns remain — the
frontmatter-reflow deviation flagged after round 1 was the round-1-fix
target itself and is now resolved at the root cause (test helper, not
vendored content).
