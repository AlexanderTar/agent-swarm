# Spec: finish a top-level item with a PR

## Context

**Problem (user, 2026-09-29):** an epic's acceptance was requested although the orchestrator's
own note said the branch was not pushed and had no PR. Accepting it moved the epic to Done and
cleaned up its agents and worktrees while the work never reached a default branch.

**The change:** before any top-level item (epic, bug, chore) can close, the user is asked, through
the native question tool, whether to create a PR and whether it should auto-merge once checks
pass. This is loosely based on `superpowers:finishing-a-development-branch`, with the user's own
option set. Picking a PR option is the acceptance, but the item only becomes Done once every PR
(and every local merge) is merged.

**Today (verified against `main` at 16a87e1):**
- `reconcileRoot` (`internal/items/transition.go:626-730`) opens `accept_epic` (epic) or
  `accept_fix` (bug, chore) when every child is done or cancelled and an `integrated` checkpoint
  is newer than the last child change. It moves the root to InReview (:703) and writes the prompt
  strings (:706-712) and the binding `{item_revision, integrated_checkpoint, git}` (`acceptBinding`,
  :479; built at :713).
  - Stale handling is at :648-674.
  - An approval bound to the current integration and revision sets the root to Done (:677-684).
  - With no live request, InReview goes back to InProgress (:689).
- `checkRoot` (:354-367) allows a daemon move to Done only when `approvedCurrent` (:511) holds.
- `OnRequestOpened` (`internal/runtime/requests.go:469`) → `routeAcceptTx` (:576) binds the row to
  the live top-level orchestrator → `relayRequestTx` (:501) sends a `request_open` relay with
  `native_prompt` and `next`. Notification rules: `internal/notifyrules/notifyrules.go:54-55`.
- `nativePromptFor` (`internal/runtime/native.go:289-303`) builds headers "Accept epic" /
  "Accept fix" / "Accept chore" with `approveOptions` = `["Approve","Request changes"]` (:42).
  `NativePrompt{Header,Question,Options}` (:29) has no option descriptions.
- The first relay freezes `$.question`/`$.header` into `binding_json`
  (`storedNativePromptTx` :430, `freezeNativeQuestionTx` :507). A frozen replay always returns
  `approveOptions` (:478).
- `nativeAnswer` (:1027) maps `decision` through `decisionLabel` (:885), checks evidence with
  `matchDecisionEvidence` (:929) against the other label (`otherApprovalLabel` :889), and resolves
  `approved` via `approveCheck` (`requests.go:1444`) or `changes_requested`.
- `resolve` (`requests.go:1356`) writes the user's comment into `response_text`.
- Board: `POST /api/requests/{id}/approve` and `/request-changes`
  (`internal/httpapi/requests.go:89,114`), with `approveBody` at `internal/httpapi/runtime.go:363`.
  CLI: `cmdApprove` (`cmd/swarm/runtime_cmds.go:501`).
- `ApprovalChatBlock` (`native.go:330`) covers section/plan/report and child approvals only.
  `docs/specs/2026-09-28-approval-chat-block.md` locked decision 2 says `accept_*` gets no
  `chat_block`. **This spec supersedes that line for `accept_epic`/`accept_fix`.**
- Nothing in the daemon runs `git push` or `gh`. `repos` has `remote_url`, `remote_owner` and
  `default_branch` (`internal/repos/service.go:27-29`, filled by `ReadGitInfo` in
  `internal/repos/git.go:51-64`). A checkpoint's `GitRef{repo,branch,sha,dirty}`
  (`internal/runtime/model.go:172`) names the repo by **repo name**, not id or path
  (`rwWorktreesFor`, `checkpoint.go:665`; `changedFiles`, `checkpoint.go:996`).
- `checkpoints.kind` has a CHECK without room for a new kind (`0001_init.sql:193-194`, never
  rebuilt).
- Loop pattern: `ReclaimWorktreesLoop` (`internal/runtime/reconcile.go:1585`), wired in
  `cmd/swarm/daemon.go:346-367`.
- `worktree.mergedOrPushed` (`internal/worktree/worktree.go:553`) keeps a worktree unless HEAD is
  merged into its base or equals `@{u}`. A squash-merged PR leaves the local branch unmerged by
  ancestry, so the branch must be pushed with an upstream for cleanup to proceed.
- Notification titles are static: `notify.Render` (`internal/notify/notify.go:41`) expands
  placeholders in `Body` only.
- The latest migration is 0021.

**Collision warnings:**
- The skills are duplicated in `skills/` and `internal/install/skills/`; both copies must stay
  byte-identical (`make skills-sync`).
- The `accept` todo entry (`internal/runtime/todos.go`, spec 2026-09-28-orchestrator-todos) keeps
  its rule, `completed` only when the root is Done. It now stays `in_progress` through the merge
  wait, which is correct as-is.

## Locked decisions

1. **One native question with three options** replaces the "Accept" question for epic, bug and
   chore. Kinds stay `accept_epic`/`accept_fix`. The options are:
   1. "Create PR, auto-merge when checks pass"
   2. "Create PR, I'll merge it myself"
   3. "Request changes"

   Picking option 1 or 2 is acceptance: the request resolves `approved`. The item becomes Done
   only when every repo of the integrated checkpoint is merged.
2. **Merge method** is the repo's default: squash if allowed, else a merge commit, else rebase. The
   orchestrator reads it with `gh api repos/{owner}/{repo}` (`allow_squash_merge`,
   `allow_merge_commit`, `allow_rebase_merge`).
3. **A repo without a GitHub remote** (`repos.remote_url` does not contain `github.com`) is merged
   into its default branch locally by the orchestrator. The daemon verifies the local merge.
   - **All-local item** (no repo has a GitHub remote): the question has **two** options, "Merge
     into <base> locally" and "Request changes". Options 1 and 2 would be identical here.
   - **Mixed item:** it uses the three PR options. Options 1 and 2 have descriptions naming each
     local repo.
4. **The orchestrator acts; the daemon verifies and watches.**
   - The orchestrator runs `git push`, `gh pr create`, `gh pr merge --auto` or the local merge,
     then reports on `swarm_checkpoint kind:"finishing"`.
   - The daemon verifies every entry with `gh`/`git` and refuses a bad report with a plain error.
   - The daemon then polls open PRs every 60 s.
5. **The merge choice lives in `binding_json.$.merge`** (`"auto"|"manual"|"local"`), set in the
   same tx as the approval. It is also sent as `merge` on `approval_result`. `response_text` keeps
   carrying the user's comment.
6. **Status rule** (`reconcileRoot`, InReview branch), in this order:
   1. An approved finish request exists for the current integration and revision, and the children
      are finished:
      - `merge` is empty (a pre-0022 approval) → Done, as today.
      - Any `item_merges` row of the current integrated checkpoint is `closed` → InProgress.
      - Every repo named in the integrated checkpoint's `git` has a `merged` row → Done.
      - Otherwise → stay InReview. **This check runs before** the "no live request → InProgress"
        fall-through.
   2. The rest is unchanged: an open current request keeps InReview; anything else → InProgress,
      then step 4 opens a request only for a new integrated checkpoint.
