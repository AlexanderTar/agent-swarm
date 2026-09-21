# Deterministic "Needs you" Implementation Plan

- **Spec**: `docs/specs/2026-09-21-deterministic-needs-you.md` (canonical; read it first)
- **Date**: 2026-09-21 (revised the same day: native question tools stay enabled, Needs you becomes read-only and click-through)
- **Order**: strict TDD per task: failing test, run and watch it fail, minimal implementation, run and watch it pass, commit. Tasks run in this order: sweep (1-2, fixes the live leak), relay guards (3-4), pane scrape (5), hooks (6-7), wire (8), web UI (9), menubar UI (10), dead-path cleanup (11), skill (12), verification (13).
- **Open assumptions** (spec section 2, marked ASSUMPTION): prompt rows target the asking agent's terminal; `swarm answer` CLI stays; any human prompt closes all of an agent's question/blocker rows; agy sends no `prompt`; a web row click selects and opens the terminal. Nothing in Tasks 1-8 depends on the last one; Task 11 depends on the second only for the `/answer` route, which it keeps.
- **Tracking**: ad-hoc session, no Notion page, no Swarm board task.

## Rules for every task

- Work in a git worktree that is a **sibling** of the primary checkout, never inside `.claude/` or another tool dir, never on `main`.
- Stage explicit paths (`git add <path> <path>`). Never `git add -A`. Never `git commit --amend`.
- End each commit message with the session's `Co-Authored-By` trailer.
- **Never delete a test silently.** Tasks 5, 6, 9, 10 and 11 rewrite or update tests whose behavior is intentionally removed; the ledger in spec section 8.3 says which and why. The only removals are the three `httpapi` `/resolve` tests in Task 11, listed there.
- Test helpers used below exist already: `newStore(t)` returns `(*Store, *fakeTmux, *adapter.Fake)`; `clockStore(t)` in `reconcile_test.go`; `worker(t, s)` returns `(orch Agent, w Agent, wSes Session)` with `w.ParentAgentID = orch.ID` (`checkpoint_test.go:17`); `panes(tm, ...)`; `tm.env`, `tm.captures`, `tm.keys` (entries look like `"<tmux name>|Down,Enter"`); `(*Store).startSessionForTest(ctx, a, attempt, generation)`; in `internal/hook`: `seed(t, pending, state)` returns `(*Handler, sessionID)`.
- Web tasks run from `web/`, menubar tasks from `apps/menubar/`; both live in the same worktree as the Go code.
- `stateOfRequest(t, s, id)` is added in Task 2 (`reconcile_test.go`) and reused by later runtime tests.

## Task 0: worktree and green baseline (3 min)

```bash
cd /Users/alexandertar/GitHub/agent-swarm
git fetch origin && git worktree add ../agent-swarm--deterministic-needs-you -b feat/deterministic-needs-you origin/main
cd ../agent-swarm--deterministic-needs-you
go build ./... && go test ./internal/runtime/... ./internal/hook/... ./internal/mcpserver/... -count=1
```

Expected: build clean, all three packages `ok`. If anything is red on a clean `origin/main`, stop and report which test; do not proceed.

## Task 1: extract `closeRequestTx` (refactor, 5 min)

Files: `internal/runtime/requests.go`. No new behavior, so the existing tests are the safety net.

1. Run the baseline: `go test ./internal/runtime/ -run 'Withdraw|Ask' -count=1` (expect `ok`).
2. In `internal/runtime/requests.go` add:

```go
// closeRequestTx withdraws one open request inside the caller's tx: UPDATE,
// request.resolved event, item reconcile. withdraw() and the orphan sweep share it.
func (s *Store) closeRequestTx(ctx context.Context, tx *sql.Tx, reqID string) error {
	req, err := s.requestTx(ctx, tx, reqID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'withdrawn', responded_at = ?
		WHERE id = ? AND state = 'open'`, db.Millis(s.Now()), reqID); err != nil {
		return err
	}
	key, err := s.itemKey(ctx, tx, req.ItemID)
	if err != nil {
		return err
	}
	w, err := s.RequestWireTx(ctx, tx, reqID)
	if err != nil {
		return err
	}
	if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
		return err
	}
	return s.Items.ReconcileTx(ctx, tx, key)
}
```

3. In `withdraw()`, keep the ownership and `req.State != "open"` checks, then replace everything from the `UPDATE requests SET state = 'withdrawn'` through `s.Items.ReconcileTx` with:

```go
		if err := s.closeRequestTx(ctx, tx, reqID); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, reqID)
		return err
```

4. Re-run `go test ./internal/runtime/ -run 'Withdraw|Ask' -count=1` (expect `ok`), then commit:

```bash
git add internal/runtime/requests.go
git commit -m "refactor(runtime): extract closeRequestTx from withdraw"
```

## Task 2: orphan sweep (15 min)

Files: `internal/runtime/reconcile.go`, `internal/runtime/reconcile_test.go`.

**2a. Failing test** – append to `internal/runtime/reconcile_test.go`:

```go
func stateOfRequest(t *testing.T, s *Store, id string) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRow(`SELECT state FROM requests WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSweepWithdrawsOpenHITLRequestsOfEndedSessions(t *testing.T) {
	cases := []struct {
		state SessionState
		want  string
	}{
		{Completed, "withdrawn"}, {Failed, "withdrawn"}, {Crashed, "withdrawn"}, {Cancelled, "withdrawn"},
		{Paused, "open"}, {Interrupted, "open"}, {Running, "open"},
	}
	for _, c := range cases {
		t.Run(string(c.state), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
			ses, _ := s.LatestSession(ctx, a.ID)
			req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which one?"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetSessionState(ctx, ses.ID, c.state); err != nil {
				t.Fatal(err)
			}
			if err := s.withdrawOrphanedRequests(ctx); err != nil {
				t.Fatal(err)
			}
			if got := stateOfRequest(t, s, req.ID); got != c.want {
				t.Fatalf("session %s: request state = %s, want %s", c.state, got, c.want)
			}
		})
	}
}

func TestSweepKeepsRowWhenNewerAttemptIsRunning(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Retry me", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses1, _ := s.LatestSession(ctx, a.ID)
	req, err := s.Ask(ctx, ses1.ID, AskInput{Kind: "question", Prompt: "which one?"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses1.ID, Crashed); err != nil {
		t.Fatal(err)
	}
	ses2, err := s.startSessionForTest(ctx, a, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionState(ctx, ses2.ID, Running); err != nil {
		t.Fatal(err)
	}
	if err := s.withdrawOrphanedRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if got := stateOfRequest(t, s, req.ID); got != "open" {
		t.Fatalf("request state = %s, want open (newest attempt is running)", got)
	}
}

func TestSweepWithdrawsRowsOfFinishedAgentEvenIfSessionPaused(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Done", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which one?"})
	_ = s.SetSessionState(ctx, ses.ID, Paused)
	if _, err := s.DB.ExecContext(ctx, `UPDATE agents SET state = 'finished' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.withdrawOrphanedRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if got := stateOfRequest(t, s, req.ID); got != "withdrawn" {
		t.Fatalf("request state = %s, want withdrawn", got)
	}
	evs, _ := s.Events.After(ctx, 0, 200)
	var resolved int
	for _, e := range evs {
		if e.Type == "request.resolved" {
			resolved++
		}
	}
	if resolved != 1 {
		t.Fatalf("request.resolved events = %d, want 1", resolved)
	}
}
```

**2b. Watch it fail**: `go test ./internal/runtime/ -run 'TestSweep' -count=1`. Expected: build error `s.withdrawOrphanedRequests undefined`.

**2c. Implement** – in `internal/runtime/reconcile.go` add:

```go
// withdrawOrphanedRequests closes every open HITL request whose owning agent
// is done: agents.state = 'finished', or its newest session (same order as
// LatestSession) is completed/failed/crashed/cancelled. paused/interrupted
// keep their rows: the answer is delivered as a message on resume.
func (s *Store) withdrawOrphanedRequests(ctx context.Context) error {
	ids, err := s.queryIDs(ctx, `SELECT r.id FROM requests r
		JOIN agents a ON a.id = r.agent_id
		WHERE r.state = 'open' AND r.is_hitl = 1
		  AND (a.state = 'finished'
		    OR (SELECT se.state FROM sessions se WHERE se.agent_id = r.agent_id
		        ORDER BY se.generation DESC, se.attempt DESC LIMIT 1)
		       IN ('completed', 'failed', 'crashed', 'cancelled'))
		ORDER BY r.created_at`)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.tx(ctx, func(tx *sql.Tx) error { return s.closeRequestTx(ctx, tx, id) }); err != nil {
			s.logf("reconcile: withdraw orphaned request %s: %v", id, err)
		}
	}
	return nil
}
```

Call it in `Reconcile`, **before** `sweepFinishedRoots` (that function reads open requests in a tree):

```go
	if err := s.notifyUndeliveredMessages(ctx); err != nil {
		return err
	}
	if err := s.withdrawOrphanedRequests(ctx); err != nil {
		return err
	}
	return s.sweepFinishedRoots(ctx)
