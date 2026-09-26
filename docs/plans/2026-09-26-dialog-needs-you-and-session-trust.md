# Plan: dialog-blocked panes in Needs you + per-session workspace trust

**Spec:** `docs/specs/2026-09-26-dialog-needs-you-and-session-trust.md` (read
it first).

**Worktree:** `/Users/alexandertar/GitHub/agent-swarm-dialog-needs-you`,
branch `feat/dialog-needs-you`.

**Rules:**
- Strict TDD per task: write the failing test, run it and watch it fail, write
  the minimal code, run it and watch it pass, then commit.
- Stage explicit paths only. Never `git add -A`, never `--amend`.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

**Test run command, unless a task says otherwise:**
`go test ./internal/runtime/ -run '<TestName>' -count=1`

**Batches:**
- **Batch 1:** Tasks 1-8, runtime. One build → review loop.
- **Batch 2:** Tasks 9-14, trust. Its own loop. Task 9 (claude) needs open
  question Q1 answered first. If it is not answered by the time batch 2
  starts, do Tasks 10-14 and park Task 9.

**Fake-harness facts the tests rely on** (`internal/runtime/agents_test.go`):
- `newStore(t)` runs `watchStartup` inline (`Go: f()`).
- `fakeTmux.captures[name]` hands out one capture per poll, and the last one
  repeats.
- Each poll's `After(500ms)` advances the test clock by 500 ms, so 30 captures
  are about 15 s.
- `tm.keys` holds `"<name>|K1,K2"` strings; `tm.killed` holds names.

---

## Batch 1: runtime

### Task 1: `Dialog.Title` and titles on every existing dialog

- **Consumes:** nothing.
- **Produces:** `adapter.Dialog.Title string`.

1. **Test** (`internal/adapter/adapter_test.go`), a new test:
   ```go
   func TestEveryStartupDialogHasATitle(t *testing.T) {
   	d := Deps{Home: t.TempDir(), UserHome: t.TempDir(), Log: func(string, ...any) {}}
   	for _, a := range []Adapter{NewClaude(d), NewCodex(d), NewAgy(d), NewCursor(d), NewMuse(d)} {
   		for i, dg := range a.StartupDialogs() {
   			if dg.Title == "" {
   				t.Errorf("%s dialog %d has no Title", a.Kind(), i)
   			}
   		}
   	}
   }
   ```
   Use whatever constructor names `adapter_test.go` already uses for the five
   kinds.
2. **Run** `go test ./internal/adapter/ -run TestEveryStartupDialogHasATitle -count=1`.
   It fails to compile (no field).
3. **Implement.**
   - Add `Title string` to `Dialog` (`adapter.go:44`).
   - Set the titles exactly as in the spec's copy table:
     - `claude.go:224-227`: `Trust this project`, `Confirm local development`.
     - `codex.go:121-125`: `Trust this directory`, `Keep the current model`,
       `Hook sandbox approval`.
     - `agy.go:177`: `Trust this project`.
4. **Run.** It passes. Also run `go test ./internal/adapter/ -count=1`.
5. **Commit:** `feat(adapter): name every startup dialog`.

### Task 2: `OpenDialogPrompt` / `ResolveDialogPrompt`

- **Consumes:** the existing `AskPrompt` SQL, `ResolvePrompt`.
- **Produces:**
  ```go
  func (s *Store) OpenDialogPrompt(ctx context.Context, sessionID, title string) (Request, bool, error)
  func (s *Store) ResolveDialogPrompt(ctx context.Context, sessionID, title string) error
  ```

