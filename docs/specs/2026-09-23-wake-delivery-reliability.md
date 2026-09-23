# 2026-09-23 — Wake delivery reliability

Two independent defects in the wake path, found from one live incident
(`full-go-api-migration-orchestrator-2`, 2026-09-23T19:08:51Z), plus one
related finding flagged for follow-up.

- **Bug 1 (primary).** Native wake is permanently disabled after a session's
  first-ever wake, so claude/codex/agy fall through to the tmux paste path for
  every message batch from the second one onward.
- **Bug 2.** That paste path silently truncates any notice over ~1022 bytes
  when the target is Claude Code, keeping only the final `read()` worth of
  bytes and dropping everything before it.
- **Bug 3 (in scope, cheap).** `Claude.Wake` reports `delivered=true` whenever
  `PublishWake` does not error, even when nothing is subscribed to that
  session — a fictional success that makes Bug 1 worse.

Affected repo: `agent-swarm`. Worktree `../agent-swarm--paste-truncation-fix`,
branch `fix/wake-delivery-reliability`. No collisions expected: the only files
touched are `internal/runtime/wake.go`, `internal/spawn/tmux.go`,
`internal/adapter/{adapter,claude}.go`, `cmd/swarm/daemon.go` and their tests.

## Context

### Bug 1 — native wake starves after the first wake

`wake.go` intends "native wake, once per message batch" (the comment at the
top of step 1). It actually does "once per session, ever":

- `wakeCandidates` sets `r.NativeTried = true` whenever `sessions.last_wake_at`
  is non-NULL.
- `last_wake_at` is written in exactly one place, `markWoken`, which is called
  after **either** a successful native wake **or** a successful paste, and is
  never cleared anywhere in the codebase.

So the first wake of a session's life sets `last_wake_at` forever, and every
later batch sees `NativeTried == true` and skips straight to the paste
fallback. The incident session had many prior turns, so by the time of the
truncated delivery native wake was structurally never being attempted; paste
was the only path being tried, which is how it reached Bug 2 at all.

### Bug 2 — what actually truncates the paste

Three earlier theories were tested and **refuted**:

1. **MAX_CANON (1024-byte canonical line limit).** A `cat`-based probe broke
   around n≈1030 bytes, which looked conclusive. It is an artifact of `cat`
   running in *canonical* mode. Claude Code, cursor and muse all run their tty
   in raw mode; a raw-mode reader received 6001 bytes through the exact
   `load-buffer`/`paste-buffer` path with zero loss.
2. **A race between `paste-buffer` and `send-keys Enter`.** Impossible:
   both write to the same pty master in sequence and a pty is a FIFO, so
   Enter cannot overtake the payload. Every probe confirmed the `\r` arrives
   last.
3. **Kernel pty input-queue overflow-with-flush.** Refuted directly: a raw
   reader stalled for 3 s and then draining 64 bytes at a time received the
   full payload with the prefix intact. tmux writes through libevent and
   backpressures on `EAGAIN`; it does not overrun the queue.

The real mechanism, proven by instrumenting each `os.read()` on the slave:

```
plain single paste of the 1716-byte notice   -> reads = [1022, 694]
paced 512-byte chunks of the same notice     -> reads = [512, 512, 512, 180]
```

On Darwin, `ptcwrite` blocks the master writer once the slave's raw queue
reaches `TTYHOG - 2`, so the *first* `read()` of any burst larger than that
returns exactly 1022 bytes and the remainder arrives in a second read.
**Claude Code's raw-mode input handler keeps only the last `read()` and
discards the earlier ones.** The arithmetic matches the live incident exactly:
1716 − 1022 = 694, and 694 is exactly what Claude Code recorded in its
transcript, starting mid-word.

Ground truth, all from the agents' own stores rather than the screen:

| variant | payload | what the agent actually recorded |
|---|---|---|
| plain, multi-line, Claude Code | 1716 B | 694 B, starts mid-word (`ler filler … ITEM2END`) |
| plain, single-line, Claude Code | 1716 B | 694 B, identical break point — newlines are irrelevant |
| `paste-buffer -p` (bracketed), Claude Code | 1716 B | 1774 B, full but wrapped in `<pasted_content>` |
| **paced 512 B chunks, Claude Code** | 1716 B | **1716 B, intact, one typed prompt starting `[swarm]`** |
| plain, cursor-agent | 1716 B | 1716 B intact in `pasted_text.json`, but `hasConversation:false` — **Enter was swallowed** |
| **paced 512 B chunks, cursor-agent** | 1716 B | **1716 B intact and submitted (`hasConversation:true`)** |

Chunking fixes the truncation. Two further findings came out of the same runs:

- **Claude Code will not submit a multi-line paste at all.** `paste-buffer`
  turns every `\n` into a carriage return. With the content chunked and
  therefore fully delivered, the complete notice still sat unsent in Claude
  Code's input box through settle windows of 0.05 s, 0.5 s and 1.0 s — 0 typed
  prompts in the transcript each time. The identical notice flattened to one
  line was submitted by the first Enter. So `PasteLine` must actually paste one
  line, which is what its name has always claimed.
- **cursor-agent ate the Enter that followed an unpaced paste.** Its chat store
  showed the bytes intact but `hasConversation:false`; the notice never
  reached the model. A 50 ms gap before Enter was enough to fix it. Hence one
  settle before Enter on every paste, single-chunk ones included.

### Bug 3 — fictional native success

`Claude.Wake` returns `(true, nil)` as soon as `Deps.PublishWake` does not
error. `Store.PublishWake` fans out to whatever channels `SubscribeWake` handed
out for that session and returns `nil` even when there are none. So a claude
session whose mcpshim bridge is not connected reports a delivered native wake,
sets `last_wake_at`, and gets nothing at all until the next tick's cooldown
expires.

## Locked decisions

1. **Chunked, paced plain paste — not `paste-buffer -p`.** Bracketed paste
   delivers the bytes intact, but Claude Code wraps the result in
   `<pasted_content id=…>`, which (a) makes the model treat the notice as
   untrusted pasted material it declines to act on — observed verbatim in the
   probe — and (b) breaks `runtime.IsDaemonPrompt`: after `TrimSpace` the
   prompt starts `<pasted_content…`, not `[swarm]`, and `Inbox()` contains
   `inboxTrailer`, not `ShortPreamble`, so neither branch matches and the
   `UserPromptSubmit` hook re-injects the whole notice a second time. Chunked
   plain paste arrives as ordinary typed text beginning `[swarm]`, so
   `IsDaemonPrompt` keeps working unchanged.
2. **512-byte chunks.** Half of the proven 1022-byte first-read ceiling.
   `pasteChunkSize` and `pasteChunkGap` are package vars, i.e. calibration
   knobs — the 1022 figure is a kernel constant on this platform, not a
   portable one.
3. **Chunks are cut on rune boundaries.** `inboxTrailer` contains `—` (3
   bytes) and so does the `(+N more pending —` tail. Ink-style TUIs decode
   each stdin chunk as UTF-8 independently, so a mid-rune split would inject
   U+FFFD.
4. **`PasteLine` flattens its argument to one line.** Non-negotiable: Claude
   Code left a delivered multi-line notice unsent through settle windows up to
   1 s. Flattening costs only the visual line breaks between header, items and
   trailer.
5. **One settle before Enter, unconditionally** — including for a single-chunk
   paste (`IdleToken`, `ControlNotice`), which is what fixed cursor's
   swallowed Enter.
6. **The pacing is not a band-aid here.** A fixed delay *before Enter* was
   explicitly rejected (it cannot help: the bytes are already gone by then).
   The pacing *between chunks* is the mechanism: it keeps every slave `read()`
   under the ceiling. It is bounded and proportional to the payload, and the
   whole paste path is already best-effort.
7. **`NativeTried` is derived, not stored.** No new column. It becomes the
   exact mirror of the existing cooldown escape clause, so the two can never
   disagree.
8. **`PublishWake` reports subscriber count.** Its `Deps` signature becomes
   `func(ctx, sessionID, notice string) (bool, error)`.

## API / type changes

```go
// internal/adapter/adapter.go — Deps
PublishWake func(ctx context.Context, sessionID, notice string) (bool, error)

// internal/runtime/wake.go
func (s *Store) PublishWake(ctx context.Context, sessionID, notice string) (bool, error)

// internal/spawn/tmux.go — calibration knobs
var pasteChunkSize = 512                    // half the measured 1022 B first-read ceiling
var pasteChunkGap  = 50 * time.Millisecond  // between chunks
var pasteSettle    = 500 * time.Millisecond // once before Enter, always

func chunkRunes(s string, max int) []string // rune-safe split
```

`PasteLine` flattens whitespace runs to single spaces before pasting
(`strings.Join(strings.Fields(line), " ")`), because tmux turns every `\n`
into a carriage return and Claude Code will not submit a multi-line paste.

`wakeCandidates`' `NativeTried` assignment moves after `NewestPendingAt` is
populated and becomes:

```go
r.NativeTried = !r.NewestPendingAt.After(*r.LastWakeAt)
```

No DB model change, no migration, no user-facing copy change.

## File list

