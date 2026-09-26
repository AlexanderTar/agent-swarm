# Plan: Needs you = everything waiting on the user, and a closed child → orchestrator → user loop with real native approvals

- **Rev 3 (2026-09-25)**: follows the user's decisions in spec sections 1.5 and 1.6. Approvals appear in Needs you. Agent-reported approvals are accepted and flagged `agent_reported`. The green dot stays. The copy is locked. The web board follows the same rules (Tasks 17a-17b). The hook findings are in spec section 1.7. No open questions remain.
- **Rev 4 (2026-09-26)**: adds Batch B6 (spec §1.8) — typed free text is accepted on any native approval prompt and forwarded with the meant decision (mismatch is refused only when the typed text picks the contradicting option), the "Request changes needs a comment" rule is removed, and Approve on `confirm_repos` confirms proposed + expansion repos together, minus dropped.

- **Spec**: `docs/specs/2026-09-25-needs-you-and-child-approval-routing.md` (read sections 1.5-1.7 and 2 first)
- **Base**: `origin/main` @ `bb67390`
- **Execution**: `superpowers:subagent-driven-development`. Implementers run on Sonnet and reviewers on Opus, at most 2-3 subagents at once. Tasks in different phases touch disjoint files except where a **Depends** line says otherwise.
- **Every task follows TDD**: write the failing test → run it and see it fail → write the minimal code → run it and see it pass → commit with explicit paths (never `git add -A`, never `--amend`).
- **Tests are updated, never deleted.** When a contract changes, port the old assertion to the new contract in the same commit.

## Execution batches

The tasks are grouped into five build batches plus a final check. Each batch goes through one build → review → fix → verify loop, with one Opus review per batch instead of one per task. Batches in different lanes touch different files, so they run at the same time. At most three subagents run at once. Everything happens in the single worktree `../agent-swarm-needs-you`.

| Batch | Tasks | Lane | Starts after | Review |
|---|---|---|---|---|
| **B1: Protocol basics** | T1, T2, T3, T10, T11, T12, T13 | Go (mcpserver, repos, runtime) | T0 | Opus, focused on B1 |
| **B2: Live probes** | T4, T4b | Fixtures in testdata + handler sub-tests | T0 | None of its own; B3's review covers it |
| **B3: Hook wiring + refusal** | T5, T6, T7, T8, T9 | Go (adapter, install, hook, mcpserver) | B1 and B2 | Opus, covering B2 and B3 |
| **B4: Real native approvals** | T13a, T13b, T13c, T13d, T13e, T14 | Go (runtime, hook, httpapi, skills) | B3 | Opus, focused on B4 |
| **B5: UI (menubar + web)** | T16, T17, then T15, T17a, T17b | Swift and web | T16/T17 start at T0; T15/T17a/T17b wait for B4 | One Opus review for all UI |
| **B6: confirm_repos native routing polish** | T6.1, T6.2, T6.3, T6.4 | Go (runtime) + skills | B4 | Opus, focused on B6 |
| **Final** | T18 | Whole branch | B1–B6 | Opus review of the whole branch against the spec, then verification commands |

Rules for every batch:
- The implementer (Sonnet) works through its tasks in plan order with TDD and commits per task.
- The reviewer (Opus) follows `superpowers:requesting-code-review` and reviews the batch's commit range.
- Critical or Important findings go to one fix pass (Sonnet, `superpowers:receiving-code-review`), then one re-review. If the re-review still has Critical or Important findings, stop and escalate to the user.
- `git commit` can hit `index.lock` while other lanes commit. When it does, retry after a few seconds. Stage explicit paths only.
- If a probe outcome changes the design (spec §1.7), B2 records it in the spec. B3 then follows the fallback the plan already decided: a kind whose hook doesn't fire joins cursor's exception, and a kind whose hook shows no answer text records `agent_reported`.

## Phase 0: Setup

### Task 0: Worktree
1. `git -C /Users/alexandertar/GitHub/agent-swarm fetch origin`
2. `git -C /Users/alexandertar/GitHub/agent-swarm worktree add ../agent-swarm-needs-you -b feat/needs-you-routing origin/main`
3. Copy the spec and this plan into the worktree and commit them: `git add docs/specs/2026-09-25-needs-you-and-child-approval-routing.md docs/plans/2026-09-25-needs-you-and-child-approval-routing.md && git commit -m "docs: needs-you and child approval routing spec + plan"`

All paths below are relative to the worktree.

---

## Phase A: The rubbish row's upstream causes (independent, can start at once)

### Task 1: `swarm_read {repos:{}}` returns registered repos (F11)
**Files**: `internal/mcpserver/orchestrator_test.go` (extend the existing repos block at `:704-718`), `internal/mcpserver/tools.go:433`
**Consumes**: `repos.Service.Search(ctx, q string, limit int)` (unchanged)

1. Test. Add after the `{"repos":{"q":"proj"}}` assertion:
```go
	// repos: {} (empty q) lists registered repos even when none was ever used
	// (last_used_at is NULL everywhere; MarkUsed has no production caller).
	out, err = s.call(ctx, seed.Caller, "swarm_read", `{"repos":{}}`)
	if err != nil {
		t.Fatal(err)
	}
	var res2c struct {
		Repos []struct{ ID string `json:"id"` } `json:"repos"`
	}
	json.Unmarshal(mustJSON(out), &res2c)
	if len(res2c.Repos) == 0 || res2c.Repos[0].ID != seed.RepoID {
		t.Fatalf("empty repos query = %+v, want the seeded repo", res2c)
	}
```
2. `go test ./internal/mcpserver/ -run TestRead` → FAIL (`repos` is `[]`).
3. In `tools.go`, replace the `default:` branch:
```go
				default:
					found, err = s.RT.Repos.Search(ctx, "", limit)
```
4. Re-run the test → PASS. Then run `go test ./internal/mcpserver/ ./internal/repos/`.
5. `git add internal/mcpserver/tools.go internal/mcpserver/orchestrator_test.go && git commit -m "fix(mcp): swarm_read repos with empty q lists registered repos"`

### Task 2: The unknown-repo error names the fix (F11)
**Files**: `internal/runtime/confirm_test.go` (extend `TestValidateItemReposRefusesAStaleVersionOrAnUnknownRepo` at `:84`, or `TestConfirmReposRefusesAnUnknownOrAlreadyResolvedRequest` at `:122`, whichever hits `repoNameTx`), `internal/runtime/confirm.go:31,50`

1. Test:
```go
	_, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "p",
		Repos: []ReposProposal{{Repo: "endurio-chat", Reason: "r"}}})
	want := `Unknown repository "endurio-chat". Pass a repository id from swarm_read {repos:{q:"endurio-chat"}}.`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
```
2. Run `go test ./internal/runtime/ -run 'Confirm|ValidateItemRepos'` and watch it FAIL.
3. In both places:
```go
Message: fmt.Sprintf("Unknown repository %q. Pass a repository id from swarm_read {repos:{q:%q}}.", id, id)
```
4. Run it and see PASS. Then update any existing test that asserts the old `Unknown repository X.` text: `grep -rn "Unknown repository" --include='*_test.go' .`
5. Commit `internal/runtime/confirm.go` and the touched `*_test.go` files: `fix(runtime): unknown-repo error points at swarm_read repos search`.

### Task 3: `swarm_ask` stops claiming it blocks (F10)
**Files**: `internal/mcpserver/tools_test.go`, `internal/mcpserver/tools.go:150-151`