7. **One finishing report per integration.** A second `finishing` checkpoint for the same
   integrated checkpoint is refused, and a new integration starts a new round. After
   `pr_checks_failed` the orchestrator pushes to the same PR and sends no new checkpoint.
8. **No pushing before the user picks.** The skill forbids `git push` and `gh pr create` before
   the finish answer, so the question's "not pushed" is a stated contract. The daemon does not
   check it.
9. **`gh` runs outside transactions**, like the Muse session-log scan in `nativeAnswer`. The
   finishing pre-pass and each watcher tick call `gh`/`git` first, then write in a short tx that
   re-checks state.
10. **The finish `chat_block` is not gate-enforced.** `SummaryGate` keeps its
    section/plan/report switch. The block is delivered on the relay and in `next`, like a child
    approval's block.
11. **Pre-deploy open accept rows:** migration 0022 removes their frozen `$.question`/`$.header`,
    so the next relay re-freezes the new three-option question. That relay comes only on the
    orchestrator's next session start or unacked re-relay: `resurfaceOpenRequests` skips a request
    whose last relay was acked. A live orchestrator that already acked the old two-option relay
    keeps the old text for that session, and its old "Approve" answer is refused by
    `decisionLabels`. The fallback is the user answering on the board, or the new prompt arriving on
    the next session start.
12. **A board/CLI finish approval with no routed orchestrator is delivered on the next session
    start.** `resolve()` skips `approval_result` when `agent_id` is empty. `resurfaceOpenRequests`
    (`requests.go:616`), which already binds open accept rows for a top-level orchestrator, also
    binds that root's approved accept row in the same tx. The row must have `$.merge` set, and its
    `integrated_checkpoint` must be the newest one with no `item_merges` rows. It then enqueues the
    same `approval_result` payload `Approve` builds. The Details panel shows a one-line
    "waiting for the orchestrator" state for that window.

## DB models

Migration `internal/db/schema/0022_item_merges.sql`:

```sql
-- 0022_item_merges.sql: per-repo PR / local-merge tracking for finishing a top-level item
-- (docs/specs/2026-09-29-finish-with-pr.md); unfreeze open accept questions so they re-freeze
-- with the new finish options.
CREATE TABLE item_merges (
  id                    TEXT PRIMARY KEY,
  item_id               TEXT NOT NULL REFERENCES items(id),
  integrated_checkpoint TEXT NOT NULL REFERENCES checkpoints(id),
  repo                  TEXT NOT NULL,          -- repo name, as in the checkpoint's GitRef.repo
  repo_id               TEXT NOT NULL REFERENCES repos(id),
  kind                  TEXT NOT NULL CHECK (kind IN ('pr','local')),
  url                   TEXT,                   -- pr only
  number                INTEGER,                -- pr only
  base                  TEXT NOT NULL,          -- repo default branch
  head                  TEXT NOT NULL,          -- integrated branch
  auto_merge            INTEGER NOT NULL DEFAULT 0,
  state                 TEXT NOT NULL CHECK (state IN ('open','merged','closed')),
  checks                TEXT NOT NULL DEFAULT '' CHECK (checks IN ('','pending','passing','failing')),
  merged_sha            TEXT,
  checked_at            INTEGER,
  created_at            INTEGER NOT NULL,
  UNIQUE (item_id, integrated_checkpoint, repo)
);
CREATE INDEX item_merges_open ON item_merges(state, kind);

UPDATE requests SET binding_json = json_remove(binding_json, '$.question', '$.header')
 WHERE state = 'open' AND kind IN ('accept_epic', 'accept_fix') AND binding_json IS NOT NULL;
```

Notes:
- A `local` row is inserted already `merged` with `merged_sha` set, `url`/`number` NULL and
  `checks` `''`.
- A `pr` row is inserted `open`, or `merged` if `gh` already reports MERGED.
- There is no new checkpoint kind in SQL. A finishing report is stored as a `progress` checkpoint
  row carrying its summary (see Model / API types).
- After the migration, health reports `schema: 22`.

## Model / API types

### Go: `internal/runtime/merges.go` (new)

```go
type FinishPR struct {
	Repo string `json:"repo"` // repo name, as in the integrated checkpoint's git
	URL  string `json:"url"`  // https://github.com/<owner>/<name>/pull/<n>
}

type FinishMerged struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"` // the default branch commit that contains the integrated sha
}

