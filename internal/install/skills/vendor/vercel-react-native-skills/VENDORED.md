# Vendored: vercel-react-native-skills
- Source: https://github.com/vercel-labs/agent-skills@063bee94c3f4df8453406c830b0a7df0f2860278, path skills/react-native-skills
- License: MIT (see LICENSE). Copyright: Vercel, Inc. (per the agent-skills repo README; that repo has no LICENSE file of its own).
- Vendored on: 2026-09-24
- Changes:
  - Added this `LICENSE` file (MIT, crediting Vercel) since the source repo
    ships no LICENSE file of its own.
  - `SKILL.md`: upstream's frontmatter `name` was already
    `vercel-react-native-skills` (it differs from the upstream directory
    name `react-native-skills`, which is why this skill is vendored under
    `vercel-react-native-skills`). Its `description` was a multi-line
    folded YAML scalar (value starting on the line after `description:`);
    reflowed it to one line, wording unchanged, since this repo's
    frontmatter parser (`internal/install/skills.go`) only reads flat,
    single-line `key: value` frontmatter.