1. Test (new):
```go
func TestAskToolDescriptionSaysItReturnsAtOnce(t *testing.T) {
	d := askTool(nil)
	if strings.Contains(d.Description, "block for the answer") ||
		!strings.Contains(d.Description, "Returns at once with the request id") {
		t.Fatalf("description = %q", d.Description)
	}
}
```
2. Run it → FAIL.
3. Set `Description: "Request an approval, propose repos to confirm, forward a native answer, or withdraw an earlier ask. Returns at once with the request id; the answer arrives later as a message."`. Leave the `kind` enum text alone until Tasks 9 and 13b update it.
4. Run it → PASS. Commit: `fix(mcp): swarm_ask description matches its non-blocking contract`.

---

## Phase B: Muse hooks (F1). **Blocks Task 9.**

### Task 4: Probe muse's hook surface (no production code)
**Output**: `internal/adapter/testdata/muse-hook-pretooluse.json`, `muse-hook-posttooluse.json`, `muse-hook-userpromptsubmit.json`, plus one paragraph appended to the doc comment above `(*Muse).ParseHook` recording the facts.

Acceptance (every item must be recorded; nothing may be guessed):
1. The plugin install location and manifest shape muse actually loads **inside a daemon launch**, where `XDG_CONFIG_HOME` points at `~/.swarm/run/launch/<ses>/muse-config` and `HOME` at `…/muse-home` (`internal/adapter/muse.go:240-300`). Read `native-plugin-contract.md` and `capability-examples.json` under `~/.local/share/muse/skills/bundled/muse-core/skills/create-plugin/references/`.
2. The exact stdin JSON for `PreToolUse` and `PostToolUse` of `request_user_input`, and for `UserPromptSubmit`: the field names for the tool name, tool input, tool response and prompt text. Capture them with a throwaway plugin whose hook runs `cat > /tmp/muse-hook-$EVENT.json` in a scratch muse TUI. Do **not** do this in a Swarm-managed session.
3. Whether a PreToolUse hook can deny `request_user_input`, and the exact stdout/exit-code shape that does it.
4. The muse tool name as the hook sees it (the transcripts show `request_user_input`).

Commit the fixtures and the comment: `test(adapter): muse hook payload fixtures from live probe`. **Stop and report** if item 3 is "cannot deny". In that case Task 9 must not refuse `swarm_ask question` for parented muse agents without a fallback. Escalate it as a new OQ.

### Task 5: `Muse.ParseHook` fills tool fields
**Files**: `internal/adapter/muse_test.go`, `internal/adapter/muse.go:371-380`
**Produces**: `HookInput{ProviderSessionID, Event, ToolName, RawToolInput, ToolResponse, Prompt, Command}`, the same fields `Claude.ParseHook` fills.

1. Test (table-driven over the three fixtures):
```go
func TestMuseParseHookReadsProbedFixtures(t *testing.T) {
	m := newMuse(Deps{})
	raw, _ := os.ReadFile("testdata/muse-hook-pretooluse.json")
	in, err := m.ParseHook("PreToolUse", raw)
	if err != nil {
		t.Fatal(err)
	}
	if in.ToolName != "request_user_input" || len(in.RawToolInput) == 0 || in.ProviderSessionID == "" {
		t.Fatalf("PreToolUse = %+v", in)
	}
	// PostToolUse: ToolResponse non-empty; UserPromptSubmit: Prompt non-empty.
}
```
2. Run `go test ./internal/adapter/ -run Muse` → FAIL. Update the existing muse test that asserts the no-op (`grep -n "ParseHook" internal/adapter/muse_test.go`) to the new contract in the same commit.
3. Implement with the field names recorded in Task 4.
4. Run → PASS. Commit: `feat(adapter): muse ParseHook reads tool name, input, response, prompt`.

### Task 6: `Muse.HookOutput` emits the probed deny/context shape
**Files**: `internal/adapter/muse_test.go`, `internal/adapter/muse.go:369`
1. Test: `HookOutput("PreToolUse", HookDecision{Block: true, Reason: "x"})` equals the exact bytes from Task 4 item 3, and `HookOutput("PreToolUse", HookDecision{})` is `nil`.
2. Run it and see it FAIL. Implement. Run it and see PASS. Commit: `feat(adapter): muse HookOutput deny shape`.

### Task 7: The muse hook plugin is wired per launch, and doctor checks it
**Files**: `internal/adapter/muse.go` setupEnv (`:240-300`) and `internal/adapter/muse_test.go`. Claude registers its hooks **per launch** in `settingsJSON` (`internal/adapter/claude.go:27-42`), not at install time, and muse should follow the same pattern if Task 4 item 1 allows it. Also `internal/install/muse.go` and `muse_test.go` for the doctor check. New plugin files go wherever Task 4 item 1 says muse loads them from.
1. Test: after muse setupEnv for a Spec, the plugin manifest in the launch config registers `PreToolUse`, `PostToolUse` and `UserPromptSubmit`. Each has the structured argv `[<Spec.Bin>, "hook", "muse", "<Event>"]`, which is the same `swarm hook <agent> <event>` entry (`cmd/swarm/hook.go:10`) and the same event tokens (`PreToolUse`, …) that claude's `hook(...)` uses at `claude.go:30,40-42`. `CheckMuse` returns a failing `muse hooks` check when the manifest is missing. Update the doc comments at `muse.go:13` and `:53` ("no hooks step") in the same change.
2. Run it and see it FAIL. Implement. Run it and see PASS. Commit: `feat(install): muse hook plugin`.

### Task 8: End-to-end hook behaviour for muse
**Files**: `internal/hook/handler_test.go` (add sub-tests next to `TestQuestionToolInterceptionCreatesHITLRequest` `:868` and `TestParentedAgentQuestionToolIsBlockedAndOpensNoRequest` `:1142`), `internal/hook/handler.go:165-175` (comment only)
1. Tests, using the Task 4 fixtures as input with `runtime.Muse`:
   - Top-level muse: PreToolUse `request_user_input` creates one `question` row with `is_hitl=1`, and the matching PostToolUse closes it `via terminal`.
   - Parented muse: PreToolUse returns the Task 6 deny bytes containing `nativeQuestionRelay`, and no row is created.
   - Muse UserPromptSubmit with human text closes the agent's open question rows.
2. Run them and see them FAIL (or PASS if Tasks 5-7 already cover everything; then the commit carries only the tests). Change the comment block so muse is listed and its status reflects the probe. Commit: `test(hook): muse question intercept, relay block, terminal close`.

### Task 4b: Live check of the codex and agy question hooks (no production code)
Docs (agy) and source (codex) say the question hook fires (spec section 1.7). This task confirms it live before Task 9 relies on it.
**Output**: `internal/adapter/testdata/codex-hook-pretooluse-request_user_input.json`, `codex-hook-posttooluse-request_user_input.json`, `agy-hook-pretooluse-ask_question.json`, `agy-hook-posttooluse-ask_question.json`.

Acceptance:
1. In a scratch (non-Swarm) session of each CLI, with a hook that runs `cat > /tmp/<cli>-$EVENT.json`, make the agent ask one question. Record the payloads.
2. **Ordering:** does PreToolUse run *before* the dialog renders? Timestamp the hook file and note when the dialog appears. Gemini CLI's `ask_user` runs its hook after the answer ([gemini-cli#20605](https://github.com/google-gemini/gemini-cli/issues/20605)); check that agy does not do the same.
3. The existing deny output is honoured: codex `{"decision":"block"}` (`codex.go:139`) and agy `{"decision":"deny"}` (`agy.go:187`). No dialog appears and the reason reaches the model.
4. The codex `tool_response` shape for `request_user_input`, which `extractToolResponseText` must handle. Add a case there if the shape is new.
5. Add handler sub-tests that replay the fixtures (next to `handler_test.go:868` and `:1142`): top-level opens a row and PostToolUse closes it; parented is blocked. Commit: `test(hook): codex and agy question hook fixtures from live check`.