type ItemMerge struct {
	Repo      string `json:"repo"`
	Kind      string `json:"kind"`              // "pr" | "local"
	URL       string `json:"url,omitempty"`
	Number    int    `json:"number,omitempty"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	AutoMerge bool   `json:"auto_merge"`
	State     string `json:"state"`             // "open" | "merged" | "closed"
	Checks    string `json:"checks"`            // "" | "pending" | "passing" | "failing"
	MergedSHA string `json:"merged_sha,omitempty"`
}

type MergeProgress struct {
	Merged int `json:"merged"`
	Total  int `json:"total"` // distinct repos in the integrated checkpoint's git
}

// finishRepo is one integrated repo resolved against the catalog.
type finishRepo struct {
	Ref                                    GitRef
	RepoID, Path, RemoteURL, Owner, Base   string
	GitHub                                 bool // strings.Contains(RemoteURL, "github.com")
}

// ghPR is the subset of `gh pr view --json <prFields>` the daemon reads.
type ghPR struct {
	State            string `json:"state"`            // OPEN | CLOSED | MERGED
	HeadRefName      string `json:"headRefName"`
	BaseRefName      string `json:"baseRefName"`
	Number           int    `json:"number"`
	URL              string `json:"url"`
	AutoMergeRequest *struct{} `json:"autoMergeRequest"`
	MergeCommit      *struct{ Oid string `json:"oid"` } `json:"mergeCommit"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`       // CheckRun
		Status     string `json:"status"`     // CheckRun: QUEUED | IN_PROGRESS | COMPLETED | ...
		Conclusion string `json:"conclusion"` // CheckRun: SUCCESS | FAILURE | ...
		Context    string `json:"context"`    // StatusContext
		State      string `json:"state"`      // StatusContext: SUCCESS | FAILURE | ERROR | PENDING | EXPECTED
	} `json:"statusCheckRollup"`
}

const prFields = "state,headRefName,baseRefName,number,url,autoMergeRequest,mergeCommit,statusCheckRollup"

func isGitHubRemote(url string) bool
func (s *Store) finishReposTx(ctx context.Context, q txQuerier, rootItemID string, refs []GitRef) ([]finishRepo, error)
func (s *Store) ghPRView(ctx context.Context, url string) (ghPR, error) // s.Exec "gh pr view <url> --json <prFields>"
func rollupChecks(p ghPR) (checks string, failing []string)
func (s *Store) writeFinishing(ctx context.Context, sessionID string, in CheckpointInput) (CheckpointResult, error)
func (s *Store) Merges(ctx context.Context, rootItemID string) ([]ItemMerge, error)             // newest integrated checkpoint's rows; nil when none
func (s *Store) MergeProgressFor(ctx context.Context, rootItemID string) (*MergeProgress, error) // nil unless root in_review with an approved finish
func (s *Store) WatchMerges(ctx context.Context) error
func (s *Store) WatchMergesLoop(ctx context.Context, every time.Duration) // same shape as ReclaimWorktreesLoop
```

**Resolving repos (`finishReposTx`):**
- For each distinct `GitRef.Repo`, it runs
  `SELECT id, path, COALESCE(remote_url,''), COALESCE(remote_owner,''), COALESCE(default_branch,'') FROM repos WHERE name = ? AND id IN (SELECT value FROM json_each((SELECT confirmed_repos_json FROM items WHERE id = ?)))`.
- If no confirmed repo matches, it falls back to `WHERE name = ? ORDER BY last_used_at DESC LIMIT 1`.
- If there is still none, the repo is unknown.

**`rollupChecks`:**
- Empty rollup → `""`.
- Any entry with `Conclusion` or `State` in FAILURE, ERROR, TIMED_OUT, CANCELLED,
  ACTION_REQUIRED or STARTUP_FAILURE → `failing`. `failing` holds those entries' `Name`, or
  `Context` when `Name` is empty, in rollup order.
- Otherwise, any CheckRun with `Status` ≠ COMPLETED, or any StatusContext in PENDING or EXPECTED
  → `pending`.
- Otherwise → `passing`.

### Go: changed types

`internal/runtime/model.go`:

```go
Finishing CheckpointKind = "finishing" // API-only; stored as a 'progress' row
```

`internal/runtime/checkpoint.go`:
- `CheckpointInput` gains:
  ```go
  PRs    []FinishPR     // kind finishing only
  Merged []FinishMerged // kind finishing only
  ```
- `WriteCheckpoint` starts with `if in.Kind == Finishing { return s.writeFinishing(ctx, sessionID, in) }`.
- On any other kind, sending `prs` or `merged` is refused.

`internal/runtime/native.go`:

```go
type NativePrompt struct {
	Header       string   `json:"header"`
	Question     string   `json:"question"`
	Options      []string `json:"options"`
	Descriptions []string `json:"descriptions,omitempty"` // parallel to Options; finish prompts only
}

type ChatBlockInput struct {
	// ...existing fields...
	ItemKey string   // finish: root key
	Git     []GitRef // finish: integrated refs, one line each
}

// finishDecisions lists a finish prompt's decisions in option order:
// 3 options → auto_merge, manual_merge, request_changes; 2 options → merge_locally, request_changes.
func finishDecisions(opts []string) []string

// decisionLabels returns the chosen decision's option label and every other label. A msg_ ref and
// every non-accept kind keep approve → "Approve" (others ["Request changes"]) and
// request_changes → "Request changes" (others ["Approve"]).
func decisionLabels(req Request, np NativePrompt, decision string) (label string, others []string, err error)

// finishDecisionFor maps a native answer's text to a finish decision ("" when none):
// labelShape-match against "Create PR, auto-merge when checks pass" → auto_merge,
// "Create PR, I'll merge it myself" → manual_merge, "Merge into <x> locally" → merge_locally.
func finishDecisionFor(trimmed string) string

func matchDecisionEvidence(responseText, label string, others []string, callerComment string) (evidence, comment string, err error)
func NativePromptNextStep(ref string, decisions []string) string
```

- `approveOptions` stays for every other kind. `otherApprovalLabel` is deleted; `others` replaces
  it.
- The decision enum is `approve | request_changes | auto_merge | manual_merge | merge_locally`:
  - `approve` is valid only for non-accept kinds;
  - the three merge decisions are valid only for accept kinds, and only when they appear in
    `finishDecisions(frozen options)`.
- Merge decision → `$.merge`: `auto_merge` → `"auto"`, `manual_merge` → `"manual"`,
  `merge_locally` → `"local"`.
- `nativePromptFor`, case `KindAcceptEpic, KindAcceptFix`, reads the binding's `git` and resolves
  repos with `finishReposTx`. It builds the copy under "User-facing copy" and caps the question at
  1000 runes with `capRunes`.
- `freezeNativeQuestionTx` also stores `$.options` and `$.descriptions` when
  `len(np.Descriptions) > 0`.
- `effectiveNativeQuestionTx` returns the frozen `$.options`/`$.descriptions` when present, else
  `approveOptions`.
- `approvalChatBlockTx`, case accept kinds, reads the integrated checkpoint's summary
  (`binding.integrated_checkpoint`).
- `nativeAnswer`: for a `req_` ref it loads the request (and its effective prompt) before
  `matchDecisionEvidence` to get `label`/`others`. For an accept kind with a merge decision, it
  resolves `approved` with `approveCheck` plus an `after` hook
  `json_set(binding_json,'$.merge',?)`. The payload is
  `{"decision":"approved","merge":<m>,"section_id":"","section_sha256":"","evidence":<e>}`.
- `NativeAnswerNextStep`: when `finishDecisionFor(trimmed)` ≠ "", it returns
  `[swarm] Recorded "<label>" for <ref>. Forward it now: swarm_ask kind:"native_answer", ref:"<ref>", decision:"<d>"`.

`internal/runtime/requests.go`:
- `ApproveInput` gains `Merge string` (`"auto"|"manual"|"local"`).
- `Approve`, for `accept_epic`/`accept_fix`:
  - it validates `Merge` against the finish mode (all-local → only `local`; otherwise `auto` or
    `manual`);
  - it adds the `$.merge` after hook;
  - the payload adds `"merge"`.
- For other kinds, `Merge` is ignored.
- `relayRequestTx` adds `chat_block` for accept kinds and passes
  `finishDecisions(np.Options)` (or `["approve","request_changes"]`) to `NativePromptNextStep`.
- `RequestWire` gains `FinishLocal bool json:"finish_local,omitempty"`: true on an accept row when
  no repo in its binding has a GitHub remote.

`internal/items/transition.go`:

```go
// FinishApproval is the approved finish request bound to a root's newest integrated checkpoint.
type FinishApproval struct {
	RequestID, AgentID, Merge, CheckpointID string // Merge "" for a pre-0022 approval
	Git                                     json.RawMessage // the checkpoint's git_json ([]GitRef shape; items can't import runtime)
}

// FinishApprovalTx is the exported accessor runtime uses (writeFinishing, MergeProgressFor,
// WatchMerges for the relay recipient, resurfaceOpenRequests). ok is false when none.
func (s *Store) FinishApprovalTx(ctx context.Context, q querier, rootID string) (fa FinishApproval, ok bool, err error)

// approvedCurrent now also returns the approval's $.merge ("" for a pre-0022 approval).
func (s *Store) approvedCurrent(ctx context.Context, q querier, it Item) (ok bool, merge string, err error)

// mergeState counts the current integrated checkpoint's repos and their item_merges rows.
func (s *Store) mergeState(ctx context.Context, q querier, it Item, st rootState) (merged, total int, closed bool, err error)

// finishedCurrent = approvedCurrent && (merge == "" || merged == total && total > 0 && !closed).
func (s *Store) finishedCurrent(ctx context.Context, q querier, it Item) (bool, error)
```

- `reconcileRoot` applies locked decision 6.
- `checkRoot` gates a daemon Done on `finishedCurrent` instead of `approvedCurrent`.
- `total` is the distinct `repo` values of `st.ckpGit`. `merged` counts rows with
  `integrated_checkpoint = st.ckpID AND state = 'merged' AND repo IN (those)`.

### `writeFinishing`, in order

1. **Read pass (`s.DB`, no tx):**
   - the session is live and its agent is an orchestrator with no parent;
   - the item is `in.ItemKey` or the agent's item, and it must be the agent's root;
   - the root is `in_review`;
   - `Items.FinishApprovalTx` returns ok and a non-empty `Merge`;
   - no `item_merges` row exists for `(root, st.ckpID)`;
   - resolve repos with `finishReposTx`.
2. **Coverage:**
   - every integrated repo appears exactly once across `prs` and `merged`;
   - a GitHub repo appears in `prs`, and a non-GitHub repo appears in `merged`;
   - no unknown repo names appear.
3. **Verification, outside any tx:**
   - **PR:**
     - `url` matches `^https://github\.com/([^/]+)/[^/]+/pull/\d+$`, and group 1 equals
       `remote_owner`, case-insensitive;
     - run `ghPRView(url)`;
     - `headRefName` = `Ref.Branch` and `baseRefName` = `Base`;
     - `state` ≠ CLOSED;
     - if merge is `auto`: `autoMergeRequest` ≠ null, or state = MERGED.
   - **Local:**
     - run `git -C <Path> merge-base --is-ancestor <Ref.SHA> <sha>`;
     - then run `git -C <Path> merge-base --is-ancestor <sha> <Base>`.
4. **Write tx** (`IdemTx` keyed by `in.RequestID`):
   - re-check steps 1's approval and no-rows conditions;
   - insert one `item_merges` row per repo:
     - `pr`: state `merged` when gh says MERGED, with `merged_sha` = `mergeCommit.oid`; otherwise
       `open`; `checks` = `rollupChecks`; `auto_merge` = (merge = `auto`); `checked_at` = now;
     - `local`: state `merged`, with `merged_sha` = the reported sha;
   - insert a `progress` checkpoint row with `in.Summary` (1-500 runes, same check as today) and
     `git_json` = the integrated refs;
   - append `events.ItemChanged`;
   - run `Items.ReconcileTx(root key)`;
   - if the root is now Done, raise `item.merged`.
5. **Result:** `CheckpointResult{CheckpointID, ItemStatus, ItemRevision}`.

### `WatchMerges`, each tick

1. `SELECT … FROM item_merges m JOIN items i ON i.id = m.item_id WHERE m.kind = 'pr' AND m.state = 'open' AND i.status = 'in_review'`.
2. For each row, `ghPRView(url)` runs outside any tx. On error it logs
   `merges: gh pr view <url>: <err>` and skips the row until the next tick. There is no
   notification.
3. Then, in one tx per row:
   - **MERGED:** set `state='merged'`, `merged_sha`, `checks`, `checked_at`, then run
     `Items.ReconcileTx(root key)`. If the root is now Done, raise `item.merged`.
   - **CLOSED:** set `state='closed'` and enqueue a relay `pr_closed` to the approved finish
     request's `agent_id` (skipped when empty). Then run `Items.ReconcileTx` (root →
     InProgress).
   - **OPEN:** update `checks`, `checked_at`. When the new value is `failing` and the old one was
     not, enqueue a relay `pr_checks_failed` and raise `pr.checks_failed`.
   - Every changed row appends `events.ItemChanged` for the root.

