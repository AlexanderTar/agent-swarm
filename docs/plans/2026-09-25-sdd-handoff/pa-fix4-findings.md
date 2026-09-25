# PA fix round 4 (fresh implementer; prior rounds in pa-report.md)
Ruling (controller): one bad link must never abort `swarm install` / cursor vendoring. In copyTreeGuarded (internal/install/plugins.go:~413-466):
- a link whose target doesn't exist (EvalSymlinks/Stat IsNotExist) → skip that entry, log one line naming it, continue.
- a detected cycle → skip that entry with a log line naming it, continue (replaces "abort with error"); update the existing cycle test (skills_internal_test.go:16-34) to the new contract: no error, the cyclic entry absent, siblings copied. This is an intentional contract change — say so in the commit.
- seed `visited` with EvalSymlinks(src) so a cycle is caught before an extra copy lands.
- add a `ponytail:` comment: following links copies whole targets (no size cap); add a cap if vendored/legacy trees ever link to huge dirs.
Tests to commit (red first where they fail today):
1. TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks gains a nested relative `AGENTS.md -> SKILL.md` link → real file with content (reviewer repro TestReview2CursorNestedFileLink in scratchpad/rr-pa2-base/internal/install/zz_review2_test.go).
2. Salvaged file link `alias.md -> SKILL.md` survives RemoveAll(run/launch/ses_x) (repro TestReview2SalvageSurvivesReap, same file).
3. Top-level dangling legacy link and nested dangling link → WriteAgy succeeds, entry skipped (repros TestReview3TopLevelDangling / TestReview3NestedDangling in scratchpad/rr-pa3/internal/install/zz_review3_test.go).
4. Cycle → skipped, siblings copied, no partial copy of the cyclic entry.
Minor: fix stale comments (agy_test.go:563, header of TestWriteAgyRepairResolvesARelativeSymlinkTargetToAbsolute, agy_test.go:410) to describe deep-copy; extract one `underLaunchRoot(c, p) bool` helper for the duplicated check (agy.go:213-219, 279-284, 312-313); rename the `real` variables (shadow builtin) at agy.go:310 and plugins.go:447.
Verify: go test ./internal/install/... ./internal/adapter/... ./cmd/... ; go build ./... && go vet ./... ; go test ./... once.
