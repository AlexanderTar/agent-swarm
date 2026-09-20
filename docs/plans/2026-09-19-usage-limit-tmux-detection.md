# Implementation plan: tmux-detected usage-limit hit → immediate fallback eligibility

Companion to `docs/specs/2026-09-19-usage-limit-tmux-detection.md`. Read that
spec in full first — this plan assumes its Locked Decisions. **Not executed
by this pass** (research/design task); for whoever picks this up next.

Every task below is TDD-ordered: write the failing test, run it, watch it
fail for the right reason, write the minimal implementation, run it again,
confirm green, commit. Run `go build ./... && go vet ./... && gofmt -l .`
before every commit.

## Task 0 — Capture a real pane (blocking; not code)

**This is not skippable.** The spec's "Evidence" section establishes exact
*message text* from real JSONL transcripts but explicitly does not confirm
*terminal rendering*. Everything from Task 2 onward depends on a real
fixture, not an assumed one.

1. Start (or find) a Claude Code session that is actually near a usage
   limit — cheapest real path: a Claude Max/Pro account late in its 5-hour
   window, or deliberately run a token-heavy loop against a low-limit plan.
   Alternatively, watch for the next time this happens organically (the
   grep in the spec's Evidence section can be re-run periodically against
   `~/.claude/projects/*/*.jsonl` — a fresh hit is a signal to capture it
   live within the next few minutes, since the 5-hour variant self-heals
   and may not sit on screen long, though the weekly/org variants should
   persist).
2. The moment the pane shows `You've hit your weekly limit` or `You've hit
   your org's monthly spend limit` (the two variants this feature reacts
   to — the plain session-limit one is lower priority to capture since it's
   never acted on, but capture it too if convenient), run:
   ```
   tmux capture-pane -e -p -t <pane-name> > pane-usage-limit-weekly.txt
   ```
   (`-e` preserves the escape sequences `StripANSI` needs to be tested
   against, matching how `pane-busy-ansi.txt` was captured for the sibling
   ANSI-busy-spinner fix.)
3. Save as `internal/adapter/testdata/claude/pane-usage-limit-weekly.txt`
   and, if captured, `pane-usage-limit-org-spend.txt` /
   `pane-usage-limit-session.txt`.
4. **If a real capture cannot be arranged, stop here.** Do not proceed to
   Task 1 against a constructed/guessed fixture — that is exactly the
   "fabricated regex against a fabricated fixture" failure mode the
   original research task for this spec explicitly warned against, and
   what the sibling `no-stalled-work` spec's own item 2 shows the cost of.
   A confirmed message string without confirmed rendering is not enough to
   safely write `StripANSI`-dependent matching code against; wait for a
   real occurrence (or deliberately induce one) rather than guess.

## Task 1 — `adapter.UsageLimitKind` / `UsageLimitDetector`

Files: `internal/adapter/adapter.go`, `internal/adapter/claude.go`,
`internal/adapter/claude_test.go`.

