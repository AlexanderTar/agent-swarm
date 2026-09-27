# Plan: quota-reset wake fix (A-E)

Companion to `docs/specs/2026-09-27-quota-reset-wake-fix.md`. Strict TDD per
task: failing test, run + watch fail, minimal fix, run + watch pass, commit.
Order: A, E, D (all in `wake.go`, one block), then C (`codex.go`), then B
(`daemon.go`) — matches advisor guidance: land the wake.go trio first since
they touch the same function, then the two isolated files.

## Task A — provider session id reaches `WakeTarget`

1. Edit `internal/runtime/wake_test.go`:
   `TestWakeOnQuotaResetResolvesAgyLaunchModel` — after creating the agy
   session, seed `UPDATE sessions SET provider_session_id = 'conv-agy-1'
   WHERE id = ?` (ses.ID), and assert
   `fa.LastWakeTarget.ProviderSessionID == "conv-agy-1"` alongside the
   existing `Model` assertion.
2. In the same test file, add
   `TestWakeOnQuotaResetResolvesCodexProviderSessionID`: `newStore`, register
   `s.Adapters[Codex] = fa`, seed a codex catalog row (reuse
   `seedCatalog(t, s, "codex", "codex-1", ...)` from `fallback_test.go`,
   same package), `UPDATE settings ... enabled_agents` to include `"codex"`,
   `StartSpike(Kind: Codex, Model: "gpt-6-astra")`, seed
   `provider_session_id = 'thread-codex-1'` on its session, call
   `WakeOnQuotaReset(ctx, Codex, cutoff)`, assert
   `fa.LastWakeTarget.ProviderSessionID == "thread-codex-1"`.
3. `go test ./internal/runtime/... -run TestWakeOnQuotaResetResolves -v` —
   watch both fail (`ProviderSessionID` is always `""`).
4. Fix `internal/runtime/wake.go`'s `WakeOnQuotaReset`: add
   `COALESCE(ses.provider_session_id, '')` to the `SELECT`, scan into a new
   `providerID string`, add `ProviderSessionID: providerID` to the
   `adapter.WakeTarget{...}` literal.
5. Re-run the same `go test` — watch pass.
6. Commit: `fix(wake): quota-reset wake carries the provider session id`.

## Task E — reject empty provider id; log `Wake` errors

1. `internal/adapter/codex_test.go`: add
   `TestCodexWakeRejectsEmptyProviderSessionID` — `d.RunEnv` set to fail the
   test if called; `Wake(ctx, WakeTarget{SessionID: "s", AgentID: "a"})`
   (no `ProviderSessionID`); assert `ok == false` and `err != nil`.
2. `internal/adapter/agy_test.go`: add
   `TestAgyWakeRejectsEmptyProviderSessionID` — `d.StartEnv` set to fail the
   test if called; `Wake(ctx, WakeTarget{SessionID: "s"})` (no
   `ProviderSessionID`); assert `ok == false` and `err != nil`.
3. `go test ./internal/adapter/... -run RejectsEmptyProviderSessionID -v` —
   watch both fail (today both call `RunEnv`/`StartEnv` regardless).
4. Fix: in `Codex.Wake` (`codex.go:307`) and `Agy.Wake` (`agy.go:404`), add
   as the first statement:
   ```go
   if w.ProviderSessionID == "" {
       return false, fmt.Errorf("<kind>: wake for agent %s has no provider session id", w.AgentID)
   }
   ```
   (agy.go already imports `fmt`? check; codex.go does.)
5. Re-run — watch pass.
6. `internal/runtime/wake_test.go`: add
   `TestWakeOnQuotaResetLogsWakeError` — agy/codex fake adapter with
   `WakeOK=false` and an injected error (extend `adapter.Fake.Wake` with a
   `WakeErr error` field, returned alongside `WakeOK`); call
   `WakeOnQuotaReset`; assert the store's log sink recorded a line
   mentioning "quota-reset" and the agent name (use the existing
   `s.Log`/log-capture pattern other wake tests use, e.g.
   `TestWakeOnQuotaResetLogsSkipWhenPaneNotIdle`).
