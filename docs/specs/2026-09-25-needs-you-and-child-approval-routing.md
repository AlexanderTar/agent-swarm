# Needs you = everything waiting on the user, and a closed child → orchestrator → user loop with real native approvals

- **Date**: 2026-09-25 (rev 2, the same day, after the user's decisions in section 1.5)
- **Status**: Draft, rev 3. The user answered every open question (rev 1 in section 1.5, rev 2 in section 1.6). None remain.
- **Base**: `origin/main` @ `bb67390` (the local `main` matched `origin/main` on 2026-09-25).
- **Repo**: `agent-swarm` (`internal/runtime`, `internal/hook`, `internal/adapter`, `internal/install`, `internal/mcpserver`, `internal/repos`, `skills/`, `apps/menubar/`).
- **Companion plan**: `docs/plans/2026-09-25-needs-you-and-child-approval-routing.md`
- **Source session**: Muse session `01a0da78-a785-7210-9064-dd55a95a5510`. It answered prompt 1, then ran out of quota in the middle of prompt 2's investigation (`429 Subscription quota exhausted`). This spec finishes that work and checks each claim the session made against the code.

---

## 1. Context

### 1.1 What the user asked (verbatim)

**Prompt 1 (21:36Z):** "I want you to double-check how clarification/approval loop is done from child agents. The requirement is that if a sub-agent has any questions to the user or requires them to approve something explicitly this is routed via MCP to the sub-agents orchestrator where user can act on it, the answer is then relayed back. The orchestrator MUST use a corresponding agent's native question tool to resolve this with the user and the entire loop needs to be closed with very clear and deterministic messaging protocl between sub-agent and orchestrator. Check the code logic and skills, find any weak points and inconsistencies. We already have skills for plan approval forcing the native question tool, we could reuse that"

**Prompt 2 (21:44Z):** "any questions intercepted by hooks need to show in "Needs you" section of the menubar app. nothing else should appear there. and the "needs you" row should be generic showing task, agent name and a generic message + terminal button; yellow warning background. If there's anything in "Needs you" queue, show red dot in a right top corner of the swarm icon in the menu bar. One of the agents has just reported some rubbish in needs you, investigate that. Once done, combine your search with my original query and put together a remediation plan for everything"

Rev 2 applies the user's decisions on rev 1's open questions (section 1.5). Where those decisions conflict with prompt 2, the decisions win.

### 1.2 How the loop works today (traced end to end)

```
child (parented)                  daemon                                  orchestrator (top-level)            user
────────────────                  ──────                                  ────────────────────────            ────
native question tool ──PreToolUse─► handler.go:486 block: nativeQuestionRelay
                                   (hooked: claude, codex, agy; muse: no hook; cursor: hook never fires)
swarm_ask question ───────────────► requests.go:479 requireTopLevel → errRelayToParent
swarm_blocker ────────────────────► requests.go:519 requireTopLevel → errRelayToParent
swarm_checkpoint blocked ─────────► checkpoint.go:1463 relay {event:"blocked"} ───────► inbox
swarm_send kind:question ─────────► inbox.go:492 Send, payload {"body"} ──────────────► inbox (wakes, ImmediateKinds)
                                                                          native tool ──PreToolUse──► handler.go:530
                                                                             AskQuestion → requests row (question, is_hitl=1)
                                                                             ────────────────────────────────────────► Needs you
                                                                          (or) swarm_ask question → the same row, same INSERT
                                                                          ◄────────────── the user answers in the terminal
                                                                          PostToolUse → ResolveQuestionByPrompt (handler.go:548)
                                   ◄── swarm_send kind:answer reply_to:<x>  (reply_to not validated, stored as correlation_id)
inbox ◄───────────────────────────
```

### 1.3 Findings (file:line on `bb67390`)

| # | Finding | Evidence |
|---|---|---|
| F1 | **Muse, the agent kind behind every recent orchestrator, has no hook wiring.** It gets no PreToolUse block or intercept, and nothing closes its rows on PostToolUse or UserPromptSubmit. A muse orchestrator's `request_user_input` creates no Needs-you row. A parented muse child's `request_user_input` is not blocked, so it asks the user in a pane nobody is watching. | `internal/adapter/muse.go:366-380` (`HookOutput` returns nil; `ParseHook` fills only the session id and event). `internal/install/muse.go:13` ("There is no hooks step"). Live: `go-migration-railway-orchestrator` (muse) called `request_user_input` twice (muse session `01a0d824-…`), and the DB has 0 question rows for it. Muse does support plugin `PreToolUse` hooks (`~/.local/share/muse/skills/bundled/muse-core/skills/create-plugin/references/native-plugin-contract.md:81-92`). Swarm has never probed what muse sends on stdin. |
| F2 | **A hook-intercepted question and a `swarm_ask` question produce identical rows.** | Both run the same `INSERT … 'question', 1 …` in `askQuestion` (`internal/runtime/requests.go:483-486`). The hook enters through `AskQuestion` (`:500`) and `swarm_ask` through `Ask` → `case "question"` (`:389`). |
| F3 | **Menubar Needs you shows only HITL kinds and prints free text.** Approval kinds (`approve_*`, `confirm_repos`, `accept_*`, `close_spike`) are left out even though they wait on the user. `apply()` pops the section open for any new open request, approvals included. | `apps/menubar/Sources/SwarmBarKit/AppModel.swift:287-289` (`open && isHITL`), `:46` (`RequestLine.text` returns `r.prompt`), `:195`. `Popover/NeedsYouSection.swift:41-63` (a separate Review-button row for approvals). |
| F4 | **The menubar dot means something else today.** It is green while an agent is live and red while the daemon is offline. There is no Needs-you indicator. | `apps/menubar/Sources/SwarmBarKit/MenuLabel.swift:15,53`. The dot is at the top-right of the glyph (`SwarmBarUI/MenuBarLabel.swift:44-45,65-90`). Tests: `Tests/SwarmBarTests/MenuLabelTests.swift:135-143`. |
| F5 | **The orchestrator skill makes the native tool optional.** It says "ask the user (via `swarm_ask` or native question tool)". | `skills/swarm-orchestrator/SKILL.md:43`, `skills/swarm/SKILL.md:17`. |
| F6 | **A native "Approve" approves nothing.** Line 64 says to forward the native answer into a `swarm_ask` outcome, but `swarm_ask` has no approve path. Only daemon-token HTTP routes approve, with origin `user_action`. | `skills/swarm-orchestrator/SKILL.md:64`, `internal/runtime/requests.go:388-398`, `internal/httpapi/requests.go:18-22,100`, `internal/runtime/requests.go:740-780` (`Approve` → `resolve(…, "user_action", …)`). |
| F7 | **No deterministic question/answer correlation.** `reply_to` is optional and never validated, it is stored in `correlation_id` instead of the FK column `reply_to`, and a question's options are lost. | `internal/mcpserver/tools.go:235-261`, `internal/runtime/inbox.go:492-545`, `internal/runtime/model.go:217-218`, `internal/db/schema/0001_init.sql:225`. |
| F8 | **An owed answer is never tracked.** `progress_deadlock` only covers a last checkpoint of `progress`. | `internal/runtime/reconcile.go:361-364`. `alreadyRelayedForMessage` is at `:1275-1280`. |
| F9 | **A blocked checkpoint reaches the orchestrator as a daemon `relay` (`event:"blocked"`), and no skill says how to answer it.** | `internal/runtime/checkpoint.go:1463-1477`, `skills/swarm-orchestrator/SKILL.md:43`. |
| F10 | **`swarm_ask` claims to "block for the answer" but returns at once with `{"request_id","state":"open"}`.** | `internal/mcpserver/tools.go:150,194-196`. |
| F11 | **`swarm_read {repos:{q:""}}` always returns `[]`, and `confirm_repos` given a repo name fails with no hint.** An empty query goes to `Recent()` (`last_used_at IS NOT NULL`), and `MarkUsed` has no production caller. | `internal/mcpserver/tools.go:421-434`, `internal/repos/service.go:300-302,335-345`, `internal/runtime/confirm.go:31,50`. Live DB: 16 repos, every `last_used_at` NULL. |
| F12 | **The daemon token and every session token are readable by agent processes** (same UID). So "only the user can approve" (skill rule 6) is a policy, not a security boundary. An agent can `curl /api/requests/{id}/approve` with `~/.swarm/run/daemon.token`, or post forged `/hook/…` events with its own session token. | `internal/hook/client.go:30-58` (the hook posts with `SWARM_TOKEN_FILE`), `internal/httpapi/agentio.go:18` (`/hook/{agent}/{event}` is `authSession`), `~/.swarm/run/daemon.token` (mode of the running install). |
| F13 | The question hook reads only `Questions[0]`, and PostToolUse matches the exact prompt. This is **consistent** (Pre and Post agree), so it is not a defect. | `internal/hook/handler.go:48-50,548-556`. |

Two claims from the Muse session **are not carried forward**:
- **Skill drift.** Daemon-spawned agents link `~/.swarm/skills`, which is identical to the repo (`internal/adapter/muse.go:260-264`, `claude.go:122-130`). Only the hand-installed copies are stale.
- **Nested orchestrators.** Each root has exactly one orchestrator (`internal/runtime/agents.go:667-673`).

### 1.4 The "rubbish" Needs-you row: root cause and cleanup

The row is `req_01M3D85QAGEPWZBE8P69KHQV3H` (SPIKE-16, agent `go-migration-agent-debug`, muse, top-level). Its text claims "No repos are registered in Swarm…". There are three causes:
1. **Why it is in Needs you:** the agent used `swarm_ask kind:"question"`. Muse has no hooks, and `swarm_ask` writes a HITL row (F1, F2).
2. **Why its text is false:** `swarm_read {repos:{q:""}}` returned `[]` (F11), and `confirm_repos` rejected the name `endurio-chat` because it wants a repo id.
3. **Why it stayed:** `swarm_ask` returned `{"state":"open"}` at once (F10), and muse never sends the hook events that would close the row (F1).

**Cleanup done on 2026-09-25** (the one live-system action the user authorised). Neither the HTTP API nor the CLI has a withdraw path: `withdraw` exists only as the asking agent's `swarm_ask`. So the supported user-side close was `swarm answer req_01M3D85QAGEPWZBE8P69KHQV3H "<text>"`, which calls `POST /api/requests/{id}/answer` via `cli`. The answer text told the agent that repos are registered and gave the `endurio-chat` id. Before: `swarm requests` listed the row as open. After: `answered`, `responded_via = cli`, `swarm requests` prints "No open requests.", and `/api/state` returns no open requests. Side effect: the `answer` route enqueues a `user_answer` message to the SPIKE-16 agent, which was still running.

### 1.5 User decisions (2026-09-25, replacing rev 1's OQ1-OQ8)

1. **Dot:** the Needs-you indicator is a **warning-yellow** dot at the top-right of the swarm menubar icon. The "daemon offline" dot is **removed entirely**.
2. **Scope:** "anything that requires the user's input appears there, and nothing else does." Permission prompts and blockers belong in Needs you. The generic row stays: task, agent name, generic message, terminal button, yellow warning background.
3. **Unverified hooks:** verify codex, cursor and agy against official docs, cite URLs, and set Task 9 by the findings (section 1.7).
4. **Native "Approve" must really approve**, possibly through MCP (section 2.3).
5. **Withdraw the stale row** through a supported path (done, section 1.4).

### 1.6 User answers to rev 2's open questions (2026-09-25, locked)

1. **Approvals appear in Needs you.** This was OQ-E and is locked in 2.2.1.
2. **Agent-reported approvals (agy, and muse if the probe shows no answer text) are accepted and marked for audit.** This was OQ-B and is locked in 2.3.5.
3. **The green "agent live" dot stays.** This was OQ-A and is locked in 2.2.3.
4. **Copy.** `Copy.needsYouMessage` = `Waiting for your input`, with no trailing period. The other three strings are locked as proposed (section 6).
5. **The web board inbox follows the same Needs-you rules.** This was OQ-D and is locked in 2.2.5.
6. **Skill rule 6 has a named exception (2026-09-26, final review finding 1).** For a child whose parent's kind has no native approval hook (muse, codex, cursor), a plain `answer` from that parent, `reply_to` its own `approval: true` question, counts as the user's approval decision — because that parent can never produce a bound, hook-observed `approval_result` (§2.3's trust model can't apply where no hook fires at all), and refusing to define an answer path there leaves the child waiting forever. This is a deliberate, narrower trust model than §2.3's default (no hook evidence, decision taken on the parent's word), accepted for these three kinds only. `skills/swarm/SKILL.md` (near the `:17` bullet and rule 6), `skills/swarm-orchestrator/SKILL.md:43`, and `errChildApprovalNoNativePath` (`internal/runtime/native.go:136-139`) all state it consistently. The routing precondition (this answer really targets that child's own approval question, from its real parent) is already enforced by `Send`'s `reply_to` validation (`internal/runtime/inbox.go:490-556`, task T10) — no new code guard needed.

### 1.7 Hook support per agent kind (research 2026-09-25)

| Kind | Native ask tool | Hook fires for it? | Deny shape | PostToolUse carries the answer? | Human-prompt event | Sources |
|---|---|---|---|---|---|---|
| claude | `AskUserQuestion` | Yes. It is already intercepted and tested (`handler_test.go:868`). | exit 2 / `permissionDecision:deny` (current adapter) | Yes (`tool_response`) | `UserPromptSubmit` | existing code |
| codex | `request_user_input` | **Yes, per source (live PreToolUse/PostToolUse for `request_user_input` unconfirmed — see below).** `RequestUserInputHandler` does not override `pre_tool_use_payload`/`post_tool_use_payload`. The registry's defaults return `Some(..)` for every `ToolPayload::Function`, so `PreToolUse` and `PostToolUse` run with `tool_name = "request_user_input"`. The docs list "Other local function tools" as hookable and exclude only hosted tools such as `WebSearch`. **Task 4b live check (2026-09-25/26, isolated `CODEX_HOME`, scratch tmux session, no Swarm involvement):** `SessionStart` and `UserPromptSubmit` fired correctly and match their documented shape (fixtures captured, not committed — the required Output is only the `request_user_input` PreToolUse/PostToolUse pair). Every model turn after the prompt failed with `401 Unauthorized: Incorrect API key provided: sk-svcac…` from `chatgpt.com/backend-api/codex/responses`, so the `request_user_input` tool call itself was never reached — reproduced 8× across 3 fresh sessions, 2 models (`gpt-6-sol`, `gpt-6-luna`), both a symlinked and a copied `auth.json` (ruling out a token-refresh race with the long-lived real `codex --dangerously-bypass-approvals-and-sandbox` process also on this machine), and confirmed no `OPENAI_API_KEY` in the tmux server's or the shell's environment (ruling out an env-key override). `codex login status` reports `Logged in using ChatGPT` throughout, and the account's weekly limit meter never moved, so this is not the usual quota-exhaustion path either. Root cause unresolved — looks like an account/backend-side fault independent of Swarm's hook wiring. **No evidence found either way on live hook firing for `request_user_input`. REVISED (code review, 2026-09-26): unconfirmed is treated as not-confirmed, not as source-confidence-stands — codex joins cursor's/muse's exception (section 1.7 verdict) until a retry live once the backend issue clears produces the fixtures.** | `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"…"}}`. The docs also accept the older `{"decision":"block","reason":"…"}`, which the current adapter emits (`internal/adapter/codex.go:139`). Not exercised live (blocked by the 401 above). | `tool_response` field present; its shape for this tool is unknown (not exercised live) | `UserPromptSubmit` (`prompt`) — confirmed live, real payload shape matches docs | [Codex hooks docs](https://learn.chatgpt.com/docs/hooks) (was developers.openai.com/codex/hooks), [registry.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/registry.rs), [request_user_input.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/request_user_input.rs), [hook_runtime.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/hook_runtime.rs) |
| cursor | `AskQuestion` (the name `ask_question` in `isQuestionTool` is wrong) | **No.** Cursor staff, 2026-05-29: "This is a confirmed bug. The `AskQuestion` tool is not wired to fire `preToolUse`/`postToolUse` hooks in either the IDE or the CLI … No workaround exists." It was still open in 3.9.16 (July 2026). The docs have no question or approval hook. | n/a for questions; `{"permission":"deny","user_message":…,"agent_message":…}` for other tools | n/a | `beforeSubmitPrompt` (CLI support undocumented) | [Cursor hooks docs](https://cursor.com/docs/hooks), [forum bug 161836](https://forum.cursor.com/t/cursor-cli-askquestion-tool-skips-pretooluse-and-posttooluse-hooks/161836) |
| agy (Antigravity CLI; its `~/.gemini` config dir is not Gemini CLI) | `ask_question` (also `ask_permission`) | **Yes, confirmed live (2026-09-25/26, isolated `HOME`, scratch tmux session, no Swarm involvement).** `PreToolUse` fired with `toolCall.name = "ask_question"`, `toolCall.args.questions[0] = {"question","options"}` (fixtures: `internal/adapter/testdata/agy-hook-pretooluse-ask_question.json`, `agy-hook-posttooluse-ask_question.json`) — exactly the shape `Agy.ParseHook` already expects. **Ordering confirmed by the deny run, not by timestamps:** a deny hook suppressed the question dialog entirely (see Deny shape column), which is only possible if `PreToolUse` runs *before* the dialog renders — so agy does not share Gemini CLI's post-answer hook bug. The current install uses matcher `*` (`internal/install/agy.go:40-43`). | **Confirmed live.** `{"decision":"deny","reason":…}` (current adapter, `internal/adapter/agy.go:182-188`) suppressed the dialog entirely; the model's next turn saw `Error: tool call denied by pre-tool hook: <reason>` verbatim, with no dialog ever shown. | **No, confirmed live.** The captured `PostToolUse` payload is `artifactDirectoryPath`, `conversationId`, `error` (empty string), `modelName`, `stepIdx`, `toolCall` (echoed), `transcriptPath`, `workspacePaths` — no result/answer field, matching the source-derived row. `extractToolResponseText` correctly returns `""` for it; agy stays `agent_reported` (2.3.5). | **None, confirmed live** (no separate human-prompt event payload appeared). | [Antigravity hooks docs](https://antigravity.google/docs/hooks/), [Where does Antigravity look for Hooks?](https://atamel.dev/posts/2026/07-16_where_agy_hooks/), live check 2026-09-25/26 |
| muse | `request_user_input` | **No — confirmed live (2026-09-25/26, isolated scratch project, no Swarm involvement, twice).** A workspace `.muse-plugin/plugin.json` with `matcher: null` (all tool names) `PreToolUse`/`PostToolUse` hooks was installed and trusted. Across two live turns asking the model to call `request_user_input`, the dialog rendered, was answered ("Structured user input answered — Color: Red"), and the model continued — but no `PreToolUse`/`PostToolUse` payload for `request_user_input` was ever captured, while the same plugin's hooks *did* fire correctly for the model's other tool calls in the same turns (`submit_reminder_decision`, `read_skill`). muse renders this tool with dedicated "Structured user input" UI chrome, not the `●  ToolName(...)` line it uses for real tool calls — it is not dispatched through the hookable tool-call path at all. | **n/a — cannot deny.** There is no `PreToolUse` event to return a deny decision from. | **n/a.** No `PostToolUse` event fires for it either. | **None for `request_user_input` itself.** `UserPromptSubmit` fires normally for the surrounding human prompt (`internal/adapter/testdata/muse-hook-userpromptsubmit.json`). | live probe 2026-09-25/26; `native-plugin-contract.md` (local) for the plugin/manifest shape |

Note: in **Gemini CLI**, `BeforeTool` for `ask_user` fires only *after* the user answers ([gemini-cli#20605](https://github.com/google-gemini/gemini-cli/issues/20605)). Swarm's `agy` is the Antigravity CLI (`internal/adapter/agy.go:148`), so this does not apply. If agy shows the same ordering in the Task 4b live check, its row opens late (only after the answer), so Needs you would miss it while the user decides.

**What follows for Task 9 (superseded by the live probes below for agy and muse):** ~~refuse `swarm_ask kind:"question"` for claude, codex, agy and muse (muse only once Task 8 lands)~~. **Keep it for cursor**, because its native `AskQuestion` is invisible to Swarm. For cursor agents, `swarm_ask question` stays the only way to reach Needs you, and the cursor skill line says so.

**agy** is now backed by a live-confirmed row and joins claude, not the cursor exception: refused. **codex** could not be live-confirmed (backend 401, see the row above). **REVISED (code review, 2026-09-26): this decision reopened.** Task 4b's own contract was to confirm the hook live *before* Task 9 relies on it (not to fall back to source reading when the live check is blocked), and "source confidence" was never validated against item 2 (dialog-before-hook ordering) or item 3 (deny honored) the way agy's row was — both are live-only questions source code cannot answer. Refusing on that basis risks a top-level codex agent losing its Needs-you path entirely and being pointed at `errQuestionUseNativeTool`'s native tool, which Swarm then cannot see. **codex joins cursor's and muse's exception** (`swarm_ask kind:"question"` stays available) until a live retry produces the T4b fixtures (`codex-hook-{pre,post}tooluse-request_user_input.json`) and answers item 4 (the `tool_response` shape `extractToolResponseText` must handle). **muse joins cursor's exception**, not the refused list: its `request_user_input` is confirmed live to never dispatch a hook at all (see the muse row and OQ-F in section 10) — refusing `swarm_ask question` for muse with no hook-based fallback would leave a top-level muse agent with no way to reach Needs you.

**Refused (hooked): claude, agy. Exception (native question invisible or unconfirmed, `swarm_ask question` stays available): cursor, muse, codex (pending a live retry).**

---

## 2. Locked decisions

### 2.1 Superseded decisions from `docs/specs/2026-09-21-deterministic-needs-you.md`

- **21-D1 → replaced.** A top-level agent asks the user with its native question tool. The hook opens the row. `swarm_ask kind:"question"` is refused, except for cursor agents (section 1.7).
- **21-D4 → amended.** Needs you is no longer HITL-only. It lists every open request, and approvals get the same generic row as everything else.
- **21-D5 → amended.** Approval kinds with an asking agent get `terminal_agent` = that tree's root orchestrator. `accept_epic`/`accept_fix` rows have no agent, so they target the root item's live orchestrator, or null when there is none (the row then opens the board).
- **21-D6 → amended.** Approvals keep their web and CLI buttons, and gain a native path (2.3).

`docs/specs/2026-09-25-spike-native-approvals.md` "Native answer drives the daemon request" is now made real by 2.3.

### 2.2 Needs you (menubar and web board)

1. **Membership rule:** every request with `state == "open"` is in Needs you, minus the one duplicate case: an approval request whose native prompt is currently open (`native_pending == true`), because the bound native-question row already represents it. This covers questions, permission prompts, blockers, repo confirmations, approvals, acceptances and spike closes. Nothing else can appear, because Needs you reads only `requests`.
2. **Generic row for every kind:** `itemKey · itemTitle` / agent name / `Copy.needsYouMessage` / terminal button, on a yellow warning background. The agent's text is not shown. When `terminal_agent` is null, the same button opens the request on the board. An agent-reported approval is never an *open* row, so it gets no marker here; its audit flag is on the resolved request (2.3.6).
3. **Dot:** warning-yellow while Needs you is non-empty. The offline red dot is gone. The green "agent live" dot stays, and yellow takes precedence (locked, 1.6.3).
4. **No schema change.**
5. **Web board inbox, same rules (locked, 1.6.5).**
   - The inbox list (`web/src/panels/NeedsYou.tsx`) and the header count (`web/src/App.tsx:105`) use one shared `needsYou(requests)`: `state === "open" && !native_pending`.
   - Every list row is the generic row: `item_key · item_title` / agent name / `C.needsYouMessage`, with a terminal button, on a yellow warning background (`bg-warn/15`; `--color-warn` already exists at `web/src/index.css:20`).
   - The filter tabs stay as subsets of that set: All; Questions (`question`, `prompt`, `blocker`); Approvals (`approve_section`, `approve_plan`, `approve_report`, `confirm_repos`, `close_spike`); Reviews (`accept_epic`, `accept_fix`).
   - The detail pane (`renderReview`) keeps its existing Approve, Request changes and Confirm controls. The board is still the fallback approval surface (cursor, headless).
   - `requestTarget` drops its `is_hitl` guard, matching the menubar (2.1, 21-D5).

### 2.3 Native approvals that really approve (MCP forward + hook evidence)

**Trust model (stated plainly):** agents run as the user's UID and can read every token (F12), so no design can stop a deliberately malicious agent. The threat this path guards against is an agent *claiming* an approval the user never gave (hallucination, or a misread free-text answer). Rule: **an approval is recorded only if the daemon observed the user answering a daemon-issued prompt in a hook event, and the forwarded decision matches the observed answer.** The MCP call forwards; the hook evidence decides.

**Accepted exception (2026-09-26, user decision, §1.6):** this rule cannot hold for a child parented by an orchestrator kind with no native approval hook at all (muse, codex, cursor) — there is no hook event to observe, ever, for that parent. Rather than leave the child waiting forever (the final review's finding 1), the user accepted the weaker, pre-hook trust model for exactly this case: the parent's own `swarm_send kind:"answer"` to the child's `approval: true` question, matched by `reply_to`, counts as the approval, on the parent's word alone, with no hook evidence and no `evidence` audit field. This is narrower than the existing agent-reported path (step 5): it has no bound question row at all, because there is nothing to bind it — the daemon's `Send` validation (`internal/runtime/inbox.go:490-556`, `T10`) already guarantees the answer's `reply_to` is that exact child's own `question` addressed to the answering agent, so the routing (right child, right parent) is enforced; only the "was this really the user" trust is weaker. Scope: only child approval questions (`for_msg`/message-ref, §2.4), only when the parent's kind is muse, codex or cursor. Every other approval path (request-refs, claude/agy parents) keeps the full hook-evidence rule.

Flow (the same for spike approvals and for a child's approval request):

1. **Issue.** The daemon generates the exact native prompt, which ends with a ref token `⟦swarm:<ref>⟧`:
   - For an approval request (`approve_section`, `approve_plan`, `approve_report`, `confirm_repos`, `close_spike`), `swarm_ask kind:"approval"` / `"confirm_repos"` returns `native_prompt: {header, question, options}` next to `request_id`, with `ref` = the request id.
   - For a child's approval (see 2.4), `swarm_ask kind:"native_prompt", for_msg:<msg_id>` returns the prompt, with `ref` = the message id.
2. **Show.** The orchestrator passes `header`, `question` and `options` **verbatim** to its native tool.
3. **Observe.** At PreToolUse, `extractQuestion` finds the ref token. `AskQuestion` then opens the question row with `binding_json = {"ref":"<ref>"}` (a new optional argument; no schema change, since `binding_json` exists). At PostToolUse, `ResolveQuestionByPrompt` sets the row to `answered` via `terminal`, with `response_text` = the tool response text.
4. **Forward (MCP).** The orchestrator calls `swarm_ask {kind:"native_answer", ref, decision:"approve"|"request_changes", comment?}`.
5. **Verify.** The daemon requires one question row with `binding_json.ref = ref` and `agent_id = caller agent`, `state = 'answered'`, `responded_via = 'terminal'`, and `responded_at ≥` the ref's creation. It also requires evidence that matches the decision: `response_text`, trimmed and case-folded, must start with the chosen label (`approve` → `Approve`, `request_changes` → `Request changes`).
   - **Agent-reported (locked, 1.6.2).** Some adapters have PostToolUse but no response text: agy per 1.7, and muse if Task 4 finds the same. The bound row then shows `"Resolved in terminal"`, so the daemon has proof the prompt was answered but not of which option. It accepts the forwarded decision **on the agent's word** and records `evidence = "agent_reported"`. When the text matches the label, it records `evidence = "observed"`. When the text is present but does not match, the call is still refused (`errDecisionMismatch`).
   - **Cursor, muse and codex** have no hook, so there is never a row for them. For a request-ref (step 1's first bullet) `native_answer` is refused with `errNoNativeEvidence`, and the board/`swarm approve` fallback (native_pending) is unaffected. For a message-ref (a child's approval, step 1's second bullet) there is no such fallback, so `native_prompt`/`native_answer` refuse up front for these kinds with `errChildApprovalNoNativePath` (REVISED 2026-09-26, finding 1: this refusal is now for these three kinds specifically, not the generic evidence-missing case) — see 2.4 step 3.
6. **Record.**
   - When `ref` is a request id: `Approve(id, ApproveInput{SectionSHA256: req.SectionSHA256, ArtifactRevision: req.ArtifactRevision, Binding: req.Binding, Via: "terminal"})`, or `RequestChanges(id, comment, "terminal")`. For `confirm_repos` it is `ConfirmRepos(id, proposed ids, "", 0, "terminal")`. These call the existing `resolve(…, "user_action", …)` at a **new, explicit call site** (`nativeAnswer`), which is where the evidence check sits. The normal `approval_result` or `repos_confirmed` message follows.
   - When `ref` is a message id: the bound question row moves from `answered` to `approved` or `changes_requested` (both allowed by the `requests.state` CHECK). That row is the approval record. The daemon enqueues `approval_result {request_id:<row id>, decision, comment, reply_to_msg:<msg_id>}` to the **child** (origin `daemon`, `ReplyTo: msg_id`).
   - **Audit record.** `evidence` is written to three places, in the same transaction:
     - (a) the bound question row's `binding_json` → `{"ref":…,"evidence":"observed"|"agent_reported"}`;
     - (b) the `approval_result` / `repos_confirmed` message payload, as `"evidence"`;
     - (c) the `request.resolved` event wire, through `RequestWire.ApprovalEvidence` (JSON `approval_evidence`). This is derived in `RequestWireTx` from the bound row. It is `null` for approvals made on the board or CLI, which are user actions by definition.

     Audit read path: `GET /api/requests/{id}` (`RequestWireByID`, `internal/runtime/requests.go:314`) returns `approval_evidence` for a resolved request, and so does the SSE `request.resolved` event. The DB query is the bound row's `binding_json.evidence`. There is no new UI, because every request list in the menubar and web shows open requests only (`internal/runtime/requests.go:159` filters `state = 'open'`).
7. **Unblock.** `approval_result` is in `ImmediateKinds` (`internal/runtime/inbox.go:17`), so the child or orchestrator is woken at once. Skill rule 6 stays true — "a user approval exists only as an `approval_result` message with a request id" — **with one exception, accepted by the user on 2026-09-26 (final review finding 1, see §1.6):** when the child's parent's kind has no native approval hook (muse, codex, cursor per §1.7/§2.4 step 3), the parent can never produce a bound `approval_result` for that child's `approval: true` question, so it answers with a plain `swarm_send kind:"answer"` instead, and the child treats that answer (matched by `reply_to` to its own question) as the approval decision. `skills/swarm/SKILL.md` rule 6 and the `:17` bullet both carry this exception; `errChildApprovalNoNativePath` (`internal/runtime/native.go:136-139`) tells the orchestrator to do it.
8. **Replay safety.** A ref can be forwarded once. A second `native_answer` for the same ref hits `resolve`'s "Already resolved." (request refs), or the row is no longer `answered` (message refs). A stale approval (artifact revised) fails `Approve`'s check with "This request changed…", and the orchestrator must re-issue.

### 2.4 Child ↔ orchestrator protocol

1. Child question: `swarm_send {to:"parent", kind:"question", body, options?, approval?}`. The payload is stored as `{"body","options","approval"}`. With `approval:true`, `options` is fixed to `["Approve","Request changes"]`.
2. Orchestrator answers a plain question as follows: ask with the native tool, using the header `<child> asks`, the child's text and options verbatim, and free text allowed. Then `swarm_send {to:<child>, kind:"answer", reply_to:<msg_id>, body}`.
3. Orchestrator answers an approval question by issuing `swarm_ask kind:"native_prompt", for_msg`, then showing the prompt natively, then forwarding with `native_answer` (2.3). It does not also `swarm_send answer`: the daemon's `approval_result` is the answer. **Cursor, muse and codex** (REVISED 2026-09-26, finding 1): both calls refuse for these kinds instead of building a prompt that can never be answered — the hook that would bind evidence to it never fires. These orchestrators skip 2.3 entirely and `swarm_send {to:<child>, kind:"answer", reply_to:<msg_id>, body}` the same as a plain question; the child treats that plain answer as its approval decision.
4. `reply_to` is required for `kind:"answer"`. It must name a message addressed to the caller that is a `question` from the target, or a daemon `relay` with `event:"blocked"` about the target. It is stored in `messages.reply_to`.
5. If a question (plain or approval) was acked and has neither an `answer` naming it nor a bound `approval_result` after 10 minutes, the daemon sends one `relay {event:"question_unanswered"}` to the orchestrator.

### 2.5 Other locked fixes

- **Repo search:** an empty `q` runs `Search(ctx, "", limit)`. The unknown-repo error names `swarm_read {repos:{q}}`.
- **`swarm_ask` description:** "Request an approval, propose repos to confirm, forward a native answer, or withdraw an earlier ask. Returns at once with the request id; the answer arrives later as a message."
- **Muse hooks** (probe first), registered per launch like claude's `settingsJSON`.
- **`isQuestionTool`** adds `AskQuestion` (cursor's real name, harmless while the hook doesn't fire) and keeps the others.
- **Tests are updated, never deleted.** Fixtures built with `Ask(…"question")` move to `AskQuestion(…)`.

---

## 3. DB models

**No schema change.** The existing columns carry everything:
- `requests.binding_json` on a question row holds `{"ref":"req_…|msg_…","evidence":"observed|agent_reported"}`. `evidence` is absent until `native_answer`. The whole column is NULL for unbound questions.
- `requests.state` on a message-ref approval row goes `answered → approved | changes_requested`.
- `messages.reply_to` holds an answer's question id, a `question_unanswered` relay's question id, and a child `approval_result`'s question message id.
- `messages.payload_json` for a question is `{"body":"…","options":[…],"approval":true}`. The last two keys are omitted when empty or false.

Approval evidence for the wire (exact, in `RequestWireTx`; `?` = the request id or, for a message-ref approval row, the row's own `binding_json.ref`):

```sql
SELECT json_extract(q.binding_json, '$.evidence') FROM requests q
WHERE q.kind = 'question' AND json_extract(q.binding_json, '$.ref') = ?
  AND json_extract(q.binding_json, '$.evidence') IS NOT NULL
LIMIT 1;
```

Native-evidence lookup (exact):

```sql
SELECT id, response_text, responded_at FROM requests
WHERE kind = 'question' AND agent_id = ? AND state = 'answered' AND responded_via = 'terminal'
  AND json_extract(binding_json, '$.ref') = ?
ORDER BY responded_at DESC LIMIT 1;
```

`native_pending` for the wire (exact, in `RequestWireTx`):

```sql
SELECT EXISTS (SELECT 1 FROM requests q
  WHERE q.kind = 'question' AND q.state = 'open' AND json_extract(q.binding_json, '$.ref') = ?);
```

Answer validation:

```sql
SELECT 1 FROM messages m
WHERE m.id = ? AND m.to_agent_id = ?
  AND ((m.kind = 'question' AND m.from_agent_id = ?)
    OR (m.kind = 'relay' AND json_extract(m.payload_json, '$.event') = 'blocked'
        AND json_extract(m.payload_json, '$.agent') = ?));
```

Owed-answer scan:

```sql
SELECT q.id, q.from_agent_id, q.to_agent_id, q.root_item_id, q.item_id, q.payload_json
FROM messages q
WHERE q.kind = 'question' AND q.origin = 'agent' AND q.state = 'acked' AND q.created_at < ?
  AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.reply_to = q.id
                  AND a.kind IN ('answer', 'approval_result', 'relay'));
```

`messages.reply_to` has no index (`0001_init.sql:225,234`). `ponytail:` add `CREATE INDEX messages_reply_to ON messages(reply_to)` if the tick slows; it is not planned.

Terminal target for approval kinds (in `terminalAgent`): kinds with `agent_id` use the existing root walk. `accept_epic`/`accept_fix` use:

```sql
SELECT a.name FROM agents a JOIN items i ON i.id = ?          -- req.item_id
WHERE a.root_item_id = i.root_id AND a.role = 'orchestrator' AND a.parent_agent_id IS NULL
  AND a.state IN ('queued','active') LIMIT 1;
```

---

## 4. Model / API types

### 4.1 Go

```go
// internal/runtime/requests.go
const errQuestionUseNativeTool = "Ask the user with your own native question tool " +
	"(claude AskUserQuestion, codex request_user_input, agy ask_question, muse request_user_input). " +
	"Swarm shows it in Needs you and closes it when the user answers."
const errNoNativeEvidence = "No answered native prompt for %s in your terminal. Show the native_prompt " +
	"from swarm_ask verbatim with your native question tool, then forward the user's answer."
const errDecisionMismatch = "The user's native answer was %q, not %q."

// kinds whose native question tool Swarm intercepts (section 1.7); cursor is absent on purpose.
var questionHookKinds = map[AgentKind]bool{Claude: true, Codex: true, Agy: true, Muse: true}

type NativePrompt struct {
	Header   string   `json:"header"`
	Question string   `json:"question"` // ends with " ⟦swarm:<ref>⟧"
	Options  []string `json:"options"`
}
type AskInput struct { /* existing fields */ ; ForMsg, Ref, Decision, Comment string }
type Request struct { /* existing */ ; NativePrompt *NativePrompt }

func nativePromptFor(req Request, sectionTitle string) NativePrompt
func nativePromptForMsg(child string, body string, msgID string) NativePrompt
func refToken(ref string) string                         // "⟦swarm:" + ref + "⟧"
func refFromPrompt(prompt string) string                 // "" when absent
func (s *Store) AskQuestion(ctx context.Context, sessionID, prompt string, options []string) (Request, error)
	// unchanged signature; stores binding_json {"ref"} when refFromPrompt(prompt) != ""
func (s *Store) nativeAnswer(ctx context.Context, sessionID string, in AskInput) (Request, error)

// internal/runtime/inbox.go
func (s *Store) Send(ctx context.Context, sessionID, to string, kind MessageKind,
	body, replyTo, requestID string, options ...string) (string, error)
func (s *Store) SendApproval(ctx context.Context, sessionID, body, requestID string) (string, error)
	// question to parent with {"approval":true,"options":["Approve","Request changes"]}
const errAnswerNeedsReplyTo = "An answer needs reply_to: the msg_id of the question (or blocked relay) you are answering."
const errAnswerBadReplyTo   = "reply_to %s is not a question or blocked relay from %s addressed to you."

// internal/runtime/reconcile.go
const questionAnswerTimeout = 10 * time.Minute
func (s *Store) notifyUnansweredQuestions(ctx context.Context) error

// RequestWire gains
	NativePending    bool    `json:"native_pending"`
	ApprovalEvidence *string `json:"approval_evidence"` // "observed" | "agent_reported" | null

const (
	EvidenceObserved      = "observed"
	EvidenceAgentReported = "agent_reported"
)
```

### 4.2 MCP

`swarm_ask`:
- kinds: `approval`, `confirm_repos`, `withdraw`, `native_prompt`, `native_answer`, and `question` (cursor only).
- New fields: `for_msg` (native_prompt), `ref`, `decision` (enum `approve`, `request_changes`), `comment`.
- Result: `{"request_id","state","native_prompt"?}`.

`swarm_send`:
- `options: string[]` (question only; at most 10, each at most 200 chars).
- `approval: boolean` (question to parent only).
- `reply_to` is required for `kind:"answer"`.

### 4.3 Swift (menubar)

```swift
// SwarmBarKit/Wire.swift: SwarmRequest gains
public var nativePending: Bool            // "native_pending", default false when absent

// SwarmBarKit/AppModel.swift
static func needsYou(_ rs: [SwarmRequest]) -> [SwarmRequest] {
    rs.filter { $0.state == "open" && !$0.nativePending }.sorted { $0.createdAt < $1.createdAt }
}
public var openRequests: [SwarmRequest] { Self.needsYou(state.requests) }
public func requestTarget(_ r: SwarmRequest) -> RequestTarget?  // drops the isHITL guard; nil only when terminalAgent is nil
public func openRequest(_ r: SwarmRequest) async                // terminal if .terminal, else review(r) (board)

// SwarmBarKit/MenuLabel.swift
public enum Badge: Equatable, Sendable { case none, green, yellow }   // .red removed
public static func make(activeCount: Int, connected: Bool, needsYou: Int = 0, enabled: [AgentKind],
                        usage: [UsageSnapshot], compact: Bool, format: Format) -> MenuLabel
// badge: needsYou > 0 ? .yellow : (connected && activeCount > 0) ? .green : .none

// SwarmBarKit/Copy.swift
public enum NeedsYouRow { public static func lines(_ r: SwarmRequest) -> [String] }
```

### 4.4 TypeScript (web)

```ts
// web/src/types.ts: Request gains
native_pending: boolean;               // default false in mock fixtures
approval_evidence: "observed" | "agent_reported" | null;

// web/src/logic/inbox.ts
export function needsYou(reqs: Request[]): Request[];                         // open && !native_pending, oldest first
export function filterRequests(reqs: Request[], f: InboxFilter): Request[];   // needsYou(reqs) then kind subset per 2.2.5
export function needsYouRow(r: Request): [string, string, string];           // [`${item_key} · ${item_title}`, agent_name ?? terminal_agent ?? "—", C.needsYouMessage]
export function requestTarget(r: Request, agents: AgentNode[]): RequestTarget | null; // is_hitl guard removed
```

---

## 5. Screens

### 5.1 Menu bar item

```
 idle / offline                   agents live                    Needs you non-empty (any connectivity)
 ┌───────────────────────┐        ┌───────────────────────┐      ┌───────────────────────┐
 │ ⬡  2  ✳ 42%  ◎ 18%    │        │ ⬡• 2  ✳ 42%  ◎ 18%   │      │ ⬡• 2  ✳ 42%  ◎ 18%   │
 └───────────────────────┘        └───────────────────────┘      └───────────────────────┘
   no dot (offline dot removed)     • green 5pt                     • warning-yellow 5pt (Color.yellow),
                                                                      wins over green; same slot:
                                                                      badgeOffset (9,0), badgeSize 5
```

Offline is shown only by the empty count (`MenuLabel.count == ""`), as it is today. Accessibility label: `Copy.needsYouBadge` for yellow, `Copy.agentsWorking` for green, `Copy.appTitle` otherwise.

### 5.2 Popover: Needs-you section

```
 Needs you                                        3
 ┌──────────────────────────────────────────────┐  ← Color.yellow.opacity(0.18), RoundedRectangle(6), padding 8
 │ SPIKE-16 · go-migration-agent-debug      [>_]│    line 1 .caption .secondary, 1 line
 │ go-migration-agent-debug                     │    line 2 .callout, agent name, 1 line
 │ Waiting for your input                      │    line 3 .callout, Copy.needsYouMessage
 └──────────────────────────────────────────────┘
 ┌──────────────────────────────────────────────┐   approval with no live orchestrator:
 │ EPIC-4 · Checkout redesign               [>_]│   [>_] opens the request on the board
 │ —                                            │   (agent line "—" when no agent)
 │ Waiting for your input                      │
 └──────────────────────────────────────────────┘
 ┌──────────────────────────────────────────────┐   paused orchestrator: [>_] disabled,
 │ BUG-2 · Railway cutover                  [>_]│   hint below (existing copy)
 │ go-migration-railway-orchestrator            │
 │ Waiting for your input                      │
 │ Orchestrator is paused. Resume it to continue.│
 └──────────────────────────────────────────────┘
 View all 5 requests                               ← unchanged link when count > 3
```

### 5.3 Web board: Needs you panel (`#/inbox`)

```
 Needs you                     │  (detail pane: unchanged renderReview —
 [All][Questions][Approvals][Reviews]   question view / approval viewer with
 ┌───────────────────────────┐ │   Approve · Request changes · Confirm)
 │ SPIKE-16 · go-migration…  ⌨│ │
 │ go-migration-agent-debug  │ │
 │ Waiting for your input    │ │
 └───────────────────────────┘ │
 ┌───────────────────────────┐ │
 │ EPIC-4 · Checkout redesign⌨│ │  ⌨ = terminal icon button; board-only rows
 │ —                         │ │   select the request (no terminal)
 │ Waiting for your input    │ │
 └───────────────────────────┘ │
```

Rows use `rounded bg-warn/15 px-2 py-1`, and the selected row adds `ring-1 ring-warn`. The header button `T.needsYouButton(n)` counts `needsYou(requests).length`. The empty state is unchanged: `C.inboxEmpty`.

**Not shown (menubar and web list):** the prompt text, options, section titles, kind-specific wording (`RequestLine` stays for the web and notifications), and Answer, Approve or Review buttons. An approval request never appears twice: while its native prompt is open, only the question row shows (`native_pending`). Empty state: `Copy.emptyNeedsYou`, unchanged.

---

## 6. User-facing copy (exact)

| Key | Copy | Status |
|---|---|---|
| `Copy.needsYouMessage` / web `C.needsYouMessage` | `Waiting for your input` (no trailing period) | Locked (user, 1.6.4) |
| `Copy.openAgentTerminal` / web `C.openAgentTerminal` | `Open agent terminal` | Locked (default) |
| `Copy.openOnBoard` (button help when `terminal_agent` is nil) | `Open in Swarm board` | Locked (default) |
| `Copy.needsYouBadge` | `Swarm needs you` | Locked (default) |
| Agent line with no agent | `—` | Locked |
| `errQuestionUseNativeTool`, `errNoNativeEvidence`, `errDecisionMismatch`, `errAnswerNeedsReplyTo`, `errAnswerBadReplyTo` | section 4.1 | Locked |
| Unknown repo | `Unknown repository "endurio-chat". Pass a repository id from swarm_read {repos:{q:"endurio-chat"}}.` | Locked |
| Relay line | `question_unanswered from <child> (<KEY>): "<question body>"` | Locked |

Native prompts (daemon-generated, exact):

| Ref | header | question | options |
|---|---|---|---|
| `approve_section` | `Spike approval` | `Approve Spec section "<SectionTitle>" (rev <N>)? ⟦swarm:<req id>⟧` | `Approve`, `Request changes` |
| `approve_plan` | `Spike approval` | `Approve the plan (rev <N>)?` + `\nWarnings:\n- <w>` when present + ` ⟦swarm:<req id>⟧` | same |
| `approve_report` | `Spike approval` | `Approve the debug report (rev <N>)? ⟦swarm:<req id>⟧` | same |
| `confirm_repos` | `Repositories` | `Confirm <N> repositories for <KEY>: <name1>, <name2>? ⟦swarm:<req id>⟧` | `Approve`, `Request changes` |
| `close_spike` | `Close spike` | `Close <KEY>? ⟦swarm:<req id>⟧` | `Approve`, `Request changes` |
| child approval | `<child> asks` | `<child body> ⟦swarm:<msg id>⟧` | `Approve`, `Request changes` |

Free text is allowed on every prompt. It becomes the `comment` for Request changes.

Skill text (exact replacements):
- **`skills/swarm/SKILL.md:17`**, from "Top-level agents may use…" onward:
  > "Top-level agents ask the user only with their native question tool (claude `AskUserQuestion`, agy `ask_question`); `swarm_ask kind: "question"` is refused. Cursor, muse and codex are the exception: their question tool is invisible to Swarm or unconfirmed, so agents of these kinds use `swarm_ask kind: "question"`. With a parent, send `swarm_send` `kind: "question"` (add `options` when you have choices, and `approval: true` when you need explicit approval), then end your turn. A plain answer arrives as `kind: "answer"` with `reply_to` = your question's `msg_id`; an approval arrives as `approval_result`." (REVISED 2026-09-26: codex moved from refused to the exception, see section 1.7.)
  Delete: "If the user answers you in your terminal, call `swarm_ask` with `withdraw` for any open request."
- **`skills/swarm-orchestrator/SKILL.md:43`** → (REVISED 2026-09-26: names cursor, muse and codex explicitly — F-review finding 1 — instead of leaving them under a stale "cursor excepted" carried over from before section 1.7 moved muse and codex into the exception list; native_prompt/native_answer now refuse a msg_ ref for these kinds, see §2.3/native.go's `requireNativeApprovalHook`.)
  > "- Answering child questions and blockers: for a `kind: "question"` message or a `relay` with `event: "blocked"`, answer from the brief, plan or code if you can. Otherwise ask the user — claude and agy with their native question tool (header `<child> asks`, the child's text and options verbatim); cursor, muse and codex with `swarm_ask kind: "question"` instead, since their native question tool has no Swarm hook — and reply `swarm_send(to: <child>, kind: "answer", reply_to: <msg_id>, body: <the user's choice + note>)`. If the question has `approval: true`: claude and agy call `swarm_ask kind: "native_prompt", for_msg: <msg_id>`, show the returned `native_prompt` verbatim, then forward with `swarm_ask kind: "native_answer", ref: <msg_id>, decision, comment`, and the daemon sends the child `approval_result`. cursor, muse and codex have no native approval hook either, so native_prompt/native_answer always refuse this: answer the child with the same `swarm_send(to: <child>, kind: "answer", reply_to: <msg_id>, ...)` as a plain question, and the child treats that answer as its approval decision. A relay `event: "question_unanswered"` means you still owe that answer."
- **`skills/swarm-orchestrator/SKILL.md:64`** → (REVISED 2026-09-26: same fix, request-ref side. This path already had a working fallback — the board/`swarm approve` resolve the request row directly, no native_answer needed — so only the wording changes.)
  > "- Approving through native question tools: every `swarm_ask` approval (`approval`, `confirm_repos`) returns a `native_prompt`. Show it with your native question tool exactly as given (one per spec section, never batched; plan warnings are already in it). Then forward the user's answer: `swarm_ask kind: "native_answer", ref: <request_id>, decision: "approve" | "request_changes", comment`. The daemon records the approval only if it saw that prompt answered in your terminal with the same choice; it then sends `approval_result`. Never forward a decision the user did not pick. Only top-level orchestrators, interactive TUI only. Cursor, muse and codex have no native path: the user approves in the board or with `swarm approve`."
- Mirrors: `internal/install/skills/…` via `make skills-sync`.

---

## 7. File list

**Change**
- `internal/runtime/requests.go`: `Ask` gets `question` (per kind), `native_prompt` and `native_answer`; `NativePrompt` returned for approval and confirm_repos; `AskQuestion` binds a ref; `nativeAnswer`; `terminalAgent` covers approval kinds; `RequestWire.NativePending`.
- `internal/runtime/confirm.go`: `native_prompt` on the result; unknown-repo text.
- `internal/runtime/inbox.go`: `Send` gets variadic options, approval payload, and reply_to validation and storage; `SendApproval`; relay renderer quotes `question`.
- `internal/runtime/reconcile.go`: `notifyUnansweredQuestions`.
- `internal/mcpserver/tools.go`: `swarm_ask` schema, description and result; `swarm_send` schema; repos with empty `q`.
- `internal/hook/handler.go`: `isQuestionTool` gets `AskQuestion`; the comment block is updated with section 1.7 and its URLs.
- `internal/adapter/muse.go`: `ParseHook`, `HookOutput`, per-launch hook registration. `internal/install/muse.go`: doctor check.
- `skills/swarm/SKILL.md`, `skills/swarm-orchestrator/SKILL.md`, and mirrors.
- `apps/menubar/Sources/SwarmBarKit/{AppModel,MenuLabel,Copy,Wire}.swift`, `Sources/SwarmBarUI/Popover/NeedsYouSection.swift`, `Sources/SwarmBarUI/MenuBarLabel.swift`.
- `web/src/types.ts`, `web/src/logic/inbox.ts`, `web/src/panels/NeedsYou.tsx`, `web/src/App.tsx:105` (header count), `web/src/copy.ts`, and `web/src/mock/fixtures.ts`.
- Tests (updated, never deleted): `web/src/logic/inbox.test.ts`, `web/src/copy.test.ts`, `web/src/App.test.tsx` / `App.flows.test.tsx` (inbox flows), `web/src/contract.test.ts` (wire fields), `internal/runtime/{requests,inbox,reconcile,confirm,pause,checkpoint,text}_test.go`, `internal/hook/handler_test.go`, `internal/mcpserver/{tools,server,orchestrator}_test.go`, `internal/adapter/muse_test.go`, `internal/install/muse_test.go`, `cmd/swarm/runtime_cmds_test.go`, `internal/advisor/context_test.go`, `scripts/e2e/spike_test.go`, `apps/menubar/Tests/SwarmBarTests/{AppModelTests,MenuLabelTests,PopoverRenderTests,FixtureTests}.swift`, `apps/menubar/Tests/Fixtures/state.json` (plus `native_pending`).

**New:** `internal/adapter/testdata/muse-hook-*.json`, and codex/agy `request_user_input`/`ask_question` fixtures from Task 4b.

**Reused unchanged:** `resolve`, `Approve`, `RequestChanges`, `ConfirmRepos` (called from `nativeAnswer`), `ResolveQuestionByPrompt`, `ResolveAnsweredInTerminal`, the session-end sweep, `alreadyRelayedForMessage`, and `repos.Service.Search`.

**Deleted:** nothing.

---

## 8. Verification

Command order:
1. `go test ./internal/repos/ ./internal/runtime/ ./internal/mcpserver/ ./internal/hook/ ./internal/adapter/ ./internal/install/ ./cmd/swarm/`
2. `go vet ./... && test -z "$(gofmt -l .)"`
3. `make skills-sync && git diff --exit-code internal/install/skills`
4. `cd apps/menubar && swift test`
5. `cd web && pnpm test` (or the repo's configured runner; `package.json` "test" is `vitest run --coverage`)
6. `go test ./scripts/e2e/ -run Spike`
7. The user runs `make install-daemon`, then the manual scenarios below.

Scenarios:
- **Top-level native question** on claude, codex, agy and muse. One row, yellow dot, and the row and dot clear when the question is answered.
  - agy: the row closes on PostToolUse with "Resolved in terminal".
  - If agy's PreToolUse fires only after the answer (Task 4b), record it as a known gap.
- **Cursor top-level:** `swarm_ask question` is accepted and the row shows. Every other kind gets `errQuestionUseNativeTool`.
- **Permission prompt and top-level blocker:** each appears as a generic row and dot, and clears on its existing close path.
- **Spike section approval via native:**
  - `swarm_ask approval` returns `native_prompt`. The orchestrator shows it, and the approval row is hidden while the question row is open (`native_pending`).
  - The user picks Approve, and `native_answer approve` records `approved via terminal`. The orchestrator receives `approval_result`, and Needs you is empty.
- **Agent-reported approval (agy):** the hook closes the bound row with "Resolved in terminal", and `native_answer approve` is accepted.
  - The request is `approved via terminal` and `approval_evidence = "agent_reported"`.
  - `approval_result.evidence = "agent_reported"`, and `GET /api/requests/{id}` returns `approval_evidence: "agent_reported"`.
  - The same flow on claude records `"observed"`. A board approval records `null`.
- **Web inbox parity:** the same open set as the menubar, in the same order. The header count matches. Every row is generic and yellow. A request with `native_pending` is hidden in both.
- **Decision mismatch:** the user picked Request changes but the agent forwards approve. The call is refused with `errDecisionMismatch` and the request stays open.
- **No evidence:** `native_answer` without a shown prompt is refused with `errNoNativeEvidence`. On cursor it is always refused.
- **Replay:** a second `native_answer` for the same ref gets "Already resolved."
- **Stale:** the artifact is revised between prompt and forward. The call gets "This request changed…" and nothing is approved.
- **Child approval:**
  - The child sends `swarm_send question approval:true`. The orchestrator runs `native_prompt for_msg`, shows the prompt, the user approves, and the orchestrator forwards `native_answer`.
  - The child is woken by `approval_result` with `reply_to` = its question id, and the bound row is `approved`.
- **Child plain question, bad answer, owed answer, blocked relay:** as in rev 1. `answer` without or with a bad `reply_to` is refused, and the 10-minute nudge fires once.
- **Offline daemon:** no dot, empty count, and the popover shows cached rows. The yellow dot shows if the cached Needs you is non-empty.
- **Repo search:** `swarm_read {repos:{}}` lists repos, and `confirm_repos` given a name returns the new error.

---

## 9. Explicitly out of scope

- macOS notification rules.
- Stopping a deliberately malicious same-UID agent (F12). A real boundary would need token isolation per agent (for example, no readable daemon token), which is a separate spec.
- Cursor native question interception (vendor bug, section 1.7).
- The `requests` schema, a `source` column, and a `messages.reply_to` index.
- Removing `swarm answer`, `swarm approve` or their HTTP routes.
- Codex `PermissionRequest` registration. Codex launches with approvals bypassed, so it would never fire.

---

## 10. Open questions

Every question from revs 1 and 2 is answered (sections 1.5 and 1.6). The live probes are done: Task 4 (muse) and Task 4b (codex and agy). Their outcome was already decided for most cases (a kind that fails joins cursor's exception in Task 9, and a kind with no answer text records `agent_reported`) — except one new question the muse probe raised:

**OQ-F (2026-09-26, from Task 4's live probe): muse's `request_user_input` is invisible to hooks — how does Task 9 handle it?** The probe (section 1.7) found that muse never dispatches `PreToolUse`/`PostToolUse` around its own `request_user_input` tool at all (confirmed live, twice, against a plugin whose `matcher: null` hooks correctly fired for muse's other tool calls in the same turns). This is stronger than "hook fires but can't deny" — there is no hook to intercept, so Swarm cannot open a Needs-you row for a top-level muse agent's native question, cannot deny it for a parented one, and cannot record its answer. **Consequence for Task 9:** muse must join cursor's exception (keep `swarm_ask kind:"question"` available), not the refused list Task 4's own row assumed pending this probe. Unlike cursor, though, muse *does* still get `UserPromptSubmit` hooks (confirmed live, `internal/adapter/testdata/muse-hook-userpromptsubmit.json`), so a fallback may exist beyond the plain exception — candidates to evaluate when Task 9 is implemented: (a) scan `UserPromptSubmit`'s human-typed reply text for an answer to the daemon's last-issued question (fragile: no direct tool-call linkage); (b) `muse session-message` (cross-session messaging, seen in `muse --help`) as an alternative channel; (c) accept the gap and document that muse questions only ever surface Swarm-side via `swarm_ask`, same as cursor. This spec does not pick one — that decision belongs to whoever implements Task 9, informed by this probe.