1. Failing test first, in `claude_test.go` (reuses the existing `pane(t,
   "claude", name)` helper from the shared test file):
   ```go
   func TestClaudeUsageLimitHit(t *testing.T) {
       d := testDeps(t)
       a := newClaude(d)
       cases := []struct {
           file string
           want UsageLimitKind
       }{
           {"pane-usage-limit-weekly.txt", UsageLimitWeekly},
           {"pane-usage-limit-org-spend.txt", UsageLimitOrgSpend},
           {"pane-usage-limit-session.txt", UsageLimitSession},
           {"pane-idle.txt", UsageLimitNone},
           {"pane-busy.txt", UsageLimitNone},
           {"pane-dialog-trust.txt", UsageLimitNone},
       }
       for _, c := range cases {
           t.Run(c.file, func(t *testing.T) {
               got := a.UsageLimitHit(pane(t, "claude", c.file))
               if got != c.want {
                   t.Fatalf("UsageLimitHit(%s) = %q, want %q", c.file, got, c.want)
               }
           })
       }
   }
   ```
   Run `go test ./internal/adapter/...` — fails to compile (`UsageLimitKind`,
   `UsageLimitHit` don't exist yet). Expected failure.
2. Add to `internal/adapter/adapter.go` (near `Dialog`):
   ```go
   type UsageLimitKind string

   const (
       UsageLimitNone     UsageLimitKind = ""
       UsageLimitSession  UsageLimitKind = "session"
       UsageLimitWeekly   UsageLimitKind = "weekly"
       UsageLimitOrgSpend UsageLimitKind = "org_spend"
   )

   // UsageLimitDetector is implemented by an Adapter with real captured
   // evidence of its own CLI's usage-limit message shape (see
   // docs/specs/2026-09-19-usage-limit-tmux-detection.md). Optional: an
   // adapter that doesn't implement it is simply never checked.
   type UsageLimitDetector interface {
       UsageLimitHit(capture string) UsageLimitKind
   }
   ```
3. Add to `internal/adapter/claude.go`, alongside the existing
   `claudeTrust`/`claudeDev` regex block:
   ```go
   var (
       claudeWeeklyLimit   = regexp.MustCompile(`You've hit your weekly limit`)
       claudeOrgSpendLimit = regexp.MustCompile(`You've hit your org's monthly spend limit`)
       claudeSessionLimit  = regexp.MustCompile(`You've hit your session limit`)
   )

   // UsageLimitHit checks capture against Claude's three known usage-limit
   // message shapes (spec: "Evidence"). Order matters: the org-spend
   // message also contains the substring "session limit resets", so the
   // more specific checks run first.
   func (c *Claude) UsageLimitHit(capture string) UsageLimitKind {
       plain := StripANSI(capture)
       switch {
       case claudeWeeklyLimit.MatchString(plain):
           return UsageLimitWeekly
       case claudeOrgSpendLimit.MatchString(plain):
           return UsageLimitOrgSpend
       case claudeSessionLimit.MatchString(plain):
           return UsageLimitSession
       default:
           return UsageLimitNone
       }
   }
   ```
4. Run `go test ./internal/adapter/...` — green. Commit: "feat(adapter):
   detect Claude's usage-limit pane messages".

## Task 2 — `UsageReader.MarkExhausted` + `usagegate.Gate` override

Files: `internal/runtime/model.go`, `internal/usagegate/usagegate.go`,
`internal/usagegate/usagegate_test.go`, `internal/runtime/fallback_test.go`.

1. Failing test first, in `usagegate_test.go`:
   ```go
   func TestGateExhaustedAfterMarkExhausted(t *testing.T) {
       g := &Gate{Poller: newTestPoller(t)} // reuse whatever existing helper
                                             // usagegate_test.go already has
                                             // for a Poller with zero snapshots
       if g.Exhausted(context.Background(), runtime.Claude) {
           t.Fatal("unmarked kind should not be exhausted")
       }
       g.MarkExhausted(runtime.Claude)
       if !g.Exhausted(context.Background(), runtime.Claude) {
           t.Fatal("marked kind should be exhausted regardless of snapshot state")
       }
       if g.Exhausted(context.Background(), runtime.Codex) {
           t.Fatal("a different, unmarked kind must be unaffected")
       }
   }
   ```
   Run — fails to compile (`MarkExhausted` doesn't exist). Expected. Also
   add a TTL-expiry case in the same test file (see spec Locked Decision 5
   — the override is a bounded TTL, not permanent, after review found the
   indefinite version causes a swarm-wide Claude outage until daemon
   restart):
   ```go
   func TestGateMarkExhaustedExpires(t *testing.T) {
       now := time.Now()
       g := &Gate{Poller: newTestPoller(t), Now: func() time.Time { return now }}
       g.MarkExhausted(runtime.Claude)
       now = now.Add(forcedExhaustionTTL + time.Second)
       if g.Exhausted(context.Background(), runtime.Claude) {
           t.Fatal("override should have expired")
       }
   }
   ```
2. Add to `internal/runtime/model.go`'s `UsageReader` interface:
   ```go
   type UsageReader interface {
       Exhausted(ctx context.Context, kind AgentKind) bool
       MarkExhausted(kind AgentKind)
   }
   ```
3. Add to `internal/usagegate/usagegate.go`:
   ```go
   // forcedExhaustionTTL bounds how long a tmux-detected MarkExhausted call
   // forces Exhausted() true, independent of any snapshot. Not derived from
   // a confirmed reset-time format (spec Locked Decision 5) -- a stated,
   // tunable assumption, not evidence.
   const forcedExhaustionTTL = time.Hour

   type Gate struct {
       Poller *usage.Poller
       Now    func() time.Time

       overrideMu sync.Mutex
       override   map[runtime.AgentKind]time.Time // kind -> forced-until
   }

   func (g *Gate) MarkExhausted(kind runtime.AgentKind) {
       g.overrideMu.Lock()
       defer g.overrideMu.Unlock()
       if g.override == nil {
           g.override = map[runtime.AgentKind]time.Time{}
       }
       g.override[kind] = g.now().Add(forcedExhaustionTTL)
   }
   ```
   and at the top of the existing `(g *Gate) Exhausted` body:
   ```go
   func (g *Gate) Exhausted(ctx context.Context, kind runtime.AgentKind) bool {
       g.overrideMu.Lock()
       until, forced := g.override[kind]
       g.overrideMu.Unlock()
       if forced && g.now().Before(until) {
           return true
       }
       // ... existing body unchanged
   }
   ```
   (add `"sync"` to imports; `g.now()` is the existing private helper already
   used elsewhere in this file).
4. `internal/runtime/fallback_test.go`'s `fakeUsage` gains a real setter (not
   a no-op — an earlier draft of this plan had it as a no-op before review
   caught that the reconcile tests in Task 3 need it to actually record):
   ```go
   func (f fakeUsage) MarkExhausted(k AgentKind) { f[k] = true }
   ```
   Harmless to every existing fallback test since none of them call it.
5. Run `go test ./internal/usagegate/... ./internal/runtime/...` — green
   (confirms the existing fallback suite still compiles/passes with the
   interface change). Commit: "feat(usagegate): add a TTL-bounded immediate
   exhaustion override alongside the polled heuristic".

## Task 3 — `resolveAlive` reacts to a detected weekly/org-spend hit

Files: `internal/runtime/reconcile.go`, `internal/runtime/reconcile_test.go`.

1. Failing test first. Reuse whatever fake-tmux/fake-adapter harness
   `reconcile_test.go` already has for `resolveAlive` (the same one used for
   the existing idle/waiting/stale tests). Add a fake adapter variant that
   implements `adapter.UsageLimitDetector`:
   ```go
   type fakeUsageLimitAdapter struct {
       Adapter // embed the existing fake so everything else still works
       hit adapter.UsageLimitKind
   }

   func (f fakeUsageLimitAdapter) UsageLimitHit(string) adapter.UsageLimitKind { return f.hit }

   func TestResolveAliveKillsAndMarksExhaustedOnWeeklyLimit(t *testing.T) {
       s, tm := newStore(t) // existing helper
       fu := fakeUsage{}    // existing fallback_test.go type; zero value = nothing exhausted yet
       s.Usage = fu
       s.Adapters[Claude] = fakeUsageLimitAdapter{Adapter: s.Adapters[Claude], hit: adapter.UsageLimitWeekly}
       // ... existing scaffolding to create one live session under Claude,
       // matching however TestResolveAliveMarksStale (or similar) already
       // sets one up ...
       if err := s.resolveAlive(ctx, row, pane); err != nil {
           t.Fatal(err)
       }
       if !fu[Claude] { // requires fakeUsage.MarkExhausted to actually record — see note below
           t.Fatal("want Claude marked exhausted")
       }
       if len(tm.killed) != 1 || tm.killed[0] != row.TmuxName {
           t.Fatalf("killed = %v, want exactly this session's pane killed", tm.killed)
       }
       n := notified(t, s, "agent.usage_limit")
       if n.Args["kind"] != "weekly" {
           t.Fatalf("usage_limit notify args = %+v", n.Args)
       }
   }
   ```
   Note: `fakeUsage` from `fallback_test.go` is a plain `map[AgentKind]bool`
   with a no-op `MarkExhausted` (Task 2, step 4) — it can't record the call.
   Either upgrade that no-op to actually set the map entry (`func (f
   fakeUsage) MarkExhausted(k AgentKind) { f[k] = true }` — trivial, and
   makes it a real double instead of a stub, harmless to every existing
   fallback test since none of them call it), or add a second,
   call-tracking fake in `reconcile_test.go` if keeping `fallback_test.go`
   untouched is preferred. Prefer the former: one honest fake, not two.
   Also add:
   ```go
   func TestResolveAliveIgnoresPlainSessionLimit(t *testing.T) {
       // same scaffolding, hit: adapter.UsageLimitSession
       // assert: no kill, no MarkExhausted call, no agent.usage_limit notify,
       // and the existing idle/waiting logic still runs normally (Locked
       // Decision 1's self-heal carve-out).
   }
   ```
2. Run `go test ./internal/runtime/...` — new tests fail (no such branch in
   `resolveAlive` yet). Expected.
3. Implement in `internal/runtime/reconcile.go`. The existing code (verified
   against `main`) is:
   ```go
   ad, ok := s.Adapters[r.Kind]
   idle := false
   if ok {
       capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
       if err != nil {
           return err
       }
       idle = ad.Idle(capture)
   }
   waiting := idle && owesNothing
   ```
   `capture` only exists inside that `if ok` block, so the new check must be
   nested inside it too, right after `idle = ad.Idle(capture)` (not as a
   sibling block — a separate top-level `if ok { ... }` after this one would
   not compile, `capture` is out of scope there):
   ```go
   ad, ok := s.Adapters[r.Kind]
   idle := false
   if ok {
       capture, err := s.Tmux.Capture(ctx, r.TmuxName, 15)
       if err != nil {
           return err
       }
       idle = ad.Idle(capture)
       if lim, ok := ad.(adapter.UsageLimitDetector); ok {
           if k := lim.UsageLimitHit(capture); k == adapter.UsageLimitWeekly || k == adapter.UsageLimitOrgSpend {
               return s.handleUsageLimitHit(ctx, r, k)
           }
       }
   }
   waiting := idle && owesNothing
   ```
   (the inner `lim, ok :=` shadows the outer `ok` inside its own `if`
   scope, matching this file's existing style elsewhere).
   Add the new function, same file:
   ```go
   // handleUsageLimitHit reacts to a tmux-detected usage-limit message with
   // no self-heal path (docs/specs/2026-09-19-usage-limit-tmux-detection.md,
   // Locked Decision 1 — the plain "session" variant never reaches here).
   // It marks kind exhausted immediately so the next resolveUsageFallback
   // call substitutes correctly instead of waiting on (for weekly/org caps,
   // waiting forever on) a poll, notifies, then kills the pane so the next
   // reconcile tick's resolveDead makes the session retryable.
   func (s *Store) handleUsageLimitHit(ctx context.Context, r liveRow, k adapter.UsageLimitKind) error {
       if s.Usage != nil {
           s.Usage.MarkExhausted(r.Kind)
       }
       if err := s.notify(ctx, nil, NotifyInput{Kind: "agent.usage_limit", AgentName: r.AgentName, ItemKey: r.ItemKey,
           Args: map[string]string{"name": r.AgentName, "KEY": r.ItemKey, "kind": string(k)}}); err != nil {
           return err
       }
       return s.Tmux.Kill(ctx, r.TmuxName)
   }
   ```
   Add `"github.com/AlexanderTar/agent-swarm/internal/adapter"` to
   `reconcile.go`'s imports if not already present (it isn't — `reconcile.go`
   today only imports `context`, `database/sql`, `encoding/json`, `time`,
   and the local `db` package).
4. Run `go test ./internal/runtime/...` — green. Also run the *full* existing
   `reconcile_test.go`/`agents_test.go` suite to confirm no regression (the
   new branch is additive and gated behind the `UsageLimitDetector` type
   assertion, so every existing fake adapter that doesn't implement it takes
   the old path unchanged).
5. Commit: "feat(runtime): react to a tmux-detected weekly/org usage-limit
   hit in resolveAlive".

## Task 4 — notification rule

Files: `internal/notifyrules/notifyrules.go`, its existing test file (check
for a table-driven "every kind has a rule"/placeholder-validation test and
extend it the same way `agent.fallback_used` was added).

1. If a test already asserts something like "every `NotifyInput.Kind` used
   in `internal/runtime` has a matching `notifyrules.Rules` entry" or
   validates `Placeholders` against `Args`, add `agent.usage_limit` to
   whatever fixture/table drives it first (failing test), then:
2. Add the row:
   ```go
   "agent.usage_limit": {"attention", "Usage limit hit",
       "{name} hit a usage limit on {KEY} and was stopped. Retry to use the fallback agent.", "swarm.agent"},
   ```
3. Run `go test ./internal/notifyrules/...` and `./internal/runtime/...`
   (in case a runtime test validates notify Args against the rule's
   placeholders, the way `TestStartSpikeSubstitutesExhaustedFallback` checks
   `n.Args` shape) — green.
4. Commit: "feat(notifyrules): add agent.usage_limit".

## Task 5 — end-to-end falsification test

File: `internal/runtime/reconcile_test.go` (or a new
`usage_limit_e2e_test.go` alongside it if that reads cleaner — matches
`fallback_test.go` being its own file rather than folded into
`agents_test.go`).

1. Failing test first:
   ```go
   func TestUsageLimitHitMakesRetrySubstituteWithoutWaitingOnAPoll(t *testing.T) {
       s, tm := newStoreWithFallback(t) // from fallback_test.go
       setFallback(t, s, settings.RoleDefault{Agent: Codex, Model: "gpt-6-astra"})
       gate := &fakeGateNoSnapshot{} // a UsageReader whose Exhausted() always
                                     // returns false unless MarkExhausted was
                                     // called for that kind -- i.e. simulates
                                     // "no fresh poll has run" (or, for the
                                     // weekly case, "poll ran but headline-only
                                     // heuristic would never say true")
       s.Usage = gate
       // ... spin up one live Claude session, same scaffolding as Task 3 ...
       s.Adapters[Claude] = fakeUsageLimitAdapter{Adapter: s.Adapters[Claude], hit: adapter.UsageLimitWeekly}

       if err := s.resolveAlive(ctx, row, pane); err != nil {
           t.Fatal(err)
       }
       // next tick: pane is dead -> resolveDead -> crashed
       if err := s.resolveDead(ctx, row, deadPane, true); err != nil {
           t.Fatal(err)
       }

       out, err := s.Retry(ctx, agentName, "", "", "")
       if err != nil {
           t.Fatalf("Retry should now succeed (session is crashed, kind is marked exhausted): %v", err)
       }
       if out.Kind != Codex {
           t.Fatalf("Retry.Kind = %v, want substituted to Codex", out.Kind)
       }
   }
   ```
2. Run — should already pass once Tasks 2-3 are done (this test doesn't add
   new production code, only proves the composition works end to end). If
   it fails, that's a real integration gap between Tasks 2/3 — fix before
   moving on, don't just adjust the test to match.
3. **Falsification step** (per this repo's verification-before-completion
   habit and the spec's own Verification plan item 4): temporarily comment
   out the `s.Usage.MarkExhausted(r.Kind)` line inside `handleUsageLimitHit`,
   re-run this test, confirm it now fails with `Retry`'s `out.Kind ==
   Claude` (no substitution — proving `fakeGateNoSnapshot` really does
   return "not exhausted" without the mark, i.e. this test is actually
   exercising the override and not passing for some unrelated reason).
   Restore the line, confirm green again.
4. Commit: "test(runtime): prove a tmux-detected usage-limit hit makes
   Retry substitute without waiting on a poll".

## Task 6 — full-suite verification

```
go build ./...
go vet ./...
gofmt -l .
go test -race ./...
```
All green, zero regressions, matching the bar every other item in the
`no-stalled-work` incident cluster was held to.

## Explicitly not part of this plan

Same list as the spec's "Explicitly out of scope": non-Claude adapters,
auto-expiring the override, the daemon auto-calling `Retry`, any change to
`internal/usage/*.go` or `resolveUsageFallback` itself, and reacting to the
plain 5-hour `session` variant. See the spec for the reasoning behind each.