**If item 2 or 3 fails for a kind**, add that kind to the cursor exception in Task 9 (keep `swarm_ask question` for it), and record the result in the spec section 1.7 table.

---

## Phase C: Protocol (F2, F5-F9)

### Task 9: `swarm_ask kind:"question"` is refused for every agent kind whose native question is hooked (claude, codex, agy, muse); cursor keeps it
**Depends**: Tasks 4-8 (muse) and 4b (codex, agy). If a kind fails 4b, it joins cursor in the exception.
**Files**: `internal/runtime/requests.go:388-398`, `internal/runtime/requests_test.go`, `internal/hook/handler.go:176-181` (`isQuestionTool` gets `AskQuestion`, and the comment block is rewritten from spec 1.7 with its URLs), and every test that uses `Ask(…Kind:"question")` as a fixture. Find them with `grep -rn 'Kind: "question"\|"kind":"question"' --include='*_test.go' .`. Known locations: `internal/runtime/requests_test.go` (`:24,65,104,135,300,315,356,385,416,492,630,669`), `pause_test.go:200`, `reconcile_test.go:517,1990,2012,2040`, `internal/hook/handler_test.go:1185,1211`, `internal/mcpserver/tools_test.go:218,283,297`, `cmd/swarm/runtime_cmds_test.go`, `internal/advisor/context_test.go`, `internal/runtime/text_test.go`, `scripts/e2e/spike_test.go`.

1. Tests:
```go
func TestSwarmAskQuestionIsRefusedForHookedKindsOnly(t *testing.T) {
	for _, tc := range []struct {
		kind    AgentKind
		refused bool
	}{{Claude, true}, {Codex, true}, {Agy, true}, {Muse, true}, {Cursor, false}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s, _, _ := newStore(t)
			ctx := context.Background()
			_, a, _, _ := s.StartSpike(ctx, SpikeInput{Name: "Ask me", Intent: "feature", Kind: Fake, Model: "fake-1"})
			s.DB.ExecContext(ctx, `UPDATE agents SET kind = ? WHERE id = ?`, string(tc.kind), a.ID)
			ses, _ := s.LatestSession(ctx, a.ID)
			_, err := s.Ask(ctx, ses.ID, AskInput{Kind: "question", Prompt: "Anything?"})
			if tc.refused != (err != nil && strings.Contains(err.Error(), errQuestionUseNativeTool)) {
				t.Fatalf("err = %v, refused want %v", err, tc.refused)
			}
		})
	}
}
```
   Rewrite `TestAskQuestionOpensARequestAndNotifies` (`requests_test.go:19`) to drive `s.AskQuestion(...)` and keep its assertions. `TestParentedAgentCannotOpenQuestionOrBlocker` (`:604`) is unchanged. In `handler_test.go`, add `AskQuestion` to the tool names that `TestQuestionToolInterceptionCreatesHITLRequest` covers.
2. Run `go test ./internal/runtime/ -run SwarmAskQuestionIsRefused` → FAIL.
3. Implement:
```go
// requests.go
const errQuestionUseNativeTool = "Ask the user with your own native question tool " +
	"(claude AskUserQuestion, codex request_user_input, agy ask_question, muse request_user_input). " +
	"Swarm shows it in Needs you and closes it when the user answers."

var questionHookKinds = map[AgentKind]bool{Claude: true, Codex: true, Agy: true, Muse: true}

	case "question":
		refused := false
		if err := s.tx(ctx, func(tx *sql.Tx) error { // sessionAndAgent takes *sql.Tx (inbox.go:136)
			_, a, err := s.sessionAndAgent(ctx, tx, sessionID)
			if err != nil {
				return err
			}
			if err := requireTopLevel(a); err != nil {
				return err
			}
			refused = questionHookKinds[a.Kind]
			return nil
		}); err != nil {
			return Request{}, err
		}
		if refused {
			return Request{}, &items.Error{Code: items.CodeBadRequest, Message: errQuestionUseNativeTool}
		}
		return s.askQuestion(ctx, sessionID, in)
```
   `Fake` is not in the map, so the fake-agent tests that use `swarm_ask question` keep passing unchanged. Only fixtures running as a hooked kind need porting.
4. Port each fixture that runs as a hooked kind from `Ask(ctx, sesID, AskInput{Kind: "question", Prompt: p, Options: o})` to `AskQuestion(ctx, sesID, p, o)`. MCP tests on hooked kinds assert the refusal and create any needed row with `s.RT.AskQuestion`.
5. Run `go test ./...` → PASS. Commit: `feat(runtime): swarm_ask question refused where the native question tool is hooked`.

### Task 10: `swarm_send` answers require a valid `reply_to`, stored in `messages.reply_to` (F7, F9)
**Files**: `internal/runtime/inbox_test.go`, `internal/runtime/inbox.go:487-545`, `internal/mcpserver/tools.go:235-266`, `internal/mcpserver/tools_test.go`
**Produces**: `Send(ctx, sessionID, to string, kind MessageKind, body, replyTo, requestID string, options ...string) (string, error)`

1. Tests in `inbox_test.go` (use `worker(t, s)` → `orch, w, wSes`; `orchSes := mustSessionID(t, s, orch.ID)`):
```go
func TestAnswerNeedsAValidReplyTo(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	orchSes := mustSessionID(t, s, orch.ID)
	q, err := s.Send(ctx, wSes.ID, "parent", "question", "which db?", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, orchSes, w.Name, "answer", "postgres", "", ""); err == nil ||
		!strings.Contains(err.Error(), errAnswerNeedsReplyTo) {
		t.Fatalf("missing reply_to err = %v", err)
	}
	if _, err := s.Send(ctx, orchSes, w.Name, "answer", "postgres", "msg_bogus", ""); err == nil {
		t.Fatal("bogus reply_to accepted")
	}
	a, err := s.Send(ctx, orchSes, w.Name, "answer", "postgres", q, "")
	if err != nil {
		t.Fatal(err)
	}
	var replyTo, corr sql.NullString
	s.DB.QueryRowContext(ctx, `SELECT reply_to, correlation_id FROM messages WHERE id = ?`, a).Scan(&replyTo, &corr)
	if replyTo.String != q || corr.Valid {
		t.Fatalf("reply_to=%v correlation_id=%v", replyTo, corr)
	}
}

func TestAnswerMayReplyToABlockedRelay(t *testing.T) {
	// worker writes a blocked checkpoint → a relay to orch; orch answers with reply_to=<relay id> → accepted.
}
```
   Also cover this: a `finding` with a non-empty `reply_to` is still accepted unvalidated (today's behaviour for findings). Then check with `grep -n "reply_to" internal/mcpserver/tools_test.go internal/runtime/inbox_test.go` whether any existing test sends `answer` without `reply_to`, and port it to pass a real question id.
2. Run `go test ./internal/runtime/ -run 'Answer'` → FAIL.
3. Implement inside `Send`'s tx, after `target` is resolved:
```go
		if kind == "answer" {
			if replyTo == "" { // Send's correlationID param is renamed replyTo
				return &items.Error{Code: items.CodeBadRequest, Message: errAnswerNeedsReplyTo}
			}
			var ok int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM messages m
				WHERE m.id = ? AND m.to_agent_id = ?
				  AND ((m.kind = 'question' AND m.from_agent_id = ?)
				    OR (m.kind = 'relay' AND json_extract(m.payload_json, '$.event') = 'blocked'
				        AND json_extract(m.payload_json, '$.agent') = ?))`,
				replyTo, a.ID, target.ID, target.Name).Scan(&ok)
			if errors.Is(err, sql.ErrNoRows) {
				return &items.Error{Code: items.CodeBadRequest,
					Message: fmt.Sprintf(errAnswerBadReplyTo, replyTo, target.Name)}
			}
			if err != nil {
				return err
			}
		}