1. **Tests** (`internal/runtime/requests_test.go`):
   ```go
   func TestOpenDialogPromptDedupesPerSessionAndTitle(t *testing.T) {
   	s, _, _ := newStore(t)
   	ctx := context.Background()
   	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dlg", Intent: "feature", Kind: Fake, Model: "fake-1"})
   	ses, _ := s.LatestSession(ctx, a.ID)
   	r1, created1, err := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
   	if err != nil || !created1 {
   		t.Fatalf("first open: created=%v err=%v", created1, err)
   	}
   	r2, created2, err := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
   	if err != nil || created2 || r2.ID != r1.ID {
   		t.Fatalf("second open: id=%s created=%v err=%v, want %s reused", r2.ID, created2, err, r1.ID)
   	}
   	if r1.Kind != KindPrompt || !r1.IsHITL || r1.Prompt != "Trust this project" {
   		t.Fatalf("row = %+v", r1)
   	}
   	n := 0
   	for _, k := range s.Notify.(*fakeNotifier).kinds() {
   		if k == "request.prompt" {
   			n++
   		}
   	}
   	if n != 1 {
   		t.Fatalf("request.prompt raised %d times, want 1", n)
   	}
   }

   func TestResolveDialogPromptClosesOnlyThatTitle(t *testing.T) {
   	s, _, _ := newStore(t)
   	ctx := context.Background()
   	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dlg2", Intent: "feature", Kind: Fake, Model: "fake-1"})
   	ses, _ := s.LatestSession(ctx, a.ID)
   	dlg, _, _ := s.OpenDialogPrompt(ctx, ses.ID, "Trust this project")
   	perm, _ := s.AskPrompt(ctx, ses.ID, "rm -rf build", nil)
   	if err := s.ResolveDialogPrompt(ctx, ses.ID, "Trust this project"); err != nil {
   		t.Fatal(err)
   	}
   	got, _ := s.Request(ctx, dlg.ID)
   	if got.State != "answered" || got.RespondedVia == nil || *got.RespondedVia != "terminal" {
   		t.Fatalf("dialog row = %+v, want answered via terminal", got)
   	}
   	other, _ := s.Request(ctx, perm.ID)
   	if other.State != "open" {
   		t.Fatalf("permission row state = %s, want open", other.State)
   	}
   	if err := s.ResolveDialogPrompt(ctx, ses.ID, "Trust this project"); err != nil {
   		t.Fatalf("second resolve should be a no-op, got %v", err)
   	}
   }
   ```
   Adapt `s.Request` and the `RespondedVia` field names to the existing
   getters and struct in `requests.go`; check with
   `grep -n "func (s \*Store) Request(" internal/runtime/requests.go`.
