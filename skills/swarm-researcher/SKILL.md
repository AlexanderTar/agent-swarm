---
name: swarm-researcher
description: Extra rules for Swarm researcher agents (role researcher) answering one sub-question for a spike. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "researcher".
---

# Researching with Swarm

Follow the `swarm` skill first; this adds to it.

## Frame your sub-question first
Your brief carries one sub-question of a spike's research split. Before you search anything, run `superpowers:brainstorming`'s "understand the idea" phase on your own question — there's no user dialogue here; questions go to your parent, not the user:
- Restate the sub-question in your own words.
- List the angles and candidate answers you plan to test.
- Decide, concretely, what "answered" looks like: the fact, decision, or comparison that lets you stop.

## The research loop
Then run the deep-research loop: short, targeted searches; fetch full sources rather than trusting a snippet; prefer primary sources — the actual docs, the actual code, the actual spec — over summaries and secondhand write-ups. When two sources disagree, record the conflict instead of silently picking a winner. Budget roughly 10–15 tool calls. Stop when your question is answered, or when the budget runs dry — a dry budget with an open question becomes a Gap, not a reason to guess.

## Notes format
Write your notes to `~/.swarm/research/<SPIKE-KEY>/<ITEM-KEY>.md`, with these sections:
- `### Takeaway` — the answer, stated up front.
- `### Cited findings` — every claim links to its source: `[source](url)` for the web, `path:line` for code.
- `### Inferences` — reasoning you did beyond what a source states outright, marked as such.
- `### Gaps` — what you could not establish, and why.

Never invent a finding. An unsourced claim belongs under Gaps, never under Cited findings.

## Completing
Write `completed` with the notes path in `artifacts`. If your sub-question turns out to need an actual code change to answer, say so in your notes and stop — that decision belongs to the spike owner, not to you.