Changed:
- `internal/runtime/wake.go` — `NativeTried` derivation; `PublishWake` returns delivered.
- `internal/runtime/wake_test.go` — native-per-batch regression test.
- `internal/spawn/tmux.go` — chunked, paced `pasteViaTempFile`.
- `internal/spawn/tmux_test.go` — read-size regression test against real tmux.
- `internal/adapter/adapter.go` — `Deps.PublishWake` signature.
- `internal/adapter/claude.go` — `Wake` returns the real delivered flag.
- `internal/adapter/claude_test.go` — fake updated; no-subscriber case.
- `cmd/swarm/daemon.go` — wiring.
- `internal/httpapi/agentio_test.go` — call site.

Reused unchanged: `runtime.Inbox`, `sanitizeOneLine`, `IsDaemonPrompt`, the
cooldown logic in `WakeDue`, `execx.Runner`, `TestPasteLineDeliversExactlyOneLine`
(now covers the single-chunk path).

Deleted: nothing.

### End-to-end validation of the shipped code

Not a hand-rolled shell approximation: a throwaway driver called the real
`spawn.PasteLine` (as built on this branch) against real panes, with the
notice produced by the real `runtime.Inbox`.

- **Claude Code** — 1496-byte notice with 4 newlines. Transcript: exactly one
  typed prompt, `LEN=1491`, byte-for-byte equal to the flattened notice,
  starting `[swarm] Durable runtime events for probe-agent (T-9)…`.
- **cursor-agent** — `pasted_text.json` entry `len=1491`, byte-for-byte equal
  to the flattened notice, and `meta.json` `hasConversation:true` (i.e. it was
  actually submitted, which the pre-fix run was not).
- **muse** — `~/.local/share/muse/sessions/2026/09/23/01a0cfd1…/session.jsonl`,
  event `runtime.user_intent.accepted`, `payload.model_messages[0].content[0].text`
  `LEN=1491`, byte-for-byte equal. The agent then reasoned about `msg_00/01/02`
  by id, so the content reached the model, not just the input box.

All three kinds therefore verified against their own durable stores, with the
notice produced by the real `runtime.Inbox` and pasted by the real
`spawn.PasteLine`.

### Does claude still reach the paste lane?

After these fixes claude pastes in exactly two situations, and the second is a
policy call the user should make rather than something this branch changed:

1. **`PublishWake` has no subscriber** — the mcpshim bridge is not connected,
   so there is no native channel at all and paste is the only route left.
   Before Bug 3 was fixed this case was invisible (it claimed success and then
   pasted anyway a tick later); now it is honest and immediate.
2. **I11: native was delivered but no sync followed within `pasteRetry` (30 s)**
   — `WakeDue`'s "a sync must follow; if it does not, the next pass pastes".
   This is a deliberate §11.3 escalation pinned by
   `TestNativeWakeWithoutASyncFallsBackToThePaste`, so it was **not** changed
   here. If claude should never be pasted at while its bridge is live, the
   change is to retry native instead of escalating for kinds whose `Wake`
   confirmed a subscriber — a spec amendment plus a rewrite of that test, not
   a silent tweak.

## Verification

```
go test ./internal/spawn/... ./internal/runtime/... ./internal/adapter/... ./internal/httpapi/...
go build ./...
```

End-to-end scenarios:
1. New message batch after a prior wake → native wake attempted again (not paste).
2. Same batch, no new message, within cooldown → no repeat wake.
3. Same batch, cooldown expired → paste fallback, native not retried.
4. Native publish with no subscriber → `delivered=false` → paste path runs.
5. `PasteLine` with a ~1700-byte notice → every slave `read()` < 1022 bytes and
   the total equals the payload.
6. `PasteLine` with a short notice → one chunk, still exactly one line, Enter sent.

## Related finding — flagged, not fixed

`Codex.Wake` has the same fictional-success shape: `codex queue` exiting 0 means
the CLI accepted the message, not that the thread is live and will surface it.
`Agy.Wake` at least streams into a real subprocess. Neither was changed here.

Separately: the claim that "cursor and muse always deliver the full paste
content even when the terminal collapses it to `[Pasted text #N lines]`" was
**half right and half wrong**, and this spec is the first ground-truth check of
it. Cursor's own `~/.cursor/chats/<id>/<uuid>/pasted_text.json` confirms the
bytes arrive intact — but `meta.json` showed `hasConversation:false`, i.e. the
content was never submitted at all. Muse was **not** verified against its own
store (`~/.local/share/muse/sessions/**/session.jsonl`) and still needs the
same check before anyone relies on its delivery.

## Explicitly out of scope

- Any change to `Inbox()` / notice content or length.
- `Codex.Wake` / `Agy.Wake` delivery confirmation.
- A muse ground-truth probe.
- Making the paste path non-best-effort (it stays fire-and-forget).
