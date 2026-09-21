# HITL Resolution Relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement bidirectional resolution relay for human-in-the-loop (HITL) requests so that resolving prompts or answering questions in the terminal/session auto-clears them in Swarm, and resolving prompts from Menubar/Board delivers keypresses to tmux and clears them immediately.

**Architecture:** 
1. The runtime reconciler captures the tmux pane during `resolveAlive` and auto-resolves open `prompt` requests when their pattern is no longer present.
2. The hook handler intercepts `PostToolUse` for native question tools and auto-resolves open `question` requests with the user's answer.
3. The store exposes `ResolvePrompt` (which transmits keystrokes to tmux via `Tmux.Keys` before resolving) and `ResolveQuestion`.
4. HTTP API exposes `POST /api/requests/:id/resolve`.
5. Menubar and Web Board display an "Approve" button alongside "Open Terminal" for prompts.

**Tech Stack:** Go (SQLite, tmux, HTTP API), Swift (SwiftUI, async/await), TypeScript (React, Vitest).

**Spec:** `docs/specs/2026-09-21-hitl-resolution-relay.md`

## Global Constraints
- Every substantive change must be covered by tests before implementation (strict TDD).
- Do not enqueue artificial `"user_action"` inbox messages from automated daemon paths (`reconciler` or `PostToolUse`).
- Never delete or disable existing tests.
- Maintain ADHD-friendly concise communication.

## Review Focus
1. Prompt already answered in terminal before user clicks "Approve" in UI: `ResolvePrompt` must handle already resolved requests gracefully with conflict/idempotency.
2. Tmux session dead or detached when user clicks "Approve": `ResolvePrompt` must still resolve the request and not crash on missing tmux pane.
3. Multiple prompt patterns configured: Reconciler must check each open prompt request against its specific matching pattern, not just any pattern.
4. `PostToolUse` fired for a non-question tool: Must not attempt to resolve question requests.
5. Menubar draft state or UI desync: Resolving via UI or terminal must update the badge and list via standard event streaming / SSE.

---

### Task 1: Schema Migration & Store Methods (`ResolvePrompt`, `ResolveQuestion`)

**Files:**
- Create: `internal/db/schema/0005_hitl_terminal_via.sql`
- Modify: `internal/db/schema/0001_init.sql`
- Modify: `internal/runtime/requests.go`
- Test: `internal/runtime/requests_test.go`

**Interfaces:**
- Consumes: `Store.Tmux`, `Store.tx`, `Store.requestTx`, `Store.RequestWireTx`, `Store.Events`
- Produces:
  ```go
  func (s *Store) ResolvePrompt(ctx context.Context, id, action, via string) (Request, error)
  func (s *Store) ResolveQuestion(ctx context.Context, id, answer, via string) (Request, error)
  ```

- [ ] **Step 1: Write the failing test**