```
   Enqueue with `ReplyTo: replyTo` when `kind == "answer"`, and keep `CorrelationID: replyTo` for other kinds (a `finding`'s `reply_to` has no FK guarantee, so it stays in `correlation_id`).
4. `tools.go`: update the schema per spec 4.2 and pass `in.Options...` (options are validated in Task 11). Update the stale comment at `:255-258`.
5. Run `go test ./internal/runtime/ ./internal/mcpserver/` → PASS. Commit: `feat(runtime): swarm_send answers carry a validated reply_to`.

### Task 11: A question carries `options`
**Files**: `internal/runtime/inbox_test.go`, `internal/runtime/inbox.go`, `internal/mcpserver/tools_test.go`
1. Tests:
   - `Send(…, "question", "which db?", "", "", "postgres", "sqlite")` stores the payload `{"body":"which db?","options":["postgres","sqlite"]}`.
   - `options` on `finding` is refused.
   - More than 10 options is refused.
   - An option longer than 200 chars is refused.
   - Through MCP, `{"to":"parent","kind":"question","body":"b","options":["a","b"]}` round-trips into the parent's `swarm_sync` message payload.
   - `{"to":"parent","kind":"question","body":"may I drop table x?","approval":true}` stores `{"body":…,"options":["Approve","Request changes"],"approval":true}` (the `SendApproval` helper). `approval:true` is refused on any kind other than `question` and on any target other than `parent`.
2. Run them and see them FAIL. Implement:
```go
		p := map[string]any{"body": body}
		if len(options) > 0 {
			if kind != "question" {
				return &items.Error{Code: items.CodeBadRequest, Message: "options are only for kind question."}
			}
			if len(options) > 10 {
				return &items.Error{Code: items.CodeBadRequest, Message: "A question takes at most 10 options."}
			}
			for _, o := range options {
				if utf8.RuneCountInString(o) > 200 {
					return &items.Error{Code: items.CodeBadRequest, Message: "Each option is limited to 200 characters."}
				}
			}
			p["options"] = options
		}
		payload, err := json.Marshal(p)
```
3. Add `SendApproval(ctx, sessionID, body, requestID)`, which calls the same code with `to:"parent"`, the fixed options and `p["approval"]=true`. Wire the MCP `approval` field to it.
4. Run them and see PASS. Commit: `feat(runtime): swarm_send questions carry options and an approval flag`.

### Task 12: Owed-answer relay (F8)
**Files**: `internal/runtime/reconcile_test.go` (model it on `TestUndeliveredMessageRelaysToSenderOnceAfterGracePeriod`, `:1205`), `internal/runtime/reconcile.go` (new func, called right after `notifyUndeliveredMessages` at `:214`)
1. Test:
```go
func TestUnansweredQuestionRelaysOnceToTheOrchestrator(t *testing.T) {
	s, tm, at := clockStore(t)
	ctx := context.Background()
	orch, w, wSes := worker(t, s)
	_ = tm
	q, _ := s.Send(ctx, wSes.ID, "parent", "question", "which db?", "", "")
	s.DB.ExecContext(ctx, `UPDATE messages SET state = 'acked' WHERE id = ?`, q)
	at.Advance(9 * time.Minute)
	s.Reconcile(ctx)
	if n := relaysWithReplyTo(t, s, q); n != 0 { t.Fatalf("early relays = %d", n) }
	at.Advance(2 * time.Minute)
	s.Reconcile(ctx)
	s.Reconcile(ctx)
	if n := relaysWithReplyTo(t, s, q); n != 1 { t.Fatalf("relays = %d, want exactly 1", n) }
	// payload event, recipient
	var to, payload string
	s.DB.QueryRowContext(ctx, `SELECT to_agent_id, payload_json FROM messages WHERE kind='relay' AND reply_to=?`, q).Scan(&to, &payload)
	if to != orch.ID || !strings.Contains(payload, `"event":"question_unanswered"`) || !strings.Contains(payload, w.Name) {
		t.Fatalf("to=%s payload=%s", to, payload)
	}
}
// + an answered question (Send answer reply_to=q before the timeout) → 0 relays.
// + an approval question answered through native_answer (approval_result with reply_to=q) → 0 relays.
```
   Add a small `relaysWithReplyTo` helper next to the test if nothing like it exists.
2. Run it and see it FAIL. Implement `notifyUnansweredQuestions` with the spec section 3 SQL, `questionAnswerTimeout = 10 * time.Minute`, and payload `{"event":"question_unanswered","agent":<from name>,"item":<key>,"question":<body>}`, enqueued to `q.to_agent_id` with `ReplyTo: q.id`, `Origin: "daemon"`.
3. Run it and see PASS. Run the full `go test ./internal/runtime/`. Commit: `feat(runtime): relay question_unanswered to the orchestrator after 10 min`.

### Task 13: The relay notice shows the question text
**Files**: `internal/runtime/inbox_test.go` (find the existing renderer test with `grep -n "from .* (" internal/runtime/inbox_test.go`), `internal/runtime/inbox.go` relay case (`:651-660`)
1. Test: rendering a relay with `{"event":"question_unanswered","agent":"w","item":"TASK-1","question":"which db?"}` gives `question_unanswered from w (TASK-1): "which db?"`.
2. Run it and see it FAIL. Add after the checkpoint-summary branch:
```go
		if q, ok := str("question"); ok && q != "" {
			line += ": " + quote(q)
		}
```
3. Run it and see PASS. Commit: `feat(runtime): relay notices quote an unanswered question`.

### Task 13a: Daemon-issued native prompts with a ref token
**Files**: `internal/runtime/requests_test.go`, new `internal/runtime/native.go` (`NativePrompt`, `nativePromptFor`, `nativePromptForMsg`, `refToken`, `refFromPrompt`), `internal/runtime/requests.go` (`askApproval` result), `internal/runtime/confirm.go` (`askConfirmRepos` result), `internal/mcpserver/tools.go` (`requestOut` adds `native_prompt` when set)
**Produces**: `Request.NativePrompt *NativePrompt`, and the exact strings from spec section 6.

1. Tests (table over `approve_section`, `approve_plan` with warnings, `approve_report`, `confirm_repos`, `close_spike`): `Ask(...)` returns `NativePrompt` whose `Header`, `Question` and `Options` equal the spec 6 table. The question ends with ` ⟦swarm:<req id>⟧`. `refFromPrompt(q) == req.ID`. `refFromPrompt("plain text") == ""`. There is also an MCP test: the `swarm_ask approval` result JSON has `native_prompt.question`.
2. Run them and see FAIL. Implement:
```go
func refToken(ref string) string { return " ⟦swarm:" + ref + "⟧" }

var refRe = regexp.MustCompile(`⟦swarm:((?:req|msg)_[0-9A-Z]+)⟧`)

