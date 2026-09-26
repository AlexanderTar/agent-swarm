# Epic approval lane: implementation plan

Spec: `docs/specs/2026-09-26-epic-approval-lane.md` (read it first; the copy, payload shape and scenarios E1-E15 are normative).
Worktree: `/Users/alexandertar/GitHub/agent-swarm-epic-approvals`, branch `feat/epic-approval-lane`. Work only there.

Rules for every task: strict TDD (write the test → run it and watch it fail for the stated reason → minimal code → run and pass → commit). Stage explicit paths only (never `git add -A`), never `--amend`, never delete a test (port it). End every commit message with:

```
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
```

Never touch the primary checkout, `~/.swarm`, the live daemon, launchd or `tmux -L swarm`. `make e2e` uses its own port (17778) and socket (`swarm-e2e`), so it is safe.

## Batches

| Batch | Scope | Tasks | One implementer, one review loop |
|---|---|---|---|
| **A** | Daemon routing: accept native prompt, `request_open` relay on open, `native_answer` for accept kinds with the stale refusal, close_spike relay, e2e scenario | A1-A6 | yes |
| **B** | Wake re-surface for every open request: `askQuestion` dedupe, `resurfaceOpenRequests`, `startSession` and `WakeOnQuotaReset` wiring | B1-B5 | yes (depends on A) |
| **C** | Web Reviews → Approvals merge, menubar `Accept EPIC-N`, skills plus `make skills-sync`, final verification | C1-C4 | yes (independent of B; can run parallel to B) |

New Go runtime tests for A and B all go in one new file, `internal/runtime/approval_lane_test.go` (package `runtime`).

---

## Batch A: daemon routing, native prompt, close_spike

### A1. Accept native prompts and a shared next-step string

**Consumes:** `nativePromptFor(ctx, tx, req, sectionTitle, warnings)` (`internal/runtime/native.go:77`), `truncateWithToken`, `approveOptions`.
**Produces:** `nativePromptFor` cases `KindAcceptEpic` / `KindAcceptFix`, and `func NativePromptNextStep(ref string) string`.

1. Create `internal/runtime/approval_lane_test.go`. Go refuses unused imports, so each later task adds only the imports it needs: `encoding/json` in A2, `errors` in A3, and `strings` plus `internal/adapter` in B3.

```go
package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// Spec E1 copy: the two accept kinds' native prompts.
func TestNativePromptForAcceptKinds(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ep := seedEpicWithTask(t, s)
	bug, err := s.Items.Create(ctx, items.CreateInput{Type: items.Bug, Title: "Login loop"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		got, err := s.nativePromptFor(ctx, tx, Request{ID: "req_e1", Kind: KindAcceptEpic, ItemID: ep.ID}, "", nil)
		if err != nil {
			return err
		}
		want := NativePrompt{Header: "Accept epic", Question: `Accept EPIC-1 "Build it" as done? ⟦swarm:req_e1⟧`, Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_epic prompt = %+v, want %+v", got, want)
		}
		got, err = s.nativePromptFor(ctx, tx, Request{ID: "req_f1", Kind: KindAcceptFix, ItemID: bug.ID}, "", nil)
		if err != nil {
			return err
		}
		want = NativePrompt{Header: "Accept fix",
			Question: fmt.Sprintf(`Accept the fix for %s "Login loop" as done? ⟦swarm:req_f1⟧`, bug.Key), Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("accept_fix prompt = %+v, want %+v", got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

2. Run `go test ./internal/runtime/ -run TestNativePromptForAcceptKinds -count=1`. Expected FAIL: `accept_epic prompt = {Header: Question: Options:[]}` (the default case).
3. In `internal/runtime/native.go`, add this case to `nativePromptFor`'s switch, before `default:`:

```go
	case KindAcceptEpic, KindAcceptFix:
		var key, title string
		if err := tx.QueryRowContext(ctx, `SELECT key, title FROM items WHERE id = ?`, req.ItemID).Scan(&key, &title); err != nil {
			return NativePrompt{}, err
		}
		if req.Kind == KindAcceptFix {
			q := fmt.Sprintf("Accept the fix for %s %q as done?", key, title)
			return NativePrompt{Header: "Accept fix", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
		}
		q := fmt.Sprintf("Accept %s %q as done?", key, title)
		return NativePrompt{Header: "Accept epic", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
```

   Update the doc comment above `nativePromptFor`: the list of kinds now ends `..., close_spike, accept_epic, accept_fix`.

4. Still in `native.go`, move the next-step string out of mcpserver (the text is unchanged):

```go
// NativePromptNextStep is the show-and-forward instruction that rides with
// every daemon-issued native prompt: swarm_ask's result (mcpserver
// requestOut) and the request_open relay share it verbatim (2026-09-26
// epic-approval-lane; text unchanged from the native-railway-tracing fix).
func NativePromptNextStep(ref string) string {
	return fmt.Sprintf("Print the summary in chat first, not in the question. Then show native_prompt "+
		"with your native question tool now (one question per call, verbatim, no added text). Once the user "+
		"answers, call swarm_ask kind:\"native_answer\", ref:%q, decision:\"approve\"|\"request_changes\" "+
		"forwarding only what the user picked, never a decision they did not make.", ref)
}
```

   In `internal/mcpserver/tools.go` `requestOut`, replace the `out["next"] = fmt.Sprintf(...)` statement with:

```go
		out["next"] = runtime.NativePromptNextStep(r.ID)
```

   Drop the `fmt` import only if `go vet` reports it unused.
5. Run `go test ./internal/runtime/ -run TestNativePromptForAcceptKinds -count=1` and `go test ./internal/mcpserver/ -run 'TestAskResultWithNativePromptCarriesNextStep' -count=1`. Both must PASS; the existing mcpserver test guards the moved text.
6. Commit:

```
git add internal/runtime/native.go internal/runtime/approval_lane_test.go internal/mcpserver/tools.go
git commit -m "feat(runtime): native prompts for accept_epic/accept_fix; share the next-step text

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### A2. Route accept rows to the live root orchestrator and relay them

**Consumes:** `enqueueRaw`, `rootItemID`, `itemKey`, `agentByIDTx`, `sectionTitle`, `nativePromptFor`, `NativePromptNextStep`.
**Produces:** `storedNativePromptTx`, `relayRequestTx` (approval kinds only in this batch), `liveRootOrchestratorTx`, `routeAcceptTx`. `OnRequestOpened` routes before notifying.

1. Add `"encoding/json"` to the test file's imports, then append these helpers and tests to `internal/runtime/approval_lane_test.go`:

```go
// openAcceptRow inserts an accept row exactly as reconcileRoot does (no agent,
// no session) and fires the RequestOpened hook in the same tx.
func openAcceptRow(t *testing.T, s *Store, id, kind, itemKey string) {
	t.Helper()
	ctx := context.Background()
	it, err := s.Items.Get(ctx, itemKey)
	if err != nil {
		t.Fatal(err)
	}
	binding := fmt.Sprintf(`{"item_revision":%d,"integrated_checkpoint":"ckp_x","git":[]}`, it.Revision)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at)
			VALUES (?, ?, ?, 'Review completed work and accept the epic.', 'open', ?, 1)`, id, kind, it.ID, binding); err != nil {
			return err
		}
		return s.OnRequestOpened(ctx, tx, id)
	}); err != nil {
		t.Fatal(err)
	}
}

