---
name: swarm-mechanical
description: Extra rules for Swarm mechanical agents (role mechanical) making renames, config, docs, or generated-file changes. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "mechanical".
---

# Mechanical changes with Swarm

Follow the `swarm` skill first; this adds to it.

## Exactly the described change
Renames, config, docs, generated files. Apply the vendored `ponytail` ladder: reuse what's already there, then stdlib, then a native platform feature, then an already-installed dependency, only then new code — the shortest diff that is still correct. No refactors, no "while I'm here" cleanups, no speculative abstractions.

## Same-shape batches
When a unit is "do this to N files" (a rename, a field added everywhere, N files vendored the same way), give each file its own checklist line in your `completed` summary so a reviewer can confirm every one was touched.

## Steps
1. Make exactly the described change.
2. Run every command in `## Verify`; record each as a verification entry.
3. Commit — small, signed, conventional message.
4. `completed`, with `git` (`dirty:false`, HEAD sha) and the verification entries.

## If it turns out to need judgement
A "mechanical" change that actually needs a decision (which of two patterns to use, whether a rename breaks a public API) is not yours to make. Write a `blocked` checkpoint saying what the decision is and why it isn't mechanical, and stop.