7. Run — watch fail (today the error is discarded, no log line).
8. Fix `wake.go`: change `delivered, _ := ad.Wake(...)` to
   `delivered, err := ad.Wake(...)`; if `err != nil`,
   `s.logf("wake: quota-reset native wake for %s: %v", agentName, err)`.
9. Re-run — watch pass.
10. Commit: `fix(wake): reject an empty provider session id and log wake failures`.

## Task D — paste fallback carries the real notice

1. `internal/runtime/wake_test.go`: change `TestWakeOnQuotaReset`'s
   assertion from
   `strings.HasSuffix(tm.pasted[0], "|"+IdleToken)` to
   `strings.Contains(tm.pasted[0], "Quota reset window passed")`.
2. Run — watch fail (current code pastes `IdleToken` verbatim).
3. Fix `wake.go`: `s.Tmux.PasteLine(ctx, tmuxName, IdleToken)` →
   `s.Tmux.PasteLine(ctx, tmuxName, notice)`.
4. Re-run — watch pass. Also add one assertion for the open-requests-reminder
   path if a cheap seam exists (skip if it needs a full request fixture —
   `resurfaceOpenRequests` already has its own coverage; this task only
   needs to prove the paste carries `notice`, not `IdleToken`).
5. `grep -rn "IdleToken" --include='*.go' --include='*.sh' internal/ cmd/ scripts/`
   — confirm no e2e scenario or other test still expects the bare token from
   this path; port any that do.
6. Commit: `fix(wake): quota-reset paste fallback carries the reset notice, not the idle token`.

## Task C — codex rate-limit picker + bottom-anchored idle

1. Add fixture `internal/adapter/testdata/codex/pane-dialog-rate-limit.txt`
   (synthesized, per spec): an old bare `›` prompt line near the top
   (stale-scrollback stand-in), then the picker:
   ```
   ›

     You've hit your usage limit for GPT-6-Astra.

     Switch to GPT-6-Astra Mini for lower credit usage?

   › 1. Switch to GPT-6-Astra Mini
     2. Keep current model

     Use ↑/↓ to move, press enter to confirm
   ```
2. `internal/adapter/codex_test.go`: add
   `TestCodexPromptPatternsAnswersRateLimitPickerWithKeepCurrentModel` —
   load the fixture, find the `PromptPatterns()` entry matching it, assert
   `Require` also matches and `Action == "Down+Enter"`; assert
   `codexRetire` does NOT match this fixture (no cross-firing) and the new
   pattern does NOT match `pane-dialog-model-retirement.txt` or
   `pane-idle.txt`/`pane-idle-ansi.txt`.
3. Add `TestCodexIdleIgnoresStaleScrollbackAboveTheRateLimitDialog` — assert
   `!newCodex(testDeps(t)).Idle(pane(t, "codex", "pane-dialog-rate-limit.txt"))`
   (today: true, because the top-of-capture bare `›` matches anywhere).
4. Run both — watch fail (no such `PromptPatterns` entry; `Idle` reports
   true).
5. Fix `internal/adapter/codex.go`:
   - add `codexRateLimit = regexp.MustCompile(`Switch to \S+ for lower credit usage\?`)`
     and `codexKeepCurrentModel = regexp.MustCompile(`Keep current model`)`.
   - append to `PromptPatterns()`:
     `{Match: codexRateLimit, Require: codexKeepCurrentModel, Title: "Keep current model on rate limit", Action: "Down+Enter"}`.
   - add a small helper (package `adapter`, usable by any future kind but
     wired only into codex per spec scope):
     ```go
     // lastNonBlankLines keeps the tail of capture: trailing lines whose
     // ANSI-stripped content is blank are dropped, then at most n raw lines
     // remain. Anchors a line-anywhere idle regex to what the pane actually
     // shows now, not stale scrollback further up (root cause C).
     func lastNonBlankLines(capture string, n int) string {
         lines := strings.Split(capture, "\n")
         for len(lines) > 0 && strings.TrimSpace(StripANSI(lines[len(lines)-1])) == "" {
             lines = lines[:len(lines)-1]
         }
         if len(lines) > n {
             lines = lines[len(lines)-n:]
         }
         return strings.Join(lines, "\n")
     }
     ```
   - change `func (c *Codex) Idle(capture string) bool { return idle(c, capture) }`
     to `return idle(c, lastNonBlankLines(capture, 3))`.