2. **Run.** It fails: undefined methods.
3. **Implement** in `requests.go`, next to `AskPrompt`:
   ```go
   func (s *Store) OpenDialogPrompt(ctx context.Context, sessionID, title string) (Request, bool, error) {
   	var out Request
   	created := false
   	err := s.tx(ctx, func(tx *sql.Tx) error {
   		var id string
   		err := tx.QueryRowContext(ctx, `SELECT id FROM requests WHERE session_id = ? AND kind = 'prompt'
   			AND state = 'open' AND prompt = ? ORDER BY created_at LIMIT 1`, sessionID, title).Scan(&id)
   		if err == nil {
   			out, err = s.requestTx(ctx, tx, id)
   			return err
   		}
   		if err != sql.ErrNoRows {
   			return err
   		}
   		_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
   		if err != nil {
   			return err
   		}
   		id = ids.New("req")
   		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, is_hitl, agent_id, session_id, item_id,
   			prompt, options_json, state, created_at) VALUES (?, 'prompt', 1, ?, ?, ?, ?, '[]', 'open', ?)`,
   			id, a.ID, sessionID, a.ItemID, title, db.Millis(s.Now())); err != nil {
   			return err
   		}
   		key, err := s.itemKey(ctx, tx, a.ItemID)
   		if err != nil {
   			return err
   		}
   		created = true
   		out, err = s.finishOpen(ctx, tx, id, a.Name, key, map[string]string{"prompt": title})
   		return err
   	})
   	return out, created, err
   }

   func (s *Store) ResolveDialogPrompt(ctx context.Context, sessionID, title string) error {
   	ids, err := s.queryIDs(ctx, `SELECT id FROM requests WHERE session_id = ? AND kind = 'prompt'
   		AND state = 'open' AND prompt = ?`, sessionID, title)
   	if err != nil {
   		return err
   	}
   	for _, id := range ids {
   		if _, err := s.ResolvePrompt(ctx, id, "terminal"); err != nil {
   			return err
   		}
   	}
   	return nil
   }
   ```
4. **Run.** It passes. Then run `go test ./internal/runtime/ -run 'Prompt' -count=1`.
5. **Commit:** `feat(runtime): open and resolve one prompt row per visible dialog`.

### Task 3: `watchStartup` retries while the dialog is visible

- **Consumes:** `Dialog.Title`.
- **Produces:** the constants `dialogRetryEvery = 5 * time.Second`,
  `dialogMaxSends = 3`, `dialogEscalateAfter = 15 * time.Second`
  (`agents.go`), and `type dialogState struct{ firstSeen, lastSent time.Time; sends int; reqID string }`.

1. **Test** (`agents_test.go`):
   ```go
   func TestStartupResendsDialogKeysWhileTheDialogStaysVisible(t *testing.T) {
   	s, tm, f := newStore(t)
   	f.Dialogs = []adapter.Dialog{{Match: regexp.MustCompile(`Trust me\?`), Keys: []string{"Enter"}, Title: "Trust this project"}}
   	var caps []string
   	for i := 0; i < 24; i++ { // ~12s of polls: sends at 0s, 5s, 10s, no escalation yet
   		caps = append(caps, "Trust me?\n")
   	}
   	tm.captures["retry-me"] = append(caps, "─────\n❯ \n─────\n")
   	if _, _, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Retry me", Intent: "feature", Kind: Fake, Model: "fake-1"}); err != nil {
   		t.Fatal(err)
   	}
   	sent := 0
   	for _, k := range tm.keys {
   		if k == "retry-me|Enter" {
   			sent++
   		}
   	}
   	if sent != 3 {
   		t.Fatalf("sent %d times, want 3 (t=0,5,10s)", sent)
   	}
   }
   ```
   `TestSpawnAnswersStartupDialogsOnce` stays unchanged and must still pass:
   its dialog clears at poll 3, so there is no resend.
2. **Run.** It fails (sent = 1).
3. **Implement** in `watchStartup` (`agents.go:1249`):
   - Replace `answered := map[int]bool{}` with `st := map[int]*dialogState{}`.
   - For each dialog `i` that matches (plus `Require`):
     - Create state if it is missing (`firstSeen = now`).
     - If `len(d.Keys) > 0 && st[i].sends < dialogMaxSends && (st[i].sends == 0 || now.Sub(st[i].lastSent) >= dialogRetryEvery)`:
       send, record `lastSent`/`sends`, and log
       `startup: %s: sent %v for dialog %q (send %d of 3)`.
   - For each dialog with state that doesn't match this poll: `delete(st, i)`.
4. **Run.** It passes. Also run
   `go test ./internal/runtime/ -run 'Startup|StartupDialog|SpawnAnswers|SpawnFails' -count=1`.
5. **Commit:** `fix(runtime): resend startup-dialog keys while the dialog stays on screen`.

### Task 4: `watchStartup` escalates to a Needs-you row and waits for the user

- **Consumes:** Task 2 and Task 3.
- **Produces:** the escalation behaviour.

1. **Tests** (`agents_test.go`):
   ```go
   func TestStartupDialogThatOutlivesRetriesOpensAPromptAndWaits(t *testing.T) {
   	s, tm, f := newStore(t)
   	f.Dialogs = []adapter.Dialog{{Match: regexp.MustCompile(`Trust me\?`), Keys: []string{"Enter"}, Title: "Trust this project"}}
   	var caps []string
   	for i := 0; i < 200; i++ { // ~100s: well past the 30s stall timeout
   		caps = append(caps, "Trust me?\n")
   	}
   	tm.captures["stuck-dialog"] = append(caps, "─────\n❯ \n─────\n")
   	_, a, _, err := s.StartSpike(context.Background(), SpikeInput{Name: "Stuck dialog", Intent: "feature", Kind: Fake, Model: "fake-1"})
   	if err != nil {
   		t.Fatal(err)
   	}
   	ses, _ := s.LatestSession(context.Background(), a.ID)
   	if ses.State != Running {
   		t.Fatalf("state = %s, want running once the user cleared the dialog", ses.State)
   	}
   	var state, via string
   	if err := s.DB.QueryRow(`SELECT state, COALESCE(responded_via,'') FROM requests WHERE session_id = ? AND kind = 'prompt'`, ses.ID).Scan(&state, &via); err != nil {
   		t.Fatal(err)
   	}
   	if state != "answered" || via != "terminal" {
   		t.Fatalf("row = %s/%s, want answered/terminal", state, via)
   	}
   	if slices.Contains(s.Notify.(*fakeNotifier).kinds(), "agent.preflight_failed") {
   		t.Fatal("a human-blocked dialog must not fail the session")
   	}
   }

   func TestStartupDetectOnlyDialogOpensAPromptWithoutKeys(t *testing.T) {
   	s, tm, f := newStore(t)
   	f.Dialogs = []adapter.Dialog{{Match: regexp.MustCompile(`Do you trust this workspace\?`), Title: "Trust this workspace"}}
   	var caps []string
   	for i := 0; i < 40; i++ {
   		caps = append(caps, "Do you trust this workspace?\n")
   	}
   	tm.captures["detect-only"] = append(caps, "─────\n❯ \n─────\n")
   	_, a, _, _ := s.StartSpike(context.Background(), SpikeInput{Name: "Detect only", Intent: "feature", Kind: Fake, Model: "fake-1"})
   	ses, _ := s.LatestSession(context.Background(), a.ID)
   	if len(tm.keys) != 0 {
   		t.Fatalf("keys = %v, want none for a detect-only dialog", tm.keys)
   	}
   	var n int
   	s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE session_id = ? AND prompt = 'Trust this workspace'`, ses.ID).Scan(&n)
   	if n != 1 {
   		t.Fatalf("rows = %d, want 1", n)
   	}
   }
   ```
2. **Run.** Both fail: the first gets `preflight_failed`, the second has no
   row.
3. **Implement** in `watchStartup`:
   - When a matching dialog has `now.Sub(firstSeen) >= dialogEscalateAfter`
     and `reqID == ""`: call `s.OpenDialogPrompt(ctx, ses.ID, d.Title)`, store
     the id, and log `startup: %s: dialog %q still visible after 15s, opened %s`.
     On error, log and continue.
   - When any state has `reqID != ""`: set `stallDeadline = now + startupStallTimeout`
     and `ceiling = now + startupCeiling` each poll, so the loop waits for the
     human.
   - When a state is deleted (dialog gone) and `reqID != ""`:
     `s.ResolveDialogPrompt(ctx, ses.ID, d.Title)`.
   - On the Idle/Busy → Running path: first resolve every open state's row.
   - The loop still exits on a `Capture` error, `ctx.Done()`, or the session
     leaving `Spawning`. These are the existing checks, unchanged.
4. **Run** both new tests, then the full startup set from Task 3.
5. **Commit:** `feat(runtime): a startup dialog nobody can clear becomes a Needs-you prompt`.

### Task 5: reconcile strips ANSI, retries, escalates, and skips spawning sessions

- **Consumes:** Task 2 and the constants from Task 3.
- **Produces:**
  - `promptTick(sessionID, title string, hasKeys bool, now time.Time) promptStep`
    and `clearPromptState(sessionID, title string)`;
  - the `Store.promptState map[string]*dialogState` field in `model.go`,
    replacing `promptAnswered`.

1. **Tests** (`reconcile_test.go`):
   - **Rename** `TestPromptPatternAutoAnswersOncePerSessionAndOpensNoRequest`
     to `TestPromptPatternAutoAnswersOnceWhenTheDialogClears`. Change its
     captures so the dialog shows on tick 1 only, then the idle prompt:
     ```go
     tm.captures[a.Name] = []string{"Some output\nDo you trust this? [y/n]\n", "─────\n❯ \n─────\n"}
     ```
     The assertions stay the same (1 press, 0 rows). This is a port, not a
     deletion.
   - **Add:**
     ```go
     func TestPromptPatternRetriesThenOpensARowAndResolvesWhenCleared(t *testing.T) {
     	s, tm, clk := clockStore(t)
     	ctx := context.Background()
     	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "PromptEsc", Intent: "feature", Kind: Fake, Model: "fake-1"})
     	ses, _ := s.LatestSession(ctx, a.ID)
     	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
     	tm.env[a.Name] = map[string]string{"SWARM_SESSION": ses.ID}
     	s.Adapters[Fake].(*adapter.Fake).PromptMatchers = []adapter.PromptMatcher{
     		{Match: regexp.MustCompile(`Do you trust this\?`), Title: "Trust prompt", Action: "Enter"},
     	}
     	// per-word ANSI, like Claude 2.1.278 draws highlighted text
     	tm.captures[a.Name] = []string{"\x1b[1mDo\x1b[0m \x1b[1myou\x1b[0m trust this?\n"}
     	for i := 0; i < 5; i++ { // t = 0,5,10,15,20s
     		if err := s.Reconcile(ctx); err != nil {
     			t.Fatal(err)
     		}
     		clk.Advance(5 * time.Second)
     	}
     	pressed := 0
     	for _, k := range tm.keys {
     		if k == a.Name+"|Enter" {
     			pressed++
     		}
     	}
     	if pressed != 3 {
     		t.Fatalf("pressed %d, want 3", pressed)
     	}
     	var state string
     	s.DB.QueryRow(`SELECT state FROM requests WHERE session_id = ? AND prompt = 'Trust prompt'`, ses.ID).Scan(&state)
     	if state != "open" {
     		t.Fatalf("row state = %q, want open", state)
     	}
     	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"}
     	if err := s.Reconcile(ctx); err != nil {
     		t.Fatal(err)
     	}
     	s.DB.QueryRow(`SELECT state FROM requests WHERE session_id = ? AND prompt = 'Trust prompt'`, ses.ID).Scan(&state)
     	if state != "answered" {
     		t.Fatalf("row state = %q, want answered once the dialog cleared", state)
     	}
     }

     func TestReconcileLeavesSpawningSessionsDialogsToWatchStartup(t *testing.T) {
     	// Build a live row in state spawning, with the prompt visible.
     	// Assert: no keys sent, no row opened.
     }
     ```
   - **For the spawning test:** set the session to spawning with
     `UPDATE sessions SET state='spawning' WHERE id=?` after `StartSpike`, then
     reconcile once. Assert `len(tm.keys) == 0`, and that the requests count
     for the session is 0.
   - **Keep** `TestPromptPatternRequireGuardsAutoAnswer` unchanged; it must
     pass.
2. **Run.** The new tests fail: 1 press, no row (the raw match misses the ANSI
   capture), and keys are sent for the spawning session.
3. **Implement:**
   - **`model.go:396-398`:** replace `promptAnswered map[string]bool` with
     `promptState map[string]*dialogState`. Update the comment to "per
     (session|title) retry/escalation state for visible prompts; in-memory
     like lastAliveAt".
   - **`reconcile.go`:** replace `markPromptAnswered` with:
     ```go
     type promptStep struct{ Send, Escalate bool }

     func (s *Store) promptTick(sessionID, title string, hasKeys bool, now time.Time) promptStep {
     	s.bookkeepingMu.Lock()
     	defer s.bookkeepingMu.Unlock()
     	if s.promptState == nil {
     		s.promptState = map[string]*dialogState{}
     	}
     	k := sessionID + "|" + title
     	st := s.promptState[k]
     	if st == nil {
     		st = &dialogState{firstSeen: now}
     		s.promptState[k] = st
     	}
     	var out promptStep
     	if hasKeys && st.sends < dialogMaxSends && (st.sends == 0 || now.Sub(st.lastSent) >= dialogRetryEvery) {
     		st.sends++
     		st.lastSent = now
     		out.Send = true
     	}
     	if st.reqID == "" && now.Sub(st.firstSeen) >= dialogEscalateAfter {
     		st.reqID = "pending" // set to the real id by the caller
     		out.Escalate = true
     	}
     	return out
     }

     // openPromptTitles returns the titles with state for this session (for resolve-on-clear).
     func (s *Store) openPromptTitles(sessionID string) []string { /* scan keys with prefix sessionID+"|" */ }
     func (s *Store) clearPromptState(sessionID, title string) { /* delete under lock */ }
     ```
   - **`resolveAlive`** (`reconcile.go:817-835`):
     - If `r.State == Spawning`, skip the prompt block entirely.
     - `plain := stripANSI(capture)`; match `m.Match` and `m.Require` on
       `plain`.
     - Drop the `m.Action == ""` skip; `hasKeys := m.Action != ""`.
     - On `Send`: `Keys(strings.Split(m.Action, "+")...)`, then log
       `reconcile: %s: sent %v for prompt %q (send %d of 3)`.
     - On `Escalate`: `OpenDialogPrompt(r.SessionID, m.Title)`, then log
       `reconcile: %s: prompt %q still visible after 15s, opened %s`.
     - Record the matched title. After the loop, for every title in
       `openPromptTitles(r.SessionID)` other than the matched one, call
       `ResolveDialogPrompt` and `clearPromptState`.
     - `idle` is still computed first. An idle pane matches no prompt, so its
       titles clear.
4. **Run** `go test ./internal/runtime/ -run 'PromptPattern|Reconcile' -count=1`.
5. **Commit:** `fix(runtime): reconcile matches prompts on stripped text, retries, and escalates to Needs you`.

### Task 6: `failSession` kills the pane and writes a readable reason

- **Produces:** `func firstReadableLine(pane string) string`.

1. **Tests** (`agents_test.go`):
   ```go
   func TestFirstReadableLineSkipsAnsiRules(t *testing.T) {
   	pane := "\x1b[38;5;220m────────\n\x1b[39m \x1b[1mAccessing\x1b[0m workspace:\n"
   	if got := firstReadableLine(pane); got != "Accessing workspace:" {
   		t.Fatalf("got %q", got)
   	}
   	if got := firstReadableLine("────\n   \n"); got != "" {
   		t.Fatalf("got %q, want empty", got)
   	}
   }
   ```
   Also extend `TestStartupTimesOutAfterThirtySeconds` (keep every existing
   assertion) with:
   ```go
   if !slices.Contains(tm.killed, "stuck") {
   	t.Fatalf("killed = %v, want the failed pane killed", tm.killed)
   }
   ```
   plus a check that the raised `agent.preflight_failed` `reason` arg equals
   `"Loading… Couldn't start agent."`. Use the `raised` slice of
   `fakeNotifier`.
2. **Run.** It fails.
3. **Implement:**
   - `firstReadableLine`: split `stripANSI(pane)` on `\n`, `TrimSpace` each
     line, and return the first line containing a rune with
     `unicode.IsLetter || unicode.IsDigit`, truncated to 120 runes plus `…`.
   - In `failSession`, replace the `firstLine` loop with `firstReadableLine(paneText)`.
   - After `s.tx(...)` succeeds: `if err := s.Tmux.Kill(ctx, ses.TmuxName); err != nil { s.logf("failSession: kill %s: %v", ses.TmuxName, err) }`.
     Keep the tx error as the return value.
4. **Run** `-run 'FirstReadable|Startup|Preflight|FailSession' -count=1`.
   `TestFailSessionRelaysToParent` must still pass.
5. **Commit:** `fix(runtime): failSession kills the pane and names the first readable line`.

### Task 7: reaper kills panes of finished and acknowledged agents

- **Produces:** `func (s *Store) finishedAgentTmuxNames(ctx context.Context) (map[string]bool, error)`.

1. **Tests** (`reconcile_test.go`):
   ```go
   func TestReconcileKillsLivePaneOfAnAcknowledgedAgent(t *testing.T) {
   	s, tm, _ := clockStore(t)
   	ctx := context.Background()
   	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Old pane", Intent: "feature", Kind: Fake, Model: "fake-1"})
   	ses, _ := s.LatestSession(ctx, a.ID)
   	s.DB.Exec(`UPDATE sessions SET state = 'failed' WHERE id = ?`, ses.ID)
   	s.DB.Exec(`UPDATE agents SET state = 'acknowledged' WHERE id = ?`, a.ID)
   	panes(tm, Pane{Session: a.Name, Command: "claude"})
   	if err := s.Reconcile(ctx); err != nil {
   		t.Fatal(err)
   	}
   	if !slices.Contains(tm.killed, a.Name) {
   		t.Fatalf("killed = %v, want %s", tm.killed, a.Name)
   	}
   }

   func TestReconcileKeepsLivePaneOfAnActiveAgentsCrashedSession(t *testing.T) {
   	// same setup, sessions.state = 'crashed', agents.state stays 'active' -> not killed (P0-crash-1)
   }
   ```
2. **Run.** The first fails.
3. **Implement** in `reconcile.go:185-201`:
   - Load `finished, err := s.finishedAgentTmuxNames(ctx)` next to
     `terminalTmux`.
   - In the loop, before the `terminalTmux` branch:
     `if finished[p.Session] { kill; s.logf("reconcile: killed pane %s of finished agent %s", p.Session, p.Session); continue }`.
   - The query is
     `SELECT DISTINCT s.tmux_name FROM sessions s JOIN agents a ON a.id = s.agent_id WHERE a.state IN ('finished','acknowledged')`.
   - `known` (live sessions) is checked first, so a retried agent reusing the
     name is never hit.
4. **Run** `-run 'Reconcile' -count=1`.
5. **Commit:** `fix(runtime): reap panes left behind by finished and acknowledged agents`.

### Task 8: batch 1 full check and review hand-off

1. Run `go build ./... && go vet ./... && go test ./... -count=1` and paste
   the tail of the output into the review package.
2. Request review (Opus reviewer). Scope: `git diff origin/main...HEAD -- internal/`.
   Checklist:
   - no test deleted;
   - the loop exits on a session state change;
   - no double key press between `watchStartup` and reconcile;
   - dedupe.

---

## Batch 2: per-session trust

Probe fixtures live in `internal/adapter/testdata/trust/`, one `.txt` per
captured dialog. Copy the plain-text captures from the spec's probe table:
`claude-trust.txt`, `codex-trust-folder.txt`, `agy-trust.txt`,
`cursor-trust.txt`, `muse-trust.txt`. They are used by the pattern tests.

### Task 9: claude pre-trusts `Spec.Cwd` in `~/.claude.json` (blocked on Q1)

- **Produces:** `func (c *Claude) trustClaudeWorkspace(cwd string) error`,
  called at the top of `Launch` and `Resume` (errors logged via
  `c.d.Log("claude: pre-trust %s: %v", cwd, err)`, never returned).

1. **Tests** (`claude_test.go`):
   - `TestClaudeLaunchPreTrustsCwdAndKeepsOtherEntries`:
     - Seed `UserHome/.claude.json` with
       `{"numStartups":3,"projects":{"/x":{"allowedTools":["a"]},"<cwd>":{"lastCost":1}}}`
       and call `Launch(Spec{Cwd: cwd, ...})`.
     - Assert `projects[cwd].hasTrustDialogAccepted == true`,
       `projects[cwd].lastCost == 1`, `projects["/x"]` byte-identical,
       `numStartups == 3`, the file mode is unchanged, and no
       `.claude.json.lock` is left behind.
   - `TestClaudePreTrustIsIdempotent`: a second `Launch` leaves the mtime
     unchanged.
   - `TestClaudePreTrustAddsRealpathForSymlinkedCwd`: when the cwd is a
     symlink, both keys are present.
   - `TestClaudePreTrustWaitsForAStaleLock`:
     - Pre-create `.claude.json.lock` with an mtime 20 s old; `Launch`
       succeeds and removes it.
     - A fresh lock held throughout means the write is skipped, `Launch`
       still succeeds, and a log line is recorded.
   - `TestClaudeTrustPatternMatchesProbeFixture`: `claudeTrust` and
     `claudeTrustYes` match `testdata/trust/claude-trust.txt`.
2. **Run** `go test ./internal/adapter/ -run 'ClaudePreTrust|ClaudeLaunchPreTrust|ClaudeTrustPattern' -count=1`.
   It fails.
3. **Implement** the protocol from the spec §E exactly: mkdir lock, the 2 s
   wait, the 10 s stale rule, read and merge with `map[string]json.RawMessage`,
   re-read-compare up to 3 times, `writeFileAtomic` with the existing mode.
4. **Run.** It passes.
5. **Commit:** `feat(claude): pre-trust each session's workspace before launch`.

### Task 10: codex per-launch trust and the 0.157 dialog wording

- **Produces:** `func writeCodexTrust(codexHome, cwd string) error`, and
  `codexTrustFolder = regexp.MustCompile(`Trust this folder\?`)` with Require
  `Trust and continue`.

1. **Tests** (`codex_test.go`):
   - **Port** `TestCodexTrustFolderIsAStructuredIdempotentEdit` to
     `TestCodexLaunchWritesPerLaunchTrustIdempotently`. It keeps the same
     assertions (structured merge keeps other keys, and a second launch
     doesn't rewrite), against
     `<Home>/run/launch/<ses>/codex-home/config.toml`. Pre-seed that file
     with `[tui]\nscreen_reader_detection_done = true` to prove the merge.
   - `TestCodexNewTrustDialogIsAnsweredWithEnter`: `StartupDialogs()` contains
     a dialog matching `testdata/trust/codex-trust-folder.txt`, with
     `Keys == ["Enter"]` and `Title == "Trust this folder"`. `PromptPatterns()`
     has the same entry with `Action: "Enter"`.
2. **Run.** It fails.
3. **Implement:**
   - Call `writeCodexTrust(codexHome, s.Cwd)` at the end of `setupEnv`,
     using `toml.Unmarshal`/`Marshal` as the old `TrustFolder` did.
   - Key it by `s.Cwd`, plus `filepath.EvalSymlinks(s.Cwd)` if that differs.
   - Delete `Codex.TrustFolder`.
   - Add the new dialog and matcher.
4. **Run** `go test ./internal/adapter/ -run Codex -count=1`.
5. **Commit:** `fix(codex): trust the workspace in the per-launch CODEX_HOME and match 0.157's dialog`.

### Task 11: agy per-launch `settings.json`

- **Produces:** `func linkAgyCLIDir(real, dst, cwd string) error`.

1. **Tests** (`agy_test.go`):
   - `TestAgyLaunchTrustsCwdInAPerLaunchSettingsCopy`:
     - Seed the real `antigravity-cli/` with `settings.json`
       (`{"trustedWorkspaces":["/a"],"theme":"x"}`),
       `antigravity_state.pbtxt` and `conversations/`.
     - After `Launch`, the `agy-home` copy is a real dir; `settings.json` is
       regular with `trustedWorkspaces == ["/a", cwd]` and `theme` kept;
       every other entry is a symlink to the real path; the real
       `settings.json` is unchanged.
   - `TestAgyLaunchLeavesALegacyWholeDirSymlinkAlone`: `dst` already a
     symlink means it is not replaced, and a log line is recorded.
   - `TestAgyLaunchWritesMinimalSettingsWhenRealIsMissing`.
   - The existing `setupEnv` tests that asserted the whole-dir symlink are
     updated to assert the per-entry layout (`grep -n "antigravity-cli" internal/adapter/agy_test.go`).
     Port them; do not delete them.
2. **Run.** It fails.
3. **Implement** per spec §E. Replace `agy.go:54-63`.
4. **Run** `go test ./internal/adapter/ -run Agy -count=1`.
5. **Commit:** `feat(agy): trust each session's workspace in a per-launch settings copy`.

### Task 12: cursor and muse detect-only patterns, and flag guards

1. **Tests:**
   - `cursor_test.go`:
     - `TestCursorArgvAlwaysTrustsTheWorkspace`: `Launch` and `Resume` argv
       contain `--trust` and `--workspace <cwd>`.
     - `TestCursorTrustDialogIsDetectOnly`: `PromptPatterns()` has a matcher
       with `Title == "Trust this workspace"` and `Action == ""` that matches
       `testdata/trust/cursor-trust.txt`, and `StartupDialogs()` has a
       keyless `Dialog` with the same title.
   - `muse_test.go`:
     - `TestMuseArgvAlwaysTrustsTheWorkspace`: `--trust-workspace` on
       `Launch` and `Resume`.
     - `TestMuseTrustDialogIsDetectOnly`: the same shape, matching
       `muse-trust.txt`.
2. **Run.** It fails.
3. **Implement:**
   - `cursor.go:115-121`: a detect-only `Dialog`, plus a `PromptMatcher`
     (Match `Trust this workspace`, Require `\[q\] Quit`).
   - `muse.go:360-363`: the same, with Match `Do you trust this workspace\?`.
     Remove the `TODO(probe)` comment.
4. **Run** `go test ./internal/adapter/ -count=1`.
5. **Commit:** `feat(adapter): surface cursor and muse trust dialogs if their flags ever stop working`.

### Task 13: drop `TrustFolder` from the interface

1. **Test:** in `adapter_test.go:66`, remove the `TrustFolder` call. The
   method is gone; the `ForgetFolder` assertion stays. `go build ./...` is
   the failing check until step 3.
2. **Run** `go build ./...`. It fails once the interface method is removed
   and callers remain.
3. **Implement:**
   - Remove `TrustFolder` from `Adapter` (`adapter.go:119`) and from `base`
     (`:168`).
   - Remove `_ = ad.TrustFolder(ctx, cwd)` (`agents.go:1031`).
   - Remove the `Fake` override if there is one.
4. **Run** `go build ./... && go test ./internal/... -count=1`.
5. **Commit:** `refactor(adapter): trust moves into Launch/Resume; drop TrustFolder`.

### Task 14: batch 2 full check, live verification, and review

1. Run `go build ./... && go vet ./... && go test ./... -count=1`.
2. **Live** (after merge, `make install-daemon`): the spec's "Live" steps.
   - Spawn a claude, codex and agy worker; none shows a trust dialog.
   - Check the three trust artefacts.
   - Re-run `ls -la` on an agy-home after an agy session finishes, to confirm
     the entries are still symlinks (Q2).
3. Request review (Opus reviewer). Scope: `git diff <batch-1-tip>...HEAD`.
