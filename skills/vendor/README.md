# Vendored skills

Third-party skill content we ship as-is (or lightly trimmed), each under its
own upstream license. Every directory here carries the upstream `LICENSE` (or
`LICENSE.md`) file plus a `VENDORED.md` recording the exact source commit and
every change we made. See spec A2 for the full rationale.

| Skill | Source | License | Used by |
|---|---|---|---|
| web-design-guidelines | vercel-labs/agent-skills + vercel-labs/web-interface-guidelines | MIT | swarm-ui-reviewer |
| building-components | vercel/components.build | Apache-2.0 | swarm-ui-reviewer, swarm-designer |
| ui-ux-pro-max | nextlevelbuilder/ui-ux-pro-max-skill, tag v2.13.0 | MIT | swarm-ui-reviewer, swarm-designer |
| expo-native-ui | expo/skills | MIT (650 Industries) | swarm-ui-reviewer, swarm-designer |
| expo-design-system | expo/skills | MIT (650 Industries) | swarm-designer |
| vercel-react-native-skills | vercel-labs/agent-skills | MIT | swarm-ui-reviewer, swarm-designer |
| mobile-ios-design | wshobson/agents | MIT (Seth Hobson) | swarm-ui-reviewer, swarm-designer |
| mobile-android-design | wshobson/agents | MIT (Seth Hobson) | swarm-ui-reviewer, swarm-designer |
| ponytail | DietrichGebert repo, commit e3ba2aa | MIT (DietrichGebert) | swarm-coder, swarm-mechanical |
| ponytail-review | DietrichGebert repo | MIT | swarm-reviewer |
| ponytail-debt | DietrichGebert repo | MIT | swarm-orchestrator (integration pass) |

Every modification to an upstream file, however small, is listed in that
skill's own `VENDORED.md` under `Changes`. `internal/install/skills_test.go`
enforces that each of the 11 directories above has a license file and a
`VENDORED.md`, and bans a short list of leftover upstream strings anywhere
under this tree.