```


`queryIDs` is the one place that scans a list of ids (Tasks 6 and 7 reuse it). Add it to `internal/runtime/requests.go`:

```go
// queryIDs runs a query that selects one id column and returns the ids. Callers
// resolve them one by one afterwards, so no rows stay open across a transaction.
func (s *Store) queryIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

**2d. Watch it pass**: `go test ./internal/runtime/ -run 'TestSweep' -count=1` (expect `ok`, 9 subtests), then `go test ./internal/runtime/ -count=1`.

**2e. Commit**:

```bash
git add internal/runtime/reconcile.go internal/runtime/reconcile_test.go internal/runtime/requests.go
git commit -m "feat(runtime): withdraw open HITL requests whose agent has ended"
```

## Task 3: relay guard for `swarm_ask` and `swarm_blocker` (10 min)

Files: `internal/runtime/requests.go`, `internal/runtime/requests_test.go` (add `"fmt"` only if used; the test below uses `strings`, already imported).

**3a. Failing test** – append to `internal/runtime/requests_test.go`:

```go
func TestParentedAgentCannotOpenQuestionOrBlocker(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, _, wSes := worker(t, s)

	if _, err := s.Ask(ctx, wSes.ID, AskInput{Kind: "question", Prompt: "which db?"}); err == nil ||
		!strings.Contains(err.Error(), errRelayToParent) {
		t.Fatalf("Ask err = %v, want %q", err, errRelayToParent)
	}
	if _, err := s.AskBlocker(ctx, wSes.ID, "need a key", nil); err == nil ||
		!strings.Contains(err.Error(), errRelayToParent) {
		t.Fatalf("AskBlocker err = %v, want %q", err, errRelayToParent)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE is_hitl = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("HITL rows = %d, want 0", n)
	}
}
```

**3b. Watch it fail**: `go test ./internal/runtime/ -run TestParentedAgentCannotOpenQuestionOrBlocker -count=1`. Expected: build error `undefined: errRelayToParent`.

**3c. Implement** – in `internal/runtime/requests.go`:

```go
// errRelayToParent is the refusal a parented agent gets from swarm_ask
// (kind question) and swarm_blocker. "parent" is swarm_send's alias for the
// caller's parent (skills/swarm/SKILL.md rule 5), so no parent lookup is needed.
const errRelayToParent = "You report to an orchestrator, not the user. Send this to it with swarm_send (to: \"parent\", kind: \"question\") and keep working on anything you are not blocked on."

// requireTopLevel returns the refusal for an agent that has a parent, nil otherwise.
func requireTopLevel(a Agent) error {
	if a.ParentAgentID == "" {
		return nil
	}
	return &items.Error{Code: items.CodeBadRequest, Message: errRelayToParent}
}
```

In `askQuestion` and `AskBlocker`, directly after `_, a, err := s.sessionAndAgent(ctx, tx, sessionID)` and its `if err != nil` block, add:

```go
		if err := requireTopLevel(a); err != nil {
			return err
		}
```

Do not add it to `AskPrompt` (spec decision 1b).

**3d. Watch it pass**: `go test ./internal/runtime/ -count=1` (expect `ok`; existing ask/blocker tests use `StartSpike` agents, which have no parent). Also `go test ./internal/mcpserver/ -count=1` (its seeded agents have no `parent_agent_id`).

**3e. Commit**:

```bash
git add internal/runtime/requests.go internal/runtime/requests_test.go
git commit -m "feat(runtime): only top-level agents may open question/blocker requests"
```

## Task 4: blocked checkpoint from a parented agent opens no row (7 min)

Files: `internal/runtime/checkpoint.go`, `internal/runtime/checkpoint_test.go`.

**4a. Failing test** – append to `checkpoint_test.go` (imports `items` already):

```go
func TestParentedBlockedCheckpointRelaysButOpensNoRequest(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, wSes := worker(t, s)
	if _, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.WriteCheckpoint(ctx, wSes.ID, CheckpointInput{Kind: BlockedCkp,
		Summary: "signing key needs a passphrase", Blockers: []string{"gpg-agent has no cached passphrase"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ItemStatus != items.Blocked {
		t.Fatalf("item status = %s, want blocked", res.ItemStatus)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE is_hitl = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("HITL rows = %d, want 0", n)
	}
	var payload string
	if err := s.DB.QueryRow(`SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND payload_json LIKE '%gpg-agent%'`, orch.ID).Scan(&payload); err != nil {
		t.Fatalf("parent got no relay with the blocker: %v", err)
	}
}
```

**4b. Watch it fail**: `go test ./internal/runtime/ -run TestParentedBlockedCheckpointRelaysButOpensNoRequest -count=1`. Expected: `HITL rows = 1, want 0`.

**4c. Implement** – `internal/runtime/checkpoint.go` (~line 366), change the condition:

```go
		if in.Kind == BlockedCkp && len(in.Blockers) > 0 && a.ParentAgentID == "" {
```

The `if a.ParentAgentID != ""` relay block below is untouched.

**4d. Watch it pass**: `go test ./internal/runtime/ -count=1`. `TestBlockedCheckpointOpensHITLRequest` still passes (spike agent, no parent).

**4e. Commit**:

```bash
git add internal/runtime/checkpoint.go internal/runtime/checkpoint_test.go
git commit -m "feat(runtime): blocked checkpoint of a parented agent relays, opens no request"
```

## Task 5: pane scrape auto-answers instead of opening rows (15 min)

Files: `internal/runtime/model.go`, `internal/runtime/reconcile.go`, `internal/runtime/reconcile_test.go`.

**5a. Rewrite the two tests first.** They cover behavior the spec intentionally removes (a scraped prompt becomes a row; a row auto-resolves when the pattern vanishes). Rewrite in place, same names kept in the comment for `git blame`:

Replace `TestPromptDetectedInRunningSessionOpensHITLRequest` with:

```go
func TestPromptPatternAutoAnswersOncePerSessionAndOpensNoRequest(t *testing.T) {
	s, tm, _ := clockStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "PromptSpy", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}

	fakeAd := s.Adapters[Fake].(*adapter.Fake)
	fakeAd.PromptMatchers = []adapter.PromptMatcher{
		{Match: regexp.MustCompile(`Do you trust this\?`), Title: "Trust prompt", Action: "Down+Enter"},
	}
	tm.captures[a.Name] = []string{"Some output\nDo you trust this? [y/n]\n"}

	for i := 0; i < 3; i++ {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var pressed int
	for _, k := range tm.keys {
		if k == a.Name+"|Down,Enter" {
			pressed++
		}
	}
	if pressed != 1 {
		t.Fatalf("auto-answer keys pressed %d times (keys=%v), want exactly 1", pressed, tm.keys)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE session_id = ?`, ses.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("requests = %d, want 0", count)
	}
}
```

Replace `TestReconcileAutoResolvesPromptWhenDismissedInTerminal` with:

```go
func TestReconcileNeverResolvesAPromptRowByPatternAbsence(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Perm", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	panes(tm, Pane{Session: ses.TmuxName, Command: "swarm-fake-agent"})
	tm.env[ses.TmuxName] = map[string]string{"SWARM_SESSION": ses.ID}

	// A permission dialog row (raised by the PermissionRequest hook) whose text is a raw command.
	req, err := s.AskPrompt(ctx, ses.ID, "terraform apply", nil)
	if err != nil {
		t.Fatal(err)
	}
	tm.captures[ses.TmuxName] = []string{"nothing matching here\n"}
	for i := 0; i < 2; i++ {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := stateOfRequest(t, s, req.ID); got != "open" {
		t.Fatalf("prompt row state = %s, want open (only PostToolUse or a human may close it)", got)
	}
}
```

(`stateOfRequest` comes from Task 2.)

**5b. Watch them fail**: `go test ./internal/runtime/ -run 'TestPromptPatternAutoAnswers|TestReconcileNeverResolves' -count=1`. Expected: first test `pressed 0 times` (the old code opens a row and presses nothing); second `state = answered`.

**5c. Implement.**

`internal/runtime/model.go`, next to `lastAliveAt` (~line 343):

```go
	// promptAnswered marks (sessionID|title) pairs whose PromptPattern keys were
	// already pressed, so a dialog still on screen is answered once, not every tick.
	promptAnswered map[string]bool
```

`internal/runtime/reconcile.go`:

```go
// markPromptAnswered reports true the first time it sees a (session, title) pair.
func (s *Store) markPromptAnswered(sessionID, title string) bool {
	s.bookkeepingMu.Lock()
	defer s.bookkeepingMu.Unlock()
	if s.promptAnswered == nil {
		s.promptAnswered = map[string]bool{}
	}
	k := sessionID + "|" + title
	if s.promptAnswered[k] {
		return false
	}
	s.promptAnswered[k] = true
	return true
}
```

In `resolveAlive`, replace the `if !idle { for _, matcher := range ad.PromptPatterns() { ... AskPrompt ... } }` block with:

```go
		if !idle {
			for _, m := range ad.PromptPatterns() {
				if m.Match == nil || m.Action == "" || !m.Match.MatchString(capture) {
					continue
				}
				if s.markPromptAnswered(r.SessionID, m.Title) {
					if err := s.Tmux.Keys(ctx, r.TmuxName, strings.Split(m.Action, "+")...); err != nil {
						s.logf("reconcile: auto-answer %q for %s: %v", m.Title, r.SessionID, err)
					}
				}
				break
			}
		}
```

Delete the whole "Auto-resolve any open prompt request whose pattern is no longer present in capture" block (from `rows, err := s.DB.QueryContext(ctx, \`SELECT id, prompt FROM requests` through its closing brace, including the `owesNothing` recompute inside it). Add `"strings"` to the imports if missing; drop `sql`/`regexp` imports only if now unused.

**5d. Watch them pass**: `go test ./internal/runtime/ -count=1`.

**5e. Commit**:

```bash
git add internal/runtime/model.go internal/runtime/reconcile.go internal/runtime/reconcile_test.go
git commit -m "feat(runtime): auto-answer known terminal dialogs instead of opening requests"
```

## Task 6: native question tools, relay for parented agents, permission evidence (25 min)

Files: `internal/hook/handler.go`, `internal/hook/handler_test.go`, `internal/runtime/requests.go`, `internal/runtime/requests_test.go`. Spec sections 4.1, 4.5, 5. Top-level agents keep their native question tool and their row; parented agents are blocked; PostToolUse matches by prompt.

**6a. Failing tests** – in `internal/hook/handler_test.go`.

Add (note: `UPDATE agents SET parent_agent_id = 'agt_1' WHERE id = 'agt_1'` makes the seeded agent "have a parent" without a second row; the FK is satisfied by its own id, and the hook only tests for a non-empty value):

```go
func TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		kind runtime.AgentKind
		tool string
	}{{runtime.Claude, "AskUserQuestion"}, {runtime.Codex, "request_user_input"}, {runtime.Cursor, "ask_question"}, {runtime.Agy, "ask_question"}} {
		t.Run(string(c.kind)+" "+c.tool, func(t *testing.T) {
			h, ses := seed(t, 0, runtime.Running)
			if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET parent_agent_id = 'agt_1', kind = ? WHERE id = 'agt_1'`, string(c.kind)); err != nil {
				t.Fatal(err)
			}
			in, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": c.tool,
				"tool_input": map[string]any{"question": "Deploy?"}})
			out, err := h.Handle(ctx, c.kind, "PreToolUse", ses, in)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "swarm_send") {
				t.Fatalf("a parented agent's question tool must be blocked with the relay text, got %s", out)
			}
			var n int
			if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("requests = %d, want 0", n)
			}
		})
	}
}

func TestQuestionToolPostToolUseClosesOnlyTheMatchingRow(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	a, err := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "A?"})
	if err != nil {
		t.Fatal(err)
	}
	pre, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion", "tool_input": map[string]any{"question": "B?"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, pre); err != nil {
		t.Fatal(err)
	}
	post, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"question": "B?"}, "tool_response": map[string]any{"answer": "yes"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}
	state := func(prompt string) (s string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE prompt = ?`, prompt).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got := state("B?"); got != "answered" {
		t.Fatalf("B? = %s, want answered", got)
	}
	if got := state("A?"); got != "open" {
		t.Fatalf("A? = %s, want open (%s)", got, a.ID)
	}
}

func TestQuestionToolPostToolUseWithoutToolInputClosesNothing(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "A?"}); err != nil {
		t.Fatal(err)
	}
	post, _ := json.Marshal(map[string]any{"session_id": "p1", "tool_name": "AskUserQuestion", "tool_response": map[string]any{"answer": "yes"}})
	if _, err := h.Handle(ctx, runtime.Claude, "PostToolUse", ses, post); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "open" {
		t.Fatalf("state = %s, want open (no tool_input, no match)", st)
	}
}

func TestPostToolUseResolvesOnlyTheMatchingPermissionPrompt(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	if _, err := h.DB.ExecContext(ctx, `UPDATE agents SET kind = 'codex' WHERE id = 'agt_1'`); err != nil {
		t.Fatal(err)
	}
	perm, _ := json.Marshal(map[string]any{"command": "terraform apply"})
	if _, err := h.Handle(ctx, runtime.Codex, "PermissionRequest", ses, perm); err != nil {
		t.Fatal(err)
	}
	stateOf := func() (state, via string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state, COALESCE(responded_via, '') FROM requests
			WHERE session_id = ? AND kind = 'prompt'`, ses).Scan(&state, &via); err != nil {
			t.Fatal(err)
		}
		return
	}
	other, _ := json.Marshal(map[string]any{"command": "ls"})
	if _, err := h.Handle(ctx, runtime.Codex, "PostToolUse", ses, other); err != nil {
		t.Fatal(err)
	}
	if st, _ := stateOf(); st != "open" {
		t.Fatalf("state after unrelated tool = %s, want open", st)
	}
	if _, err := h.Handle(ctx, runtime.Codex, "PostToolUse", ses, perm); err != nil {
		t.Fatal(err)
	}
	if st, via := stateOf(); st != "answered" || via != "terminal" {
		t.Fatalf("state after matching tool = %s via %s, want answered via terminal", st, via)
	}
}
```

Rewrite three existing tests in place (same names): `TestPostToolUseResolvesOpenQuestionRequest`, `TestPostToolUseResolvesOpenQuestionRequestFallbackWhenEmptyResponse`, `TestPostToolUseClaudeResolvesOpenQuestionRequest`. Each keeps its assertions; only the PostToolUse payload gains the `tool_input` a real hook sends, equal to the PreToolUse one:
- first two (agy `ask_question`): `postInput` becomes `{"session_id":"<id>","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]},"tool_response":{"answer":"PostgreSQL"}}` (second test: same without `tool_response`);
- third (Claude): `postInput` gains `"tool_input": map[string]any{"question": "Deploy to staging?", "options": []string{"yes", "no"}}`.

`TestQuestionToolInterceptionCreatesHITLRequest` and `TestPostToolUseNonQuestionToolDoesNotResolveOpenQuestionRequest` are unchanged.

**6b. Watch them fail**: `go test ./internal/hook/ -run 'TestParentedAgentQuestionTool|TestQuestionToolPostToolUse|TestPostToolUseResolvesOnlyTheMatching|TestPostToolUseResolvesOpenQuestion|TestPostToolUseClaudeResolves' -count=1`. Expected: parented test `must be blocked`; the matching-row test `A? = answered` (old "newest" logic); the no-tool-input test `state = answered`; the permission test `state after matching tool = open`.

**6c. Implement.**

`internal/hook/handler.go`, `sessionRow` gains `ParentAgentID string`; in `load()` add `COALESCE(a.parent_agent_id, '')` to the SELECT (after `a.kind`) and `&s.ParentAgentID` to the Scan in the same position.

```go
// isQuestionTool is the one list of native "ask the human" tools.
func isQuestionTool(name string) bool {
	switch name {
	case "ask_question", "AskUserQuestion", "request_user_input", "experimental_request_user_input":
		return true
	}
	return false
}

// nativeQuestionRelay is the PreToolUse block reason for a parented agent.
const nativeQuestionRelay = "[swarm] You report to an orchestrator, not the user. Send this question to it with swarm_send (to: \"parent\", kind: \"question\") instead of a question tool."
```

PreToolUse: put this **before** the `swarm_spawn` budget check:

```go
		if isQuestionTool(in.ToolName) && s.ParentAgentID != "" {
			return adapter.HookDecision{Block: true, Reason: nativeQuestionRelay}, nil
		}
```

Replace the inlined `isQuestionTool := ...` in the intercept with a call to the function (behavior unchanged for top-level agents). PostToolUse: delete the `isQuestionTool := ...` block and the whole "newest open question" branch; insert:

```go
		if isQuestionTool(in.ToolName) && h.RT != nil && s.ID != "" {
			prompt, _ := extractQuestion(in.ToolName, in.RawToolInput)
			answer := extractToolResponseText(in.ToolResponse)
			if answer == "" {
				answer = "Resolved in terminal"
			}
			if err := h.RT.ResolveQuestionByPrompt(ctx, s.ID, prompt, answer); err != nil {
				h.logf("hook: resolve question for %s: %v", s.ID, err)
			}
		}
		if h.RT != nil && s.ID != "" {
			if err := h.RT.ResolveSessionPrompts(ctx, s.ID, in.Command); err != nil {
				h.logf("hook: resolve prompts for %s: %v", s.ID, err)
			}
		}
```

`internal/runtime/requests.go` (`queryIDs` comes from Task 2):

```go
// ResolveQuestionByPrompt closes the session's open native-question row whose
// prompt equals the one this tool call asked, answered via terminal. No match
// is not an error (the tool may have been blocked, or the row swept).
func (s *Store) ResolveQuestionByPrompt(ctx context.Context, sessionID, prompt, answer string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE session_id = ? AND kind = 'question' AND state = 'open' AND prompt = ?
		ORDER BY created_at DESC LIMIT 1`, sessionID, prompt)
	if err != nil || len(ids) == 0 {
		return err
	}
	_, err = s.ResolveQuestion(ctx, ids[0], answer, "terminal")
	return err
}

