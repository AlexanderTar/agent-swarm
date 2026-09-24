# Vendored: web-design-guidelines
- Source: https://github.com/vercel-labs/agent-skills@063bee94c3f4df8453406c830b0a7df0f2860278, path skills/web-design-guidelines (SKILL.md); https://github.com/vercel-labs/web-interface-guidelines@e3d624baaf29dc1fc645aff3e38f03e564d2d6b1, path command.md (vendored as references/rules.md)
- License: MIT (see LICENSE). Copyright: Vercel, Inc. (SKILL.md, per the agent-skills repo README, which has no LICENSE file); Vercel Labs (references/rules.md, per that repo's own LICENSE file).
- Vendored on: 2026-09-24
- Changes:
  - Vendored `command.md` as `references/rules.md`.
  - `SKILL.md`: replaced the "fetch guidelines with WebFetch from a raw
    GitHub content URL" instructions with reading the local
    `references/rules.md` file, so the skill works offline and does not
    depend on a live fetch.
  - Added this `LICENSE` file (the agent-skills repo has no LICENSE file of
    its own; its README states MIT).