// relayFor returns the newest request_open relay payload for reqID and how many exist.
func relayFor(t *testing.T, s *Store, toAgentID, reqID string) (map[string]any, int) {
	t.Helper()
	rows, err := s.DB.QueryContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'relay' AND request_id = ? ORDER BY seq`, toAgentID, reqID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var last map[string]any
	n := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		last = map[string]any{}
		if err := json.Unmarshal([]byte(p), &last); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return last, n
}

func decodeNP(t *testing.T, p map[string]any) NativePrompt {
	t.Helper()
	raw, _ := json.Marshal(p["native_prompt"])
	var np NativePrompt
	if err := json.Unmarshal(raw, &np); err != nil {
		t.Fatal(err)
	}
	return np
}

// Spec E1.
func TestAcceptRowRoutesToLiveRootOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")

	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != orch.ID || req.SessionID != orchSes {
		t.Fatalf("bound to (%q, %q), want (%q, %q)", req.AgentID, req.SessionID, orch.ID, orchSes)
	}
	p, n := relayFor(t, s, orch.ID, "req_accept")
	if n != 1 {
		t.Fatalf("%d request_open relays, want 1", n)
	}
	if p["event"] != "request_open" || p["kind"] != "accept_epic" || p["item"] != "EPIC-1" || p["request_id"] != "req_accept" {
		t.Fatalf("relay payload = %v", p)
	}
	np := decodeNP(t, p)
	if np.Header != "Accept epic" || np.Question != `Accept EPIC-1 "Build it" as done? ⟦swarm:req_accept⟧` {
		t.Fatalf("native_prompt = %+v", np)
	}
	if p["question"] != np.Question || p["next"] != NativePromptNextStep("req_accept") {
		t.Fatalf("question/next = %v / %v", p["question"], p["next"])
	}
}

// Spec E4.
func TestAcceptRowStaysAgentlessWithoutLiveOrchestrator(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused' WHERE agent_id = ?`, orch.ID); err != nil {
		t.Fatal(err)
	}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	req, err := s.RequestByID(ctx, "req_accept")
	if err != nil {
		t.Fatal(err)
	}
	if req.AgentID != "" || req.SessionID != "" {
		t.Fatalf("bound to (%q, %q) with no live orchestrator", req.AgentID, req.SessionID)
	}
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE request_id = 'req_accept'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d messages for an unrouted accept row", n)
	}
}

// Spec E11 (exhaust): the relay keeps the native prompt even while the kind is out of usage.
func TestAcceptRelayIsNotHeldWhileExhausted(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, _, _ := worker(t, s)
	s.Usage = fakeUsage{Fake: true}
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	if _, n := relayFor(t, s, orch.ID, "req_accept"); n != 1 {
		t.Fatalf("%d relays while exhausted, want 1", n)
	}
	var held int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM suppressed_relays`).Scan(&held)
	if held != 0 {
		t.Fatalf("%d suppressed_relays rows, want 0", held)
	}
}
```

2. Run `go test ./internal/runtime/ -run 'TestAcceptRow|TestAcceptRelay' -count=1`. Expected FAIL: `bound to ("", "")` / `0 request_open relays`.
3. In `internal/runtime/native.go`, add:

```go
// storedNativePromptTx rebuilds a stored approval's native prompt exactly as
// swarm_ask first returned it (same section title from the asked revision,
// same plan warnings), so a re-shown question matches the original byte for
// byte and the hook's question row binds to the same ref.
func (s *Store) storedNativePromptTx(ctx context.Context, tx *sql.Tx, req Request) (NativePrompt, error) {
	title, err := s.sectionTitle(ctx, tx, req.ArtifactID, req.ArtifactRevision, req.SectionID)
	if err != nil {
		return NativePrompt{}, err
	}
	var warnings []string
	if req.Kind == KindApprovePlan {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(warnings_json,'[]') FROM artifact_revisions
			WHERE artifact_id = ? AND revision = ?`, req.ArtifactID, req.ArtifactRevision).Scan(&raw); err != nil {
			return NativePrompt{}, err
		}
		json.Unmarshal([]byte(raw), &warnings)
	}
	return s.nativePromptFor(ctx, tx, req, title, warnings)
}
```

4. In `internal/runtime/requests.go`, add below `OnRequestOpened`:

```go
// relayRequestTx sends req.AgentID one request_open relay: the request id,
// its native prompt (approval kinds) and the next step (spec
// 2026-09-26-epic-approval-lane). It is the one delivery path for a request
// the agent did not get back from its own swarm_ask call: an accept row
// routed to it, a close_spike its checkpoint opened, or an open request
// re-surfaced after a restart or wake. enqueueRaw, not enqueue: the
// exhausted-kind hold keeps only a 500-char sample and would destroy the
// native prompt, and this is one message per open request, not a fan-in
// flood; WakeDue already skips an exhausted kind, so nothing wakes early.
func (s *Store) relayRequestTx(ctx context.Context, tx *sql.Tx, id string) error {
	req, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return err
	}
	a, err := s.agentByIDTx(ctx, tx, req.AgentID)
	if err != nil {
		return err
	}
	key, err := s.itemKey(ctx, tx, req.ItemID)
	if err != nil {
		return err
	}
	payload := map[string]any{"event": "request_open", "agent": a.Name, "item": key,
		"request_id": req.ID, "kind": req.Kind}
	switch req.Kind {
	default:
		np, err := s.storedNativePromptTx(ctx, tx, req)
		if err != nil {
			return err
		}
		payload["question"], payload["native_prompt"], payload["next"] = np.Question, np, NativePromptNextStep(req.ID)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	rootID, err := s.rootItemID(ctx, tx, req.ItemID)
	if err != nil {
		return err
	}
	_, err = s.enqueueRaw(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: req.AgentID,
		RootItemID: rootID, ItemID: req.ItemID, RequestID: req.ID, Payload: body})
	return err
}

// liveRootOrchestratorTx returns the live top-level orchestrator of itemID's
// root (the agent terminalAgent names for an accept row) and its newest live
// session; ok is false when there is none.
func (s *Store) liveRootOrchestratorTx(ctx context.Context, tx *sql.Tx, itemID string) (agentID, sessionID string, ok bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT a.id, se.id FROM agents a
		JOIN items i ON i.id = ? AND a.root_item_id = i.root_id
		JOIN sessions se ON se.agent_id = a.id
		WHERE a.role = 'orchestrator' AND a.parent_agent_id IS NULL AND a.state = 'active'
		  AND se.state IN ('spawning', 'running', 'pause_requested', 'quiescing', 'stopping')
		ORDER BY se.generation DESC, se.started_at DESC LIMIT 1`, itemID).Scan(&agentID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return agentID, sessionID, err == nil, err
}

// routeAcceptTx binds a daemon-opened accept_epic/accept_fix row to its
// root's live top-level orchestrator and relays it the native prompt, like
// any approval that orchestrator asked for itself (locked decision 1). With
// no live orchestrator the row stays agentless -- board and CLI still
// resolve it -- and startSession binds and relays it when one starts
// (resurfaceOpenRequests). Any other kind is a no-op.
func (s *Store) routeAcceptTx(ctx context.Context, tx *sql.Tx, id string) error {
	req, err := s.requestTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if req.Kind != KindAcceptEpic && req.Kind != KindAcceptFix {
		return nil
	}
	agentID, sesID, ok, err := s.liveRootOrchestratorTx(ctx, tx, req.ItemID)
	if err != nil || !ok {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ? WHERE id = ?`,
		agentID, sesID, id); err != nil {
		return err
	}
	return s.relayRequestTx(ctx, tx, id)
}
```

   In `OnRequestOpened`, make the first statement:

```go
	if err := s.routeAcceptTx(ctx, tx, id); err != nil {
		return err
	}
