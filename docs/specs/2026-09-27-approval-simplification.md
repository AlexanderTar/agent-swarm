# Approval simplification — revised design

## Outcome

Swarm asks for user decisions about what will be built, not about which repository an agent may inspect. The starting repository picker is a hint. Spec review presents short summaries of consequential sections through each agent's native question tool. Once those sections are approved, the agent shows full paths to the spec and plan and asks for one plan approval. Final acceptance remains a separate decision.

```text
Starting repositories (hint) → research in any repository → spec section summaries
→ section approvals → full spec + plan paths → plan approval → implementation
```

## Repository authority

The agent may choose any repository during research, worktree creation, or plan materialization. No root repository list, `repos_version`, or confirmation request may gate that choice. `swarm_worktree` accepts any catalog repository, even if absent from the starting hint. It also accepts a local repository path and registers that path when needed. The path must exist and be a real Git repository so Swarm can create a worktree; this is data validation, not scope approval. Provide a catalog registration route independent of creating a worktree so a plan can name a newly discovered repository. Materialization resolves every task repository to a catalog id and stores those ids on the resulting items; it never compares them with the starting hint. An unresolvable name or invalid path gets a direct error explaining how to register it. `swarm_repos` may remain for optional display/bookkeeping, but using it is never required to work or materialize.

On startup, automatically resolve **all open `confirm_repos` requests** as approved in a one-time, idempotent migration. Record each request's proposed and expansion repositories, excluding entries explicitly marked dropped, in its resolution. Keep the starting selection as a hint; the migration must not narrow or gate it. Emit normal request resolution and agent relay events, but label the action as a daemon migration, never as a user click. Refuse new `confirm_repos` asks so no new confirmation dialog appears. Other open approvals remain untouched.

## Spec section review

The agent writes and registers the spec before requesting approvals. Normalize each heading by removing an optional leading decimal section number (`2.` or `2.3.`), trimming whitespace, and case folding. Only `context`, `background`, `bibliography`, `references`, `file list`, `files`, and `work breakdown` are informational and require no approval. **`out of scope` and `explicitly out of scope` require approval**, as do unknown and headingless sections. The same classifier drives request creation and the materialization gate.

Each requested section has a plain-language summary of 1–1000 Unicode characters, including Markdown table or ASCII sketch syntax. It explains what will be delivered or excluded. Use a compact visual when it clarifies a comparison or flow. Reject empty, whitespace-only, and overlong summaries. Preserve the summary exactly in the approval response and replay relay. The agent prints it before the native prompt. Approval binds to the current section hash; changing that section requires approval again.

## Plan review

The plan may be drafted while spec review is underway. A plan approval request opens only after all required sections of the current spec are approved. The agent prints a short plan summary with delivery packages and verification, followed by the **full absolute path** to both registered artifacts, immediately before the native question. `review_paths.spec` and `review_paths.plan` carry those paths losslessly in both the initial response and replay relay. The native question remains short and retains its ref token. The plan approval binds to its current revision. Materialization checks the current spec section hashes and plan revision.

| Stage | What the user sees | Decision |
|---|---|---|
| Start | Suggested repositories | Start work |
| Spec section | Up to 1000 characters; table/sketch when helpful | Approve / Request changes |
| Plan | Summary plus full spec and plan paths | Approve / Request changes |
| Completion | Integrated result | Accept / Request changes |

## Native question delivery

Claude (`AskUserQuestion`), agy (`ask_question`), and Codex (`request_user_input_async`) use their existing hook-backed answer path. Cursor (`AskQuestion`) and Muse (`request_user_input`) use their native question tools as well. Recorded probes for currently installed versions did not surface those two tools' answers to Swarm hooks. After the native tool returns, the agent forwards the selected decision and exact returned answer text through `swarm_ask kind:"native_answer"`, tied to the daemon-issued request or child-message ref. The daemon accepts that explicit MCP report as `agent_reported` evidence. This is an agent attestation, not independently observed user input; the audit trail must say so. No returned answer means no MCP submission and no approval. A cancellation leaves the request open for replay or board/CLI action.

For both request and child-message refs, validate caller ownership, current request state, decision/answer consistency, and current artifact revision. Reject a mismatched option, stale ref, second resolution, or empty reported answer. Preserve typed comments. Hook-observed routes retain `observed` evidence when answer text is visible. Board/CLI remains available if a native tool fails. Test the native tool contract for all five agents, covering approval, changes, typed comments, cancellation, and stale/replayed refs. Record whether each result comes from a live interactive run, a recorded fixture, or a simulated tool return; do not label one as another.

## Evidence and compatibility

Installed versions recorded during the earlier review: Claude Code 2.1.283, Codex 0.157.0, Cursor 3.21.13, agy 1.2.11, and Muse Code 1.4.0. Earlier Cursor and Muse probes showed no answer hook in their recorded fixtures; this must be rechecked during implementation. Existing relative artifact paths continue to display as absolute paths, and revising one preserves its artifact history. Existing request refs, approval hashes, stale-revision rules, and board/CLI endpoints remain compatible.

## Acceptance

1. Worktrees and materialized tasks may use repositories outside the starting hint. A newly found local Git repository can be registered without approval; a non-repository path fails with a clear data error.
2. Every open historical `confirm_repos` request is automatically marked approved on startup with migration provenance; new confirmation asks are refused.
3. Only the seven informational headings are skipped. Both out-of-scope headings require approval. Section summaries preserve visual text and accept exactly 1000 Unicode characters, rejecting 1001.
4. Full spec and plan paths are displayed immediately before plan approval, and approval cannot open before the current required spec sections are approved.
5. All five agent kinds have tested native-question approval flows. Cursor and Muse MCP-reported answers are explicitly marked `agent_reported`; cancellation, stale refs, and duplicate submissions cannot approve anything.
