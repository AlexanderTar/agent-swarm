# P2 review r1 (Opus) — fix all of these (fix round 1)

1. (Important) Revert the 4 frontmatter reflows; fix the test helper instead.
   Files: skills/vendor/{vercel-react-native-skills,ponytail,ponytail-review,ponytail-debt}/SKILL.md:2-3 and internal/install/skills_test.go:20-38.
   Spec A2 says "None"/"everything else verbatim" for these. Production frontmatterField is only called for "name" (skills.go:66, :248); only the test helper parseFrontmatter (used by TestSkillsRegistryMatchesTree :98) misreads folded `description: >`. And ">" being non-empty is itself a hole in the test.
   Fix: decode frontmatter in parseFrontmatter with gopkg.in/yaml.v3 (already a direct dep, see internal/kb/doc.go). Restore the 4 SKILL.md frontmatters byte-verbatim from upstream (clones in scratchpad/vendor-src at the recorded SHAs). Delete the reflow bullets from those 4 VENDORED.md (vercel-react-native-skills keeps "Added LICENSE"). Leave production frontmatterField alone.
2. (Minor) skills/vendor/ponytail/SKILL.md:4 `argument-hint: "[lite|full|ultra]"` is the removed mode switch's hint — drop it and list the change in VENDORED.md.
3. (Minor) internal/install/skills_test.go:733 t.Fatalf on wrong dir count skips the banned-strings walk — use t.Errorf.
4. (Minor) skills/vendor/ui-ux-pro-max/SKILL.md:39 example says "`skills/vendor/ui-ux-pro-max/scripts/search.py` in this repo" — wrong once installed (<cwd>/.claude/skills/ui-ux-pro-max/ or ~/.swarm/skills/ui-ux-pro-max/). Say "relative to the directory containing this SKILL.md". List in VENDORED.md.
5. (Minor) internal/install/skills_test.go:804 sortedKeys duplicates slices.Sorted(maps.Keys(m)) — use stdlib.