6. Re-run — watch pass; also re-run
   `go test ./internal/adapter/... -run TestCodexIdleUsesTheDimAttribute -v`
   to confirm no regression (existing 3-case fixture set).
7. Commit: `fix(codex): auto-answer the credit-usage rate-limit picker and anchor idle to the pane's tail`.

## Task B — remember every `ResetsAt` seen, not just the latest

1. New file `cmd/swarm/quota_reset_test.go`:
   `TestDueResetsFiresOnAnOldCutoffAfterTheWindowRolledOver` — build
   `seen := map[string]map[int64]struct{}{}` (or via a constructor if one is
   added), call `dueResets(seen, []usagesvc.Snapshot{...ResetsAt: T...}, T-5m)`
   (before the window, expect no fire, but T gets remembered), then call
   `dueResets(seen, []usagesvc.Snapshot{...ResetsAt: T+5h...}, T+3m)` (the
   live value has already rolled over) and assert the returned fires include
   `{Kind: Codex, Cutoff: T}` — proving detection survives the value moving
   on. Add a second case: calling again at `T+90m` (past the 1h window)
   returns no fire for `T` and the internal set no longer holds it (prune).
2. `go test ./cmd/swarm/... -run TestDueResets -v` — watch fail (function
   doesn't exist / wrong behavior).
3. Implement in `cmd/swarm/daemon.go`:
   ```go
   type quotaResetFire struct {
       Kind   runtime.AgentKind
       Cutoff time.Time
   }

   // dueResets records every meter ResetsAt ever observed (keyed by kind+meter
   // id) in seen, then returns every remembered cutoff currently inside the
   // (now-1h, now-1m] detection window, pruning anything older than that.
   // Pure and DB-free by design (root cause B, spec decision 2): a live
   // snapshot's ResetsAt can roll to the NEXT window before checkQuotaResets
   // ever ticks with the old one in view (Codex's 5h window restarts on
   // first use after reset), so reading only the latest value drops the
   // reset. seen is owned by one long-lived quotaResetLoop goroutine; no
   // locking needed.
   func dueResets(seen map[string]map[int64]bool, snaps []usagesvc.Snapshot, now time.Time) []quotaResetFire {
       for _, snap := range snaps {
           for _, m := range snap.Meters {
               if m.ResetsAt == nil {
                   continue
               }
               key := string(snap.Agent) + "|" + m.ID
               if seen[key] == nil {
                   seen[key] = map[int64]bool{}
               }
               seen[key][m.ResetsAt.UnixMilli()] = true
           }
       }
       var fires []quotaResetFire
       for key, times := range seen {
           kind, _, _ := strings.Cut(key, "|")
           for ms := range times {
               cutoff := time.UnixMilli(ms)
               switch {
               case now.Sub(cutoff) >= time.Hour:
                   delete(times, ms)
               case now.After(cutoff.Add(time.Minute)):
                   fires = append(fires, quotaResetFire{Kind: runtime.AgentKind(kind), Cutoff: cutoff})
               }
           }
       }
       return fires
   }
   ```
   Rewire `checkQuotaResets`/`quotaResetLoop` to own a `seen` map and call
   `dueResets`, then `rt.WakeOnQuotaReset(ctx, fire.Kind, fire.Cutoff)` per
   fire (log same as before).
4. Re-run — watch pass.
5. Commit: `fix(daemon): remember every quota-reset cutoff seen, not just the latest snapshot`.

## Final verification (after all 5 commits)

```
go build ./...
go vet ./...
gofmt -l .
go test ./... -count=1 -timeout 40m
make e2e
make skills-sync clean
```
Run the full-suite and `make e2e` commands with an explicit generous
foreground timeout; do not block the turn on a background monitor.