Relay payloads go through `enqueueRaw`, `Kind:"relay"`, with the wake class `immediate`:

```json
{"event":"pr_checks_failed","item":"EPIC-14","repo":"agent-swarm","url":"https://github.com/o/agent-swarm/pull/412","number":412,"failing":["test (ubuntu)","lint"]}
{"event":"pr_closed","item":"EPIC-14","repo":"agent-swarm","url":"https://github.com/o/agent-swarm/pull/412","number":412}
```

- `WakeDue` wakes a live orchestrator for these.
- An orchestrator that is not live finds the relay in its inbox on its next session. The
  notification is the user's signal.

### MCP (`internal/mcpserver/tools.go`)

- The `swarm_checkpoint` description becomes: "Record progress: accepted, progress, blocked,
  handoff, completed, failed, integrated or finishing, with the verification evidence TDD
  requires."
- The schema adds:
  ```json
  "prs":{"type":"array","items":{"type":"object","properties":{"repo":{"type":"string"},"url":{"type":"string"}},"required":["repo","url"]},"description":"kind finishing: one PR per repo with a GitHub remote."},
  "merged":{"type":"array","items":{"type":"object","properties":{"repo":{"type":"string"},"sha":{"type":"string"}},"required":["repo","sha"]},"description":"kind finishing: one local merge per repo without a GitHub remote; sha is the default-branch commit containing the integrated sha."}
  ```
- The handler struct gains `PRs []runtime.FinishPR json:"prs"` and
  `Merged []runtime.FinishMerged json:"merged"`.
- In `swarm_ask`, `decision` becomes
  `{"type":"string","enum":["approve","request_changes","auto_merge","manual_merge","merge_locally"],"description":"kind native_answer: the user's observed decision; finish requests use auto_merge, manual_merge, merge_locally or request_changes"}`.
- `requestOut` passes the request's decisions to `NativePromptNextStep` and emits
  `native_prompt.descriptions` untouched.

### HTTP

- `POST /api/requests/{id}/approve` body (`approveBody`) gains `"merge": "auto"|"manual"|"local"`.
  It is required for `accept_epic`/`accept_fix`.
- The response is unchanged (`RequestWire`, now carrying `finish_local`).
- `GET /api/items/{key}` adds `"merges": ItemMerge[]` for a root with an approved finish on its
  newest integrated checkpoint (`[]` before the orchestrator reports).
- `agentNodeWire` adds `"merge": {"merged":1,"total":2}` (`*runtime.MergeProgress`,
  `json:"merge,omitempty"`) for a top-level orchestrator whose root has `MergeProgressFor` ≠ nil.

### CLI

`swarm approve REQ [--merge auto|manual|local]` sends `merge` when given. The usage line becomes
`Usage: swarm approve REQ [--merge auto|manual|local]`.

### Web (`web/src/types.ts`)

```ts
export type MergeChoice = "auto" | "manual" | "local";
export interface ApproveBody { section_sha256?: string; artifact_revision?: number; binding?: AcceptBinding; merge?: MergeChoice }
export interface ItemMerge {
  repo: string; kind: "pr" | "local"; url?: string; number?: number; base: string; head: string;
  auto_merge: boolean; state: "open" | "merged" | "closed"; checks: "" | "pending" | "passing" | "failing"; merged_sha?: string;
}
// Request gains: finish_local?: boolean;
// ItemDetail gains: merges?: ItemMerge[];
```