// ResolveSessionPrompts resolves open prompt requests of one session once a
// PostToolUse proves the permission dialog was answered. Rows whose prompt
// equals command, or the fallback "Permission requested", match;
// command == "" resolves all of the session's open prompts.
func (s *Store) ResolveSessionPrompts(ctx context.Context, sessionID, command string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE session_id = ? AND kind = 'prompt' AND state = 'open'
		  AND (? = '' OR prompt = ? OR prompt = 'Permission requested')`, sessionID, command, command)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.ResolvePrompt(ctx, id, "", "terminal"); err != nil {
			s.logf("resolve prompt %s: %v", id, err)
		}
	}
	return nil
}
```

(Known limit, keep as a `// ponytail:` comment on `ResolveSessionPrompts`: an empty `in.Command` resolves every open prompt of the session; only adapters that send no command on PostToolUse hit it.)

**6d. Watch them pass**: `go test ./internal/hook/ ./internal/runtime/ -count=1`. `TestPermissionRequestCreatesHITLRequest` and `TestQuestionToolInterceptionCreatesHITLRequest` pass unchanged.

**6e. Commit**:

```bash
git add internal/hook/handler.go internal/hook/handler_test.go internal/runtime/requests.go
git commit -m "feat(hook): parented agents relay questions; question rows close by prompt match"
```

## Task 7: a human prompt closes the agent's question and blocker rows (20 min)

Files: `internal/adapter/adapter.go`, `internal/adapter/claude.go`, `codex.go`, `cursor.go`, `internal/adapter/adapter_test.go`, `internal/runtime/text.go`, `internal/runtime/text_test.go`, `internal/runtime/requests.go`, `internal/runtime/requests_test.go`, `internal/hook/handler.go`, `internal/hook/handler_test.go`. Spec sections 3 (human-prompt SQL) and 4.5 (discriminator).

**7a. Failing tests.**

`internal/adapter/adapter_test.go`:

```go
func TestParseHookReadsThePromptField(t *testing.T) {
	d := testDeps(t)
	cases := []struct {
		name  string
		parse func(event string, stdin []byte) (HookInput, error)
		event string
		stdin string
	}{
		{"claude", newClaude(d).ParseHook, "UserPromptSubmit", `{"session_id":"s","prompt":"Use zod"}`},
		{"codex", newCodex(d).ParseHook, "UserPromptSubmit", `{"session_id":"s","turn_id":"t","prompt":"Use zod"}`},
		{"cursor", newCursor(d).ParseHook, "beforeSubmitPrompt", `{"conversation_id":"c","prompt":"Use zod"}`},
	}
	for _, c := range cases {
		in, err := c.parse(c.event, []byte(c.stdin))
		if err != nil || in.Prompt != "Use zod" {
			t.Errorf("%s: Prompt = %q, err = %v", c.name, in.Prompt, err)
		}
	}
	in, _ := newAgy(d).ParseHook("PreInvocation", []byte(`{"conversationId":"c"}`))
	if in.Prompt != "" {
		t.Errorf("agy Prompt = %q, want empty", in.Prompt)
	}
}
```

`internal/runtime/text_test.go`:

```go
func TestIsDaemonPrompt(t *testing.T) {
	for _, p := range []string{
		IdleToken, " " + IdleToken + "\n",
		Kickoff("a", RoleOrchestrator, "EPIC-1", "T"), ResumeKickoff("a", RoleOrchestrator, "EPIC-1", "T"),
		PendingNotice(2, "a", "EPIC-1"), ControlNotice("a", "EPIC-1"), CompactionNotice(),
		"typed by a human, merged with " + IdleToken + " " + ShortPreamble,
	} {
		if !IsDaemonPrompt(p) {
			t.Errorf("IsDaemonPrompt(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"Use zod", "yes", "  the second one  ", "swarm: what is inbox?", ""} {
		if IsDaemonPrompt(p) {
			t.Errorf("IsDaemonPrompt(%q) = true, want false", p)
		}
	}
}
```

(An empty prompt is neither "daemon" nor "human"; the hook guards on `Prompt != ""` first, so the empty case is only asserted by the hook test below.)

`internal/runtime/requests_test.go`:

```go
func TestResolveAnsweredInTerminalClosesQuestionAndBlockerButNotPrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Human", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	q, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which?"})
	b, err := s.AskBlocker(ctx, ses.ID, "need a key", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.AskPrompt(ctx, ses.ID, "terraform apply", nil)
	if err := s.ResolveAnsweredInTerminal(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{q.ID: "answered", b.ID: "answered", p.ID: "open"} {
		if got := stateOfRequest(t, s, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
	var text, via string
	if err := s.DB.QueryRow(`SELECT response_text, responded_via FROM requests WHERE id = ?`, q.ID).Scan(&text, &via); err != nil {
		t.Fatal(err)
	}
	if text != "Answered in terminal" || via != "terminal" {
		t.Fatalf("response = %q via %q", text, via)
	}
}
```

`internal/hook/handler_test.go`:

```go
func TestHumanPromptClosesOpenRowsButDaemonPromptsDoNot(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	q, _ := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "which?"})
	submit := func(prompt string) {
		t.Helper()
		in, _ := json.Marshal(map[string]any{"session_id": "p1", "prompt": prompt})
		if _, err := h.Handle(ctx, runtime.Claude, "UserPromptSubmit", ses, in); err != nil {
			t.Fatal(err)
		}
	}
	state := func() (s string) {
		if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, daemon := range []string{runtime.IdleToken, runtime.Kickoff("login-form-coder", runtime.RoleCoder, "TASK-101", "T"),
		runtime.PendingNotice(1, "login-form-coder", "TASK-101"), ""} {
		submit(daemon)
		if got := state(); got != "open" {
			t.Fatalf("after daemon prompt %q the row is %s, want open", daemon, got)
		}
	}
	submit("Use zod")
	if got := state(); got != "answered" {
		t.Fatalf("after a human prompt the row is %s, want answered", got)
	}
}

func TestHumanPromptInANewSessionClosesTheRowOfTheOldOne(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	q, _ := h.RT.Ask(ctx, ses, runtime.AskInput{Kind: "question", Prompt: "which?"})
	if _, err := h.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE id = 'ses_1';
		INSERT INTO sessions (id,agent_id,attempt,generation,token_hash,tmux_name,cwd,state,cwd_kind,started_at)
		VALUES ('ses_2','agt_1',1,2,'hash2','login-form-coder-2','/tmp/w','running','neutral',2)`); err != nil { // adjust to the schema's unique indexes if the insert is rejected
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"session_id": "p2", "prompt": "Use zod"})
	if _, err := h.Handle(ctx, runtime.Claude, "UserPromptSubmit", "ses_2", in); err != nil {
		t.Fatal(err)
	}
	var st string
	if err := h.DB.QueryRowContext(ctx, `SELECT state FROM requests WHERE id = ?`, q.ID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "answered" {
		t.Fatalf("row = %s, want answered (rows are keyed by agent, not session)", st)
	}
}
```

**7b. Watch them fail**: `go test ./internal/adapter/ ./internal/runtime/ ./internal/hook/ -run 'ReadsThePromptField|IsDaemonPrompt|ResolveAnsweredInTerminal|HumanPrompt' -count=1`. Expected: build errors `in.Prompt undefined`, `undefined: IsDaemonPrompt`, `undefined: (*Store).ResolveAnsweredInTerminal`.

**7c. Implement.**

`internal/adapter/adapter.go`, `HookInput` (line ~65): add `Prompt string // UserPromptSubmit text; empty for adapters that don't send it`. In `claude.go`, `codex.go`, `cursor.go` `ParseHook`: add `Prompt string \`json:"prompt"\`` to the raw struct and `Prompt: raw.Prompt,` to the returned `HookInput`. `agy.go` is not changed.

`internal/runtime/text.go`:

