# Approval simplification

## Intent and user journey

Swarm should ask only for decisions the user has not already made. Selecting repositories when starting a spike or orchestrator authorizes work in those repositories. During a feature spike, the user reviews short, useful summaries of the spec's consequential sections through native approval prompts. Once the spec is approved and the plan exists, Swarm shows the absolute paths to both artifacts and asks for one native plan approval. The existing final acceptance decision remains.

```text
Start with repositories → research and worktrees → section summaries + approvals
→ spec approved → plan written → spec and plan paths + plan approval
→ implementation → final acceptance
```

## Repository scope

Remove `confirm_repos` as a separate user decision. The repository ids chosen at spike creation become the root's usable repositories. An orchestrator started from an existing root already receives the repositories selected in its start sheet. Additional repositories require an explicit scope update through `swarm_repos` or the orchestrator start picker, with registered ids and the existing active-worktree protections; no second approval dialog is raised. `swarm_worktree` still rejects an unknown or out-of-scope repository. Plan materialization still checks that every task repository is within the root's selected set. Migrate existing spikes with `suggested_repos` and no confirmed set on first repository read or materialization, atomically and once, so in-flight work does not get stranded. Do not overwrite a nonempty confirmed set. Withdraw existing open `confirm_repos` requests with normal request-resolved events when the selected set is adopted. Preserve `repos_version` concurrency checks for actual set changes. Historical requests remain readable and resolvable; the new workflow opens none.

## Spec section review

The agent writes each spec section before requesting approval. Normalize a heading by removing an optional leading decimal section number (`2.` or `2.3.`), trimming surrounding whitespace, and case folding. Only these normalized headings are informational and exempt: `context`, `background`, `bibliography`, `references`, `file list`, `files`, `explicitly out of scope`, `out of scope`, and `work breakdown`. Every other heading, including an unknown heading and a headingless `document` section, requires approval. This defaults toward review when a new title appears. The registered artifact remains the source of truth; each approval is bound to its current section hash. A revised approved section requires a new approval. Materialization applies the same classifier to the current revision, so the gate matches the visible asks.

Each approval request carries a section summary of 1–700 Unicode characters, counting all visual syntax. It must say what will be delivered in plain language; use a compact table for structured comparisons or an ASCII sketch for flows when it makes the decision easier to see. The daemon rejects empty, whitespace-only, or overlong summaries and preserves the submitted text exactly. The agent displays that exact summary in chat immediately before the native approval prompt. The prompt names the section and revision and carries the existing `⟦swarm:<ref>⟧` token. A typed change request is stored as the comment. The user can open the full spec from its path, but the summary stands alone.

## Plan review and paths

The plan can be drafted and registered during review, but its approval request is opened only after all required spec sections are approved. Approval creation rejects an unapproved current spec. The agent prints a concise plan summary, including delivery packages and verification, and the exact absolute paths to the registered spec and plan before the native prompt. The daemon includes a lossless `review_paths` object in the approval response and replay relay, and replays the exact stored section or plan summary in `summary` so the agent can print both paths in full; path length never truncates the native question or its ref token. Relative paths are normalized at registration and again when presenting legacy artifacts; revising a legacy relative-path artifact preserves its id, revision history, and stale-approval behavior. The prompt carries the plan revision and lint warnings within its existing 1,000-rune limit. The plan approval remains revision-bound. A change to the plan invalidates the earlier approval. A changed decision section invalidates its section approval and blocks materialization, even when the plan approval is unchanged; unchanged approved hashes remain valid.

| Stage | User sees | Decision |
|---|---|---|
| Repository start | Repository picker | Start |
| Spec section | Up to 700 characters, optional table/sketch | Approve / Request changes |
| Plan | Spec path, plan path, short plan summary | Approve / Request changes |
| Completion | Integrated result | Accept / Request changes |

## Native agent behavior

Claude uses `AskUserQuestion`, agy uses `ask_question`, and Codex uses `request_user_input_async` with the existing hook-backed answer forwarding. Cursor and Muse expose native question tools, but current recorded probes show no observable answer hook. Recheck the actual agent versions and live hook events, recording versions, fixture names, and whether submission, answer, cancellation, and replay are observable. If hooks can observe both the question and answer, enable the same ref-bound native approval path. If they cannot, retain board/CLI approval as an explicit supported fallback and report the evidence; never infer approval from an unobserved answer. For every agent kind, test approve, request changes, typed comments, cancellation, and stale or replayed refs through the supported route. Distinguish fixture-level verification from live verification in the report.

Installed versions checked during this change: Claude Code 2.1.283, Codex 0.157.0, Cursor 3.21.13, agy 1.2.11, and Muse Code 1.4.0. The automated evidence uses recorded fixtures and simulated approvals; version checks alone do not constitute a fresh interactive native approval test.

| Agent | Supported route | Recorded evidence |
|---|---|---|
| Claude | Native hook | `TestClaudeAskUserQuestionAnswerBecomesObservedEvidence` |
| agy | Native hook with agent-reported option | `TestAgyLiveHookFixturesOpenAndCloseAQuestionRow` |
| Codex | Native async hook and next-turn reply | `TestCodexQuestionReplyBindsByRefAndEmitsTheNextStep` |
| Cursor | Board/CLI fallback | `TestCursorParseHookToolNames`; recorded `AskQuestion` hook absence |
| Muse | Board/CLI fallback | `TestMuseSiblingToolHookFixturesOpenNoRowAndDoNotBlock`; recorded `request_user_input` hook absence |

The shared request-ledger tests cover request changes, typed comments, cancellation, stale refs, and replay. Cursor's [hook documentation](https://prod.cursor.com/docs/hooks) describes general tool hooks, while the recorded Cursor `AskQuestion` probe shows that this tool currently bypasses them. Swarm must rely on the observable route until a new live probe proves otherwise.

## Implementation boundaries and safety

Use the existing request ledger and approval hashes; no new approval state or new authentication flow. Keep the `confirm_repos` API capable of resolving historical open requests, but stop producing new requests from the normal workflow. Do not silently broaden a root beyond the user-selected repositories. Unknown repositories and stale versions still fail. Native prompts always require an observed user decision; daemon text, chat output, and agent claims never count as approval. Update MCP tool descriptions, response `next` copy, relays, repository refusal copy, canonical skills, and installed copies together.

## Acceptance

1. A spike with selected repositories can create worktrees and register artifacts without a repository confirmation request; an out-of-scope repository is rejected.
2. Only consequential spec sections require approvals. Every requested summary is nonempty and at most 700 characters; visual syntax survives intact.
3. The plan prompt includes both absolute artifact paths, and materialization requires the current approved spec sections and plan revision.
4. Claude, agy, Codex, Cursor, and Muse each have a tested native or proven fallback approval route. No adapter treats an unobserved answer as approval.
5. Existing in-flight spikes and request rows remain usable; the full Go, install, and end-to-end approval suites pass.
