# Vendored: graphify
- Source: https://pypi.org/project/graphifyy/0.9.71/ (wheel graphifyy-0.9.71-py3-none-any.whl, sha256 1400ac3a9b3b50577d450443e5ecad630e53f69875ab7150440acfad381b4f5f), paths graphify/skill-agents.md (→ SKILL.md), graphify/skills/agents/references/*.md (8 files), graphifyy-0.9.71.dist-info/licenses/LICENSE (→ LICENSE), graphifyy-0.9.71.dist-info/licenses/NOTICE (→ NOTICE), graphifyy-0.9.71.dist-info/licenses/LICENSE-MIT (→ LICENSE-MIT, retained because NOTICE references it)
- License: Apache-2.0 (see LICENSE). Copyright: Safi Shamsi and the Graphify contributors.
- Vendored on: 2026-09-29
- Changes:
  - Added the "Inside Swarm" prelude directly after the frontmatter's
    closing `---`: it pins the Swarm build command
    (`graphify extract . --code-only`), forbids the `/graphify` full
    pipeline, semantic extraction and subagent dispatch, lists the query
    commands, and names the four references Swarm doesn't use.
  - Upstream frontmatter `name:` was already `graphify`, so no frontmatter
    change was needed.
  - Everything else is byte-verbatim upstream.
