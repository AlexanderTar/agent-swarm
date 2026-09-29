# Plan: print the approval block first, ask in a separate turn

Spec: `docs/specs/2026-09-29-approval-print-then-ask.md` (all copy, wire shapes and test lists
live there; this plan cites them by name).
Worktree: `/Users/alexandertar/GitHub/agent-swarm-print-then-ask`, branch `fix/approval-print-then-ask`.
Two tasks, sequential (Task 2's MCP/skill copy asserts `runtime.PrintNext` from Task 1).
Every step: failing test → run, see red → minimal code → run, see green → commit with explicit paths.

---

## Task 1: daemon — phase tracking, turn-end verification, relays, gate removal

**Consumes:** `ApprovalChatBlock`, `approvalChatBlockTx`, `storedNativePromptTx`, `NormForMatch`,
`enqueueRaw`, `InboxNotice`, `transcriptTexter`.
**Produces:** `PrintNext`, `ReprintNext`, `TurnReply`, `(*Store).PrintTurnEnded`,
`(*Store).PrintTurnTick`, `turnReplyReader`, `startPrintTx`, `retirePrintRelaysTx`, `sendAskTx`,
`sendReprintTx` (spec "Model / API types"); `Fake.LastReply`.

### 1.1 Failing runtime tests — `internal/runtime/print_test.go` (new)

```go
package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// passPrint marks every relay to the session's agent delivered (seen; the guard only skips pending) and ends the turn as a trusted kind,
// so the next relay for a chat_block request is its request_ask.
func passPrint(t *testing.T, s *Store, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DB.ExecContext(ctx, `UPDATE messages SET state = 'delivered' WHERE kind = 'relay' AND state = 'pending'
		AND to_agent_id = (SELECT agent_id FROM sessions WHERE id = ?)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrintTurnEnded(ctx, sessionID, TurnReply{Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

func printState(t *testing.T, s *Store, reqID string) (phase string, attempts int) {
	t.Helper()
	if err := s.DB.QueryRow(`SELECT COALESCE(json_extract(binding_json, '$.print_phase'), ''),
		COALESCE(json_extract(binding_json, '$.print_attempts'), 0) FROM requests WHERE id = ?`, reqID).
		Scan(&phase, &attempts); err != nil {
		t.Fatal(err)
	}
	return phase, attempts
}

func printSection(t *testing.T) (*Store, string, string, Request) {
	t.Helper()
	ctx := context.Background()
	s, _, _ := newStore(t)
	_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Print", Intent: "feature", Kind: Fake, Model: "fake-1"})
	ses, _ := s.LatestSession(ctx, a.ID)
	spec, err := s.RegisterArtifact(ctx, ses.ID, "register", "SPIKE-1", "spec", writeFile(t, "## Design\n\nUse SQLite.\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", Prompt: "Use SQLite for storage.\n\n- one file per repo",
		ArtifactID: spec.ArtifactID, SectionID: spec.Sections[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	return s, ses.ID, a.ID, req
}

func TestAskApprovalStartsPrintPhase(t *testing.T) {
	s, _, _, req := printSection(t)
	if p, n := printState(t, s, req.ID); p != "print" || n != 0 {
		t.Fatalf("print state = %q/%d", p, n)
	}
}

func TestPrintTurnEndedPassSendsAsk(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	reply := "Here it is:\n\n**" + strings.ReplaceAll(req.ChatBlock, "- ", "* ") + "**"
	sent, err := s.PrintTurnEnded(context.Background(), ses, TurnReply{Text: reply, Readable: true})
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_ask" || p["next"] != NativePromptNextStep(req.ID, []string{"approve", "request_changes"}) {
		t.Fatalf("relay = %v", p)
	}
	if np := decodeNP(t, p); np.Question != req.NativePrompt.Question {
		t.Fatalf("native_prompt %q != frozen %q", np.Question, req.NativePrompt.Question)
	}
	if ph, _ := printState(t, s, req.ID); ph != "ask" {
		t.Fatalf("phase = %q", ph)
	}
}

func TestPrintTurnEndedParaphraseRetriesThenPasses(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	ctx := context.Background()
	if _, err := s.PrintTurnEnded(ctx, ses, TurnReply{Text: "Approving section 1: SQLite.", Readable: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_print" || p["next"] != ReprintNext || p["chat_block"] != req.ChatBlock || p["attempt"] != float64(2) {
		t.Fatalf("reprint relay = %v", p)
	}
	if _, n := printState(t, s, req.ID); n != 1 {
		t.Fatalf("attempts = %d", n)
	}
	passPrintWith(t, s, ses, req.ChatBlock)
	if p, _ := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" {
		t.Fatalf("relay = %v", p)
	}
}

func passPrintWith(t *testing.T, s *Store, ses, text string) {
	t.Helper()
	s.DB.Exec(`UPDATE messages SET state = 'delivered' WHERE kind = 'relay' AND state = 'pending'`)
	if _, err := s.PrintTurnEnded(context.Background(), ses, TurnReply{Text: text, Readable: true}); err != nil {
		t.Fatal(err)
	}
}

func TestPrintTurnEndedAsksAnywayAfterTwoFailures(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	passPrintWith(t, s, ses, "short version")
	passPrintWith(t, s, ses, "short version again")
	if p, n := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" || n != 2 {
		t.Fatalf("relay = %v (n=%d)", p, n)
	}
}

func TestPrintTurnEndedFailsOpen(t *testing.T) {
	for name, r := range map[string]TurnReply{"unreadable": {}, "trusted": {Trusted: true}} {
		t.Run(name, func(t *testing.T) {
			s, ses, agentID, req := printSection(t)
			if _, err := s.PrintTurnEnded(context.Background(), ses, r); err != nil {
				t.Fatal(err)
			}
			if p, _ := relayFor(t, s, agentID, req.ID); p["event"] != "request_ask" {
				t.Fatalf("relay = %v", p)
			}
		})
	}
}

func TestPrintTurnEndedSkipsUndeliveredRelay(t *testing.T) {
	s, ses, _, req := printSection(t)
	ctx := context.Background()
	if err := s.tx(ctx, func(tx *sql.Tx) error { return s.relayRequestTx(ctx, tx, req.ID) }); err != nil {
		t.Fatal(err)
	}
	if sent, _ := s.PrintTurnEnded(ctx, ses, TurnReply{Text: "nope", Readable: true}); sent {
		t.Fatal("judged a ref whose request_open was never delivered")
	}
	if _, n := printState(t, s, req.ID); n != 0 {
		t.Fatalf("attempts = %d", n)
	}
}

func TestApproveMidPrintResolvesAndRetires(t *testing.T) {
	s, ses, _, req := printSection(t)
	ctx := context.Background()
	passPrintWith(t, s, ses, "paraphrase") // pending request_print now exists
	if _, err := s.Approve(ctx, req.ID, ApproveInput{Via: "board", SectionSHA256: req.SectionSHA256,
		ArtifactRevision: req.ArtifactRevision}); err != nil {
		t.Fatal(err)
	}
	var pending int
	s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE request_id = ? AND state <> 'acked'
		AND json_extract(payload_json, '$.event') IN ('request_print', 'request_ask')`, req.ID).Scan(&pending)
	if pending != 0 {
		t.Fatalf("%d print/ask relays left pending", pending)
	}
	if sent, _ := s.PrintTurnEnded(ctx, ses, TurnReply{Trusted: true}); sent {
		t.Fatal("resolved request still judged")
	}
}

func TestResurfaceRetiresAskAndRestartsPrint(t *testing.T) {
	s, ses, agentID, req := printSection(t)
	ctx := context.Background()
	if _, err := s.PrintTurnEnded(ctx, ses, TurnReply{Trusted: true}); err != nil { // pending request_ask
		t.Fatal(err)
	}
	a, _ := s.AgentByID(ctx, agentID)
	if _, err := s.resurfaceOpenRequests(ctx, a, ses, true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	p, _ := relayFor(t, s, agentID, req.ID)
	if p["event"] != "request_open" || p["chat_block"] != req.ChatBlock || p["native_prompt"] != nil || p["next"] != PrintNext {
		t.Fatalf("relay = %v", p)
	}
	if ph, n := printState(t, s, req.ID); ph != "print" || n != 0 {
		t.Fatalf("print state = %q/%d", ph, n)
	}
}
```

Add `"database/sql"` to the imports. Also add, beside `TestNativePromptForMsgReturnsChatBlock` in
`chat_block_test.go`, `TestForMsgPrintThenAsk`. It reuses that test's setup and asserts:
- `json_extract(payload_json,'$.print_phase')='print'` on the child message;
- after `passPrint`, one relay has `correlation_id` = msg id and payload
  `event:"request_ask"`, `kind:"child_approval"`;
- a child message already answered (insert a `kind='answer'` row with `reply_to` = msg id) is
  not judged.

Add `TestPrintTurnTickMuseStyle` using the `Fake` adapter:
- set `fa.LastReplyReadable=true`, `fa.LastReplyFound=false`: no relay;
- then `LastReplyFound=true` with `LastReplyText=req.ChatBlock`: `request_ask`;
- the pane-idle capture comes from `fakeTmux` (use the idle capture existing wake tests use).

Run and watch it fail (undefined `PrintTurnEnded`, and so on):

```bash
cd /Users/alexandertar/GitHub/agent-swarm-print-then-ask && go test ./internal/runtime/ -run 'Print|ForMsgPrint|ApproveMidPrint|ResurfaceRetires' 2>&1 | tail -20
```

### 1.2 Port the existing runtime tests (spec "Tests updated")

- `chat_block_test.go`:
  - `TestAskApprovalReturnsChatBlock`: delete the 4-line `SummaryGate` block and assert
    `p["native_prompt"] == nil && p["next"] == PrintNext`.
  - `TestNativePromptForMsgReturnsChatBlock`: no change needed (it reads `ChatBlock`).
- `finish_question_test.go`:
  - `TestFinishChatBlock`: replace the `next` check with `p["next"] != PrintNext`, and delete the
    `SummaryGate` lines. Then `passPrint(t, s, orchSes.ID)` and assert the `request_ask` relay's
    `next == NativePromptNextStep(reqID, PromptDecisions(KindAcceptFix, decodeNP(t, p)))`.
  - `TestFinishNextStepDecisions`: add
    `strings.HasPrefix(got, "Ask this now with your native question tool:")`.
- `native_test.go`:
  - `TestNativePromptNextStepDescribesVisibleReviewAndAgentReportedAnswers`: replace the
    "If chat_block is present" assertion with the same `HasPrefix`, and assert `!strings.Contains(got, "print it exactly")`.
  - `TestPlanApprovalCarriesFullReviewPaths`: call `passPrint` before reading `payload["native_prompt"]`.
  - Delete `TestSummaryGate` (spec lists the reason).
- `approval_lane_test.go`:
  - `TestAcceptRowRoutesToLiveRootOrchestrator`, `TestAcceptRelayIsNotHeldWhileExhausted`,
    `TestResumeResurfacesOpenRequests` and `TestChoreEndToEndAcceptFix`: insert
    `passPrint(t, s, <that test's orchestrator session id>)` immediately before the `relayFor`
    whose payload feeds `decodeNP`.
  - In `TestResumeResurfacesOpenRequests`, first assert the request_open relay has
    `chat_block` and no `native_prompt`.
- `finish_question_test.go`: `TestFinishPromptFreezesOptionsAndReplays` and
  `TestFinishNativeAnswer` get the same `passPrint` insert.

### 1.3 Failing hook tests — `internal/hook/handler_test.go`

- Delete the 8 gate tests and `TestAllPathsPresentMatchesBarePathNotLabeledLine` (listed in the
  spec with the reason). Keep the helpers `summaryGateApproval`, `summaryGateAskInput`,
  `writeTranscript`, `claude*Line`. Rename `summaryGateApproval` → `seedApproval` and
  `summaryGateAskInput` → `askInput`.
- Port the first gate test to:

```go
func TestPreToolUseAllowsAnApprovalQuestionWithoutAGate(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	_, question := seedApproval(t, h, "Users table gets id, email, and hashed_password columns.")
	out, err := h.Handle(ctx, runtime.Claude, "PreToolUse", ses, askInput(t, question, writeTranscript(t, claudeUserLine("go"))))
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%s err=%v, want allowed", out, err)
	}
	var n int
	h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE kind = 'question'
		AND json_extract(binding_json, '$.ref') = 'req_sumgate1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("bound question rows = %d, want 1", n)
	}
}

func printingApproval(t *testing.T, h *Handler, summary string) (reqID, block string) {
	t.Helper()
	reqID, _ = seedApproval(t, h, summary)
	h.DB.Exec(`UPDATE requests SET binding_json = json_object('print_phase', 'print', 'print_attempts', 0, 'print_at', 1) WHERE id = ?`, reqID)
	return reqID, runtime.ApprovalChatBlock(runtime.ChatBlockInput{Kind: string(runtime.KindApproveSection), Revision: 1, Summary: summary})
}

func stopInput(t *testing.T, transcript string) []byte {
	b, _ := json.Marshal(map[string]any{"session_id": "p1", "transcript_path": transcript})
	return b
}

func TestStopAfterPrintedBlockSendsAskAndBlocks(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, block := printingApproval(t, h, "Users table gets id and email.")
	tr := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m1", "**"+block+"**"))
	out, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, tr))
	if err != nil || !strings.Contains(string(out), `"decision":"block"`) {
		t.Fatalf("out=%s err=%v", out, err)
	}
	var ev string
	var stopBlocks int
	h.DB.QueryRowContext(ctx, `SELECT json_extract(payload_json, '$.event') FROM messages WHERE request_id = ?`, reqID).Scan(&ev)
	h.DB.QueryRowContext(ctx, `SELECT stop_blocks FROM sessions WHERE id = ?`, ses).Scan(&stopBlocks)
	if ev != "request_ask" || stopBlocks != 0 {
		t.Fatalf("event=%q stop_blocks=%d", ev, stopBlocks)
	}
}

func TestStopAfterParaphraseSendsReprint(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	reqID, _ := printingApproval(t, h, "Users table gets id and email.")
	tr := writeTranscript(t, claudeUserLine("go"), claudeAssistantTextLine("m1", "Approving the users table."))
	if _, err := h.Handle(ctx, runtime.Claude, "Stop", ses, stopInput(t, tr)); err != nil {
		t.Fatal(err)
	}
	var ev string
	h.DB.QueryRowContext(ctx, `SELECT json_extract(payload_json, '$.event') FROM messages WHERE request_id = ?`, reqID).Scan(&ev)
	if ev != "request_print" {
		t.Fatalf("event = %q", ev)
	}
}

func TestStopCursorIsTrusted(t *testing.T) {
	ctx := context.Background()
	h, ses := seed(t, 0, runtime.Running)
	h.DB.Exec(`UPDATE agents SET kind = 'cursor' WHERE id = 'agt_1'`)
	printingApproval(t, h, "Users table gets id and email.")
	out, err := h.Handle(ctx, runtime.Cursor, "stop", ses, []byte(`{"conversation_id":"c1"}`))
	if err != nil || !strings.Contains(string(out), "followup_message") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}
```

Also add `TestStopWithUnreadableTranscriptSendsAsk`. It is the same as the pass test with
`transcript_path` pointing at a missing file, and expects `request_ask`.

If `seed`'s row has no `item_id`/`root_item_id` that `enqueueRaw` needs, `seedApproval` already
uses `itm_1`, so relays resolve their root through it.

Run and watch it fail:

```bash
go test ./internal/hook/ -run 'Stop|PreToolUseAllows' 2>&1 | tail -20
```

### 1.4 Implement

1. `internal/runtime/print.go`: the constants and types exactly as in the spec.
   - `startPrintTx(ref)`:
     - request: `UPDATE requests SET binding_json = json_set(COALESCE(binding_json,'{}'),'$.print_phase','print','$.print_attempts',0,'$.print_at',?) WHERE id = ?`;
     - `msg_` ref: the same on `messages.payload_json`.
   - `PrintTurnEnded`, in one `s.tx`:
     - select the open requests (`agent_id` = the session's agent, `state='open'`,
       `print_phase='print'`) and the unanswered `msg_` questions to that agent (reuse
       reconcile.go:1775's NOT EXISTS);
     - skip any ref with a relay in `state = 'pending'` where `request_id` = ref or
       `correlation_id` = ref (`delivered` counts as seen);
     - apply decision 4, logging with the spec's copy.
     - Build the blocks with `approvalChatBlockTx`; for `msg_`, with
       `ApprovalChatBlock(ChatBlockInput{Child, Summary: body})`.
   - `sendAskTx`: `storedNativePromptTx`, or `nativePromptForMsg` for `msg_`, and the
     `request_ask` payload from the spec.
   - `sendReprintTx`: the `request_print` payload.
   - Both go through `enqueueRaw`, with `Origin:"daemon"`, `Kind:"relay"`, and `RequestID` set
     (or `CorrelationID` for `msg_`).
   - `retirePrintRelaysTx`: `UPDATE messages SET state='acked', acked_at=? WHERE request_id=? AND kind='relay' AND state<>'acked' AND json_extract(payload_json,'$.event') IN ('request_ask','request_print')`.
   - `PrintTurnTick`:
     - look up live sessions with print-phase refs and no pending messages;
     - `ad.(turnReplyReader)`; `s.Tmux.Capture(ctx, tmux, 15)`; `ad.Idle`;
       `LastReply(providerSessionID, print_at)`;
     - `!readable` → `TurnReply{}`; `found` → `TurnReply{Text, Readable:true}`; otherwise skip.
   - Put the `ponytail:` ceiling comment from the spec on it.
2. `requests.go`:
   - `askApproval`: `startPrintTx(out.ID)` after `freezeNativeQuestionTx`.
   - `relayRequestTx`: when `chat_block != ""`, delete `question`/`native_prompt` from the
     payload, set `next = PrintNext`, and call `startPrintTx`.
   - `resurfaceOpenRequests`: when `fresh`, call `retirePrintRelaysTx` for each open request of
     the agent before the SELECT.
   - `resolve`: `retirePrintRelaysTx(id)`.
3. `native.go`:
   - `askNativePromptForMsg`: `startPrintTx(in.ForMsg)`.
   - `NativePromptNextStep`: the spec's copy.
   - Delete `SummaryGate`, `ReviewPathLine` and `RecordSummaryBlock`.
4. `wake.go`: in `WakeLoop`, `if err := s.PrintTurnTick(ctx); err != nil { s.logf("print: %v", err) }`.
5. `adapter/fake.go`: `LastReplyText string; LastReplyFound, LastReplyReadable bool` and
   `LastReply`.
6. `hook/handler.go`:
   - delete the gate branch (keep bind + `AskQuestionBoundTo`/`AskQuestion`), plus
     `allPathsPresent` and `summaryGateDenyReason`.
   - In `case "Stop":`, after the pausing guard:

```go
if h.RT != nil && s.ID != "" {
	r := runtime.TurnReply{}
	if texter, ok := a.(transcriptTexter); !ok {
		r.Trusted = true
	} else if in.TranscriptPath != "" {
		r.Text, r.Readable = texter.AssistantTextSinceLastTurn(in.TranscriptPath)
	}
	sent, err := h.RT.PrintTurnEnded(ctx, s.ID, r)
	if err != nil {
		h.logf("hook: print check for %s: %v", s.ID, err)
	} else if sent {
		return adapter.HookDecision{Block: true, Reason: h.inboxNoticeOrFallback(ctx, s)}, nil
	}
}
```

   (`Fake` has no `AssistantTextSinceLastTurn`, so hook tests that use Fake are trusted. That is
   intended.)

### 1.5 Green and commit

```bash
cd /Users/alexandertar/GitHub/agent-swarm-print-then-ask
gofmt -l . && go vet ./... && go test ./internal/runtime/ ./internal/hook/ ./internal/adapter/ 2>&1 | tail -20
grep -rn 'SummaryGate\|RecordSummaryBlock\|summaryGateDenyReason\|allPathsPresent\|summary_blocks' --include='*.go' . # expect no hits
git add internal/runtime/print.go internal/runtime/print_test.go internal/runtime/requests.go \
  internal/runtime/native.go internal/runtime/wake.go internal/runtime/chat_block_test.go \
  internal/runtime/finish_question_test.go internal/runtime/native_test.go \
  internal/runtime/approval_lane_test.go internal/adapter/fake.go \
  internal/hook/handler.go internal/hook/handler_test.go
git commit -m "fix(runtime): print the approval block in its own turn, then send the question

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

---

## Task 2: MCP output, Muse reply reader, skills, docs

**Consumes:** `runtime.PrintNext` and the Task 1 relays. **Produces:** `requestOut` print
output, the `swarm_ask` description, `Muse.LastReply`, the skill rules, and the skill mirror.

### 2.1 Failing tests

`internal/mcpserver/tools_test.go`, `TestAskApprovalResultHasChatBlock`: replace the last
assertion with:

```go
	var raw map[string]any
	json.Unmarshal(mustJSON(out), &raw)
	if res.Next != runtime.PrintNext || raw["native_prompt"] != nil {
		t.Fatalf("next = %q, native_prompt = %v; want PrintNext and no native_prompt", res.Next, raw["native_prompt"])
	}
```

`internal/adapter/muse_test.go`, new. Fixture `testdata/muse/session-assistant-reply.jsonl` has
4 lines in the live shape (`payload.kind:"run"`, `payload.run_id`,
`payload.event.kind:"assistant_message_committed"`, `payload.event.text`, top-level
`recorded_at` µs):
- run A at 1000: `"old reply"`;
- run B at 2000: `"### Approval · Plan (rev 1)"`;
- run B at 2001: `"Spec: /x"`;
- one `model_completed` line.

```go
func TestMuseLastReply(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "muse", "sessions", "2026", "09", "29", "sid1")
	os.MkdirAll(dir, 0o755)
	b, _ := os.ReadFile("testdata/muse/session-assistant-reply.jsonl")
	os.WriteFile(filepath.Join(dir, "session.jsonl"), b, 0o600)
	m := &Muse{d: Deps{UserHome: home}}

	text, found, readable := m.LastReply("sid1", time.UnixMicro(1500))
	if !readable || !found || text != "### Approval · Plan (rev 1)\nSpec: /x" {
		t.Fatalf("got %q found=%v readable=%v", text, found, readable)
	}
	if _, found, _ := m.LastReply("sid1", time.UnixMicro(3000)); found {
		t.Fatal("reply older than since counted")
	}
	if _, _, readable := m.LastReply("missing", time.Time{}); readable {
		t.Fatal("missing log must be unreadable (fail open)")
	}
}
```

Match the `Muse` constructor/field names in `muse_test.go`'s existing `ObservedAnswer` tests if
`d: Deps{...}` differs.

```bash
go test ./internal/mcpserver/ -run TestAskApprovalResultHasChatBlock ./internal/adapter/ -run TestMuseLastReply 2>&1 | tail
```

Expect red: `next` still says "If chat_block…", `native_prompt` is present, and `LastReply` is
undefined.

### 2.2 Implement

1. `requestOut`: `if r.ChatBlock != "" { out["chat_block"] = r.ChatBlock; out["next"] = runtime.PrintNext }`
   and emit `native_prompt` and its `next` only when `r.ChatBlock == ""`. Keep `review_paths`.
   `r.Next` still overrides, as today (auto-approve).
2. `swarm_ask` description: swap in the spec's "swarm_ask tool description" copy.
3. `Muse.LastReply`: the same glob as `ObservedAnswer`, the same `bufio.Reader.ReadBytes` loop
   and pre-filter `bytes.Contains(line, []byte("assistant_message_committed"))`. Track the newest
   `run_id` and its texts where `recorded_at >= since.UnixMicro()`, and reset the texts when the
   `run_id` changes.
4. Skills: apply the spec "Copy" edits to `skills/swarm-orchestrator/SKILL.md` (lines 45, 46,
   54, 89, 115) and `skills/swarm-spike/SKILL.md` (steps 4, 7). Then:

```bash
make skills-sync
grep -rn 'print it exactly\|print the returned `chat_block` exactly\|Print `chat_block` exactly' skills internal/install/skills # expect no hits
```

5. Docs: none beyond the spec. `skills/swarm/SKILL.md` stays unchanged (spec decision 10).

### 2.3 Green, full suite, commit

```bash
cd /Users/alexandertar/GitHub/agent-swarm-print-then-ask
gofmt -l . && go vet ./... && go test ./... 2>&1 | tail -30
git diff --exit-code internal/install/skills || true   # after skills-sync this shows only the intended mirror edits
git add internal/mcpserver/tools.go internal/mcpserver/tools_test.go internal/adapter/muse.go \
  internal/adapter/muse_test.go internal/adapter/testdata/muse/session-assistant-reply.jsonl \
  skills/swarm-orchestrator/SKILL.md skills/swarm-spike/SKILL.md \
  internal/install/skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-spike/SKILL.md
git commit -m "fix(mcp,skills): swarm_ask hands back only the block; ask on request_ask

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV"
```

Then run the spec's scenarios 2–13 by name:

```bash
go test ./internal/runtime/ ./internal/hook/ ./internal/mcpserver/ ./internal/adapter/ -run 'Print|Stop|ForMsgPrint|ApproveMidPrint|ResurfaceRetires|Finish|MuseLastReply|PreToolUseAllows' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
```