```go
// IsDaemonPrompt reports whether a UserPromptSubmit text was written by the
// daemon, not typed by the user. Every daemon prompt is either the idle token
// (wake.go tryPaste) or carries ShortPreamble: Kickoff, ResumeKickoff,
// PendingNotice, ControlNotice and CompactionNotice all do.
func IsDaemonPrompt(prompt string) bool {
	p := strings.TrimSpace(prompt)
	return p == IdleToken || strings.Contains(p, ShortPreamble)
}
```

`internal/runtime/requests.go`:

```go
// ResolveAnsweredInTerminal closes every open question/blocker row of an agent
// after a human-typed prompt: answered, via terminal. Keyed by agent, not
// session: after a pause and resume the row belongs to the old session.
func (s *Store) ResolveAnsweredInTerminal(ctx context.Context, agentID string) error {
	ids, err := s.queryIDs(ctx, `SELECT id FROM requests
		WHERE agent_id = ? AND is_hitl = 1 AND kind IN ('question', 'blocker') AND state = 'open'`, agentID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.ResolveQuestion(ctx, id, "Answered in terminal", "terminal"); err != nil {
			s.logf("resolve %s after human prompt: %v", id, err)
		}
	}
	return nil
}
```

`internal/hook/handler.go`, first statement of `case "UserPromptSubmit":`:

```go
		if in.Prompt != "" && !runtime.IsDaemonPrompt(in.Prompt) && h.RT != nil && s.AgentID != "" {
			if err := h.RT.ResolveAnsweredInTerminal(ctx, s.AgentID); err != nil {
				h.logf("hook: close rows after human prompt for %s: %v", s.ID, err)
			}
		}
```

**7d. Watch them pass**: `go test ./internal/adapter/ ./internal/runtime/ ./internal/hook/ -count=1`.

**7e. Commit**:

```bash
git add internal/adapter/adapter.go internal/adapter/claude.go internal/adapter/codex.go internal/adapter/cursor.go internal/adapter/adapter_test.go internal/runtime/text.go internal/runtime/text_test.go internal/runtime/requests.go internal/runtime/requests_test.go internal/hook/handler.go internal/hook/handler_test.go
git commit -m "feat(hook): a human prompt closes the agent's question and blocker rows"
```

## Task 8: `terminal_agent` on the request wire, notification categories (15 min)

Files: `internal/runtime/requests.go`, `internal/runtime/requests_test.go`, `internal/notifyrules/notifyrules.go`, `internal/notify/notify_test.go`. Spec sections 3 (terminal SQL), 4.1, 4.10.

**8a. Failing tests.** In `requests_test.go` (`worker(t, s)` from `checkpoint_test.go`; the direct INSERT stands in for a legacy row the guard would now refuse):