In `internal/runtime/requests_test.go`:
```go
func TestResolvePromptTransmitsKeysAndResolves(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	_, a, ses, _ := s.StartSpike(ctx, SpikeInput{Name: "Prompt spike", Intent: "feature", Kind: Fake, Model: "fake-1"})

	req, err := s.AskPrompt(ctx, ses.ID, "Trust folder?", []string{"Enter"})
	if err != nil {
		t.Fatalf("AskPrompt failed: %v", err)
	}

	resolved, err := s.ResolvePrompt(ctx, req.ID, "Enter", "menubar")
	if err != nil {
		t.Fatalf("ResolvePrompt failed: %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("expected state answered, got %s", resolved.State)
	}
	if resolved.RespondedVia != "menubar" {
		t.Fatalf("expected responded_via menubar, got %s", resolved.RespondedVia)
	}
	// Verify keys were sent to tmux
	if len(tm.keys[ses.TmuxName]) == 0 || tm.keys[ses.TmuxName][0] != "Enter" {
		t.Fatalf("expected Enter sent to tmux, got %v", tm.keys[ses.TmuxName])
	}
}

func TestResolveQuestionFromTerminal(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, ses, _ := s.StartSpike(ctx, SpikeInput{Name: "Question spike", Intent: "feature", Kind: Fake, Model: "fake-1"})

	req, err := s.AskQuestion(ctx, ses.ID, "Choose option", []string{"A", "B"})
	if err != nil {
		t.Fatalf("AskQuestion failed: %v", err)
	}

	resolved, err := s.ResolveQuestion(ctx, req.ID, "A", "terminal")
	if err != nil {
		t.Fatalf("ResolveQuestion failed: %v", err)
	}
	if resolved.State != "answered" {
		t.Fatalf("expected state answered, got %s", resolved.State)
	}
	if resolved.ResponseText != "A" {
		t.Fatalf("expected response_text A, got %s", resolved.ResponseText)
	}
	if resolved.RespondedVia != "terminal" {
		t.Fatalf("expected responded_via terminal, got %s", resolved.RespondedVia)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/runtime/... -run "TestResolvePromptTransmitsKeysAndResolves|TestResolveQuestionFromTerminal"`
Expected: FAIL with undefined `ResolvePrompt` / `ResolveQuestion`.

- [ ] **Step 3: Write minimal implementation**

1. Create `internal/db/schema/0005_hitl_terminal_via.sql` allowing `'terminal'` in `responded_via`.
2. In `internal/runtime/requests.go`:
Implement `ResolvePrompt` and `ResolveQuestion`:
```go
// ResolvePrompt resolves an open prompt request. If action is specified and tmux is present,
// it transmits the keystrokes to the session's tmux pane.
func (s *Store) ResolvePrompt(ctx context.Context, id, action, via string) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		// Transmit keys to tmux if action specified
		if action != "" && req.SessionID != "" && s.Tmux != nil {
			var tmuxName string
			if err := tx.QueryRowContext(ctx, `SELECT tmux_name FROM sessions WHERE id = ?`, req.SessionID).Scan(&tmuxName); err == nil && tmuxName != "" {
				_ = s.Tmux.Keys(ctx, tmuxName, action)
			}
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'answered', response_text = ?,
			responded_via = ?, responded_at = ? WHERE id = ?`,
			nullIf(action), nullIf(via), db.Millis(now), id); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ResolveQuestion resolves an open question request with response text and via attribution.