```

   Rewrite the `OnRequestOpened` doc comment's first sentence: "OnRequestOpened is wired into items.Store.RequestOpened: it routes the accept_* rows the reconciler opens to the root's live orchestrator (routeAcceptTx), then raises their §17.5 notification." Keep the rest of the comment.
5. Run `go test ./internal/runtime/ -run 'TestAcceptRow|TestAcceptRelay|TestNativePromptForAcceptKinds|TestRequestPayloadAndOnRequestOpenedAreWired|TestOnRequestOpenedRefusesAnUnknownRequest|TestTerminalAgent' -count=1`. Everything must PASS. The two existing OnRequestOpened tests still pass: a question is a no-op and an unknown id still errors.
6. Commit `internal/runtime/native.go internal/runtime/requests.go internal/runtime/approval_lane_test.go` with message `feat(runtime): route accept rows to the live root orchestrator with a request_open relay`.

### A3. native_answer forwards routed accept rows; stale rows refuse clearly

**Consumes:** `hookSimulate` (`native_answer_test.go:52`), `approvalTerminalKinds`, `resolve`, `approveCheck`.
**Produces:** `nativeAnswerKind`, `errRequestStale`, reworded `errNativeAnswerWrongTarget`. `resolve` now enqueues `approval_result` for bound accept rows (no code change: `req.AgentID != ""` already gates it).

1. Append to `approval_lane_test.go`:

```go
// routedAccept is E1's setup plus the native question shown and answered in
// the orchestrator's terminal.
func routedAccept(t *testing.T, answer string) (s *Store, orch Agent, orchSes string) {
	t.Helper()
	s, _, _ = newStore(t)
	orch, _, _ = worker(t, s)
	orchSes = mustSessionID(t, s, orch.ID)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1")
	p, _ := relayFor(t, s, orch.ID, "req_accept")
	hookSimulate(t, s, orchSes, decodeNP(t, p), answer)
	return s, orch, orchSes
}

func approvalResultFor(t *testing.T, s *Store, toAgentID, reqID string) map[string]any {
	t.Helper()
	var raw string
	if err := s.DB.QueryRowContext(context.Background(), `SELECT payload_json FROM messages
		WHERE to_agent_id = ? AND kind = 'approval_result' AND request_id = ?`, toAgentID, reqID).Scan(&raw); err != nil {
		t.Fatalf("no approval_result for %s: %v", reqID, err)
	}
	var p map[string]any
	json.Unmarshal([]byte(raw), &p)
	return p
}

// Spec E2.
func TestNativeAnswerApprovesRoutedAcceptRow(t *testing.T) {
	s, orch, orchSes := routedAccept(t, "Approve")
	ctx := context.Background()
	out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "approved" || out.RespondedVia != "terminal" {
		t.Fatalf("state/via = %s/%s", out.State, out.RespondedVia)
	}
	p := approvalResultFor(t, s, orch.ID, "req_accept")
	if p["decision"] != "approved" || p["evidence"] != EvidenceObserved {
		t.Fatalf("approval_result = %v", p)
	}
}

// Spec E3.
func TestNativeAnswerRequestChangesOnAcceptRow(t *testing.T) {
	s, orch, orchSes := routedAccept(t, "Request changes: rename the flag")
	ctx := context.Background()
	out, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "request_changes"})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "changes_requested" || out.ResponseText != "rename the flag" {
		t.Fatalf("state/text = %s/%q", out.State, out.ResponseText)
	}
	if p := approvalResultFor(t, s, orch.ID, "req_accept"); p["decision"] != "changes_requested" {
		t.Fatalf("approval_result = %v", p)
	}
}

