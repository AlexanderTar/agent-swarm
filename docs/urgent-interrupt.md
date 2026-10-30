# When Swarm interrupts a worker

Mail normally reaches a busy worker at a tool boundary (the hook notice, at most once every 60 s) or after its turn ends (wake and paste). Inside one long tool call, such as a 10-minute build, nothing arrives. Swarm cancels a running turn in exactly one case: a pending **pause or handoff** request for a worker that is busy and silent.

## What is urgent

`runtime.IsUrgent` (internal/runtime/inbox.go): a `control` message whose payload action is `pause` or `handoff`, created only by the daemon (`requestPreservationTx`, the subtree pause). It is stored with `priority = 0`; every other message is `1`. No agent-facing tool can send urgent mail: `swarm_send` accepts question, answer and finding only, and `enqueueRaw` derives priority from `IsUrgent`, not from the caller. Never add a way to mark an ordinary message urgent. Findings, answers, assignment updates and relays wait for the next tool gap like before.

## When the interrupt fires

`interruptForUrgent` (internal/runtime/urgent.go), called per candidate session from `WakeDue` every 5 s. All of these must hold:

1. An urgent message is pending and older than `urgentGrace` (30 s).
2. The session's `last_seen_at` is older than `urgentGrace`: a worker whose hooks still fire gets the notice at its next tool gap and needs no interrupt.
3. The worker is busy by the shared signal `Store.BusySince` (internal/runtime/busy.go: an open PreToolUse, the grok leader's "working" push, or, for any kind with neither tool nor leader evidence, the adapter's `Busy()` footer on the pane as sampled by the reconcile tick). The pane fallback is not limited to hookless kinds: a claude or codex worker streaming a long reply with no open tool call and silent hooks reads busy, so a pending pause or handoff can interrupt it (before, that took an open PreToolUse). That is intended. There is no second busy signal; the 60-minute busy ceiling does not apply, because a hung tool is exactly when to interrupt.
4. Nobody is being waited on: no open `question` or `prompt` row for the session (`prompt` is a terminal permission dialog, opened by `OpenDialogPrompt`; Escape would reject the permission the person is being asked for), and for grok no card on screen (`PasteReady`). A question dialog is dismissed by the pause path (`dismissQuestionDialog`), not here.
5. The pane runs the agent's process and the turn is not over (`Store.SessionIdle`: a hook close, otherwise the pane), read from a fresh capture right before the keys go out. Every kind whose `InterruptKeys` contain `C-c` (grok, cursor, muse) is the exception: `C-c` at the idle prompt arms quit, so an idle pane of one of those kinds is never interrupted even when a paste has opened the hook turn.

Then it sends the adapter's `InterruptKeys()` once, logs `wake: interrupted <name> …`, and raises the `agent.urgent_interrupt` notification (info level, shown on the board). The wake loop delivers the message as the next prompt as usual (native wake, then the control paste after 5 s). The message id is remembered in `Store.urgentInterrupted`, so one message causes one interrupt; `pruneUrgentInterrupted` (called from `WakeDue`) drops the entry once the message is no longer pending.

## Per kind

| Kind | Keys | Why |
| --- | --- | --- |
| claude, codex | `Escape` | the adapters' existing `InterruptKeys` |
| agy | `Escape` | its `InterruptKeys`, the same keys the pause deadline uses |
| cursor, muse | one `C-c` | their `InterruptKeys`, the same keys the pause deadline uses; not separately probed for a double `C-c`, so the same once-only and idle-prompt guards apply |
| grok | one `C-c` | probe TASK-1126 (`~/.swarm/research/SPIKE-52/TASK-1126.md`): `C-c` mid-turn prints "Turn cancelled by user" and returns to idle; `C-c` at idle arms quit and a second one **exits grok** |

For grok never send `C-c` twice, and for every `C-c` kind (grok, cursor, muse) never to an idle pane. That is why the idle-prompt check (step 5) runs right before the keys and why the once-per-message memory exists.

**Accepted risk:** the once-only memory is in process, not persisted. After a daemon restart, a worker that already got the interrupt and is now running its handoff turn (busy, hooks quiet in one long command) can be interrupted a second time, which cancels that preservation turn. The idle-prompt check protects grok from quitting, not the turn from being cancelled. Persist the mark (a column or event per message id) if this bites. The pause deadline's own `interrupt` (`TickPause`) is a separate, later step with its own kill timer.

ACP `session/cancel` through the grok leader would avoid keystrokes but is **not probed**; do not switch to it without a recorded live probe. Grok steer mode (`follow_up_behavior = "steer"`) already lets a pasted message join a running turn at the next tool gap, so the interrupt only matters inside one long tool call.

## Orchestrators end only on their own final note

Root Done never ends an orchestrator (`OnRootDone`, internal/runtime/finish.go; spec docs/specs/2026-09-26-root-finish-and-cancel-cascade.md decision 1, as amended 2026-10-30): no daemon `completed`, no operation cancel, so the 60 s idle kill and the 2 h busy cap never arm; a paused or interrupted orchestrator stays resumable. The orchestrator closes itself by writing `completed` once its root is Done (accepted only then; the approved-finish refusal still applies before Done), and that checkpoint ends the session through the normal grace + idle path. An idle orchestrator on a Done root with no final note gets a daemon `assignment_update` nudge (`nudgeFinalNote`, internal/runtime/finalnote.go): first after 5 min idle, then after 15 min, 30 min, then hourly; never more than one pending; one `agent.final_note` attention notification on the first nudge; never a kill, never a wake.go change. Never add a kill path for orchestrators on Done roots. Guarded by `go test ./internal/runtime/ -run 'TestFinalNoteNudge|TestRootDone|TestOrchestratorCompleted'`.

## Tests

`go test ./internal/runtime -run 'Urgent|Ordinary'` covers urgency marking, the grace, the hook-silence rule, the once-only rule, the idle, question, permission-dialog and ordinary-mail exclusions, the pause notice being pasted after the interrupt, pruning of the memory, the per-kind keys (including the idle-prompt exclusion for every `C-c` kind: grok, cursor, muse), and an interrupt when the hook says the turn is open on an idle-looking prompt (`TestUrgentInterruptFiresWhenTheHookSaysTheTurnIsOpen`). To check a live worker, queue a pause for an agent running `sleep 120` and watch `~/.swarm/logs/daemon.err.log` for `wake: interrupted`.