```go
func TestRequestWireTerminalAgent(t *testing.T) {
	ctx := context.Background()
	want := func(t *testing.T, s *Store, id string, name *string) {
		t.Helper()
		w, err := s.RequestWireByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (w.TerminalAgent == nil) != (name == nil) || (name != nil && *w.TerminalAgent != *name) {
			t.Fatalf("terminal_agent = %v, want %v", w.TerminalAgent, name)
		}
	}
	t.Run("top-level question is its own terminal", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Top", Intent: "feature", Kind: Fake, Model: "fake-1"})
		ses, _ := s.LatestSession(ctx, a.ID)
		req, _ := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "which?"})
		want(t, s, req.ID, &a.Name)
	})
	t.Run("legacy parented question points at the root orchestrator", func(t *testing.T) {
		s, _, _ := newStore(t)
		orch, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES ('req_legacy','question',1,?,?,?,'which?','[]','open',1)`,
			w.ID, wSes.ID, w.ItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_legacy", &orch.Name)
	})
	t.Run("permission prompt points at the asking agent", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		req, err := s.AskPrompt(ctx, wSes.ID, "terraform apply", nil)
		if err != nil {
			t.Fatal(err)
		}
		want(t, s, req.ID, &w.Name)
	})
	t.Run("approval kinds have none", func(t *testing.T) {
		s, _, _ := newStore(t)
		_, w, wSes := worker(t, s)
		if _, err := s.DB.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
			prompt, options_json, state, created_at) VALUES ('req_close','close_spike',0,?,?,?,'x','[]','open',1)`,
			w.ID, wSes.ID, w.ItemID); err != nil {
			t.Fatal(err)
		}
		want(t, s, "req_close", nil)
	})
}
```

In `internal/notify/notify_test.go` `TestRulesCoverSection175`, change the expected 4th field of `request.prompt` from `"swarm.approval"` to `"swarm.question"` and of `request.blocker` from `"swarm.agent"` to `"swarm.question"`.

**8b. Watch them fail**: `go test ./internal/runtime/ ./internal/notify/ -run 'TestRequestWireTerminalAgent|TestRulesCoverSection175' -count=1`. Expected: build error `w.TerminalAgent undefined`; notify test `request.prompt = ... want [...]`.

**8c. Implement.** `RequestWire` gets `TerminalAgent *string \`json:"terminal_agent"\``. In `RequestWireTx`, after the `AgentName` block:

```go
	w.TerminalAgent = s.terminalAgent(ctx, tx, r)
```

```go
// terminalAgent is the tmux session the user answers a HITL row in: the asking
// agent for a permission dialog (the dialog lives in its pane), the root
// orchestrator of its tree for a question or blocker. nil for non-HITL kinds
// and for a row with no agent.
func (s *Store) terminalAgent(ctx context.Context, tx *sql.Tx, r Request) *string {
	if !r.IsHITL || r.AgentID == "" {
		return nil
	}
	q := `WITH RECURSIVE up(name, parent) AS (
		SELECT name, parent_agent_id FROM agents WHERE id = ?
		UNION ALL
		SELECT a.name, a.parent_agent_id FROM agents a JOIN up ON a.id = up.parent)
		SELECT name FROM up WHERE parent IS NULL LIMIT 1`
	if r.Kind == "prompt" {
		q = `SELECT name FROM agents WHERE id = ?`
	}
	var name string
	if err := tx.QueryRowContext(ctx, q, r.AgentID).Scan(&name); err != nil {
		return nil
	}
	return &name
}
```

`internal/notifyrules/notifyrules.go`: change the category (4th field) of `request.prompt` to `"swarm.question"` and of `request.blocker` to `"swarm.question"`.

**8d. Watch them pass**: `go test ./internal/runtime/ ./internal/notify/ ./internal/notifyrules/ ./internal/httpapi/ -count=1`.

**8e. Commit**:

```bash
git add internal/runtime/requests.go internal/runtime/requests_test.go internal/notifyrules/notifyrules.go internal/notify/notify_test.go
git commit -m "feat(runtime): terminal_agent on HITL request wire; one notification category"
```

## Task 9: web Needs you is read-only (25 min)

Files: `web/src/types.ts`, `web/src/copy.ts`, `web/src/logic/inbox.ts`, `web/src/logic/inbox.test.ts`, `web/src/components/QuestionView.tsx`, `web/src/components/QuestionView.test.tsx`, `web/src/panels/Review.tsx`, `web/src/panels/Review.test.tsx`, `web/src/panels/NeedsYou.tsx`, `web/src/panels/NeedsYou.test.tsx`, `web/src/App.flows.test.tsx`, `web/src/mock/fixtures.ts`, `web/e2e/mock.spec.ts`. Spec sections 4.8, 6, 7. Run from `web/`.

**9a. Fixtures and types first** (so tests compile): `types.ts` `Request` gains `terminal_agent: string | null;`. `mock/fixtures.ts` `request()` factory default gains `terminal_agent: null` (next to `agent_name: null`); set `terminal_agent: "offline-spike-orchestrator"` on `req_question` and `terminal_agent: "auth-epic-orchestrator"` on `req_q2`. `copy.ts` `C` gains:

```ts
  openOrchestratorTerminal: "Open orchestrator terminal",
  orchestratorPaused: "Orchestrator is paused. Resume it to continue.",
  orchestratorNotRunning: "Orchestrator isn't running.",
  optionsOffered: "Options offered",
```

**9b. Failing tests.**

`web/src/logic/inbox.test.ts` (add; `makeAgent` from `../logic/agentActions`, `ses`-style session via its `session` partial):

```ts
describe("requestTarget", () => {
  const q = { ...request, kind: "question", is_hitl: true, terminal_agent: "orch" } as Request;
  const orch = (session: Partial<SessionInfo> | null) =>
    makeAgent({ name: "orch", role: "orchestrator", session: session && { ...makeAgent().session!, ...session } });
  it("opens the terminal when the tmux session is alive", () => {
    expect(requestTarget(q, [orch({ tmux_alive: true, state: "running" })])).toEqual({ kind: "terminal", agent: "orch" });
  });
  it("says paused for a paused or interrupted orchestrator", () => {
    expect(requestTarget(q, [orch({ tmux_alive: false, state: "paused" })])).toEqual({ kind: "unavailable", hint: "Orchestrator is paused. Resume it to continue." });
  });
  it("says not running otherwise, including an unknown agent", () => {
    expect(requestTarget(q, [orch({ tmux_alive: false, state: "crashed" })])).toMatchObject({ kind: "unavailable", hint: "Orchestrator isn't running." });
    expect(requestTarget(q, [])).toMatchObject({ kind: "unavailable", hint: "Orchestrator isn't running." });
  });
  it("is null without a terminal agent or for approvals", () => {
    expect(requestTarget({ ...q, terminal_agent: null }, [])).toBeNull();
    expect(requestTarget({ ...q, kind: "approve_plan", is_hitl: false }, [])).toBeNull();
  });
});
```

`web/src/components/QuestionView.test.tsx` – replace the three tests (they cover the removed answer input; ledger in spec 8.3) with:

```tsx
describe("QuestionView (read-only, §16.11)", () => {
  it("shows the prompt and options as text, with no input and no submit button", () => {
    renderWithDaemon(<QuestionView request={question()} connected />, { events: false });
    expect(screen.getByText("Which sync strategy?")).toBeInTheDocument();
    expect(screen.getByText("CRDT")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "CRDT" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send answer" })).not.toBeInTheDocument();
  });

  it("opens the terminal of terminal_agent", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<QuestionView request={question()} connected />, { daemon: d, events: false });
    await user.click(await screen.findByRole("button", { name: "Open orchestrator terminal" }));
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));
    expect(d.calls.some((c) => c.path.includes("/answer") || c.path.includes("/resolve"))).toBe(false);
  });

  it("is disabled and explains when the orchestrator is paused, or the daemon is disconnected", async () => {
    const paused = { ...question(), terminal_agent: "crash-debug-orchestrator" };
    renderWithDaemon(<QuestionView request={paused} connected />, { events: false });
    expect(await screen.findByText("Orchestrator is paused. Resume it to continue.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open orchestrator terminal" })).toBeDisabled();
  });
});
```

`web/src/panels/Review.test.tsx`: rewrite `delegates questions and repo confirmation` so the question half asserts `screen.queryByRole("button", { name: "Send answer" })` is null and `screen.getByRole("button", { name: "Open orchestrator terminal" })` is present (repo half unchanged); rewrite `renders Approve button for prompt requests and resolves on click` into `does not render Approve for prompt requests` (no `Approve` button, `d.calls` has no `/resolve`); in `launches terminal from prompt request and disables buttons when disconnected` change the button name to `Open orchestrator terminal` and give `promptReq` `terminal_agent: "test-agent"` (mock agent list must contain a live `test-agent`, or assert the disabled path only if the mock has none: read the test body and keep its intent).

`web/src/panels/NeedsYou.test.tsx`: add

```tsx
  it("opens the orchestrator terminal when a question row is clicked, and only selects an approval row", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<Host />, { daemon: d, events: false });
    await screen.findByRole("list", { name: "Needs you" });
    await user.click(screen.getByRole("button", { name: /Which sync strategy\?/ }));
    await waitFor(() => expect(d.calls.some((c) => c.path === "/api/agents/offline-spike-orchestrator/terminal")).toBe(true));
    const before = d.calls.length;
    await user.click(screen.getByRole("radio", { name: "Approvals" }));
    await user.click(screen.getAllByRole("button", { name: /Accept epic/ })[0]!);
    expect(d.calls.slice(before).some((c) => c.path.endsWith("/terminal"))).toBe(false);
  });
```

`web/src/App.flows.test.tsx` `filters the inbox through the URL`: replace the last line with `expect(screen.queryByRole("textbox", { name: "Answer" })).not.toBeInTheDocument();`.

`web/e2e/mock.spec.ts` test `the inbox answers a question and approves a section`: rename to `the inbox shows a question read-only and approves a section`; replace the `CRDT` click with:

```ts
  await page.goto("/#/inbox?req=req_question");
  await expect(page.getByRole("textbox")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "CRDT" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open orchestrator terminal" })).toBeVisible();
```

and drop the `toHaveCount(0)` line that expected the answered row to vanish. The approve-section half is unchanged.

**9c. Watch them fail**: `npm test -- --run src/logic/inbox.test.ts src/components/QuestionView.test.tsx src/panels/Review.test.tsx src/panels/NeedsYou.test.tsx src/App.flows.test.tsx`. Expected: `requestTarget is not a function`, `Unable to find role="button" name "Open orchestrator terminal"`, textbox still present.

**9d. Implement.**

`web/src/logic/inbox.ts` (add imports `C` from `../copy`, `flattenAgents` from `./agentActions`, types `AgentNode`):

```ts
export type RequestTarget = { kind: "terminal"; agent: string } | { kind: "unavailable"; hint: string };

// What a Needs-you row's click does. null for a request with no terminal_agent (approvals).
export function requestTarget(r: Request, agents: AgentNode[]): RequestTarget | null {
  if (!r.is_hitl || !r.terminal_agent) return null;
  const a = flattenAgents(agents).find((n) => n.name === r.terminal_agent);
  if (a?.session?.tmux_alive) return { kind: "terminal", agent: a.name };
  const paused = a?.session?.state === "paused" || a?.session?.state === "interrupted";
  return { kind: "unavailable", hint: paused ? C.orchestratorPaused : C.orchestratorNotRunning };
}
```

`web/src/components/QuestionView.tsx` becomes (also used for `prompt` and `blocker`):

```tsx
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useAgents } from "../data/queries";
import { requestTarget } from "../logic/inbox";
import type { Request } from "../types";

export function QuestionView({ request, connected }: { request: Request; connected: boolean }) {
  const agents = useAgents();
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));
  const options = Array.isArray(request.options) ? request.options : [];
  const target = requestTarget(request, agents.data ?? []);
  const open = target?.kind === "terminal" ? target.agent : undefined;
  return (
    <div className="space-y-3">
      <p className="whitespace-pre-wrap text-base">{request.prompt}</p>
      {options.length > 0 && (
        <div>
          <p className="text-muted">{C.optionsOffered}</p>
          <ul className="list-disc pl-5">{options.map((o) => <li key={o}>{o}</li>)}</ul>
        </div>
      )}
      <button
        type="button"
        disabled={!connected || !open}
        onClick={() => open && void terminal.run(open).catch(() => undefined)}
        className="rounded border border-line px-3 py-1"
      >
        {C.openOrchestratorTerminal}
      </button>
      {target?.kind === "unavailable" && <p className="text-muted">{target.hint}</p>}
    </div>
  );
}
```

`web/src/panels/Review.tsx`: delete `PromptView` and the imports it alone used; render

```tsx
      {(r.kind === "question" || r.kind === "prompt" || r.kind === "blocker") && <QuestionView request={r} connected={connected} />}
```

in place of the two separate lines (this also gives `blocker` rows a body; today they render an empty pane).

`web/src/panels/NeedsYou.tsx`: add `const agents = useAgents();` and `const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));` (imports from `../data/queries`, `../data/hooks`, `requestTarget` from `../logic/inbox`), and change the row `onClick`:

```tsx
                  onClick={() => {
                    p.onSelectRequest(r.id);
                    const t = requestTarget(r, agents.data ?? []);
                    if (t?.kind === "terminal") void terminal.run(t.agent).catch(() => undefined);
                  }}
```

`web/src/copy.ts`: delete `sendAnswer` and `approve`. Keep `answer` (it is `SCOPE_LABEL.question`).

**9e. Watch them pass**: `npm run typecheck && npm test -- --run && npm run test:e2e` (the e2e runs the mock daemon).

**9f. Commit**:

```bash
git add web/src/types.ts web/src/copy.ts web/src/logic/inbox.ts web/src/logic/inbox.test.ts web/src/components/QuestionView.tsx web/src/components/QuestionView.test.tsx web/src/panels/Review.tsx web/src/panels/Review.test.tsx web/src/panels/NeedsYou.tsx web/src/panels/NeedsYou.test.tsx web/src/App.flows.test.tsx web/src/mock/fixtures.ts web/e2e/mock.spec.ts
git commit -m "feat(web): Needs you is read-only; a click opens the orchestrator terminal"
```

## Task 10: menubar Needs you is read-only (30 min)

Files (under `apps/menubar/`): `Sources/SwarmBarKit/{Wire,AppModel,Notifier,Copy}.swift`, `Sources/SwarmBarUI/Popover/NeedsYouSection.swift`, `Tests/SwarmBarTests/{AppModelTests,NotifierTests}.swift`. Spec sections 4.9, 6, 7. Run from `apps/menubar/`.

**10a. Wire and copy first.** `Wire.swift` `SwarmRequest`: add `public var terminalAgent: String?`, `case terminalAgent = "terminal_agent"` in `CodingKeys`, `terminalAgent: String? = nil` as the last `init` parameter (assign it), and `terminalAgent = try c.decodeIfPresent(String.self, forKey: .terminalAgent)` in `init(from:)`. `Copy.swift` gains:

```swift
    public static let openOrchestratorTerminal = "Open orchestrator terminal"
    public static let orchestratorPaused = "Orchestrator is paused. Resume it to continue."
    public static let orchestratorNotRunning = "Orchestrator isn't running."
```

**10b. Failing tests.**

`AppModelTests.swift`: rewrite `testAnswerInline` into

```swift
    func testNeedsYouRowsAreReadOnlyAndOpenTheOrchestratorTerminal() async {
        let m = make()
        await m.refresh()
        let r = SwarmRequest(id: "req_o", kind: .question, isHITL: true, agentName: "login-form-coder", prompt: "Which?",
                             terminalAgent: "login-form-coder")
        XCTAssertEqual(m.requestTarget(r), .terminal("login-form-coder"))
        await m.openRequest(r)
        XCTAssertEqual(script.sources.count, 1, "the terminal opened")
        XCTAssertEqual(client.calls.filter { $0.hasPrefix("answer") || $0.hasPrefix("resolve") }, [], "no answer or resolve call exists")
        XCTAssertNil(m.requestTarget(SwarmRequest(id: "req_a", kind: .approvePlan, terminalAgent: nil)))
    }
```

(reuse the test fixture agents already used by `testResolvePromptCallsAPIAndUpdatesState`: `login-form-coder` is alive there; for the paused case add a fixture agent or assert with `SwarmRequest(... terminalAgent: "no-such-agent")` → `.unavailable(Copy.orchestratorNotRunning)`, and `.unavailable(Copy.orchestratorPaused)` for an agent whose `session.state == .paused`).

Rewrite `testResolvePromptCallsAPIAndUpdatesState` into `testPromptRowTargetsTheAskingAgentsTerminal`: the `.prompt` request with `terminalAgent: "login-form-coder"` gives `.terminal("login-form-coder")`; there is no Approve call. Rewrite `testAnswerFailedNotificationActionSeedsTheDraft` into `testRequestNotificationActionOpensTheOrchestratorTerminal`: `handleNotificationAction("open_orchestrator", userInfo: ["kind": "request.question", "request": "req_question"], text: nil)` opens a terminal for the request's `terminalAgent`. Update the `requestTerminal` assertion at line ~157 to `requestTarget(...) == .terminal("login-form-coder")`.

`NotifierTests.swift`: `make()` gains `openRequestTerminal: { [unowned self] in self.requestTerminals.append($0) }` (new `var requestTerminals: [String] = []`). Rewrite `testAnswerActionPostsTheAnswer` into `testOpenOrchestratorActionOpensTheRequestTerminalAndNeverAnswers` (`open_orchestrator` with `["request": "req_question"]` appends `req_question`; `client.calls == []`), rewrite `testAFailedAnswerIsReportedBackWithTheTypedText` into `testNoNotificationCategoryHasATextInputAction` (`Notifier.categories.flatMap(\.actions)` contains no action with title `Answer`; `swarm.question` has exactly `[open_orchestrator]`). Update `testCategoriesAndActions` (expected `swarm.question` actions and the removed `textInput` field) and `testOtherActionsOpenTheBoardOrTerminal` (a default click with `["kind": "request.question", "request": "req_q"]` appends to `requestTerminals`, not `opened`; approvals still open the board). Add a category test: `Notifier.category(forKind:)` returns `swarm.question` for `request.question`, `request.prompt`, `request.blocker` and `swarm.approval` for `request.approve_plan`.

**10c. Watch them fail**: `swift test --filter 'AppModelTests|NotifierTests'`. Expected: compile errors `value of type AppModel has no member requestTarget`, `extra argument openRequestTerminal`.

**10d. Implement.**

`AppModel.swift`: delete `answerDrafts`, `answering`, `sendAnswer`, `resolvePrompt`, `requestTerminal`, and the `answer.failed` seeding in `handleNotificationAction` (it becomes `await notifier.handle(action: action, userInfo: userInfo, text: text)`). Add:

```swift
    public enum RequestTarget: Equatable { case terminal(String), unavailable(String) }

    /// What tapping a Needs-you row does. nil for a request with no terminal agent (approvals).
    public func requestTarget(_ r: SwarmRequest) -> RequestTarget? {
        guard r.isHITL, let name = r.terminalAgent else { return nil }
        guard let a = AgentTree.flatten(state.agents).first(where: { $0.name == name }) else {
            return .unavailable(Copy.orchestratorNotRunning)
        }
        if tmuxAlive(a) { return .terminal(name) }
        switch a.session?.state {
        case .paused, .interrupted: return .unavailable(Copy.orchestratorPaused)
        default: return .unavailable(Copy.orchestratorNotRunning)
        }
    }

    public func openRequest(_ r: SwarmRequest) async {
        if case let .terminal(name)? = requestTarget(r) { await openTerminal(name) }
    }
```

and pass to the `Notifier` init (line ~121):

```swift
                            openRequestTerminal: { [weak self] id in
                                guard let self, let r = self.state.requests.first(where: { $0.id == id }) else { return }
                                await self.openRequest(r)
                            })
```

`Notifier.swift`: remove `NotificationAction.answer`, `answerFailed`, `Copy.answer` action and the `textInput` field of `NotificationActionSpec` (and every `textInput: false` in `categories`); add `public static let openOrchestrator = "open_orchestrator"`; `swarm.question` becomes `[NotificationActionSpec(id: NotificationAction.openOrchestrator, title: Copy.openOrchestratorTerminal)]`; `category(forKind:)` gains `case "request.question", "request.prompt", "request.blocker": return "swarm.question"` (and drops the old `request.question` case); `init` gains `openRequestTerminal: @escaping @MainActor (String) async -> Void`; in `handle` add

```swift
        case NotificationAction.openOrchestrator:
            if let req = userInfo["request"] { await openRequestTerminal(req) }
```

and in `default:` check `if let req = userInfo["request"], ["request.question", "request.prompt", "request.blocker"].contains(userInfo["kind"] ?? "") { await openRequestTerminal(req) } else if let req = userInfo["request"] { openBoard(...) } else { ... }` keeping today's branches for everything else. `Copy.swift`: delete `answer`, `sendAnswer`, `answerNotSent`, `approve`. `SystemServices.swift` `register`: delete the `a.textInput ? UNTextInputNotificationAction(...) :` branch so every action is `UNNotificationAction(identifier: a.id, title: a.title, options: [.foreground])`.

`NeedsYouSection.swift`: replace `RequestRow` with:

```swift
struct RequestRow: View {
    @Bindable var model: AppModel
    let request: SwarmRequest

    var body: some View {
        if request.isHITL {
            let target = model.requestTarget(request)
            let card = VStack(alignment: .leading, spacing: 4) {
                Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
                Text(RequestLine.text(request)).lineLimit(3)
                if case let .unavailable(hint)? = target {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
            }
            if case .terminal? = target {
                Button { Task { await model.openRequest(request) } } label: {
                    HStack(alignment: .top) {
                        card
                        Spacer()
                        Image(systemName: "terminal").foregroundStyle(.secondary)
                    }
                }
                .buttonStyle(.plain)
                .help(Copy.openOrchestratorTerminal)
            } else {
                card
            }
        } else {
            VStack(alignment: .leading, spacing: 4) {
                Text("\(request.itemKey) · \(request.itemTitle)").font(.caption).foregroundStyle(.secondary)
                Text(RequestLine.text(request)).lineLimit(3)
                HStack {
                    Spacer()
                    IconButton("doc.text.magnifyingglass", help: Copy.review) { model.review(request) }
                }
            }
        }
    }
}
```

(The non-HITL branch is the existing `else` body, unchanged.)

**10e. Watch them pass**: `swift test`. Then build and look: `make app` and open the popover against the mock daemon (`swarm` mock mode per `apps/menubar/README`), or use the Task 13 live smoke.

**10f. Commit**:

```bash
git add apps/menubar/Sources/SwarmBarKit/Wire.swift apps/menubar/Sources/SwarmBarKit/AppModel.swift apps/menubar/Sources/SwarmBarKit/Notifier.swift apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift apps/menubar/Sources/SwarmBar/SystemServices.swift apps/menubar/Tests/SwarmBarTests/AppModelTests.swift apps/menubar/Tests/SwarmBarTests/NotifierTests.swift
git commit -m "feat(menubar): Needs you is read-only; a tap opens the orchestrator terminal"
```

(Client-method removal, and the two client test files, are Task 11.)

## Task 11: delete the dead write paths (15 min)

Files: `internal/httpapi/requests.go`, `internal/httpapi/requests_test.go`, `internal/runtime/requests.go`, `internal/runtime/requests_test.go`, `web/src/api.ts`, `web/src/api.test.ts`, `web/src/mock/daemon.ts`, `apps/menubar/Sources/SwarmBarKit/{DaemonClient,HTTPDaemonClient}.swift`, `apps/menubar/Tests/SwarmBarTests/{MockDaemonClientTests,HTTPDaemonClientTests}.swift`. Spec section 4.7 lists every removed symbol with its callers; Tasks 9 and 10 already removed the last UI callers.

**11a. Failing tests first (Go).**

`internal/httpapi/requests_test.go`: rewrite `TestResolvePromptRouteSendsKeysAndResolves` into `TestResolveRouteIsGone` (POST `/api/requests/<id>/resolve` returns 404); remove `TestResolvePromptRouteConflictAlreadyResolved`, `TestResolvePromptRouteNotFound`, `TestResolvePromptRouteInvalidJSON` (they exercise only the removed handler; ledger in spec 8.3).

`internal/runtime/requests_test.go`: rewrite `TestResolvePromptTransmitsKeysAndResolves` into `TestResolvePromptResolvesAndSendsNoKeys` with the new signature (`s.ResolvePrompt(ctx, req.ID, "menubar")`; assert `answered`, `RespondedVia == "menubar"`, no key for `ses.TmuxName` in `tm.keys`, second call errors); update `TestResolvePromptEmptyActionDoesNotSendKeys` to `s.ResolvePrompt(ctx, req.ID, "terminal")` with its assertions unchanged.

Run `go test ./internal/httpapi/ ./internal/runtime/ -run 'ResolveRouteIsGone|ResolvePrompt' -count=1`. Expected: 405/200 instead of 404, and a compile error on the new `ResolvePrompt` arity.

**11b. Implement.**
- `internal/httpapi/requests.go`: delete the route line `{"POST", "/api/requests/{id}/resolve", authDaemon, s.handleResolvePrompt}`, `resolvePromptBody` and `handleResolvePrompt`.
- `internal/runtime/requests.go`: change to `func (s *Store) ResolvePrompt(ctx context.Context, id, via string) (Request, error)`, delete the `action != ""` tmux block, and set `response_text` to NULL (`nullIf("")`) in the UPDATE; update the one caller, `ResolveSessionPrompts` (`s.ResolvePrompt(ctx, id, "terminal")`). Update the doc comment: it no longer sends keys.
- `web/src/api.ts`: delete `answer` and `resolvePrompt`. `web/src/mock/daemon.ts`: delete the `answer` (line ~356) and `resolve` (line ~384) cases and drop `answer|` and `|resolve` from the route regex (line ~460). `web/src/api.test.ts`: remove the `/answer` and `/resolve` stubs (lines ~102, ~105) and the `api.answer` / `api.resolvePrompt` calls (lines ~114, ~117) and their expectations from that test.
- Menubar: delete `answer(requestID:text:)` and `resolvePrompt` from the `DaemonClient` protocol, `HTTPDaemonClient`, `MockDaemonClient` (and its `resolvedPrompts`); remove the answer call and expectations from `MockDaemonClientTests` and `HTTPDaemonClientTests` (the `"answer req_question Use zod."` call-log entry, the `"POST /api/requests/req_question/answer"` route string, the `answer-request.json` body assertion at line ~95).

**11c. Watch everything pass**: `go build ./... && go test ./internal/httpapi/ ./internal/runtime/ -count=1`; `cd web && npm run typecheck && npm test -- --run`; `cd apps/menubar && swift test`.

**11d. Commit** (three commits so each toolchain's history stays readable):

```bash
git add internal/httpapi/requests.go internal/httpapi/requests_test.go internal/runtime/requests.go internal/runtime/requests_test.go
git commit -m "refactor(httpapi,runtime): remove /resolve and ResolvePrompt's tmux keys"
git add web/src/api.ts web/src/api.test.ts web/src/mock/daemon.ts
git commit -m "refactor(web): remove answer and resolvePrompt api calls"
git add apps/menubar/Sources/SwarmBarKit/DaemonClient.swift apps/menubar/Sources/SwarmBarKit/HTTPDaemonClient.swift apps/menubar/Tests/SwarmBarTests/MockDaemonClientTests.swift apps/menubar/Tests/SwarmBarTests/HTTPDaemonClientTests.swift
git commit -m "refactor(menubar): remove answer and resolvePrompt client methods"
```

## Task 12: skill text (3 min)

Files: `skills/swarm/SKILL.md`. No test (prose). Replace the three bullets under rule 5 ("Need the user or blocked?", lines 17-19) with the text in spec section 7 (last row). Check nothing else restates the old rule: `grep -rn "native question tool\|user_answer" skills/`; in `skills/swarm-orchestrator/SKILL.md:20` the sentence "ask the user (via `swarm_ask` or native question tool) and forward the response" stays valid. Commit:

```bash
git add skills/swarm/SKILL.md
git commit -m "docs(skills): HITL is orchestrator-relayed; the user answers in the terminal"
```

## Task 13: full verification and live check (20 min)

1. `go build ./... && go vet ./... && go test ./... -count=1` – all `ok`.
2. `cd web && npm run typecheck && npm test -- --run && npm run test:e2e`; `cd apps/menubar && swift test`.
3. Open a PR from `feat/deterministic-needs-you` (do not merge without the user's go-ahead).
4. Per-adapter live check (manual, once each, spec section 5): for each of Claude, Codex, Cursor, agy, spawn a top-level orchestrator and make it call its native question tool: a row appears, Needs you shows it read-only, the tool's own dialog is answered in the terminal, the row closes. Then spawn one worker under an orchestrator and make it call the same tool: expect the block text (or, where the adapter ignores blocks, record that in the spec's section 5 as "ignored" and open a follow-up).
5. After merge, redeploy per memory `swarm-local-deploy` (release symlink layout, Node 22), rebuild the menubar (`make install-app`; the fix in `docs/plans/2026-09-21-menubar-install-and-agent-statusline.md` makes it quit and reopen the app), and run the live smoke: spawn a top-level orchestrator, have it call `swarm_ask`; the popover row taps into Ghostty attached to the orchestrator; type a reply there; the row disappears.
6. Live check, read-only:

```bash
sqlite3 -readonly ~/.swarm/swarm.db "SELECT id, kind, agent_id FROM requests WHERE state='open' AND is_hitl=1;"
```

Expected: only rows whose agent has a live, paused or interrupted newest session.
7. Clean up: confirm the branch is merged (`git log feat/deterministic-needs-you -1` against `origin/main`), then from the primary checkout `git worktree remove ../agent-swarm--deterministic-needs-you`. Never `rm -rf`.

## Coverage map (spec section 9 scenario to task)

| Scenario | Task / test |
|---|---|
| 1 worker ask refused, 2 worker blocker refused | 3 `TestParentedAgentCannotOpenQuestionOrBlocker` |
| 3 parent relay | 4 `TestParentedBlockedCheckpointRelaysButOpensNoRequest` |
| 4 top-level ask | existing `TestAskQuestionOpensARequestAndNotifies` (unchanged); 8 `TestRequestWireTerminalAgent` (top-level case) |
| 5 native tool top-level | existing `TestQuestionToolInterceptionCreatesHITLRequest` (unchanged) + 6 rewritten `TestPostToolUse...ResolvesOpenQuestionRequest*`; live per adapter in 13.4 |
| 6 native tool parented | 6 `TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest`; live per adapter in 13.4 |
| 7 cross-wire gone | 6 `TestQuestionToolPostToolUseClosesOnlyTheMatchingRow`, `...WithoutToolInputClosesNothing` |
| 8 human prompt closes rows, 9 wake paste does not close | 7 `TestHumanPromptClosesOpenRowsButDaemonPromptsDoNot`, `TestIsDaemonPrompt`, `TestResolveAnsweredInTerminal...` |
| 10 rows survive pause and resume | 7 `TestHumanPromptInANewSessionClosesTheRowOfTheOldOne` |
| 11 session ends, 12 paused keeps row, 13 retry keeps row | 2 `TestSweep*` |
| 14 daemon restart heals | 2 (`withdrawOrphanedRequests` is stateless; the finished-agent test covers a row created before the sweep ever ran) |
| 15 trust dialog auto-answered | 5 `TestPromptPatternAutoAnswersOncePerSessionAndOpensNoRequest` |
| 16 permission dialog | 5 `TestReconcileNeverResolvesAPromptRowByPatternAbsence`, 6 `TestPostToolUseResolvesOnlyTheMatchingPermissionPrompt` |
| 17 terminal target | 8 `TestRequestWireTerminalAgent` |
| 18 web read-only | 9 (`QuestionView`, `Review`, `NeedsYou`, `inbox`, `App.flows` tests, e2e) |
| 19 menubar read-only | 10 (`AppModelTests`, `NotifierTests`) |
| 20 live smoke | 13.5 |
