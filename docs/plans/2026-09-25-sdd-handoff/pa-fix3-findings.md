# PA fix round 3 (Opus re-review: findings 1-3 ADDRESSED; finding 4's copyTree change broke 2 things)
Controller ruling (replaces round-2 item 4's approach):
- copyTree DEREFERENCES symlinks everywhere inside the tree: a link to a file → CopyFile of the target's content; a link to a directory → recursively copy the target directory's contents into a real directory. Guard cycles with a set of visited real paths (EvalSymlinks) and a max depth (e.g. 32) → return a clear error on a cycle. This restores the cursor contract ("result is a real folder", plugins.go:392-393 comment stays true) and makes salvaged content survive a session reap.
- The agy salvage loop: a TOP-LEVEL legacy entry that is a symlink whose resolved target lies OUTSIDE <Home>/run/launch (e.g. the user's own skill linked from elsewhere) is recreated as a symlink to that absolute resolved target (preserve the user's link); any other entry goes through copyTree (deep copy).
Tests (red first where they fail today):
1. Reviewer's TestReview2CursorNestedFileLink (relative AGENTS.md -> SKILL.md inside a vendored plugin) → real file with the content. Add it to TestSyncVendorsCursorPluginsAsRealFoldersNotSymlinks.
2. Reviewer's TestReview2SalvageSurvivesReap (alias.md -> SKILL.md; WriteAgy; RemoveAll(run/launch/ses_x)) → alias.md still readable.
3. A nested link to a directory inside a salvaged skill → real directory after a reap.
4. A top-level legacy entry linked to a dir outside run/launch → stays a symlink to it.
5. A cycle (dir link pointing to an ancestor) → clear error, no infinite loop.
Also (minor): agy.go:208 — resolve c.Home first and join run/launch (so a missing run/launch doesn't hide an aliased dangling chain).
Reviewer repros: scratchpad/rr-pa2-base/internal/install/zz_review2_test.go.
New commits; append "Fix round 3"; reply with the short contract.
