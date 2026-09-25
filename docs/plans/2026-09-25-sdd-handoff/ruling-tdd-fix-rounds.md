# Ruling: tdd gate on fix rounds (controller, 2026-09-24)

Problem: a fix round is a new attempt (Retry bumps attempt), and B5's tdd gate reads "across this attempt's entries", per unit. Read literally, every unit needs a fresh red→green in each fix round, including units whose code is correct and unchanged — impossible to satisfy honestly.

Ruling (spec B5 amended):
- Attempt 1 of a build step (round 1): unchanged — red-before-green per unit (or once for a non-batched task).
- Fix-round attempts (the builder retried with findings, round > 1): red-before-green is required only for the units named by unit-tagged findings in that round's fix brief. If any finding has no unit (package-wide), at least one red-before-green pair (any unit or untagged) is required in the attempt. Units not named by any finding need no new tdd evidence in that attempt (the `verify` gate still requires every declared verify command green, so unchanged units are still covered by the suite).
- A finding that is not testable behaviour (wording, comments, docs) still counts; the coder states in the red entry's note why the red is a new or updated test, or — if no test can express it — writes the red as the failing check it used (e.g. a grep/lint command) with a note. No fabricated reds.
- Error copy stays "the error names the units missing evidence", listing only the required units.
Why: keeps TDD honest for every change the reviewer asked for, without forcing fake reds on untouched units.
Cost if wrong: a fix round could change code in an unnamed unit without a red; the reviewer's next round and the verify gate are the backstop.