func refFromPrompt(p string) string {
	if m := refRe.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return ""
}
```
   `nativePromptFor(req, sectionTitle)` switches on `req.Kind` and returns the spec 6 copy. The 1000-rune prompt cap (`extractQuestion`, `handler.go:70-72`) truncates from the end, so the token must survive: build `Question` as `truncate(body, 1000-len(token)) + token`.
3. Run them and see PASS. Commit: `feat(runtime): approvals return a daemon-issued native prompt`.

### Task 13b: The hook binds a native question row to its ref, and `native_prompt for_msg`
**Files**: `internal/runtime/requests_test.go`, `internal/runtime/requests.go` (`askQuestion` stores `binding_json`; `Ask` case `native_prompt`), `internal/hook/handler_test.go`, `internal/mcpserver/tools.go` (schema: kinds `native_prompt`, `native_answer`, field `for_msg`)
1. Tests:
   - `AskQuestion(ses, "Approve … ⟦swarm:req_X⟧", opts)` stores `binding_json = {"ref":"req_X"}`, and an unbound prompt stores NULL.
   - Hook level: the claude PreToolUse fixture with that prompt produces the bound row.
   - `Ask(orchSes, AskInput{Kind:"native_prompt", ForMsg: q})`, where `q` is a child approval question to this orchestrator, returns the `<child> asks` prompt with `⟦swarm:<q>⟧`. A `ForMsg` that is not an approval question addressed to the caller is refused with `reply_to %s is not a question…` (reuse the Task 10 query, restricted to `json_extract(payload_json,'$.approval') = 1`).
2. Run them and see FAIL. Implement. Run them and see PASS. Commit: `feat(runtime): native question rows bind to their approval ref`.

### Task 13c: `native_answer` records a real approval for request refs
**Files**: `internal/runtime/native_test.go` (new), `internal/runtime/native.go` (`nativeAnswer`), `internal/runtime/requests.go` (`Ask` case `native_answer`), `internal/mcpserver/tools.go` (fields `ref`, `decision` enum, `comment`)
**Consumes**: `Approve`, `RequestChanges`, `ConfirmRepos` (unchanged). The evidence SQL is in spec section 3.

1. Tests (spike fixture from `requests_test.go` plus `AskQuestion` and `ResolveQuestionByPrompt` to simulate the hook):
```go
func TestNativeAnswerApprovesOnlyWithMatchingEvidence(t *testing.T) {
	// setup: spike orchestrator registers an artifact, Ask approval → req (with NativePrompt np)
	// 1. no evidence yet:
	//    Ask{Kind:"native_answer", Ref:req.ID, Decision:"approve"} → errNoNativeEvidence; req still open
	// 2. hook simulation: AskQuestion(ses, np.Question, np.Options); ResolveQuestionByPrompt(ses, np.Question, "Request changes: tighten scope")
	//    native_answer approve → errDecisionMismatch ("Request changes: tighten scope", "Approve"); req still open
	// 3. native_answer request_changes, comment "tighten scope" → req.State == "changes_requested", responded_via "terminal";
	//    orchestrator inbox has approval_result {decision:"changes_requested", evidence:"observed"};
	//    bound row binding_json.evidence == "observed"; RequestWireByID(req.ID).ApprovalEvidence == "observed"
	// 4. repeat → "Already resolved."
}
func TestNativeAnswerAgentReportedIsAcceptedAndFlagged(t *testing.T) {
	// ResolveQuestionByPrompt with answer "" → response_text "Resolved in terminal" (handler fallback, handler.go:551-553)
	// native_answer approve → req.State "approved", responded_via "terminal";
	// bound row binding_json.evidence == EvidenceAgentReported;
	// approval_result payload evidence == "agent_reported";
	// RequestWireByID(req.ID).ApprovalEvidence == "agent_reported";
	// the request.resolved event payload (events table, type request.resolved, latest) has "approval_evidence":"agent_reported"
}
func TestNativeAnswerAgentReportedRequestChanges(t *testing.T) {
	// same setup; native_answer request_changes comment "narrow it" → changes_requested, evidence agent_reported
}
func TestBoardApprovalHasNullEvidence(t *testing.T) {
	// Approve(id, ApproveInput{..., Via:"menubar"}) → RequestWireByID(id).ApprovalEvidence == nil
}
func TestNativeAnswerMismatchIsRefusedEvenWhenTextPresent(t *testing.T) {
	// response_text "Approve" + decision request_changes → errDecisionMismatch (agent_reported only applies to "Resolved in terminal")
}
func TestNativeAnswerRefusesAnotherAgentsEvidence(t *testing.T) {} // row agent_id != caller → errNoNativeEvidence
func TestNativeAnswerStaleSectionConflicts(t *testing.T) {}         // artifact revised → "This request changed…"
func TestNativeAnswerConfirmRepos(t *testing.T) {}                  // approve → repos_confirmed with the proposed ids
```
2. Run them and see FAIL. Implement `nativeAnswer`:
   - (a) Load the evidence row (spec section 3 SQL, `agent_id` = caller).
   - (b) If `response_text` is exactly `"Resolved in terminal"` (or empty), set `evidence = EvidenceAgentReported`. Otherwise it must case-fold-start-with the label, which sets `EvidenceObserved`; if it doesn't, return `errDecisionMismatch`.
   - (c) Dispatch by the ref's kind to `resolve` directly, reusing each method's check and payload:
     - Extract `Approve`'s check closure into `approveCheck(in ApproveInput) func(Request) error` (`Approve` itself becomes a one-line caller, and its tests stay green).
     - For `RequestChanges` and `ConfirmRepos`, call their existing bodies through the same `after` hook.
     - Approvals fill `SectionSHA256`, `ArtifactRevision` and `Binding` from the request itself, with `Via: "terminal"`.
     - An empty comment becomes the free text after the label.
     - The payload map gains `"evidence"`.
   - (d) Give `resolve` a trailing variadic `after ...func(*sql.Tx, Request) error`, run inside its tx **right after the UPDATE and before `RequestWireTx`**. The existing 5 callers are unchanged. `nativeAnswer` passes a func that writes `evidence` into the bound question row's `binding_json` (`json_set(binding_json, '$.evidence', ?)`), so the `request.resolved` event already carries `approval_evidence`.

   Add a comment at the `resolve` call-site note (`requests.go:655-658`): `nativeAnswer` is the one agent-reachable `user_action` origin. It is guarded by the evidence check, and an agent-reported decision is flagged, not refused (user decision, spec 1.6.2).
3. Run them and see PASS. Commit: `feat(runtime): native_answer turns an observed native approval into a real approval`.

### Task 13d: `native_answer` for a child's approval question unblocks the child
**Files**: `internal/runtime/native_test.go`, `internal/runtime/native.go`
1. Test:
   - Child `SendApproval("may I drop table x?")` gives `q`. The orchestrator gets `native_prompt for_msg:q`.
   - The hook simulation binds and answers `Approve`. Run it twice: once with response text `"Approve"`, which must record `observed`, and once with `""`, which must record `agent_reported`. Both approve, and the payload and the row carry the evidence.
   - `native_answer {ref:q, decision:"approve"}` gives: bound row `state == "approved"`; the child's inbox has `approval_result {request_id:<row id>, decision:"approved"}` with `reply_to == q`, origin `daemon`; the child is woken (`WakeClassFor("approval_result")` is immediate).
   - The Task 12 scan sends no `question_unanswered` for `q`.
   - A second `native_answer` for `q` is refused.
2. Run it and see FAIL. Implement the message-ref branch:
```go
	res, err := tx.ExecContext(ctx, `UPDATE requests SET state = ? WHERE id = ? AND state = 'answered'`, newState, row.ID)
	// 0 rows → items.CodeConflict "Already resolved."
	s.enqueue(ctx, tx, Message{Kind: "approval_result", Origin: "daemon", ToAgentID: q.FromAgentID,
		RootItemID: q.RootItemID, ItemID: q.ItemID, ReplyTo: q.ID, RequestID: row.ID,
		Payload: mustJSON(map[string]any{"request_id": row.ID, "decision": newState, "comment": comment, "evidence": evidence})})
	// same UPDATE also sets binding_json = json_set(binding_json, '$.evidence', evidence)
	s.Events.Append(ctx, tx, events.RequestResolved, wire)