`web/src/logic/review.ts`: `approveBody(r, merge?: MergeChoice)`. For an accept kind it returns
`{ binding, merge }`.

### Swift (`apps/menubar/Sources/SwarmBarKit/Wire.swift`)

```swift
public struct MergeProgress: Codable, Sendable, Equatable { public let merged: Int; public let total: Int }
// AgentNode gains: public var merge: MergeProgress?   (CodingKeys adds `merge`)
```

## Screens

**Chat block** (printed by the orchestrator right before the question):

```
### Approval · Finish EPIC-14

Login redirect epic integrated: 6 tasks merged into swarm/epic-14, go test + web build pass.

agent-swarm: swarm/epic-14 at 3f9c2ab
endurio: swarm/epic-14 at 81d0e44
```

**Native question**, as shown by Claude `AskUserQuestion`:

```
 Finish epic
 Finish EPIC-14 "Login redirect"? Branch swarm/epic-14 at 3f9c2ab, not pushed.

 ❯ 1. Create PR, auto-merge when checks pass
      Push, open a PR into main, merge automatically when checks pass.
   2. Create PR, I'll merge it myself
      Push and open a PR into main; Done when you merge it.
   3. Request changes
      Say what to change; I'll re-integrate and ask again.
```

- **All-local** variant: two options, `1. Merge into main locally` and `2. Request changes`.
- Not shown: reviewers, labels, draft toggle, merge-method picker.

**Web Review panel, footer of an accept request:**

```
┌──────────────────────────┐ ┌───────────┐ ┌─────────────────┐
│ Create PR + auto-merge   │ │ Create PR │ │ Request changes │
└──────────────────────────┘ └───────────┘ └─────────────────┘
```

- The first button is `bg-accent`. The second uses the existing secondary style (`border
  border-line`). `RequestChanges` is unchanged.
- `finish_local`: `[ Merge locally ]  [ Request changes ]`.
- The header title uses `SCOPE_LABEL`: "Finish epic · EPIC-14 › Login redirect".

**Web Details panel, "Awaiting merge" block** (above Progress). It is shown only while the item is
`in_review` and `merges` is non-empty:

```
Awaiting merge
 agent-swarm   #412  checks ✓ passing   auto-merge on          ↗
 endurio       #88   checks ✗ failing   (orchestrator fixing)  ↗
 docs          merged locally 81d0e44
```

- `↗` links to `url` with `target="_blank" rel="noreferrer"`.
- Checks text: `✓ passing`, `✗ failing`, `… pending`, and `no checks` for `""`.
- A merged PR row reads `#412  merged`.
- If the finish request is approved but `merges` is empty (the orchestrator hasn't reported
  yet), the block shows one line: `Awaiting merge — waiting for the orchestrator to open PRs.`
- Nothing is rendered when there is no approved finish. There is no progress bar.

**Menubar orchestrator row** while `merge` is present (it overrides `progress`):

```
◉ epic-14-login                          ●
  Orchestrator · Awaiting merge · 1/2 merged · Idle
```

**Menubar Needs you** accept row, line 3: `Finish EPIC-14` (was `Accept EPIC-14`).

## User-facing copy

**Native prompt:**
- Headers: `Finish epic` (epic), `Finish fix` (bug), `Finish chore` (chore).
- Question, single repo: `Finish EPIC-14 "<title>"? Branch <branch> at <sha7>, not pushed.`
- Question, several repos:
  `Finish EPIC-14 "<title>"? Not pushed: <repo> <branch> at <sha7>, <repo> <branch> at <sha7>.`
- `<base>` is the shared default branch when all involved repos agree, else
  `each repo's default branch`.
- Option labels and descriptions:
  1. `Create PR, auto-merge when checks pass` / `Push, open a PR into <base>, merge automatically
     when checks pass.`
  2. `Create PR, I'll merge it myself` / `Push and open a PR into <base>; Done when you merge it.`
  3. `Request changes` / `Say what to change; I'll re-integrate and ask again.`
  - **Mixed item:** options 1 and 2 append ` <repo> has no GitHub remote: merged into <base>
    locally.` once per local repo.
  - **All-local item:** option 1 is `Merge into <base> locally` / `Merge the branch into <base> in
    your local checkout; Done once it's merged.`, and option 2 is option 3 above.

**Chat block:** `### Approval · Finish <KEY>`, a blank line, the integrated checkpoint summary, a
blank line, then one `<repo>: <branch> at <sha7>` line per repo.

**`next` for a finish prompt:** `NativePromptNextStep` with decisions rendered as
`decision:"auto_merge"|"manual_merge"|"request_changes"`, or
`decision:"merge_locally"|"request_changes"`.

**Errors:**

| Where | Copy |
|---|---|
| native_answer, accept kind, wrong decision | `decision for a finish request must be one of: <d1>, <d2>[, <d3>].` |
| native_answer, other kind, merge decision | `decision must be approve or request_changes.` (existing) |
| native_answer mismatch | `The user's native answer was %q, not %q.` (existing) |
| Approve, accept, bad/missing merge | `Choose how to finish: merge must be "auto" or "manual".` |
| Approve, all-local, not local | `Choose how to finish: merge must be "local" (no repository has a GitHub remote).` |
| finishing, not orchestrator/root | `Only the top-level orchestrator can write finishing on its own item.` |
| finishing, nothing approved | `Nothing to finish: <KEY> has no approved finish request for its latest integration.` |
| finishing, repeat | `Finishing for <KEY> is already recorded for this integration.` |
| finishing, missing repo | `Missing <repo>: report a PR or a local merge for every integrated repo.` |
| finishing, extra/unknown repo | `<repo> is not in <KEY>'s integrated checkpoint.` |
| finishing, wrong list | `<repo> has a GitHub remote; report it under prs.` / `<repo> has no GitHub remote; merge it locally and report it under merged.` |
| finishing, bad url | `<url> is not a PR in <owner>'s <repo> repository.` |
| finishing, gh missing | `gh isn't available to Swarm (<err>). Install GitHub CLI and run gh auth login, then send finishing again.` |
| finishing, gh failed/unauthenticated | `Couldn't read <url> with gh: <first stderr line>. Check gh auth status, then send finishing again.` |
| finishing, head | `<url> merges <headRefName>, not the integrated branch <branch>.` |
| finishing, base | `<url> targets <baseRefName>, not <repo>'s default branch <base>.` |
| finishing, closed | `<url> is closed without merging.` |
| finishing, auto not armed | `Auto-merge isn't on for <url>. Run gh pr merge <url> --auto --<method>, then send finishing again.` |
| finishing, local not merged | `<sha7> is not on <repo>'s <base>; merge the integrated branch first.` |
| other kinds with prs/merged | `prs and merged are only for a finishing checkpoint.` |
| checkRoot Done denial | `Finish this epic to mark it Done.` / `Finish this chore to mark it Done.` / `Finish this fix to mark it Done.` |

**Notifications** (`notifyrules.Rules`; `notify.Render` now expands placeholders in `Title` too):

