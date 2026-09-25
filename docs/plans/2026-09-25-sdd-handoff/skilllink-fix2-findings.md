# skilllink fix round 2 — doc only (code + tests approved). No CLI runs, don't touch ~.
Edit docs/plans/2026-09-24-skill-symlink-probe.md:
A. :150 contradicts :156 — change to "did not exist". Mark ".migrated means an earlier skills migration" as a guess (the real antigravity-cli/skills was never repointed at ~/.gemini/config/skills).
B. :118-125 overclaims "copies … then repoints … Nothing was moved". Record only what was seen: the entry at antigravity-cli/skills ended up at $HOME/.gemini/config/skills with a link back; copy vs rename was not tested. The chain real -> ses_H52 (symlink) -> ses_FPV68 (real dir) argues the original tree left ~/.gemini/antigravity-cli/skills at some point. State the real risk: the shared tree now lives inside ses_FPV68's launch dir, so cleaning up that session deletes the real files (not "one broken link away"). Match skills.go:165's hedged "migrates" wording.
C. :177-182 "every spawn" -> "every new session" (setupEnv runs once per session; Resume reuses agy-home).
D. :122 "right after this round's cleanup" -> "the original pass".
E. Method :8-13: for agy the probe went at the chain's resolved dir (readlink -f), not Config.SkillsDir.
One docs commit. Append "Fix round 2" to the report.