func (s *Store) ResolveQuestion(ctx context.Context, id, answer, via string) (Request, error) {
	var out Request
	err := s.tx(ctx, func(tx *sql.Tx) error {
		req, err := s.requestTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if req.State != "open" {
			return &items.Error{Code: items.CodeConflict, Message: "Already resolved."}
		}
		now := s.Now()
		if _, err := tx.ExecContext(ctx, `UPDATE requests SET state = 'answered', response_text = ?,
			responded_via = ?, responded_at = ? WHERE id = ?`,
			nullIf(answer), nullIf(via), db.Millis(now), id); err != nil {
			return err
		}
		key, err := s.itemKey(ctx, tx, req.ItemID)
		if err != nil {
			return err
		}
		w, err := s.RequestWireTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := s.Events.Append(ctx, tx, events.RequestResolved, w); err != nil {
			return err
		}
		if err := s.Items.ReconcileTx(ctx, tx, key); err != nil {
			return err
		}
		out, err = s.requestTx(ctx, tx, id)
		return err
	})
	return out, err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/runtime/... -run "TestResolvePromptTransmitsKeysAndResolves|TestResolveQuestionFromTerminal"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/db/schema/0005_hitl_terminal_via.sql internal/runtime/requests.go internal/runtime/requests_test.go
git commit -m "feat(runtime): add ResolvePrompt and ResolveQuestion store methods"
```

---

### Task 2: Reconciler Auto-Detection for Prompts (`reconcile.go`)

**Files:**
- Modify: `internal/runtime/reconcile.go:540-570`
- Test: `internal/runtime/reconcile_test.go`

**Interfaces:**
- Consumes: `ad.PromptPatterns()`, `s.Tmux.Capture`, `s.ResolvePrompt`
- Produces: Auto-resolution of open prompt requests in `resolveAlive`

- [ ] **Step 1: Write the failing test**

In `internal/runtime/reconcile_test.go`:
```go
func TestReconcileAutoResolvesPromptWhenDismissedInTerminal(t *testing.T) {
	s, tm, ad := newStore(t)
	ctx := context.Background()
	_, _, ses, _ := s.StartSpike(ctx, SpikeInput{Name: "Prompt auto resolve", Intent: "feature", Kind: Fake, Model: "fake-1"})

	ad.Prompts = []adapter.PromptMatcher{
		{Match: regexp.MustCompile(`Do you trust this\?`), Title: "Trust prompt", Action: "Enter"},
	}
	// Initial capture has the prompt
	tm.captures[ses.TmuxName] = []string{"Do you trust this? [y/n]\n"}

	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var reqID, state string
	err := s.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'prompt'`,
		ses.ID).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open prompt request, got err=%v, state=%s", err, state)
	}

	// User approved in terminal; prompt is gone from capture
	tm.captures[ses.TmuxName] = []string{"Folder trusted. Proceeding...\n"}

	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	err = s.DB.QueryRowContext(ctx, `SELECT state, responded_via FROM requests WHERE id = ?`, reqID).Scan(&state, &reqID)
	if err != nil || state != "answered" {
		t.Fatalf("expected state answered, got err=%v, state=%s", err, state)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/runtime/... -run "TestReconcileAutoResolvesPromptWhenDismissedInTerminal"`
Expected: FAIL (state remains `open` after second reconcile).

- [ ] **Step 3: Write minimal implementation**

In `internal/runtime/reconcile.go`:
In `resolveAlive`:
When checking prompts:
```go
		if !idle {
			for _, matcher := range ad.PromptPatterns() {
				if matcher.Match != nil && matcher.Match.MatchString(capture) {
					var openPrompt int
					err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests
						WHERE session_id = ? AND kind = 'prompt' AND state = 'open' AND prompt = ?`,
						r.SessionID, matcher.Title).Scan(&openPrompt)
					if err == nil && openPrompt == 0 {
						var opts []string
						if matcher.Action != "" {
							opts = []string{matcher.Action}
						}
						_, _ = s.AskPrompt(ctx, r.SessionID, matcher.Title, opts)
					}
					break
				}
			}
		}

		// Auto-resolve any open prompt request whose pattern is no longer present in capture
		rows, err := s.DB.QueryContext(ctx, `SELECT id, prompt FROM requests
			WHERE session_id = ? AND kind = 'prompt' AND state = 'open'`, r.SessionID)
		if err == nil {
			var toResolve []string
			for rows.Next() {
				var reqID, pText string
				if err := rows.Scan(&reqID, &pText); err == nil {
					stillActive := false
					for _, matcher := range ad.PromptPatterns() {
						if matcher.Title == pText && matcher.Match != nil && matcher.Match.MatchString(capture) {
							stillActive = true
							break
						}
					}
					if !stillActive {
						toResolve = append(toResolve, reqID)
					}
				}
			}
			rows.Close()
			for _, reqID := range toResolve {
				_, _ = s.ResolvePrompt(ctx, reqID, "", "terminal")
			}
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/runtime/... -run "TestReconcileAutoResolvesPromptWhenDismissedInTerminal"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/reconcile.go internal/runtime/reconcile_test.go
git commit -m "feat(runtime): auto-resolve prompts in reconciler when dismissed in terminal"
```

---

### Task 3: Hook `PostToolUse` Auto-Detection for Native Questions (`handler.go`)

**Files:**
- Modify: `internal/hook/handler.go:412-435`
- Test: `internal/hook/handler_test.go`

**Interfaces:**
- Consumes: `h.RT.ResolveQuestion`
- Produces: Auto-resolution of open question requests when native question tools finish

- [ ] **Step 1: Write the failing test**

In `internal/hook/handler_test.go`:
```go
func TestPostToolUseResolvesOpenQuestionRequest(t *testing.T) {
	h, rt, ses := newTestHandler(t)
	ctx := context.Background()

	// Intercept ask_question in PreToolUse opens request
	input := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_input":{"questions":[{"question":"Which database?"}]}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PreToolUse", ses, input)

	var reqID, state string
	err := rt.DB.QueryRowContext(ctx, `SELECT id, state FROM requests WHERE session_id = ? AND kind = 'question'`, ses.ID).Scan(&reqID, &state)
	if err != nil || state != "open" {
		t.Fatalf("expected open question request, got err=%v, state=%s", err, state)
	}

	// Tool finishes; PostToolUse fires
	postInput := []byte(`{"session_id":"` + ses.ID + `","tool_name":"ask_question","tool_response":{"answer":"PostgreSQL"}}`)
	_, _ = h.Handle(ctx, runtime.Agy, "PostToolUse", ses, postInput)

	err = rt.DB.QueryRowContext(ctx, `SELECT state, response_text, responded_via FROM requests WHERE id = ?`, reqID).Scan(&state, &reqID, &state)
	if err != nil || state != "answered" {
		t.Fatalf("expected request answered, got err=%v, state=%s", err, state)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/hook/... -run "TestPostToolUseResolvesOpenQuestionRequest"`
Expected: FAIL (state remains `open`).

- [ ] **Step 3: Write minimal implementation**

In `internal/hook/handler.go`:
In `case "PostToolUse":`:
```go
		isQuestionTool := in.ToolName == "ask_question" ||
			in.ToolName == "AskUserQuestion" ||
			in.ToolName == "request_user_input" ||
			in.ToolName == "experimental_request_user_input"

		if isQuestionTool && h.RT != nil && s.ID != "" {
			var reqID string
			if err := h.DB.QueryRowContext(ctx, `SELECT id FROM requests
				WHERE session_id = ? AND kind = 'question' AND state = 'open'
				ORDER BY created_at DESC LIMIT 1`, s.ID).Scan(&reqID); err == nil && reqID != "" {
				answer := extractToolResponseText(in.ToolResponse)
				if answer == "" {
					answer = "Resolved in terminal"
				}
				_, _ = h.RT.ResolveQuestion(ctx, reqID, answer, "terminal")
			}
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/hook/... -run "TestPostToolUseResolvesOpenQuestionRequest"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hook/handler.go internal/hook/handler_test.go
git commit -m "feat(hook): resolve open question request on PostToolUse"
```

---

### Task 4: HTTP API Route `POST /api/requests/:id/resolve` (`httpapi/requests.go`)

**Files:**
- Modify: `internal/httpapi/requests.go`
- Test: `internal/httpapi/requests_test.go`

**Interfaces:**
- Consumes: `s.RT.ResolvePrompt`
- Produces: `POST /api/requests/:id/resolve` endpoint

- [ ] **Step 1: Write the failing test**

In `internal/httpapi/requests_test.go`:
```go
func TestResolvePromptRouteSendsKeysAndResolves(t *testing.T) {
	api, rt, tm := newTestAPI(t)
	ctx := context.Background()
	_, a, ses, _ := rt.StartSpike(ctx, SpikeInput{Name: "HTTP prompt", Intent: "feature", Kind: Fake, Model: "fake-1"})
	req, _ := rt.AskPrompt(ctx, ses.ID, "Confirm delete?", []string{"y"})

	body := `{"action":"y","via":"board"}`
	resp := api.post(t, "/api/requests/"+req.ID+"/resolve", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	wire, _ := rt.RequestWire(ctx, req.ID)
	if wire.State != "answered" || wire.RespondedVia != "board" {
		t.Fatalf("expected state=answered, via=board, got state=%s, via=%s", wire.State, wire.RespondedVia)
	}
	if len(tm.keys[ses.TmuxName]) == 0 || tm.keys[ses.TmuxName][0] != "y" {
		t.Fatalf("expected 'y' sent to tmux, got %v", tm.keys[ses.TmuxName])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/httpapi/... -run "TestResolvePromptRouteSendsKeysAndResolves"`
Expected: FAIL (404 Not Found).

- [ ] **Step 3: Write minimal implementation**

In `internal/httpapi/requests.go`:
Register `POST /api/requests/{id}/resolve`:
```go
type resolvePromptBody struct {
	Action string `json:"action"`
	Via    string `json:"via"`
}

func (s *Server) handleResolvePrompt(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body resolvePromptBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	via := viaFromBody(body.Via)

	req, err := s.RT.ResolvePrompt(r.Context(), id, body.Action, via)
	if err != nil {
		writeError(w, err)
		return
	}
	wire, err := s.RT.RequestWire(r.Context(), req.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/httpapi/... -run "TestResolvePromptRouteSendsKeysAndResolves"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/requests.go internal/httpapi/requests_test.go
git commit -m "feat(httpapi): add POST /api/requests/:id/resolve endpoint"
```

---

### Task 5: Menubar & Web Board UI Integration

**Files:**
- Modify: `apps/menubar/Sources/SwarmBarKit/AppModel.swift`
- Modify: `apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift`
- Modify: `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift`
- Modify: `web/src/api.ts`
- Modify: `web/src/panels/Review.tsx`
- Modify: `web/src/panels/Review.test.tsx`

**Interfaces:**
- Consumes: `POST /api/requests/:id/resolve`
- Produces: "Approve" button on prompt rows in Menubar & Web Board

- [ ] **Step 1: Write the failing tests**

In `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift`:
```swift
func testResolvePromptCallsAPIAndUpdatesState() async {
    let model = AppModel(client: mockClient)
    await model.resolvePrompt("req_prompt", action: "Enter")
    XCTAssertTrue(mockClient.resolvedPrompts.contains("req_prompt"))
}
```

In `web/src/panels/Review.test.tsx`:
```tsx
it("renders Approve button for prompt requests and resolves on click", async () => {
    const promptReq = { ...mockRequest, kind: "prompt", prompt: "Trust folder?", options: ["Enter"] };
    render(<Review request={promptReq} connected={true} />);
    const approveBtn = screen.getByRole("button", { name: "Approve" });
    expect(approveBtn).toBeInTheDocument();
    await userEvent.click(approveBtn);
    expect(mockApi.resolvePrompt).toHaveBeenCalledWith(promptReq.id, { action: "Enter", via: "board" });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd apps/menubar && swift test --filter AppModelTests`
Run: `cd web && pnpm test Review.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

1. In `apps/menubar/Sources/SwarmBarKit/AppModel.swift`:
```swift
public func resolvePrompt(_ id: String, action: String? = nil) async {
    guard connected else { return }
    do {
        try await client.resolvePrompt(id, action: action, via: "menubar")
        state.requests.removeAll { $0.id == id }
    } catch {
        // notify error
    }
}
```
2. In `apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift`:
For `request.kind == .prompt`:
Add `Button(Copy.approve) { Task { await model.resolvePrompt(request.id, action: request.options?.first) } }`.

3. In `web/src/api.ts`:
Add `resolvePrompt(id: string, body: { action?: string; via?: string }): Promise<Request>`.

4. In `web/src/panels/Review.tsx`:
Add prompt view with `Approve` button calling `api.resolvePrompt(r.id, { action: r.options?.[0], via: "board" })`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd apps/menubar && swift test`
Run: `cd web && pnpm test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/menubar/ web/
git commit -m "feat(ui): add Approve button for prompt requests in menubar and web board"
```