| Kind | Level | Title | Body | Category |
|---|---|---|---|---|
| `request.accept_epic` | action | `Finish {KEY}: create PR?` | `{KEY}: integrated and verified. Pick how to finish.` | swarm.approval |
| `request.accept_fix` | action | `Finish {KEY}: create PR?` | `{KEY}: integrated and verified. Pick how to finish.` | swarm.approval |
| `pr.checks_failed` | attention | `PR checks failed` | `{KEY}: {repo} #{N} — {checks}` | swarm.agent |
| `item.merged` | info | `Merged` | `{KEY}: all PRs merged — done.` | swarm.info |

`{checks}` is the failing names joined with `, `.

**Web copy (`web/src/copy.ts`):**
- `acceptEpic: "Finish epic"`, `acceptFix: "Finish fix"`.
- New: `createPrAutoMerge: "Create PR + auto-merge"`, `createPr: "Create PR"`,
  `mergeLocally: "Merge locally"`, `awaitingMerge: "Awaiting merge"`,
  `checksPassing: "✓ passing"`, `checksFailing: "✗ failing"`, `checksPending: "… pending"`,
  `noChecks: "no checks"`, `autoMergeOn: "auto-merge on"`,
  `orchestratorFixing: "(orchestrator fixing)"`, `merged: "merged"`,
  `awaitingOrchestrator: "Awaiting merge — waiting for the orchestrator to open PRs."`,
  `T.mergedLocally(sha) = "merged locally " + sha7(sha)`.

**Menubar copy (`Copy.swift`):**
- `acceptItem(key)` returns `"Finish \(key)"`.
- New: `awaitingMerge(merged, total) = "Awaiting merge · \(merged)/\(total) merged"`.

**Skill text:** `skills/swarm-orchestrator/SKILL.md` and its mirror.

On line 20, replace `Apply \`superpowers:finishing-a-development-branch\` when deciding the final
branch integration and what to report to the user.` with:

```
Never push or open a PR before the user answers the finish question (see below).
```

Replace the line-54 bullet with:

```
- Merge in dependency order, run the plan's verification, then write `integrated` with the merged sha per repo and the verification results; its summary states each repo's branch and that nothing is pushed yet. The daemon then opens the finish request (`accept_epic`, or `accept_fix` for a bug or a chore) and sends you a `request_open` relay with `native_prompt` (options with descriptions), `chat_block` and `next`. Print `chat_block` exactly, show `native_prompt` with your native question tool (options and descriptions verbatim), and forward the answer with `swarm_ask kind: "native_answer"`, `ref` = the relay's `request_id`, `decision` = `auto_merge`, `manual_merge`, `merge_locally` or `request_changes`.
- On `approval_result` `approved`, read its `merge`. For each repo in the integrated checkpoint: if it has a GitHub remote, `git push -u origin <branch>`, then `gh pr create --base <default branch> --head <branch> --title "<item title>" --body "<integrated summary>"`; for `merge: "auto"` also read the repo's merge method (`gh api repos/{owner}/{repo}`: squash if `allow_squash_merge`, else merge if `allow_merge_commit`, else rebase) and run `gh pr merge <url> --auto --<method>`. If it has no GitHub remote, merge the branch into its default branch in the repo's checkout. Then write one `swarm_checkpoint kind: "finishing"` with `prs: [{repo, url}]` and `merged: [{repo, sha}]` covering every repo. If Swarm refuses it, fix what the error names and send it again.
- The item stays in review until every PR and local merge is merged; then Swarm moves it to done and ends your session about a minute later: post a short final summary and stop. Never write `handoff` after that; Swarm refuses it (`Root accepted; write completed.`). Don't poll PRs; the daemon watches them and wakes you.
- On `pr_checks_failed`: fix the failing checks in the integration worktree, commit and push to the same branch. Never disable, skip or weaken a check. Don't send another `finishing`.
- On `pr_closed`: the PR was closed without merging and the item is back in progress. Ask the user what to do with your native question tool before changing anything.
- On `changes_requested`, do what the comment asks, re-integrate, and the daemon asks again. If `native_answer` says the request is stale, the item changed after the question: wait for the new `request_open`.
```

Replace the line-63 bullet with:

```
- When the work is merged and verified, write `integrated` on the chore with the merged sha per repo and the verification results. The daemon opens `accept_fix` for the chore; finish it exactly like an epic or bug (finish question, PRs or local merges, `finishing`). The chore moves to Done once everything is merged.
```

## Progress-list wording

Added by the user on 2026-09-29. The progress list (`internal/runtime/todos.go`) switches to
present-continuous labels. The ids stay unchanged.

| List | id → label |
|---|---|
| Fixed (epic, bug, chore) | `work` → `Making the changes`; `integrate` → `Merging and verifying`; `accept` → `Finishing: PR or merge` |
| Feature spike | `frame` → `Understanding the request`; `research` → `Researching`; `design` → `Designing`; `spec` → `Reviewing the spec`; `plan` → `Writing the plan`; `critic` → `Checking for gaps`; `approve` → `Reviewing the plan and setting up tasks` |
| Debug spike | `frame` → `Understanding the problem`; `evidence` → `Reproducing and gathering evidence`; `root_cause` → `Finding the root cause`; `report` → `Reviewing the findings`; `plan` → `Planning the fixes`; `critic` → `Checking for gaps`; `approve` → `Reviewing the plan and setting up tasks` |

**Chore context step.** Every chore list, with or without tasks, starts with the entry
`{id: "context", label: "Gathering context"}`:
- It is `completed` once the chore has a non-cancelled task, or has any `progress` checkpoint on
  the chore itself. Otherwise it is `in_progress`.
- A chore with zero tasks also has the `work` entry ("Making the changes"). That entry is
  `pending` while context is not completed and `in_progress` once it is. It is `completed` once an
  `integrated` checkpoint exists, which takes precedence.

**Skill.** In the chore bullet that begins "Do the work directly" (`swarm-orchestrator` SKILL.md,
both copies), put this sentence first: `Gather context first: read the code, docs and recent
history the chore touches, then report a progress checkpoint.` The existing text follows it
unchanged.

## File list

**Changed:**
- `internal/db/schema/0022_item_merges.sql` (new).
- `internal/runtime/merges.go` and `merges_test.go` (new): types, `finishReposTx`, `ghPRView`,
  `rollupChecks`, `writeFinishing`, `Merges`, `MergeProgressFor`, `WatchMerges`,
  `WatchMergesLoop`.
- `internal/runtime/model.go`: `Finishing`.
- `internal/runtime/checkpoint.go`: the `CheckpointInput.PRs/Merged` fields, the finishing
  dispatch, and the prs/merged refusal on other kinds.
- `internal/runtime/native.go`:
  - `NativePrompt.Descriptions`, the finish prompt in `nativePromptFor`, and the freeze/replay of
    options;
  - `ChatBlockInput.ItemKey/Git`, the finish case in `ApprovalChatBlock`/`approvalChatBlockTx`;
  - `finishDecisions`, `decisionLabels`, `finishDecisionFor`, and the generalized
    `matchDecisionEvidence`;
  - `NativePromptNextStep(ref, decisions)`, `NativeAnswerNextStep`, and `nativeAnswer`;
  - `otherApprovalLabel` is removed.