// Spec E6.
func TestNativeAnswerStaleAcceptIsRefused(t *testing.T) {
	s, _, orchSes := routedAccept(t, "Approve")
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET state = 'stale' WHERE id = 'req_accept'`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Ask(ctx, orchSes, AskInput{Kind: "native_answer", Ref: "req_accept", Decision: "approve"})
	want := "req_accept is stale: EPIC-1 changed after the question was asked. Don't forward it; Swarm sends a new request when the work is ready again."
	var ie *items.Error
	if !errors.As(err, &ie) || ie.Code != items.CodeConflict || ie.Message != want {
		t.Fatalf("err = %v, want conflict %q", err, want)
	}
}
```

   Add `"errors"` to the test file's imports (A2 already added `"encoding/json"`).
2. Run `go test ./internal/runtime/ -run 'TestNativeAnswer(Approves|RequestChanges|Stale)' -count=1`. Expected FAIL: `req_accept is not an approve_section, ... request you asked for.`
3. In `internal/runtime/native.go`:

```go
const errNativeAnswerWrongTarget = "%s is not an approve_section, approve_plan, approve_report, " +
	"confirm_repos, close_spike, accept_epic or accept_fix request routed to you."

// errRequestStale is native_answer's refusal for a request the reconciler
// staled after the question was asked (reconcileRoot's binding sweep).
const errRequestStale = "%s is stale: %s changed after the question was asked. Don't forward it; " +
	"Swarm sends a new request when the work is ready again."

// nativeAnswerKind reports whether native_answer forwards a request of kind
// k: the asking agent's own approvals plus accept rows once routeAcceptTx
// has bound them to the root orchestrator (2026-09-26 epic-approval-lane).
func nativeAnswerKind(k RequestKind) bool {
	return approvalTerminalKinds[k] || k == KindAcceptEpic || k == KindAcceptFix
}
```

   In `nativeAnswer`, replace the kind/owner check and its comment with:

```go
	// native_answer only forwards approval kinds (nativeAnswerKind), and only
	// for the agent the request is routed to -- never a plain HITL question
	// or another agent's request, which req.Kind and req.AgentID alone can't
	// otherwise be trusted to exclude once an evidence row exists (a caller
	// can forge its own locally, Task B4 finding 3). An accept row is routed
	// to the root orchestrator by routeAcceptTx, so the same owner check
	// covers it.
	if !nativeAnswerKind(req.Kind) || req.AgentID != callerID {
		return Request{}, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(errNativeAnswerWrongTarget, in.Ref)}
	}
	// reconcileRoot stales an open accept row in the same tx as any revision
	// bump or new integration: say so instead of resolve's bare
	// "Already resolved.". The binding itself is read from the stored row
	// below (locked decision 5).
	if req.State == "stale" {
		var key string
		if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, req.ItemID).Scan(&key); err != nil {
			return Request{}, err
		}
		return Request{}, &items.Error{Code: items.CodeConflict, Message: fmt.Sprintf(errRequestStale, in.Ref, key)}
	}
```

   In `internal/runtime/requests.go`, reword two comments to match. On `approvalTerminalKinds`: "accept_epic/accept_fix are absent: they have no asking agent (the reconciler opens them), so terminalAgent keeps its item-rooted lookup; nativeAnswerKind adds them for native_answer once routed." In `resolve`, above `if req.AgentID != ""`: "An accept row routed to the root orchestrator (routeAcceptTx) has an agent and gets its approval_result like any approval; an unrouted one (no live orchestrator) has nobody to tell, so the enqueue is skipped."
4. Run `go test ./internal/runtime/ -run 'TestNativeAnswer' -count=1`. It must PASS, including the existing `TestNativeAnswerRefusesANonApprovalRequestKind` (a plain question is still refused).
5. Commit `internal/runtime/native.go internal/runtime/requests.go internal/runtime/approval_lane_test.go` with message `feat(runtime): native_answer forwards routed accept rows and refuses stale ones`.

### A4. close_spike relays its native prompt

**Consumes:** `relayRequestTx`.
**Produces:** a relay after the close_spike insert in `WriteCheckpoint`.

1. Append:

```go
// Spec E8.
func TestCloseSpikeRelaysNativePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Nothing", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "starting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: CompletedCkp,
		Summary: "nothing to build", Resolution: "no_change"}); err != nil {
		t.Fatal(err)
	}
	var reqID string
	s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE kind = 'close_spike'`).Scan(&reqID)
	p, n := relayFor(t, s, a.ID, reqID)
	if n != 1 {
		t.Fatalf("%d relays for close_spike, want 1", n)
	}
	np := decodeNP(t, p)
	if np.Header != "Close spike" || np.Question != "Close SPIKE-1? ⟦swarm:"+reqID+"⟧" {
		t.Fatalf("native_prompt = %+v", np)
	}
	hookSimulate(t, s, ses, np, "Approve")
	if _, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, "SPIKE-1"); it.Status != items.Done {
		t.Fatalf("spike status = %s, want done", it.Status)
	}
}
```

2. Run `go test ./internal/runtime/ -run TestCloseSpikeRelaysNativePrompt -count=1`. Expected FAIL: `0 relays for close_spike`.
3. In `internal/runtime/checkpoint.go`, inside `if in.Resolution != "" {`, right after the `if s.Notify != nil { ... }` block that raises `request.close_spike`, add:

```go
			// Locked decision 3 (epic-approval-lane): the spike orchestrator
			// gets its native prompt through the same relay as a routed
			// accept row, since CheckpointResult carries none.
			if err := s.relayRequestTx(ctx, tx, reqID); err != nil {
				return err
			}
```

4. Run `go test ./internal/runtime/ -run 'TestCloseSpike|TestRequestChangesOnCloseSpike' -count=1`. It must PASS.
5. Commit `internal/runtime/checkpoint.go internal/runtime/approval_lane_test.go` with message `feat(runtime): relay close_spike's native prompt to the spike orchestrator`.

### A5. e2e: the full lane through the real daemon

**Consumes:** harness helpers `materializedEpic`, `startOrchestrator`, `spawn`, `firstTask`, `mustTool`, `headSHA`, `itemStatus`, `itemRevision`, `waitForRequestFull`, `requestByKind`, `waitForRelay`, `doT`, `db` (all existing).
**Produces:** `scripts/e2e/epicapproval_test.go`.

1. Create `scripts/e2e/epicapproval_test.go`:

```go
//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Epic approval lane (docs/specs/2026-09-26-epic-approval-lane.md E1, E7):
// the accept_epic row is routed to the live orchestrator with a request_open
// relay; a board change request reaches it as approval_result and sends the
// epic back to work; a fresh integration re-opens and re-relays; approval
// reaches it too and finishes the epic. The setup mirrors scenario 29.
// ponytail: setup duplicated from staleaccept_test.go; extract a harness
// helper if a third scenario needs an in-review epic.
func TestScenarioEpicApprovalLane(t *testing.T) {
	h := newHarness(t)
	epic := h.materializedEpic(t)
	h.doT(t, http.MethodPatch, "/api/items/"+epic,
		map[string]any{"status": "ready", "revision": h.itemRevision(t, epic)}, nil)
	orch := h.startOrchestrator(t, epic)
	h.mustTool(t, orch, "swarm_checkpoint", map[string]any{"kind": "accepted", "summary": "starting the epic"})

	task := h.firstTask(t, epic)
	worker := h.spawn(t, orch, task, "coder")
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "progress", "summary": "wrote the failing test",
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "red", "ok": false}},
	})
	h.mustTool(t, worker, "swarm_checkpoint", map[string]any{
		"kind": "completed", "summary": "made it pass",
		"git":          []map[string]any{{"repo": "chat", "branch": "task/one", "sha": h.headSHA(t)}},
		"verification": []map[string]any{{"cmd": "go test ./x", "phase": "green", "ok": true}},
	})
	h.mustTool(t, orch, "swarm_items", map[string]any{
		"op": "update", "key": task, "status": "done", "revision": h.itemRevision(t, task),
	})
	integrated := func() {
		h.mustTool(t, orch, "swarm_checkpoint", map[string]any{
			"kind": "integrated", "summary": "integrated",
			"git":          []map[string]any{{"repo": "chat", "branch": "main", "sha": h.headSHA(t)}},
			"verification": []map[string]any{{"cmd": "go test ./...", "phase": "green", "ok": true}},
		})
	}
	count := func(kind, reqID string) int {
		var n int
		h.db(t).QueryRow(`SELECT COUNT(*) FROM messages m JOIN agents a ON a.id = m.to_agent_id
			WHERE a.name = ? AND m.kind = ? AND m.request_id = ?`, orch, kind, reqID).Scan(&n)
		return n
	}

	since := time.Now()
	integrated()
	first := h.waitForRequestFull(t, epic, "accept_epic", 20*time.Second)
	firstID := first["id"].(string)
	if first["agent_name"] != orch {
		t.Fatalf("accept_epic agent_name = %v, want %s", first["agent_name"], orch)
	}
	if !h.waitForRelay(t, orch, "request_open", since, 10*time.Second) || count("relay", firstID) != 1 {
		t.Fatalf("no request_open relay for %s", firstID)
	}

	// E7: request changes on the board reaches the orchestrator and reopens work.
	h.doT(t, http.MethodPost, "/api/requests/"+firstID+"/request-changes",
		map[string]any{"comment": "Rename the flag.", "via": "board"}, nil)
	if count("approval_result", firstID) != 1 {
		t.Fatalf("no approval_result for the change request")
	}
	if got := h.itemStatus(t, epic); got != "in_progress" {
		t.Fatalf("epic status = %s, want in_progress", got)
	}

	// Re-integration re-opens a fresh row and re-relays it.
	integrated()
	deadline := time.Now().Add(20 * time.Second)
	var second map[string]any
	for {
		if id := h.openRequest(t, epic, "accept_epic"); id != "" && id != firstID {
			second = h.requestByKind(t, epic, "accept_epic")
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no fresh accept_epic after re-integration")
		}
		time.Sleep(200 * time.Millisecond)
	}
	secondID := second["id"].(string)
	if count("relay", secondID) != 1 {
		t.Fatalf("no request_open relay for the re-opened %s", secondID)
	}

	h.doT(t, http.MethodPost, "/api/requests/"+secondID+"/approve",
		map[string]any{"binding": second["binding"], "via": "board"}, nil)
	if count("approval_result", secondID) != 1 {
		t.Fatalf("no approval_result for the approval")
	}
	if got := h.itemStatus(t, epic); got != "done" {
		t.Fatalf("epic status = %s, want done", got)
	}
}
```

2. Run `make e2e`. The new scenario must PASS. (It would FAIL on `accept_epic agent_name = <nil>` without A2, but A2 is already in; to see RED, run it once on a stash of A2. That is optional because A2's unit test already proved RED.) Existing scenario 29 must still PASS.
3. Commit `scripts/e2e/epicapproval_test.go` with message `test(e2e): epic approval lane routes, re-opens and reports back`.

### A6. Batch A gate

1. `make vet fmt`
2. `go test -race ./internal/runtime/ ./internal/mcpserver/ ./internal/items/ ./internal/httpapi/ ./cmd/swarm/ -count=1`
3. If an existing test now fails only because a new relay or `approval_result` message appears (for example, a test that counts all `relay` rows to an orchestrator or spike), port its assertion to count the events it meant: filter by `payload_json LIKE '%"event":"<its event>"%'`, or by `kind`. Never delete the assertion. Commit each port separately, naming the test in the message.
4. `go test -race ./... -count=1` must be green. Then hand Batch A to review.

---

## Batch B: wake re-surface for every open request

### B1. `askQuestion` reuses the open row with the same prompt

**Consumes:** `askQuestion` (`requests.go` ~line 609).
**Produces:** dedupe on (agent, `kind='question'`, `state='open'`, prompt).

1. Append:

```go
// Spec E12.
func TestAskQuestionReusesOpenRowWithSamePrompt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Dedupe", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	first, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	// As if the row was asked by an earlier generation of this agent.
	if _, err := s.DB.ExecContext(ctx, `UPDATE requests SET session_id = NULL WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.SessionID != ses {
		t.Fatalf("re-ask = (%s, %q), want (%s, %s)", again.ID, again.SessionID, first.ID, ses)
	}
	var open int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question' AND state = 'open'`).Scan(&open)
	if open != 1 {
		t.Fatalf("%d open question rows, want 1", open)
	}
	// Once answered, the same prompt is a new question.
	if _, err := s.ResolveQuestionByPrompt(ctx, ses, "Which sync strategy?", "Pull"); err != nil {
		t.Fatal(err)
	}
	third, err := s.AskQuestion(ctx, ses, "Which sync strategy?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatalf("an answered row was reused")
	}
}
```

2. Run `go test ./internal/runtime/ -run TestAskQuestionReusesOpenRowWithSamePrompt -count=1`. Expected FAIL: `re-ask = (req_…other, …)`.
3. In `askQuestion`, inside the `IdemTx` closure right after `if err := requireTopLevel(a); err != nil { return err }`, add:

```go
		// A question asked again after a restart or wake (epic-approval-lane
		// decision 2) reuses the agent's open row with the same prompt instead
		// of opening a second Needs-you row: the hook's PostToolUse closes only
		// the newest match, so a duplicate would stay open forever. The row
		// follows the caller's session so ResolveQuestionByPrompt finds it.
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT id FROM requests WHERE agent_id = ? AND kind = 'question'
			AND state = 'open' AND prompt = ? ORDER BY created_at LIMIT 1`, a.ID, in.Prompt).Scan(&existing)
		if err == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET session_id = ? WHERE id = ?`, sessionID, existing); err != nil {
				return err
			}
			out, err = s.requestTx(ctx, tx, existing)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