```
3. Run it and see PASS. Commit: `feat(runtime): child approval questions resolve into approval_result`.

### Task 13e: `native_pending` and `approval_evidence` on the wire, and a terminal target for approval kinds
**Files**: `internal/runtime/requests.go` (`RequestWire.NativePending`, `RequestWire.ApprovalEvidence`, `RequestWireTx`, `terminalAgent`), `internal/runtime/requests_test.go`, `internal/httpapi/*_test.go` (only if a golden `/api/state` body exists; check with `grep -rn '"terminal_agent"' internal/httpapi`)
1. Tests:
   - `approve_section` with an open bound question row gives `native_pending: true`, and `false` once that row is answered.
   - `approval_evidence` is null for an open request, null for a board-approved one, and the bound row's value after `native_answer`. For a message-ref row it is read from the row's own `binding_json.evidence`. The SQL is in spec section 3.
   - `GET /api/requests/{id}` includes `approval_evidence`: add an assertion to the existing httpapi request-route test (`grep -rn "requests/{id}" internal/httpapi/*_test.go`).
   - `terminal_agent` for `approve_*`, `confirm_repos` and `close_spike` is the root orchestrator's name. For `accept_epic` it is the root item's live orchestrator, or null when none is live.
   - Update the existing assertion that approvals have `terminal_agent == null` (the 09-21 decision 5 test; find it with `grep -rn "TerminalAgent" internal/runtime/*_test.go`) to the new contract.
2. Run them and see FAIL. Implement: the SQL is in spec section 3, and the `!r.IsHITL` guard at `requests.go:224` becomes a kind switch. Run them and see PASS. Commit: `feat(runtime): request wire carries native_pending and approval terminal targets`.

### Task 14: Skill text (F5, F6)
**Files**: `skills/swarm/SKILL.md:17`, `skills/swarm-orchestrator/SKILL.md:43,64`, mirrors under `internal/install/skills/`, and any skill-content test (`grep -rn "native question tool\|swarm_ask" internal/install/*_test.go`)
1. Test: if a skills content test exists, add assertions that `swarm-orchestrator` contains `kind: "answer", reply_to:`, `kind: "native_answer"` and `native_prompt`, and that `swarm` contains `approval: true` and does not contain `native question tool or \`swarm_ask\``. If none exists, the verification is `make skills-sync && git diff --exit-code internal/install/skills` after the commit.
2. Apply the exact replacements from spec section 6.
3. Run `make skills-sync`, then `go test ./internal/install/`. Commit the four files: `docs(skills): native-only user questions, reply_to answers, native approvals via native_answer`.
**Depends**: Tasks 9-13e (the skill must describe the shipped tools).

---

## Phase D: Menubar (F3, F4). Task 15 needs Task 13e's `native_pending` in the wire fixture; Tasks 16-17 are independent.

### Task 15: Needs you = every open request, minus approvals whose native prompt is open
**Files**: `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift` (`:127`, `:147`, `:192`, `:224`), `apps/menubar/Sources/SwarmBarKit/AppModel.swift:195,287-310`, `apps/menubar/Sources/SwarmBarKit/Wire.swift:106-150` (`nativePending`), `apps/menubar/Tests/Fixtures/state.json`, `apps/menubar/Tests/SwarmBarTests/FixtureTests.swift`
1. Tests:
   - Update `testNeedsYouRowsAndViewAll` with requests `[question(HITL), prompt(HITL), blocker(HITL), approveSection, acceptEpic, approvePlan(nativePending:true)]`. Assert `openRequests.map(\.id) == [the first five]` and `viewAllRequests == "View all 5 requests"`. Keep the `RequestLine` assertions (web and notifications still use it).
   - Update `testNeedsYouOpensOncePerNewRequest`: a new open approval now does pop the section open, because it is in Needs you.
   - Update `testNeedsYouRowsAreReadOnlyAndOpenTheOrchestratorTerminal`: `requestTarget` for an approval with `terminalAgent: "o"` is `.terminal("o")`. With nil it is nil, and `openRequest` then calls `review` (the board URL is asserted as in `:176`).
   - FixtureTests: `native_pending` decodes, and defaults to false when absent.
2. `cd apps/menubar && swift test --filter 'AppModelTests|FixtureTests'` → FAIL.
3. Implement:
```swift
    static func needsYou(_ rs: [SwarmRequest]) -> [SwarmRequest] {
        rs.filter { $0.state == "open" && !$0.nativePending }.sorted { $0.createdAt < $1.createdAt }
    }
    public var openRequests: [SwarmRequest] { Self.needsYou(state.requests) }
    // apply(): let open = Set(Self.needsYou(s.requests).map(\.id))
    public func requestTarget(_ r: SwarmRequest) -> RequestTarget? {
        guard let name = r.terminalAgent else { return nil }   // isHITL guard removed
        ... // unchanged below
    }
    public func openRequest(_ r: SwarmRequest) async {
        switch requestTarget(r) {
        case let .terminal(name)?: await openTerminal(name)
        case nil: review(r)
        case .unavailable(_)?: break
        }
    }
```
4. Run them and see PASS. Commit: `feat(menubar): Needs you lists every request waiting on the user`.

### Task 16: Warning-yellow dot for Needs you; the offline dot is removed
**Files**: `apps/menubar/Tests/SwarmBarTests/MenuLabelTests.swift:135-143`, `apps/menubar/Tests/SwarmBarTests/PopoverRenderTests.swift:9` (`testMenuBarLabelRendersEveryState`), `apps/menubar/Sources/SwarmBarKit/MenuLabel.swift:13-16,28,53`, `apps/menubar/Sources/SwarmBarKit/AppModel.swift:227-229`, `apps/menubar/Sources/SwarmBarUI/MenuBarLabel.swift:62-90`, `apps/menubar/Sources/SwarmBarKit/Copy.swift`
1. Test: rewrite `testActiveBadgeFollowsLiveAgents`. Its two offline assertions (`:142-143`, `.red`) change to the new contract (`.none`), not deleted:
```swift
        func badge(active: Int, connected: Bool, needsYou: Int = 0) -> MenuLabel.Badge {
            MenuLabel.make(activeCount: active, connected: connected, needsYou: needsYou, enabled: [.claude], usage: [],
                           compact: false, format: format).badge
        }
        XCTAssertEqual(badge(active: 1, connected: true), .green)
        XCTAssertEqual(badge(active: 0, connected: true), .none, "nothing running")
        XCTAssertEqual(badge(active: 3, connected: false), .none, "offline shows no dot (user decision 2026-09-25)")
        XCTAssertEqual(badge(active: 0, connected: false), .none)
        XCTAssertEqual(badge(active: 1, connected: true, needsYou: 1), .yellow, "needs you wins over live")
        XCTAssertEqual(badge(active: 0, connected: false, needsYou: 2), .yellow, "cached needs-you still shows")
```
   Add an AppModelTests case: one open request in state gives `m.label.badge == .yellow`. Update `testMenuBarLabelRendersEveryState` to render `.yellow` where it rendered `.red`.
2. Run `swift test --filter 'MenuLabelTests|AppModelTests|PopoverRenderTests'` → FAIL.
3. Implement:
   - `enum Badge { case none, green, yellow }`.
   - `make(activeCount:connected:needsYou: Int = 0, …)` with `badge: needsYou > 0 ? .yellow : (connected && activeCount > 0) ? .green : .none`.
   - `AppModel.label` passes `needsYou: openRequests.count`.
   - `MenuBarLabelImage.badgeColor`: `.yellow → .yellow`, `.green → .green`.
   - Accessibility: `.yellow → Copy.needsYouBadge`, `.green → Copy.agentsWorking`, otherwise `Copy.appTitle`.
   - Add `Copy.needsYouBadge = "Swarm needs you"`.
   - Update the doc comments at `MenuLabel.swift:13-14` and `MenuBarLabel.swift:62-64`.

   Green stays (user decision, spec 1.6.3).
4. Run them and see PASS. Commit: `feat(menubar): yellow needs-you dot; offline dot removed`.

### Task 17: Generic yellow row for every kind
**Files**: `apps/menubar/Sources/SwarmBarUI/Popover/NeedsYouSection.swift` (`RequestRow`), `apps/menubar/Sources/SwarmBarKit/Copy.swift`, `apps/menubar/Tests/SwarmBarTests/AppModelTests.swift`, `PopoverRenderTests.swift`
1. Tests:
   - Copy: `needsYouMessage == "Waiting for your input"` (no trailing period, locked), `openAgentTerminal == "Open agent terminal"`, `openOnBoard == "Open in Swarm board"`.
   - `NeedsYouRow.lines` for a question with agent `go-migration-agent-debug` on `SPIKE-16 · go-migration-agent-debug` gives `["SPIKE-16 · go-migration-agent-debug", "go-migration-agent-debug", "Waiting for your input"]`.
   - For an `acceptEpic` with no agent, line 2 is `"—"`.
   - For every `RequestKind`, `lines` never contains `r.prompt` (loop over `RequestKind.allCases`, or the explicit list used at `AppModelTests.swift:167`, with prompt `"SECRET"`).
   - A PopoverRenderTests case renders a mixed list without crashing.
2. Run them and see FAIL.
3. Implement:
```swift
    public enum NeedsYouRow {   // SwarmBarKit
        public static func lines(_ r: SwarmRequest) -> [String] {
            ["\(r.itemKey) · \(r.itemTitle)", r.agentName ?? r.terminalAgent ?? "—", Copy.needsYouMessage]
        }
    }
```
   `RequestRow` becomes a single branch (the old HITL/non-HITL split and its Review button are deleted; the tests above cover the replacement):
```swift
        let target = model.requestTarget(request)
        let l = NeedsYouRow.lines(request)
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                Text(l[0]).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                Text(l[1]).font(.callout).lineLimit(1)
                Text(l[2]).font(.callout)
                if case let .unavailable(hint)? = target {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
            }
            Spacer()
            Button { Task { await model.openRequest(request) } } label: { Image(systemName: "terminal") }
                .buttonStyle(.borderless)
                .disabled({ if case .unavailable? = target { return true }; return false }())
                .help(target == nil ? Copy.openOnBoard : Copy.openAgentTerminal)
        }
        .padding(8)
        .background(Color.yellow.opacity(0.18), in: RoundedRectangle(cornerRadius: 6))
```
4. Run `swift test` → PASS. Commit: `feat(menubar): generic yellow Needs-you row with terminal button`.

---

### Task 17a: Web shared Needs-you logic
**Files**: `web/src/logic/inbox.test.ts`, `web/src/logic/inbox.ts`, `web/src/types.ts:~204` (`Request`), `web/src/mock/fixtures.ts:80`, `web/src/copy.ts`, `web/src/copy.test.ts`, `web/src/contract.test.ts`
**Depends**: Task 13e (the wire fields).
1. Tests (`inbox.test.ts`, extend, keep existing cases, port the `is_hitl`-based expectations):
```ts
it("needsYou = open and not native_pending, oldest first, every kind", () => {
  const rs = [req({ id: "q", kind: "question", is_hitl: true, created_at: 1 }),
              req({ id: "a", kind: "approve_section", is_hitl: false, created_at: 2 }),
              req({ id: "p", kind: "approve_plan", native_pending: true, created_at: 3 }),
              req({ id: "e", kind: "accept_epic", created_at: 4 }),
              req({ id: "x", kind: "question", state: "answered", created_at: 5 })];
  expect(needsYou(rs).map((r) => r.id)).toEqual(["q", "a", "e"]);
  expect(filterRequests(rs, "all").map((r) => r.id)).toEqual(["q", "a", "e"]);
  expect(filterRequests(rs, "questions").map((r) => r.id)).toEqual(["q"]);
  expect(filterRequests(rs, "approvals").map((r) => r.id)).toEqual(["a"]);
  expect(filterRequests(rs, "reviews").map((r) => r.id)).toEqual(["e"]);
});
it("needsYouRow is generic and never shows the prompt", () => {
  const r = req({ item_key: "SPIKE-16", item_title: "go-migration-agent-debug", agent_name: "go-migration-agent-debug", prompt: "SECRET" });
  expect(needsYouRow(r)).toEqual(["SPIKE-16 · go-migration-agent-debug", "go-migration-agent-debug", "Waiting for your input"]);
  expect(needsYouRow({ ...r, agent_name: null, terminal_agent: null })[1]).toBe("—");
});
it("requestTarget works for approvals with a terminal_agent", () => {
  expect(requestTarget(req({ kind: "approve_plan", is_hitl: false, terminal_agent: "o" }), [liveAgent("o")]))
    .toEqual({ kind: "terminal", agent: "o" });
});
```
   `copy.test.ts`: `C.needsYouMessage === "Waiting for your input"`, `C.openAgentTerminal === "Open agent terminal"`. `contract.test.ts`: `native_pending` and `approval_evidence` exist in the Request contract. `fixtures.ts`: default `native_pending: false` and `approval_evidence: null`.
2. `cd web && pnpm vitest run src/logic/inbox.test.ts src/copy.test.ts src/contract.test.ts` → FAIL.
3. Implement per spec 4.4:
```ts
export const needsYou = (reqs: Request[]) =>
  reqs.filter((r) => r.state === "open" && !r.native_pending).sort((a, b) => a.created_at - b.created_at);
const QUESTIONS = new Set(["question", "prompt", "blocker"]);
const REVIEWS = new Set(["accept_epic", "accept_fix"]);
export function filterRequests(reqs: Request[], f: InboxFilter): Request[] {
  const set = needsYou(reqs);
  if (f === "questions") return set.filter((r) => QUESTIONS.has(r.kind));
  if (f === "reviews") return set.filter((r) => REVIEWS.has(r.kind));
  if (f === "approvals") return set.filter((r) => !QUESTIONS.has(r.kind) && !REVIEWS.has(r.kind));
  return set;
}
export const needsYouRow = (r: Request): [string, string, string] =>
  [`${r.item_key} · ${r.item_title}`, r.agent_name ?? r.terminal_agent ?? "—", C.needsYouMessage];
// requestTarget: drop `!r.is_hitl ||` from the guard.
```
4. Run → PASS. Commit: `feat(web): shared needs-you rules and generic row text`.

### Task 17b: Web Needs-you panel and header count use the shared rules
**Files**: `web/src/panels/NeedsYou.tsx:39-80`, `web/src/App.tsx:105`, `web/src/App.test.tsx` / `web/src/App.flows.test.tsx` (inbox cases, found with `grep -n "Needs you\|inbox" web/src/App*.test.tsx`)
1. Tests:
   - The header shows `Needs you 3` for the Task 17a fixture set, not the raw request count.
   - The inbox list renders three items. Each row has the three generic lines and no prompt text (`queryByText("SECRET")` is null).
   - Each row has a terminal button labelled `Open agent terminal`. When the row's target is `{kind:"terminal"}`, clicking it calls `terminal.run(agent)`.
   - Clicking the row body selects the request, and the detail pane still renders `renderReview`, so existing approve and request-changes flow tests keep passing unchanged.
   - Update the existing assertions that counted `requests.data.length`, or expected prompt text in the list.
2. Run `pnpm vitest run src/App.test.tsx src/App.flows.test.tsx` → FAIL.
3. Implement:
   - `App.tsx:105`: `needsYou={needsYou(requests.data ?? []).length}`.
   - `NeedsYou.tsx`: each `<li>` becomes a `div` with `rounded bg-warn/15 px-2 py-1` (plus `ring-1 ring-warn` when selected). It holds a select `<button>` rendering `needsYouRow(r)` lines 1-3 (`truncate`, `text-muted` for line 1), and an icon `<button aria-label={C.openAgentTerminal}>`. The icon is disabled when `requestTarget` is `unavailable`, and hidden when it is null.
   - Remove `inboxRow` if it has no other users. Its test cases are ported to `needsYouRow`, not deleted.
4. Run `pnpm test` (the whole web suite) → PASS. Commit: `feat(web): Needs-you inbox uses the generic yellow row and shared count`.

---

## Phase B6: confirm_repos native routing polish (spec §1.8)

### Task 6.1: `confirm_repos` prompt lists every repo Approve will confirm
**Files**: `internal/runtime/native_test.go`, `internal/runtime/native.go` (`nativePromptFor`'s `KindConfirmRepos` case, `~:85-106`)
1. Test: extend `TestNativePromptForBuildsExactCopy`'s `confirm_repos` case (and add a second case) so a request whose `options_json` has both `proposed` (one entry marked `"source":"dropped"`) and `expansion` produces `Question` = `"Confirm <N> repositories for <KEY>: <kept1>, <kept2>, <expansion1>?\nDropped: <dropped1>."` + the ref token, where `N` counts every kept name (proposed non-dropped + all of expansion). A case with no dropped entries keeps the old one-line text (no `\nDropped:` clause).
2. Run `go test ./internal/runtime/ -run TestNativePromptForBuildsExactCopy` → FAIL.
3. Implement: read both `Proposed` and `Expansion` from `req.Options`, build `names` from proposed entries whose `Source != "dropped"` plus every expansion entry, and a separate `dropped` slice from proposed entries with `Source == "dropped"`; append `"\nDropped: " + strings.Join(dropped, ", ") + "."` when `len(dropped) > 0`, before `truncateWithToken`.
4. Run it and see PASS. Also run `TestAskConfirmReposReturnsNativePrompt` (unaffected: no dropped/expansion entries there, same text as today). Commit: `feat(runtime): confirm_repos native prompt lists expansion and dropped repos`.

### Task 6.2: typed free text is accepted; a contradicting option label is still refused
**Files**: `internal/runtime/native_answer_test.go`, `internal/runtime/native.go` (`matchDecisionEvidence`, `~:186`; the comment-required check in `nativeAnswer`, `~:233`)
1. Tests (new cases, existing ones stay and must still pass):
   - `matchDecisionEvidence("Confirm endurio-chat and drop the docs repo", "Approve", "")` → `(EvidenceAgentReported, "Confirm endurio-chat and drop the docs repo", nil)` — typed free text with no option-label prefix is accepted and becomes the comment.
   - `matchDecisionEvidence("Request changes: split the migration", "Approve", "")` → mismatch error (the user's text starts with the *other* label) — this already exists (`TestNativeAnswerApprovesOnlyWithMatchingEvidence` step 2) and must keep passing.
   - `matchDecisionEvidence("actually let's go with request changes", "Approve", "")` → accepted as `agent_reported` (does not *start with* the other label, so it is free text, not a picked option).
   - End-to-end: `hookSimulate(..., "Let's tighten scope first")` then `native_answer` with `Decision: "request_changes"` and no `Comment` succeeds, `ResponseText == "Let's tighten scope first"`, evidence `agent_reported` — this replaces the old refusal.
2. Run `go test ./internal/runtime/ -run 'MatchDecision|NativeAnswer'` → FAIL.
3. Implement: in `matchDecisionEvidence`, after the existing own-label prefix check, check whether `trimmed` starts with the *other* decision's label (case-fold) and return `errDecisionMismatch` only then; every other non-blank text (including the existing blank/"Resolved in terminal" case) returns `EvidenceAgentReported` with `comment` = `callerComment`, or `trimmed` when `callerComment == ""` and `trimmed` isn't blank/"Resolved in terminal". Delete `nativeAnswer`'s `if comment == "" { return …, errors.New("Add a comment describing what to change.") }` block entirely; keep the 2000-character cap check.
4. Run it and see PASS. Run the full `go test ./internal/runtime/`. Commit: `feat(runtime): native_answer accepts typed free text, refuses only a contradicting option label`.

### Task 6.3: Approve on confirm_repos confirms proposed + expansion, minus dropped
**Files**: `internal/runtime/native_answer_test.go`, `internal/runtime/native.go` (`nativeAnswer`'s `KindConfirmRepos` branch, `~:291-306`)
1. Test: extend `TestNativeAnswerConfirmRepos` with an `Expansion` entry alongside `Repos` on the `Ask(... "confirm_repos" ...)` call. After `hookSimulate(..., "Approve")` and `native_answer{Decision:"approve"}`, assert `out.Confirmed` contains both the proposed and expansion repo ids (order-independent). Add a second case: a proposed entry with `Source:"dropped"` is excluded from `out.Confirmed`. Add a `request_changes` case: `confirmed_repos`/`repos_version` on the root item are unchanged (read them before and after) and the outgoing message is `approval_result` with `"decision":"changes_requested"` (not `repos_confirmed`).
2. Run `go test ./internal/runtime/ -run TestNativeAnswerConfirmRepos` → FAIL.
3. Implement: in the `KindConfirmRepos` branch, decode both `Proposed` and `Expansion` from `req.Options` and build `ids` from proposed entries with `Source != "dropped"` plus every expansion entry (expansion entries are never `"dropped"`, but skip any that are, defensively).
4. Run it and see PASS. Commit: `feat(runtime): native_answer confirms proposed and expansion repos together`.

### Task 6.4: Skill copy for the Approve-confirms-everything contract
**Files**: `skills/swarm-orchestrator/SKILL.md:64`
1. No new test: `make skills-sync` re-mirrors the file, and `internal/install`'s existing skill-content test (if any, from Task 14) covers the mirrored copy — run `go test ./internal/install/` after the edit.
2. Edit the confirm_repos sentence in the "Approving through native question tools" bullet to say: Approve on `confirm_repos` confirms every repository listed in the prompt (`repos_confirmed`); to change the list, the user picks Request changes (or types what to change) and the orchestrator re-asks `confirm_repos` with the revised `repos`/`expansion`. A typed answer to any native prompt is forwarded with whichever decision (`approve`/`request_changes`) the user meant, as free-text `comment`.
3. Run `make skills-sync && git diff --exit-code internal/install/skills` (expect a diff before, none after) and `go test ./internal/install/`. Commit: `docs(skills): confirm_repos Approve confirms every listed repo; typed answers forward the meant decision`.

---

## Phase E: Verification (spec section 8)

### Task 18: Full suite and review
1. `go test ./... && go vet ./... && test -z "$(gofmt -l .)"`
2. `make skills-sync && git diff --exit-code internal/install/skills`
3. `cd apps/menubar && swift test`
3b. `cd web && pnpm test`
4. `go test ./scripts/e2e/ -run Spike`
5. Request an Opus review (`superpowers:requesting-code-review`) of the whole branch against the spec.
6. Hand off to the user: they run `make install-daemon` and then the manual scenarios in spec section 8. (The stale SPIKE-16 row was already closed on 2026-09-25, spec 1.4.)

## Dependency summary

```
Phase A (T1-T3) ───────────────────────────────────────────────────────────────┐
Phase B  T4 → T5 → T6 → T7 → T8 ─┐                                              │
         T4b (codex, agy) ───────┴► T9 ─┐                                       ├► T18
Phase C  T10 → T11 → T12 → T13 ─────────┼► T13a → T13b → T13c → T13d → T13e ─► T14
Phase D  T16, T17 (any time);  T15, T17a → T17b after T13e ────────────────────┘
```

## Open questions

None (spec section 10). The only pending facts are the probe results from Task 4 (muse) and Task 4b (codex, agy). Their consequences are already decided:
- A kind whose hook does not fire joins cursor's exception in Task 9.
- A kind with no answer text records `agent_reported` (Task 13c).