- `internal/runtime/requests.go`: `ApproveInput.Merge`, `Approve`, `relayRequestTx`,
  `RequestWire.FinishLocal` in `RequestWireTx`, and `resurfaceOpenRequests` (it binds the approved
  finish row and enqueues its `approval_result`).
- `internal/items/transition.go`: `FinishApproval`/`FinishApprovalTx` (new, exported),
  `approvedCurrent` (adds merge), `mergeState`,
  `finishedCurrent`, `reconcileRoot`, and `checkRoot` (gate plus denial copy).
- `internal/notifyrules/notifyrules.go`: 2 changed rules and 2 new ones.
- `internal/notify/notify.go`: `Render` expands `Title` placeholders. The tests that validate Args
  with `notifyrules.Placeholders(rule.Body)` (`notify_test.go:214`, `runtime/agents_test.go:224`,
  `mcpserver/helpers_test.go:139`) also scan `rule.Title`.
- `internal/mcpserver/tools.go`: the checkpoint schema, description and handler; the `decision`
  enum; `requestOut`.
- `internal/httpapi/runtime.go`: `approveBody.Merge` and `agentNodeWire.Merge`.
- `internal/httpapi/requests.go`: passes `Merge`.
- `internal/httpapi/items.go`: `merges`.
- `cmd/swarm/runtime_cmds.go`: `cmdApprove --merge`.
- `cmd/swarm/daemon.go`: `func(ctx context.Context) { dm.rt.WatchMergesLoop(ctx, time.Minute) }`.
- Web:
  - `web/src/types.ts`, `web/src/copy.ts`, and `web/src/logic/review.ts`;
  - `web/src/panels/Review.tsx` (footer);
  - `web/src/panels/Details.tsx`, plus `web/src/components/MergeList.tsx` (new).
- Menubar: `Wire.swift`, `AgentActions.swift` (`subtitle`), `Copy.swift`, and `AppModel.swift`
  (no code change beyond the copy it calls). Tests next to each.
- Skills: `skills/swarm-orchestrator/SKILL.md` and `internal/install/skills/swarm-orchestrator/SKILL.md`.
- Existing tests that assert "Accept epic/fix/chore" headers, the two-option replay, the
  `approvedCurrent` → Done path and the two-label `matchDecisionEvidence` are **updated** to the
  new contract, never deleted.

**Reused unchanged:**
- `acceptKind` and the accept request kinds;
- `routeAcceptTx`, `resolveStale` and `resolve`;
- `OnRootDone`, `sweepFinishedRoots` and `worktree.mergedOrPushed`;
- `enqueueRaw`, `WakeDue` and `BindNativeQuestion`;
- `SummaryGate`;
- `ReadGitInfo`, `repos.remote_url/remote_owner/default_branch`;
- `Todos` and its `accept` entry.

**Deleted:** `otherApprovalLabel` (replaced by `others`). No tests are deleted.

## Verification

1. `gofmt -l .`, `go vet ./...`, `go test ./...`, `cd web && npm test && npm run build`,
   `cd apps/menubar && swift test`, `make skills-sync && git diff --exit-code internal/install/skills`.
2. **Migration:**
   - apply 0022 to a copy of the live DB;
   - `PRAGMA table_info(item_merges)` lists every column;
   - row counts elsewhere are unchanged;
   - open accept rows no longer have `$.question`; an already-acked live orchestrator keeps the
     old prompt until its next session (locked decision 11), and the board answers it meanwhile;
   - health reports `schema: 22`.
3. **Scenarios** (each a test, with a fake `s.Exec` for `gh`/`git`):
   - **Prompt:**
     - an epic with one GitHub repo gives header `Finish epic`, the single-repo question, 3
       options and 3 descriptions;
     - a bug gives `Finish fix` and a chore gives `Finish chore`;
     - two repos, one local, give PR labels plus the local note, and the multi-repo question;
     - all-local gives 2 options, `Merge into main locally`;
     - the replay after freeze returns the same options and descriptions.
   - **Chat block:** the header, the integrated summary and the per-repo lines are exact; the
     relay carries `chat_block`; `SummaryGate` returns empty for an accept ref.
   - **native_answer:**
     - `auto_merge` on "Create PR, auto-merge when checks pass" → approved, `$.merge = "auto"`,
       payload `merge:"auto"`, root stays `in_review`;
     - `approve` on an accept row → refused with the exact copy;
     - `auto_merge` when the answer text is "Request changes" → mismatch refusal;
     - `merge_locally` on a 3-option prompt → refused;
     - `request_changes` → changes_requested → InProgress.
   - **Board approve:**
     - `merge:"manual"` → approved, `in_review`;
     - no merge → 400 with the exact copy;
     - `merge:"auto"` on an all-local item → 400 with the local copy;
     - a stale binding → 409 (existing).
   - **Finishing:**
     - happy PR (gh OPEN, head/base match, auto armed) → an `open` row with checks set, item
       stays `in_review`;
     - gh reports MERGED → a `merged` row → Done → `OnRootDone` runs and `item.merged` is raised;
     - local: both `merge-base` calls exit 0 → a `merged` row → Done;
     - each refusal row in the error table, including gh not found (`exec: "gh": executable file
       not found`) and gh exit 1 with `gh auth login` on stderr;
     - a second finishing → refused;
     - finishing before approval → refused;
     - a worker sends finishing → refused.
   - **Watcher:**
     - OPEN passing → failing: one `pr_checks_failed` relay and one notification;
     - a second failing tick: nothing new;
     - failing → passing → failing: a second relay;
     - MERGED on the last open repo → Done;
     - MERGED on one of two → still `in_review`, and `MergeProgressFor` = 1/2;
     - CLOSED → row `closed`, a `pr_closed` relay, root → InProgress, and no new request until a
       new `integrated` checkpoint, which re-asks;
     - gh error → logged, row unchanged, retried next tick;
     - a root not in_review is skipped.
   - **rollupChecks:** empty → `""`; one FAILURE CheckRun → `failing` with its name; a StatusContext
     ERROR → failing with its `context`; one IN_PROGRESS → `pending`; all SUCCESS → `passing`.
   - **checkRoot:** a daemon Done while approved but unmerged → denied with `Finish this epic…`.
   - **Pre-0022 approval** (`$.merge` absent) on an in_review root → Done, as today.
   - **No live orchestrator:**
     - a board approve with `merge:"auto"` → approved, no message enqueued, Details shows the
       waiting line;
     - start an orchestrator → `resurfaceOpenRequests` binds the row and it receives
       `approval_result` with `merge:"auto"`;
     - a second session start after `finishing` rows exist enqueues nothing.
   - **HTTP:**
     - item detail has `merges` for a root with rows;
     - an orchestrator agent node has `merge: {merged, total}`;
     - an accept request has `finish_local` when all-local.
   - **Notify:** `Render("request.accept_epic", {KEY:"EPIC-14"})` has title
     `Finish EPIC-14: create PR?`; a title with a missing placeholder errors like a body.
   - **Web:**
     - the Review footer shows three buttons and sends `merge:"auto"`/`"manual"`;
     - `finish_local` shows `Merge locally` and sends `merge:"local"`;
     - `MergeList` renders the checks, auto-merge, fixing and local rows and the `↗` link.
   - **Menubar:** the subtitle reads `Orchestrator · Awaiting merge · 1/2 merged · Idle`; Needs you
     line 3 is `Finish EPIC-14`.