```

4. Run `go test ./internal/runtime/ -count=1` and `go test ./internal/hook/ -count=1`. If an existing test asserted two open rows for one prompt while the first was still open, port it to the one-row contract (a comment should cite this spec). `TestNativeAnswerRefusesASecondRowForTheSameRef` must still pass, because its first row is `answered` before the second ask.
5. Commit `internal/runtime/requests.go internal/runtime/approval_lane_test.go` with message `feat(runtime): a re-asked open question reuses its Needs-you row`.

### B2. `resurfaceOpenRequests`, question/blocker relays, reminder copy

**Consumes:** `relayRequestTx` (A2).
**Produces:** `reaskQuestionNext`, `blockerOpenNext`, the question/blocker cases in `relayRequestTx`, `resurfaceOpenRequests`, and `OpenRequestsReminder`.

1. Append:

```go
// Spec copy: question and blocker relays.
func TestRelayRequestForQuestionAndBlocker(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Relay", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	q, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AskBlocker(ctx, ses, "Need a staging token.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := s.relayRequestTx(ctx, tx, q.ID); err != nil {
			return err
		}
		return s.relayRequestTx(ctx, tx, b.ID)
	}); err != nil {
		t.Fatal(err)
	}
	pq, _ := relayFor(t, s, a.ID, q.ID)
	if pq["question"] != "Which sync strategy?" || pq["next"] != reaskQuestionNext || pq["native_prompt"] != nil {
		t.Fatalf("question relay = %v", pq)
	}
	if opts, _ := pq["options"].([]any); len(opts) != 2 {
		t.Fatalf("question relay options = %v", pq["options"])
	}
	pb, _ := relayFor(t, s, a.ID, b.ID)
	if pb["question"] != "Need a staging token." || pb["next"] != blockerOpenNext {
		t.Fatalf("blocker relay = %v", pb)
	}
}

// Spec E11 (dedupe): an unacked relay is counted but not sent again.
func TestResurfaceSkipsRequestsWithAPendingRelay(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	a, err := s.AgentByID(ctx, req.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		n, err := s.resurfaceOpenRequests(ctx, a, ses, true)
		if err != nil || n != 1 {
			t.Fatalf("round %d: n = %d, err = %v", i, n, err)
		}
	}
	if _, n := relayFor(t, s, a.ID, req.ID); n != 1 {
		t.Fatalf("%d relays after two unacked rounds, want 1", n)
	}
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE request_id = ?`, req.ID)
	if _, err := s.resurfaceOpenRequests(ctx, a, ses, true); err != nil {
		t.Fatal(err)
	}
	if _, n := relayFor(t, s, a.ID, req.ID); n != 2 {
		t.Fatalf("%d relays after the ack, want 2", n)
	}
	if got := OpenRequestsReminder(2); got != "2 request(s) still wait on your user. swarm_sync delivers each as a "+
		"request_open relay with its native prompt and next step; ask again as it says." {
		t.Fatalf("reminder = %q", got)
	}
}
```

2. Run `go test ./internal/runtime/ -run 'TestRelayRequestForQuestionAndBlocker|TestResurfaceSkips' -count=1`. Expected FAIL: compile errors (undefined `reaskQuestionNext`, `resurfaceOpenRequests`, `OpenRequestsReminder`).
3. In `internal/runtime/requests.go`, add:

```go
// reaskQuestionNext / blockerOpenNext are the request_open relay's next step
// for a still-open plain question and blocker (epic-approval-lane copy).
const reaskQuestionNext = "Ask the user again with the same text and options: claude and agy with your " +
	"native question tool, cursor, muse and codex with swarm_ask kind:\"question\". Swarm keeps one Needs-you row for it."
const blockerOpenNext = "Your blocker is still open in Needs you. The user's answer arrives as a " +
	"user_answer message; don't ask again."
```

   Extend `relayRequestTx`'s switch (keep `default` last):

```go
	case KindQuestion:
		payload["question"], payload["options"], payload["next"] = req.Prompt, req.Options, reaskQuestionNext
	case KindBlocker:
		payload["question"], payload["next"] = req.Prompt, blockerOpenNext
```

   Then add the helper:

```go
// resurfaceOpenRequests is the one wake/restart helper (epic-approval-lane
// locked decision 2). For a top-level orchestrator it first binds its root's
// open accept rows to sessionID (a row that opened while no orchestrator
// was live). Then it relays every open request routed to the agent that the
// agent can't already see. On a same-session wake (fresh=false), a
// question/blocker asked in this session, or an approval whose native
// question row is open in it, is visible; a fresh session sees nothing. A
// request whose last request_open relay is still unacked is counted but not
// relayed again: swarm_sync redelivers it. Skipped entirely: permission
// prompts (their pane is gone) and ref-bound question rows (the shadow of an
// approval relayed on its own; msg_ refs keep question_unanswered). n is
// how many requests still wait on the user.
func (s *Store) resurfaceOpenRequests(ctx context.Context, a Agent, sessionID string, fresh bool) (int, error) {
	visible := sessionID
	if fresh {
		visible = ""
	}
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		n = 0
		if a.Role == RoleOrchestrator && a.ParentAgentID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ?
				WHERE state = 'open' AND kind IN ('accept_epic', 'accept_fix')
				  AND item_id IN (SELECT id FROM items WHERE root_id = ?)`, a.ID, sessionID, a.RootItemID); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.id,
			EXISTS (SELECT 1 FROM messages m WHERE m.to_agent_id = r.agent_id AND m.request_id = r.id
				AND m.kind = 'relay' AND m.state <> 'acked')
			FROM requests r
			WHERE r.agent_id = ? AND r.state = 'open' AND r.kind <> 'prompt'
			  AND NOT (r.kind = 'question' AND json_extract(r.binding_json, '$.ref') IS NOT NULL)
			  AND NOT (r.kind IN ('question', 'blocker') AND r.session_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM requests q WHERE q.kind = 'question' AND q.state = 'open'
				AND q.session_id = ? AND json_extract(q.binding_json, '$.ref') = r.id)
			ORDER BY r.created_at, r.id`, a.ID, visible, visible)
		if err != nil {
			return err
		}
		var relay []string
		for rows.Next() {
			var id string
			var pending bool
			if err := rows.Scan(&id, &pending); err != nil {
				rows.Close()
				return err
			}
			n++
			if !pending {
				relay = append(relay, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range relay {
			if err := s.relayRequestTx(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
```

   In `internal/runtime/text.go`, add below `QuotaResetNotice`:

```go
// OpenRequestsReminder rides on a kickoff or quota-reset notice when
// requests still wait on the user (epic-approval-lane decision 2). The
// request_open relays carry each prompt durably; this line only points at
// them, because a paste fallback or a muse resume never shows notice text.
func OpenRequestsReminder(n int) string {
	return fmt.Sprintf("%d request(s) still wait on your user. swarm_sync delivers each as a request_open relay "+
		"with its native prompt and next step; ask again as it says.", n)
}
```

4. Run the two tests again. Both must PASS.
5. Commit `internal/runtime/requests.go internal/runtime/text.go internal/runtime/approval_lane_test.go` with message `feat(runtime): resurfaceOpenRequests relays every open request once`.

### B3. Every session start re-surfaces (startSession)

**Consumes:** `resurfaceOpenRequests`, `OpenRequestsReminder`.
**Produces:** a `startSession` kickoff with the reminder, and accept rows bound on orchestrator start.

1. Append:

```go
// Spec E5.
func TestStartOrchestratorBindsAndRelaysAgentlessAcceptRows(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	openAcceptRow(t, s, "req_accept", "accept_epic", "EPIC-1") // no orchestrator yet
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := s.RequestByID(ctx, "req_accept")
	if req.AgentID != orch.ID || req.SessionID != mustSessionID(t, s, orch.ID) {
		t.Fatalf("bound to (%q, %q)", req.AgentID, req.SessionID)
	}
	if _, n := relayFor(t, s, orch.ID, "req_accept"); n != 1 {
		t.Fatalf("%d relays, want 1", n)
	}
	if !strings.HasSuffix(fa.LastSpec.Kickoff, " "+OpenRequestsReminder(1)) {
		t.Fatalf("kickoff lacks the reminder:\n%s", fa.LastSpec.Kickoff)
	}
}

// Spec E9.
func TestResumeResurfacesOpenRequests(t *testing.T) {
	s, ses, req := seedApprovalWithNativePrompt(t)
	ctx := context.Background()
	fa := s.Adapters[Fake].(*adapter.Fake)
	q, err := s.AskQuestion(ctx, ses, "Which sync strategy?", []string{"Pull", "Push"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.AgentByID(ctx, req.AgentID)
	s.DB.ExecContext(ctx, `UPDATE sessions SET state = 'paused', provider_session_id = NULL WHERE id = ?`, ses)
	if _, err := s.Resume(ctx, a.Name, "", ""); err != nil {
		t.Fatal(err)
	}
	pa, na := relayFor(t, s, a.ID, req.ID)
	if na != 1 || !reflect.DeepEqual(decodeNP(t, pa), *req.NativePrompt) {
		t.Fatalf("approval relay = %d × %v, want the original %+v", na, pa, *req.NativePrompt)
	}
	pq, nq := relayFor(t, s, a.ID, q.ID)
	if nq != 1 || pq["next"] != reaskQuestionNext {
		t.Fatalf("question relay = %d × %v", nq, pq)
	}
	if !strings.HasSuffix(fa.LastSpec.Kickoff, " "+OpenRequestsReminder(2)) {
		t.Fatalf("resume kickoff lacks the reminder:\n%s", fa.LastSpec.Kickoff)
	}
}
```

   Add `"strings"` and `"github.com/AlexanderTar/agent-swarm/internal/adapter"` to the test file's imports.
2. Run `go test ./internal/runtime/ -run 'TestStartOrchestratorBinds|TestResumeResurfaces' -count=1`. Expected FAIL: `bound to ("", "")` / `0 relays`.
3. In `internal/runtime/agents.go` `startSession`, after the `defer func() { ... failSession ... }()` block and before `var itemKey, itemTitle, itemTypeStr string`, add:

```go
	// Epic-approval-lane decision 2: every start path (fresh, retry, drain,
	// resume, recovery, handoff successor) funnels through here, so this is
	// where open requests come back to the agent. Fail open: a relay error
	// never blocks a spawn.
	reminder := ""
	if n, err := s.resurfaceOpenRequests(ctx, a, ses.ID, true); err != nil {
		s.logf("start session %s: resurface open requests: %v", a.Name, err)
	} else if n > 0 {
		reminder = " " + OpenRequestsReminder(n)
	}
```

   Immediately after the `switch { case resume: ... default: kickoff = Kickoff(...) }`, add:

```go
	kickoff += reminder
```

4. Run `go test ./internal/runtime/ -count=1`. Kickoff tests use `Contains`/`HasPrefix` and stay green. Port any test that asserts exact kickoff equality for an agent that has open requests: append `" " + OpenRequestsReminder(n)` to its expectation. Never drop the assertion.
5. Commit `internal/runtime/agents.go internal/runtime/approval_lane_test.go` with message `feat(runtime): every session start re-surfaces the agent's open requests`.

### B4. Quota-reset wake re-surfaces what the live session can't see

**Consumes:** `resurfaceOpenRequests(…, fresh=false)`.
**Produces:** a `WakeOnQuotaReset` notice with the reminder.

1. Append:

```go
// Spec E10.
func TestQuotaResetWakeResurfacesOnlyWhatIsNotVisible(t *testing.T) {
	s, tm, fa := newStore(t)
	ctx := context.Background()
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Quota", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses := mustSessionID(t, s, a.ID)
	path := writeFile(t, "# Spec\n\n## Data model\n\nrows\n\n## API\n\ncalls\n")
	res, err := s.RegisterArtifact(ctx, ses, "register", "SPIKE-1", "spec", path, "")
	if err != nil {
		t.Fatal(err)
	}
	shown, err := s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review", ArtifactID: res.ArtifactID, SectionID: res.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.Ask(ctx, ses, AskInput{Kind: "approval", Prompt: "Review", ArtifactID: res.ArtifactID, SectionID: res.Sections[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	// shown's native question is open in this very session; q was asked here;
	// a permission prompt never comes back.
	if _, err := s.AskQuestion(ctx, ses, shown.NativePrompt.Question, shown.NativePrompt.Options); err != nil {
		t.Fatal(err)
	}
	q, _ := s.AskQuestion(ctx, ses, "Which sync strategy?", nil)
	p, _ := s.AskPrompt(ctx, ses, "rm -rf build", nil)

	panes(tm, Pane{Session: a.Name, Command: "swarm-fake-agent"})
	tm.captures[a.Name] = []string{"─────\n❯ \n─────\n"}
	if _, err := s.WakeOnQuotaReset(ctx, Fake, tm.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if want := QuotaResetNotice() + " " + OpenRequestsReminder(1); fa.LastWakeTarget.Notice != want {
		t.Fatalf("notice = %q, want %q", fa.LastWakeTarget.Notice, want)
	}
	if _, n := relayFor(t, s, a.ID, hidden.ID); n != 1 {
		t.Fatalf("hidden approval: %d relays, want 1", n)
	}
	for _, id := range []string{shown.ID, q.ID, p.ID} {
		if _, n := relayFor(t, s, a.ID, id); n != 0 {
			t.Fatalf("%s is visible or a prompt but got %d relays", id, n)
		}
	}
}
```

   No new imports are needed (`panes`, `Pane` and `tm.clk` are existing test helpers).
2. Run `go test ./internal/runtime/ -run TestQuotaResetWakeResurfaces -count=1`. Expected FAIL: `notice = "Quota reset window passed. Resuming. …"` without the reminder.
3. In `internal/runtime/wake.go` `WakeOnQuotaReset`, right after the `rows.Scan(...)` error check in the loop, add:

```go
		// Epic-approval-lane decision 2: a quota-reset wake re-surfaces what
		// this live session can't already see (fresh=false). The same write-
		// inside-the-read-loop pattern as markWoken below.
		notice := QuotaResetNotice()
		if a, err := s.agentByID(ctx, agentID); err != nil {
			s.logf("wake: quota-reset agent %s: %v", agentName, err)
		} else if n, err := s.resurfaceOpenRequests(ctx, a, sessionID, false); err != nil {
			s.logf("wake: quota-reset resurface for %s: %v", agentName, err)
		} else if n > 0 {
			notice += " " + OpenRequestsReminder(n)
		}
```

   In the `ad.Wake(...)` call, replace `Notice: QuotaResetNotice(),` with `Notice: notice,`.
   If the B4 test hangs or reports `database is locked` (the `s.tx` write runs while the `rows` cursor from `s.DB.QueryContext` is open), collect the rows into a slice and close the cursor before the loop, the same way `wakeCandidates` does.
4. Run `go test ./internal/runtime/ -run 'TestWakeOnQuotaReset|TestQuotaResetWakeResurfaces' -count=1`. It must PASS; existing quota tests have no open requests, so their notice is unchanged.
5. Commit `internal/runtime/wake.go internal/runtime/approval_lane_test.go` with message `feat(runtime): quota-reset wake re-surfaces open requests the session can't see`.

### B5. Batch B gate

1. `make vet fmt`, then `go test -race ./... -count=1`. Port (don't delete) any assertion that now sees an extra `request_open` relay or a reused question row, following the A6 rule.
2. `make e2e`. All scenarios must PASS. Scenarios that restart agents with open requests may now see extra relays; port count assertions by event.
3. Hand Batch B to review.

---

## Batch C: web and menubar merge, skills

### C1. Web: Reviews merges into Approvals

**Consumes:** `filterRequests` (`web/src/logic/inbox.ts`).
**Produces:** `InboxFilter = "all" | "questions" | "approvals"`.

1. Port the tests first:
   - `web/src/logic/inbox.test.ts`:
     - In "filters and orders oldest first", replace the `approvals` expectation and the `reviews` line with:

```ts
    expect(filterRequests(reqs, "approvals").map((r) => r.id)).toEqual([
      "req_accept", "req_section", "req_plan", "req_report", "req_close", "req_repos", "req_fix",
    ]);
```

     - In "needsYou = open and not native_pending…", replace the `approvals` and `reviews` lines with:

```ts
    expect(filterRequests(rs, "approvals").map((r) => r.id)).toEqual(["a", "e"]);
```

     - In "picks a request", change the two `"reviews"` calls to `"approvals"` (expectations unchanged: `req_fix`, then `req_accept`).
   - `web/src/panels/NeedsYou.test.tsx`: change both `toHaveLength(5)` under Approvals to `toHaveLength(7)`. Change `expect(appRows[0]).toHaveTextContent("SPIKE-3 · Offline mode")` to `expect(appRows[0]).toHaveTextContent("EPIC-12 · Authentication")`. Replace the comment `// accept_epic/accept_fix moved to Reviews (2.2.5)` with `// accept_epic/accept_fix are approvals too (2026-09-26 epic-approval-lane)`.
   - `web/src/state/url.test.ts`: add to the invalid-values test:

```ts
    expect(parseHash("#/inbox?filter=reviews").filter).toBe("all"); // merged into Approvals
```

2. Run `cd web && pnpm exec vitest run src/logic/inbox.test.ts src/panels/NeedsYou.test.tsx src/state/url.test.ts`. Expected FAIL: the approvals lists lack `req_accept` / `req_fix`, and `filter=reviews` still parses to `reviews`.
3. Implement:
   - `web/src/logic/inbox.ts`:

```ts
const QUESTIONS = new Set(["question", "prompt", "blocker"]);

export function filterRequests(reqs: Request[], f: InboxFilter): Request[] {
  const set = needsYou(reqs);
  if (f === "questions") return set.filter((r) => QUESTIONS.has(r.kind));
  // Every approval is one lane, accept_epic/accept_fix included (2026-09-26 epic-approval-lane).
  if (f === "approvals") return set.filter((r) => !QUESTIONS.has(r.kind));
  return set;
}
```

   - `web/src/types.ts:376`: `export type InboxFilter = "all" | "questions" | "approvals";`
   - `web/src/state/url.ts:25`: `const FILTERS: readonly InboxFilter[] = ["all", "questions", "approvals"];`
   - `web/src/panels/NeedsYou.tsx`: delete the `{ value: "reviews", label: C.reviews },` option.
   - `web/src/copy.ts:176`: delete `reviews: "Reviews",`.
4. Run the three test files again (PASS), then `cd web && pnpm test` and `pnpm build` (the type check must pass with no other `"reviews"` references; run `grep -rn '"reviews"' web/src` and expect no output).
5. Commit `web/src/logic/inbox.ts web/src/logic/inbox.test.ts web/src/types.ts web/src/state/url.ts web/src/state/url.test.ts web/src/panels/NeedsYou.tsx web/src/panels/NeedsYou.test.tsx web/src/copy.ts` with message `feat(web): merge the Reviews filter into Approvals`.

### C2. Menubar: accept rows read "Accept EPIC-N"

**Consumes:** `NeedsYouRow.lines`, `RequestLine.text` (`apps/menubar/Sources/SwarmBarKit/AppModel.swift`).
**Produces:** `Copy.acceptItem(_:)`.

1. Port and extend `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift`:
   - Lines 173-175 become:

```swift
        let lines = [RequestKind.approveReport, .acceptEpic, .acceptFix, .confirmRepos, .closeSpike, .prompt, .blocker]
            .map { RequestLine.text(SwarmRequest(id: "r", kind: $0, itemKey: "EPIC-12", prompt: "Sample prompt")) }
        XCTAssertEqual(lines, ["Approve report", "Accept EPIC-12", "Accept EPIC-12", "Confirm repositories", "Close spike?", "Sample prompt", "Sample prompt"])
```

   - In `testNeedsYouRowIsGenericAndNeverShowsThePrompt`, after the `noAgent` assertion, add:

```swift
        let accept = SwarmRequest(id: "r", kind: .acceptEpic, agentName: "auth-orch", itemKey: "EPIC-12",
                                  itemTitle: "Authentication", prompt: "SECRET")
        XCTAssertEqual(NeedsYouRow.lines(accept), ["EPIC-12 · Authentication", "auth-orch", "Accept EPIC-12"])
        XCTAssertEqual(NeedsYouRow.lines(SwarmRequest(id: "r", kind: .acceptFix, itemKey: "BUG-3", prompt: "SECRET"))[2],
                       "Accept BUG-3")
```

2. Run `cd apps/menubar && swift test --filter AppModelTests`. Expected FAIL: `"Accept epic"` ≠ `"Accept EPIC-12"`, and line 3 = `"Waiting for your input"`.
3. Implement:
   - `Copy.swift`: replace the two lines `public static let acceptEpic = "Accept epic"` / `public static let acceptFix = "Accept fix"` with:

```swift
    public static func acceptItem(_ key: String) -> String { "Accept \(key)" }
```

   - `AppModel.swift` `RequestLine.text`: replace the two accept cases with:

```swift
        case .acceptEpic, .acceptFix: return Copy.acceptItem(r.itemKey)
```

   - `AppModel.swift` `NeedsYouRow.lines`:

```swift
/// Needs-you row lines, generic across every request kind (spec 1.6.3): never the prompt. An accept
/// row's third line names what is being accepted (2026-09-26 epic-approval-lane) -- daemon copy, not
/// the agent's text.
public enum NeedsYouRow {
    public static func lines(_ r: SwarmRequest) -> [String] {
        let third = (r.kind == .acceptEpic || r.kind == .acceptFix) ? Copy.acceptItem(r.itemKey) : Copy.needsYouMessage
        return ["\(r.itemKey) · \(r.itemTitle)", r.agentName ?? r.terminalAgent ?? "—", third]
    }
}
```

4. Run `swift test --filter AppModelTests` (PASS), then `make test-menubar`.
5. Commit `apps/menubar/Sources/SwarmBarKit/Copy.swift apps/menubar/Sources/SwarmBarKit/AppModel.swift apps/menubar/Tests/SwarmBarTests/AppModelTests.swift` with message `feat(menubar): accept rows read "Accept EPIC-N"`.

### C3. Skills: tell agents about request_open

Skills carry no test harness beyond the mirror check. Edit, sync, then verify the mirror.

1. `skills/swarm-orchestrator/SKILL.md`:
   - Line 20: replace `then ask for user acceptance.` with:
     `then ask for user acceptance through the \`request_open\` relay the daemon sends once \`integrated\` is recorded (see below).`
   - Line 51: replace the whole bullet with:
     ```
     - Merge in dependency order, run the plan's verification, then write `integrated` with the merged sha per repo and the verification results. The user's acceptance is requested only after that: the daemon opens `accept_epic` (`accept_fix` for a bug) and sends you a `request_open` relay carrying its `native_prompt` and `next`. Print a summary of the integrated work in chat, then ask and forward exactly like any approval (`swarm_ask kind: "native_answer"`, `ref` = the relay's `request_id`); cursor, muse and codex leave it to the board or `swarm approve`. On `approval_result` `approved` the daemon has already moved the item to done: write `completed`. On `changes_requested`, do what the comment asks, re-integrate, and the daemon asks again. If `native_answer` says the request is stale, the item changed after the question: wait for the new `request_open`.
     ```
   - After the bullet that ends `A relay \`event: "question_unanswered"\` means you still owe that answer.`, add:
     ```
     - A relay with `event: "request_open"` carries one of your requests still waiting on the user: `request_id`, `kind`, `question`, `native_prompt` for approvals, and `next`. Follow `next`. Swarm sends one when a daemon-opened approval (`accept_epic`, `accept_fix`, `close_spike`) is routed to you, and again after any restart, resume or quota-reset wake for every request still open. Asking again is safe: Swarm keeps one Needs-you row per question, and a ref can be forwarded only once.
     ```
   - In the Spikes bullet starting `Approving through native question tools`, after `— follow it even if this text is stale.`, insert:
     ` Daemon-opened approvals (\`accept_epic\`, \`accept_fix\`, \`close_spike\`) arrive instead as a \`request_open\` relay with the same \`native_prompt\` and \`next\`; handle them identically.`
   - Line 68: replace `the user decides whether to close the spike.` with:
     `the user decides whether to close the spike: the daemon opens \`close_spike\` and sends you a \`request_open\` relay with its \`native_prompt\`; ask and forward it like any approval.`
2. `skills/swarm/SKILL.md`: at the end of the bullet that begins `- Only an agent with no parent may ask the user.` (after `otherwise end your turn.`), append:
   ` After a restart, resume or quota-reset wake, a \`request_open\` relay re-sends each of your requests that is still open; follow its \`next\`.`
3. `make skills-sync`
4. `git status --short skills internal/install/skills`. Expect exactly the two `skills/` files and their two mirror counterparts. Then `go test ./internal/install/ -count=1` (mirror equality).
5. Commit `skills/swarm-orchestrator/SKILL.md skills/swarm/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm/SKILL.md` with message `docs(skills): request_open relays for daemon approvals and wake re-surface`.

### C4. Final verification (after A, B and C are merged into the branch)

Run in order; each must be green:

1. `make vet fmt`
2. `go test -race ./... -count=1`
3. `cd web && pnpm test && pnpm build`
4. `make test-menubar`
5. `make skills-sync && git diff --exit-code internal/install/skills`
6. `make e2e`

Then hand the branch to final review (Opus reviewer, per `sdd-review-model-policy`).

## Self-review against the spec

- E1 → A2; E2/E3/E6 → A3; E4 → A2; E5/E9 → B3; E7 → A5; E8 → A4; E10 → B4; E11 → A2 + B2; E12 → B1; E13 is existing; E14 → C1; E15 → C2.
- All copy in the spec appears verbatim in a code block above: prompts (A1), next steps (A1, B2), reminder (B2), stale and wrong-target errors (A3), UI labels (C1, C2), skills (C3).
- No task needs a migration. No test is deleted. Ported assertions are listed in C1/C2, and the A6/B5 rule covers any unforeseen count assertions.