4. **Live, after deploy:**
   - check that `gh auth status` succeeds in the daemon's launchd environment (`launchctl` env, not
     your shell);
   - run a small chore on a GitHub repo, pick option 1 and confirm the PR opens with auto-merge on;
   - confirm the Details block and the menubar subtitle, then Done after merge, with the worktree
     removed (not "Worktree kept");
   - repeat with option 2 and merge by hand;
   - run a chore on a repo with no remote and pick `Merge into main locally`.

## Explicitly out of scope

- PR reviewers, labels, assignees and draft PRs.
- Running or re-running checks ourselves, or waiting on required reviews.
- GitLab, Bitbucket or any non-GitHub remote as a PR target (they are treated as local merges).
- Deleting remote branches after merge.
- Watching a reopened PR after it was closed.
- Gate-enforcing the finish `chat_block`.
- Changing `SummaryGate`, the todos list or `OnRootDone` cleanup.

## Resolved while writing

1. **Merge choice storage:** the design put `'auto_merge'|'manual_merge'` in `response_text`, but
   `resolve()` writes the user's comment there. The choice goes in `binding_json.$.merge` and in
   `approval_result.merge` instead.
2. **No `finishing` value in SQL:** `checkpoints.kind` has a CHECK without it
   (`0001_init.sql:193`), and rebuilding the table is heavy. `kind:"finishing"` exists only in the
   API. It is dispatched to `writeFinishing` and stored as a `progress` row, and the
   `item_merges` rows are the record.
3. **`item_merges.repo` is the repo name** (what `GitRef.repo` carries), not a catalog path.
   `repo_id` (FK `repos`) is added because `repos.name` isn't unique. `integrated_checkpoint` is
   added so a new integration round never counts old rows.
4. **All-local items:** options 1 and 2 would both read "Merge into <base> locally", so they
   collapse into one option (decision `merge_locally`, merge `local`). Mixed items keep the PR
   options with a per-repo local note. A multi-repo question and `<base>` are defined.
5. **Native answer plumbing:** the decision enum grows by three values. Evidence matching compares
   against all other option labels (`otherApprovalLabel` is removed). Options and descriptions are
   frozen with the question, since the replay used to hardcode two options.
   `NativePromptNextStep` takes the decision list.
6. **Option descriptions** are a parallel `Descriptions []string`, because `Options []string` is
   used throughout.
7. **The finish chat block** supersedes approval-chat-block locked decision 2 for accept kinds. It
   is not gate-enforced: `SummaryGate` stays section/plan/report only, since its summary is the
   integrated checkpoint's, not `req.Prompt`.
8. **Pre-deploy open accept rows** are unfrozen by 0022 so they re-freeze with the new options.
9. **The `checkRoot` Done gate** also requires the merges (`finishedCurrent`); otherwise a daemon
   Done could bypass the rule. Its denial copy changes from "Accept…" to "Finish…".
10. **`notify.Render` expands `Title`**, because the approved title `Finish {KEY}: create PR?`
    has a placeholder and titles were static. The accept notification bodies are new copy: the
    design gave titles only.
11. **"not pushed" in the question** is guaranteed by a skill rule (never push before the finish
    answer), not checked by the daemon.
12. **The web needs `finish_local`** on `RequestWire` to know the all-local footer. The CLI gains
    `--merge`, because Approve now requires it for accept kinds.
13. **`gh pr view` fields:** one field list serves both verification and the watcher, which adds
    `number,url,mergeCommit,statusCheckRollup` to the design's list. The `checks` derivation from
    CheckRun/StatusContext entries is specified.
14. **The skill must push with `-u`**, because `worktree.mergedOrPushed` would otherwise keep
    every squash-merged worktree.
15. **A PR row inserted when gh already reports MERGED** is stored `merged` at once, which also
    satisfies "auto-merge armed" for option 1.
16. **Approval with no routed orchestrator:** `resolve()` skips `approval_result` when `agent_id` is
    empty. That was harmless when approval meant Done, but now it would strand the item in review.
    `resurfaceOpenRequests` delivers it on the next session start (locked decision 12), and Details
    shows a waiting line.
17. **`FinishApprovalTx` is exported:** `approvedCurrent`/`rootState` are unexported in `items`,
    and runtime needs the approved row's id, agent, merge and checkpoint.
18. **Notification Placeholders tests scan titles too.** `notifications.kind` has no CHECK
    (`0001_init.sql:263`), so the new kinds need no migration.

## Agent name follows the agent-authored title

Added by the user on 2026-09-29. A spike orchestrator started with no Name gets a daemon-generated
agent name, `placeholderAgentName(placeholderTitle(request))`, for example `can-you-please-add`.
When its first `accepted` checkpoint names the item (`applyPendingTitle`), the daemon also
renames the agent. The rename happens in the same tx and makes no LLM call.

- **Only generated names.** `items.title_pending = 1` is set only by `StartSpike` when the typed
  Name is empty, and no agent-rename path exists, so a successful `applyPendingTitle` proves the
  name was generated. A typed name is never renamed.
- **New name:** `ids.KebabMax(title, 24)`, with no `-orchestrator` suffix. This matches a typed-Name
  spike, which uses the kebab of the Name. It is made unique with `ids.Unique` against every other
  agent's name, so a collision gets `-2`, `-3`, and so on. An empty kebab, or one equal to the
  current name, means no rename.
- **Kept in step with the name:**
  - `agents.name` changes in the checkpoint tx, which also emits `agent.changed` with the new name.
  - After commit, the daemon runs `tmux rename-session -t =<old> <new>` on the live session. On
    success, a short tx sets that session's `sessions.tmux_name` to `<new>`. The reconciler's
    single-tick dead-pane grace covers the gap between the two steps.
  - If the rename fails, the daemon logs it and leaves `tmux_name` as it was. The agent keeps
    running, and only terminal attach by the new name fails until its next session. Menubar
    attaches with `attach -t =<agent name>`, and `Store.Terminal` returns `a.Name`.
- **Needs no change (verified):**
  - messages, `parent_agent_id` and notifications are keyed by agent id. A child addresses its
    parent as `"parent"`, and notifications join `agents` for the name.
  - Worktree paths and branches come from the branch, not the agent name.
  - The reconciler's window title reads `sessions.tmux_name` and the current `agents.name`.
  - Historical message payloads keep the old name.
  - Claude's `-n <name>` display label keeps the old name until the next session.
- **Out of scope:** renaming a typed name, renaming non-orchestrator agents, and a user-facing
  rename command.
