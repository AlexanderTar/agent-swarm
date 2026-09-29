# Finish With PR Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A top-level item (epic, bug, chore) only reaches Done after the user picks a finish option in a native question and every integrated repo is merged, through a PR the daemon verifies and watches or through a local merge.

**Architecture:** A new `item_merges` table records one row per integrated repo. `reconcileRoot` holds an approved root in InReview until every row is `merged`. The finish question replaces the accept question and stores the user's choice in `binding_json.$.merge`. The orchestrator pushes and opens PRs, then reports a `finishing` checkpoint. The daemon verifies that report with `gh`/`git` outside any tx, and a 60 s watcher polls open PRs.

**Tech Stack:** Go (SQLite via modernc, `execx.Runner` for `gh`/`git`), React + Vitest (web), SwiftUI + XCTest (menubar).

**Spec:** `docs/specs/2026-09-29-finish-with-pr.md` (cited below as `spec:<lines>`). Every executor reads the cited ranges. The plan does not repeat the types, copy or SQL printed there.

## Global Constraints

- Work only in `/Users/alexandertar/GitHub/agent-swarm-finish-pr` (branch `feat/finish-with-pr`). Stage explicit paths only. Never run `git add -A` or `git commit --amend`.
- End every commit message with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01Pq4pkqoE1yUHDeGEJkxmfV
  ```
- **Tests are updated, never deleted.** Every test pinned to the old contract is rewritten to the new one: "Accept epic/fix/chore" headers, the two-option replay, `approve` on accept rows, `approvedCurrent` → Done, and the two-label `matchDecisionEvidence`.
- `web/dist` is missing in this worktree, and `web/embed.go` embeds it. Run `make web-build` once before the first `go vet`/`go test`.
- Go gate, run at the end of each Go task: `gofmt -l .` (must print nothing), `go vet ./...`, `go test ./...`.
- Web gate: `cd web && pnpm test && pnpm run typecheck`. Menubar gate: `cd apps/menubar && swift test`.
- Skills gate: `make skills-sync && git diff --exit-code internal/install/skills`.
- `gh`/`git` always go through `s.Exec` (nil means `execx.Run`) and never run inside a tx (spec locked decision 9, `spec:110-112`). Tests fake them with `execx.Fake{Responses: map[string]execx.Result{"<argv joined by spaces>": {Out:..., Err:...}}}`.
- Every copy string is taken verbatim from `spec:563-664`. Tests assert those exact strings.
- Out of scope: `spec:831-839`.

## Plan-level assumptions (the spec leaves these open)

1. **An integrated repo with no `repos` row** (`finishReposTx` finds nothing). In the prompt it counts as a local repo with `Base = ""`. Repos with an empty `Base` are ignored when `<base>` is computed. If no repo has a base, `<base>` renders as `the default branch`. The prompt path never returns an error for an unknown repo, because it runs inside reconcile's tx. `writeFinishing` refuses such a repo with `Couldn't find <repo> in Swarm's repository catalog; register it with swarm_repo_register, then send finishing again.`, because `item_merges.repo_id` is NOT NULL.
2. **Resurface guard (locked decision 12).** `resurfaceOpenRequests` binds and delivers the approved finish row only when `COALESCE(agent_id,'') = ''`. Once bound, a later session start does not re-enqueue: the message already sits in that agent's inbox.
3. **Rule placement differs from the brief.** Task 1 adds the two *new* notification rows (`item.merged`, `pr.checks_failed`). `fakeNotifier` fatals on an unknown kind, and Task 1 raises `item.merged`. Their titles have no placeholders, so the current `Render` handles them. Task 2 owns the two *changed* accept rows, the `Render` title expansion and the three scanner tests.
4. **`PromptDecisions` is a new exported helper** from Task 2. `mcpserver` needs a request's decision list, and `finishDecisions` is unexported. It gates on request kind, not option count.
5. **`FinishApprovalTx` uses the same match as `approvedCurrent`:** `integrated_checkpoint = newest` AND `item_revision = it.Revision` AND `state = 'approved'`. The watcher reads it before it calls `ReconcileTx`, because moving the root to InProgress bumps the revision.
6. **Auto-merge refusal `<method>`.** Only on this refusal, the daemon runs `gh api repos/<owner>/<name>`, parses `allow_squash_merge` / `allow_merge_commit` / `allow_rebase_merge` and picks the method as in locked decision 2. If that call fails, it falls back to `squash`.
7. **`gh` error text.** `execx.Run` errors read `gh: exit status N: <stderr>`. `ghErrLine(err)` takes the text after the first `exit status N: ` and keeps its first line. When there is no such prefix, it uses the first line of `err.Error()`. `gh` is missing when `errors.Is(err, exec.ErrNotFound)`.

## File map

| Task | Files |
|---|---|
| 1 | `internal/db/schema/0022_item_merges.sql` (new), `internal/db/db.go`, `internal/db/db_test.go`, `internal/db/schema_0022_item_merges_test.go` (new), `internal/runtime/merges.go` + `merges_test.go` (new), `internal/runtime/model.go`, `internal/runtime/checkpoint.go`, `internal/items/transition.go` + `transition_test.go`, `internal/notifyrules/notifyrules.go` (2 new rows) |
| 2 | `internal/runtime/native.go`, `internal/runtime/requests.go`, `internal/mcpserver/tools.go` (one call site only), `internal/notifyrules/notifyrules.go` (2 changed rows), `internal/notify/notify.go`, tests: `native_test.go`, `native_answer_test.go`, `approval_lane_test.go`, `requests_test.go`, `finish_question_test.go` (new), `notify_test.go`, `runtime/agents_test.go`, `mcpserver/helpers_test.go` |
| 3 | `internal/runtime/todos.go` + `todos_test.go` (progress-list wording), `internal/runtime/merges.go` (+ watcher), `merges_watch_test.go` (new), `cmd/swarm/daemon.go`, `internal/mcpserver/tools.go` + test, `internal/httpapi/{runtime,requests,items}.go` + tests, `cmd/swarm/runtime_cmds.go` + test, `skills/swarm-orchestrator/SKILL.md`, `internal/install/skills/swarm-orchestrator/SKILL.md` |
| 4 | `web/src/{types.ts,copy.ts,logic/review.ts,logic/requestTitle.ts,panels/Review.tsx,panels/Details.tsx,components/MergeList.tsx (new),mock/fixtures.ts}` + tests, `apps/menubar/Sources/SwarmBarKit/{Wire,AgentActions,Copy}.swift` + tests |
| 5 | `internal/runtime/checkpoint.go` (+ rename helper), `internal/runtime/model.go` (`Tmux.RenameSession`), `internal/spawn/tmux.go`, Tmux fakes in `runtime/agents_test.go`, `httpapi/helpers_test.go`, `mcpserver/helpers_test.go`, `runtime/checkpoint_test.go` |

Order: 1 → 2 → (3 ∥ 4) → 5 (5 after 3; 5 may run alongside 4). Task 4 depends only on the wire shapes in `spec:456-492`.

---

### Task 1: Merge state machine (migration, `item_merges`, finishing checkpoint, status rule)

**Files:**
- Create: `internal/db/schema/0022_item_merges.sql`, `internal/db/schema_0022_item_merges_test.go`, `internal/runtime/merges.go`, `internal/runtime/merges_test.go`
- Modify: `internal/db/db.go:17` (`SchemaVersion = 22`), `internal/db/db_test.go:156-170` (drop the new table in the v1 replay), `internal/runtime/model.go:86-93`, `internal/runtime/checkpoint.go:73-103,1133`, `internal/items/transition.go:354-367,511-520,626-690`, `internal/items/transition_test.go`, `internal/notifyrules/notifyrules.go:54-55` (append 2 rows)

**Interfaces:**
- Consumes: existing `items.Store.ReconcileTx(ctx, tx, key string) error`, `(*runtime.Store).tx`, `IdemTx`, `s.notify`, `s.Events.Append(ctx, tx, events.ItemChanged, map[string]string{"key":..,"root_key":..})`, `execx.Runner`, `txQuerier` (`requests.go:149`, QueryRowContext only).
- Produces (later tasks use these exact names):
  - `items.FinishApproval{RequestID, AgentID, Merge, CheckpointID string; Git json.RawMessage}`
  - `func (s *items.Store) FinishApprovalTx(ctx context.Context, q querier, rootID string) (FinishApproval, bool, error)`. `querier` is satisfied by `*sql.Tx` and `*db.DB`.
  - `runtime.FinishPR`, `runtime.FinishMerged`, `runtime.ItemMerge`, `runtime.MergeProgress` (`spec:176-203`)
  - `type finishRepo` (`spec:205-210`), `func isGitHubRemote(url string) bool`
  - `func (s *Store) finishReposTx(ctx context.Context, q txQuerier, rootItemID string, refs []GitRef) ([]finishRepo, error)`. It returns one entry per distinct `Ref.Repo`, in first-seen order. An unknown repo has `RepoID == ""` (assumption 1).
  - `type ghPR`, `const prFields`, `func (s *Store) ghPRView(ctx context.Context, url string) (ghPR, error)`, `func rollupChecks(p ghPR) (string, []string)`, `func ghErrLine(err error) string`
  - `func (s *Store) Merges(ctx context.Context, rootItemID string) ([]ItemMerge, error)`
  - `func (s *Store) MergeProgressFor(ctx context.Context, rootItemID string) (*MergeProgress, error)`
  - `runtime.Finishing CheckpointKind = "finishing"`, `CheckpointInput.PRs []FinishPR`, `CheckpointInput.Merged []FinishMerged`

- [ ] **Step 1: Migration test (failing).** Create `internal/db/schema_0022_item_merges_test.go`:

```go
package db

import "testing"

func TestItemMergesMigrationCreatesTableAndUnfreezesOpenAccepts(t *testing.T) {
	raw := openFixtureAtVersion(t, 21)
	for _, q := range []string{
		`INSERT INTO items (id, key, type, root_id, title, status, created_at, updated_at)
			VALUES ('itm_1', 'EPIC-1', 'epic', 'itm_1', 'Epic', 'in_review', 1, 1)`,
		`INSERT INTO requests (id, kind, item_id, prompt, state, binding_json, created_at) VALUES
			('req_open', 'accept_epic', 'itm_1', 'p', 'open', '{"item_revision":1,"question":"Q","header":"H"}', 1),
			('req_done', 'accept_epic', 'itm_1', 'p', 'approved', '{"item_revision":1,"question":"Q","header":"H"}', 1)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	before := tableRowCount(t, raw, "requests")
	continueMigratingTo(t, raw, 21, 22)
	for _, col := range []string{"id", "item_id", "integrated_checkpoint", "repo", "repo_id", "kind", "url", "number",
		"base", "head", "auto_merge", "state", "checks", "merged_sha", "checked_at", "created_at"} {
		var n int
		raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('item_merges') WHERE name = ?`, col).Scan(&n)
		if n != 1 {
			t.Errorf("item_merges.%s missing", col)
		}
	}
	var q, h any
	raw.QueryRow(`SELECT json_extract(binding_json,'$.question'), json_extract(binding_json,'$.header') FROM requests WHERE id='req_open'`).Scan(&q, &h)
	if q != nil || h != nil {
		t.Errorf("open accept still frozen: %v %v", q, h)
	}
	raw.QueryRow(`SELECT json_extract(binding_json,'$.question') FROM requests WHERE id='req_done'`).Scan(&q)
	if q != "Q" {
		t.Errorf("approved row touched: %v", q)
	}
	if after := tableRowCount(t, raw, "requests"); after != before {
		t.Errorf("requests rows = %d, want %d", after, before)
	}
}
```

Run `go test ./internal/db -run 'ItemMerges' -v`. Expected FAIL: the migration file for version 22 does not exist.

- [ ] **Step 2: Migration.** Write `0022_item_merges.sql` verbatim from `spec:135-162`. Set `SchemaVersion = 22`. In `TestExistingDatabaseGainsColumnsAddedByLaterMigrations`, add the following block after the 0021 drops (`db_test.go:~163`):

```go
	// 0022_item_merges.sql creates item_merges with plain CREATE TABLE.
	if _, err := raw.Exec(`DROP TABLE item_merges`); err != nil {
		t.Fatal(err)
	}
```

Run `go test ./internal/db/... ./cmd/swarm/ -run 'Migration|Existing|Status'`. Expected PASS.

- [ ] **Step 3: items tests (failing).** Add to `internal/items/transition_test.go`, reusing that file's existing root/accept fixture helpers (grep `accept_epic` there for the pattern that builds an epic with done children, an `integrated` checkpoint and an approved accept row). Write these table cases on one in_review epic whose integrated `git_json` names repos `a` and `b`:
  - approved, `$.merge` absent → `Reconcile` → **Done** (pre-0022 path, unchanged)
  - approved, `$.merge = "auto"`, no `item_merges` rows → stays **in_review**
  - approved `auto`, rows `a: merged` and `b: open` → stays **in_review**
  - approved `auto`, rows `a` and `b` both merged → **Done**
  - approved `auto`, row `a: closed` → **in_progress**, and no new `accept_epic` row is inserted (the count of rows with that `integrated_checkpoint` stays 1)
  - rows for an *older* integrated checkpoint do not count
  - `checkRoot`: a daemon `Transition(key, Done, items.Daemon())` on approved `auto` with nothing merged returns a denial with message `Finish this epic to mark it Done.`. Also cover the chore and fix copy.
  - `FinishApprovalTx` returns `{RequestID, AgentID, Merge:"auto", CheckpointID, Git}` with `ok = true`, and returns `ok = false` when the revision has moved.

  Insert `item_merges` rows with raw SQL (`repo_id` needs a `repos` row: `INSERT INTO repos (id, name, path, default_branch, source, created_at, updated_at) VALUES ('repo_a','a','/tmp/a','main','manual',1,1)`). Run `go test ./internal/items -run 'Finish|Merge' -v`. Expected FAIL: `FinishApprovalTx` is undefined.

- [ ] **Step 4: items implementation** (`spec:347-373`, locked decision 6 `spec:94-103`):

```go
type FinishApproval struct {
	RequestID, AgentID, Merge, CheckpointID string
	Git                                     json.RawMessage
}

func (s *Store) FinishApprovalTx(ctx context.Context, q querier, rootID string) (fa FinishApproval, ok bool, err error) {
	it, err := s.getByID(ctx, q, rootID) // use the existing by-id getter; adapt if it takes *sql.Tx
	if err != nil {
		return fa, false, err
	}
	st, err := s.rootState(ctx, q, it)
	if err != nil || st.ckpID == "" {
		return fa, false, err
	}
	err = q.QueryRowContext(ctx, `SELECT id, COALESCE(agent_id,''), COALESCE(json_extract(binding_json,'$.merge'),'')
		FROM requests WHERE item_id = ? AND state = 'approved' AND kind IN ('accept_epic','accept_fix')
		AND json_extract(binding_json,'$.integrated_checkpoint') = ? AND json_extract(binding_json,'$.item_revision') = ?
		ORDER BY responded_at DESC LIMIT 1`, it.ID, st.ckpID, it.Revision).Scan(&fa.RequestID, &fa.AgentID, &fa.Merge)
	if errors.Is(err, sql.ErrNoRows) {
		return fa, false, nil
	}
	fa.CheckpointID, fa.Git = st.ckpID, json.RawMessage(st.ckpGit)
	return fa, err == nil, err
}
```

  - `approvedCurrent(ctx, q, it) (bool, string, error)` is implemented through the same query, with `it` in hand. Split out an internal `finishApproval(ctx, q, it, st)` used by both, so there is no double lookup.
  - `mergeState(ctx, q, it, st)`: unmarshal `st.ckpGit` into `[]struct{ Repo string \`json:"repo"\` }` and collect the distinct repos. `total = len(distinct)`. Then `SELECT repo, state FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?`. `merged` counts rows with state `merged` and repo in the set. `closed` is true when any row is `closed`.
  - `finishedCurrent` = `approved && (merge == "" || (merged == total && total > 0 && !closed))`.
  - `checkRoot` uses `finishedCurrent`. Its three denial strings become `Finish this epic to mark it Done.` / `Finish this chore to mark it Done.` / `Finish this fix to mark it Done.`.
  - `reconcileRoot` step 2 becomes:

```go
	if it.Status == InReview {
		approved, merge, err := s.approvedCurrent(ctx, tx, it)
		if err != nil {
			return err
		}
		if approved && st.finished {
			if merge == "" {
				return s.setStatus(ctx, tx, &it, Done)
			}
			merged, total, closed, err := s.mergeState(ctx, tx, it, st)
			if err != nil {
				return err
			}
			switch {
			case closed:
				if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
					return err
				}
				// fall through to step 4: `seen` is true for this checkpoint, so no new request
			case merged == total && total > 0:
				return s.setStatus(ctx, tx, &it, Done)
			default:
				return nil
			}
		} else if openCurrent {
			return nil
		} else if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
			return err
		}
	}
```

  Rewrite, never delete, any existing `transition_test.go` / `hooks_test.go` / `runtime/reconcile_test.go` assertion that pins the `Accept this … to mark it Done.` copy. Run `go test ./internal/items/...`. Expected PASS.

- [ ] **Step 5: runtime tests (failing).** Create `internal/runtime/merges_test.go` with this fixture:

```go
// finishFixture: a chore with one integrated repo "proj" (catalog row with remoteURL, default main),
// its orchestrator session, and the accept_fix row approved with $.merge = merge ("" leaves it open).
func finishFixture(t *testing.T, remoteURL, merge string) (s *Store, orch Agent, ses, key, reqID string) {
	t.Helper()
	s, _, _ = newStore(t)
	s.Items.RootDone = s.OnRootDone
	ctx := context.Background()
	mustExec(t, s.DB, `INSERT INTO repos (id, name, path, remote_url, remote_owner, default_branch, source, created_at, updated_at)
		VALUES ('repo_proj', 'proj', '/tmp/proj', ?, 'o', 'main', 'manual', 1, 1)`, nullIfEmpty(remoteURL))
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses = mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "swarm/chore-1", SHA: "3f9c2ab0000"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = 'accept_fix' AND state = 'open'`, it.ID).Scan(&reqID); err != nil {
		t.Fatal(err)
	}
	if merge != "" {
		mustExec(t, s.DB, `UPDATE requests SET state = 'approved', agent_id = ?, responded_at = 2,
			binding_json = json_set(binding_json, '$.merge', ?) WHERE id = ?`, orch.ID, merge, reqID)
	}
	return
}
```

  (`nullIfEmpty` returns `nil` for `""`; add it only if `mustExec` doesn't already accept `any`.) Write these tests, using the `gh` JSON constants below.
  - `TestRollupChecks`: the five cases in `spec:800-801`. The failing names are `["test (ubuntu)"]` for a CheckRun, and the `context` for a StatusContext ERROR.
  - `TestFinishingHappyPROpenRowStaysInReview`: remote `https://github.com/o/proj.git`, merge `auto`. The fake returns `ghOpenArmed` for `gh pr view https://github.com/o/proj/pull/412 --json ` + `prFields`. Result: one row `{kind:pr, state:open, checks:passing, auto_merge:1, number:412, base:main, head:swarm/chore-1}` and the item stays `in_review`.
  - `TestFinishingPRAlreadyMergedIsDone`: `ghMerged` → the row is `merged` with `merged_sha = "abc123"`, the item is `done`, and `fakeNotifier.kinds()` contains `item.merged`.
  - `TestFinishingLocalMerge`: remote `""`, merge `local`. The fake has `git -C /tmp/proj merge-base --is-ancestor 3f9c2ab0000 d00d` → `{Out:""}` and `git -C /tmp/proj merge-base --is-ancestor d00d main` → `{Out:""}`. Result: a `merged` row and the item is `done`.
  - `TestFinishingRefusals`, table-driven over each row of `spec:598-612` plus assumption 1. Each case asserts the exact error string, and that no `item_merges` row and no new checkpoint was written. Cases:
    - a worker session: `w, _, _ := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleCoder, Kind: Fake, Model: "fake-1", ParentAgentID: orch.ID, Brief: BriefInput{Objective: "x"}})`, then write finishing from `mustSessionID(t, s, w.ID)`;
    - not approved (merge `""`);
    - a repeat (a second call after success);
    - missing repo (empty lists);
    - extra repo `other`;
    - GitHub repo under `merged`;
    - local repo under `prs`;
    - bad url `https://github.com/evil/proj/pull/1`;
    - gh missing: `Err: &exec.Error{Name: "gh", Err: exec.ErrNotFound}`;
    - gh unauthenticated: `Err: errors.New("gh: exit status 1: To get started with GitHub CLI, please run:  gh auth login")` → `Couldn't read https://github.com/o/proj/pull/412 with gh: To get started with GitHub CLI, please run:  gh auth login. Check gh auth status, then send finishing again.`;
    - head mismatch;
    - base mismatch;
    - closed;
    - auto not armed: `ghOpenUnarmed`, plus `gh api repos/o/proj` → `{"allow_squash_merge":false,"allow_merge_commit":true}` → `Auto-merge isn't on for https://github.com/o/proj/pull/412. Run gh pr merge https://github.com/o/proj/pull/412 --auto --merge, then send finishing again.`;
    - local not merged: the first merge-base returns `errors.New("git: exit status 1: ")` → `3f9c2ab is not on proj's main; merge the integrated branch first.`;
    - unknown repo (delete the repos row).
  - `TestPrsMergedRefusedOnOtherKinds`: kind `progress` with `PRs` set → `prs and merged are only for a finishing checkpoint.`
  - `TestMergesAndMergeProgress`: before finishing, `Merges` = nil and `MergeProgressFor` = `&{0 1}`. After an open row, `Merges` has 1 entry with `State:"open"`. With merge `""` (not approved), `MergeProgressFor` = nil.

```go
const ghOpenArmed = `{"state":"OPEN","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":{},"mergeCommit":null,
 "statusCheckRollup":[{"name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]}`
const ghOpenUnarmed = `{"state":"OPEN","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":null,"mergeCommit":null,"statusCheckRollup":[]}`
const ghMerged = `{"state":"MERGED","headRefName":"swarm/chore-1","baseRefName":"main","number":412,
 "url":"https://github.com/o/proj/pull/412","autoMergeRequest":null,"mergeCommit":{"oid":"abc123"},"statusCheckRollup":[]}`
```

  Run `go test ./internal/runtime -run 'Finishing|RollupChecks|MergeProgress|PrsMerged' -v`. Expected FAIL: `Finishing`, `PRs` and `rollupChecks` are undefined.

- [ ] **Step 6: runtime implementation.**
  - `model.go`: add `Finishing` (`spec:262-264`).
  - `checkpoint.go`: add the `PRs` and `Merged` fields. At the top of `WriteCheckpoint`:

```go
	if in.Kind == Finishing {
		return s.writeFinishing(ctx, sessionID, in)
	}
	if len(in.PRs) > 0 || len(in.Merged) > 0 {
		return CheckpointResult{}, &items.Error{Code: items.CodeBadRequest, Message: "prs and merged are only for a finishing checkpoint."}
	}
```

  - `merges.go`: the types and constants come from `spec:176-241`.
  - `finishReposTx` follows `spec:243-247`.
  - `rollupChecks` follows `spec:249-256`. A CheckRun is an entry with `Name != ""` or `Status != ""`. A StatusContext is an entry with `Context != ""`.
  - `ghPRView` runs `runner(ctx, "gh", "pr", "view", url, "--json", prFields)` and unmarshals the output.
  - `writeFinishing` follows `spec:375-410` step by step:
    - Read pass on `s.DB`: `sessionAndAgent` works on a `*sql.Tx`, so either open a short read-only `s.tx` for step 1, or query `sessions`/`agents` directly.
    - Verification runs with no tx open.
    - Write via `IdemTx(ctx, s, sessionID, in.RequestID, "swarm_checkpoint", &out, fn)`. It re-checks `FinishApprovalTx` and the no-rows condition, inserts the rows (`ids.New("mrg")`, `created_at = db.Millis(s.Now())`), inserts the `progress` checkpoint row with the same columns as `checkpoint.go:1417` (`git_json` = the integrated refs from `fa.Git`), appends `ItemChanged`, runs `s.Items.ReconcileTx(ctx, tx, key)`, re-reads the item, and when it is `Done` calls `s.notify(ctx, tx, NotifyInput{Kind: "item.merged", ItemKey: key, Args: map[string]string{"KEY": key}})`.
    - The URL regex is `^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)$`. `number` comes from `ghPR.Number`.
    - Every refusal is `&items.Error{Code: items.CodeBadRequest, Message: ...}`, except "already recorded", which is `CodeConflict`.
  - `Merges`: `SELECT … FROM item_merges WHERE item_id = ? AND integrated_checkpoint = (newest integrated checkpoint id) ORDER BY created_at, repo`. Returns nil when there are no rows.
  - `MergeProgressFor`: nil unless the root is `in_review` and `FinishApprovalTx` is ok with a non-empty `Merge`. `Total` is the number of distinct repos in `fa.Git`. `Merged` counts `merged` rows for `fa.CheckpointID`.
  - `notifyrules.go`: append these two rows verbatim from `spec:621-622`:

```go
	"pr.checks_failed": {"attention", "PR checks failed", "{KEY}: {repo} #{N} — {checks}", "swarm.agent"},
	"item.merged":      {"info", "Merged", "{KEY}: all PRs merged — done.", "swarm.info"},
```

  Run the Step 5 command. Expected PASS.

- [ ] **Step 7: Gate and commit.**

```bash
make web-build && gofmt -l . && go vet ./... && go test ./...
git add internal/db/schema/0022_item_merges.sql internal/db/db.go internal/db/db_test.go internal/db/schema_0022_item_merges_test.go \
  internal/runtime/merges.go internal/runtime/merges_test.go internal/runtime/model.go internal/runtime/checkpoint.go \
  internal/items/transition.go internal/items/transition_test.go internal/notifyrules/notifyrules.go
# plus any existing test file you updated (list it explicitly)
git commit -m "feat(runtime): item_merges and finishing checkpoint hold a root in review until merged"
```

---

### Task 2: Finish question (prompt, freeze/replay, native answer, board approve, notifications)

**Files:**
- Modify: `internal/runtime/native.go:29-42,213-303,315-414,416-427,430-516,794-958,1027-1258`, `internal/runtime/requests.go:96-147,374-436,501-555,616-688,1482-1489`, `internal/mcpserver/tools.go:246` (call site only), `internal/notifyrules/notifyrules.go:54-55`, `internal/notify/notify.go:41-63`
- Test: `internal/runtime/finish_question_test.go` (new). Update in place: `native_test.go`, `native_answer_test.go`, `approval_lane_test.go` (`TestNativePromptForAcceptKinds`, `TestAcceptRowRoutesToLiveRootOrchestrator`, `TestNativeAnswerApprovesRoutedAcceptRow`, `TestChoreEndToEndAcceptFix`, …), `requests_test.go`, `p32_test.go` if it pins accept text, `internal/notify/notify_test.go:210-224`, `internal/runtime/agents_test.go:224`, `internal/mcpserver/helpers_test.go:139`

**Interfaces:**
- Consumes (Task 1): `finishReposTx`, `finishRepo`, `isGitHubRemote`, `items.FinishApprovalTx`, `item_merges`.
- Produces:
  - `NativePrompt.Descriptions []string` (`spec:278-283`), `ChatBlockInput.ItemKey string`, `ChatBlockInput.Git []GitRef`
  - `func finishDecisions(opts []string) []string`
  - `func PromptDecisions(kind RequestKind, np NativePrompt) []string`: accept kinds → `finishDecisions(np.Options)`; every other kind → `[]string{"approve", "request_changes"}`
  - `func decisionLabels(req Request, np NativePrompt, decision string) (string, []string, error)`
  - `func finishDecisionFor(trimmed string) string`
  - `func matchDecisionEvidence(responseText, label string, others []string, callerComment string) (string, string, error)`
  - `func NativePromptNextStep(ref string, decisions []string) string`
  - `ApproveInput.Merge string`, `RequestWire.FinishLocal bool \`json:"finish_local,omitempty"\``
  - `approval_result` payload for a finish approval: `{"decision":"approved","merge":"auto|manual|local","section_id":"","section_sha256":"",["evidence":…]}`

- [ ] **Step 1: Failing tests.** In `finish_question_test.go`, build prompts through `nativePromptFor` inside `s.tx`. Seed `repos` rows (`name`, `remote_url`, `default_branch='main'`) and pass `Request{Kind, ItemID, Binding: []byte(\`{"item_revision":1,"integrated_checkpoint":"ckp_x","git":[…]}\`)}`. Asserted values (`spec:565-580`, `spec:759-765`):

```go
// epic EPIC-1 "Build it", one GitHub repo agent-swarm at swarm/epic-1 sha 3f9c2ab0…
want := NativePrompt{Header: "Finish epic",
	Question: `Finish EPIC-1 "Build it"? Branch swarm/epic-1 at 3f9c2ab, not pushed.`,
	Options:  []string{"Create PR, auto-merge when checks pass", "Create PR, I'll merge it myself", "Request changes"},
	Descriptions: []string{
		"Push, open a PR into main, merge automatically when checks pass.",
		"Push and open a PR into main; Done when you merge it.",
		"Say what to change; I'll re-integrate and ask again.",
	}}
// mixed: agent-swarm (GitHub) + docs (no remote), both main:
//   Question: `Finish EPIC-1 "Build it"? Not pushed: agent-swarm swarm/epic-1 at 3f9c2ab, docs swarm/epic-1 at 81d0e44.`
//   Descriptions[0] == "Push, open a PR into main, merge automatically when checks pass. docs has no GitHub remote: merged into main locally."
//   Descriptions[1] == "Push and open a PR into main; Done when you merge it. docs has no GitHub remote: merged into main locally."
// all-local: Options {"Merge into main locally", "Request changes"},
//   Descriptions {"Merge the branch into main in your local checkout; Done once it's merged.", "Say what to change; I'll re-integrate and ask again."}
// bug → Header "Finish fix"; chore → Header "Finish chore"
// two repos with bases main and master → "<base>" = "each repo's default branch"
```

  Also write these tests:
  - **Replay:** `storedNativePromptTx` twice returns a `reflect.DeepEqual` prompt, and the binding holds `$.options` and `$.descriptions`.
  - **Chat block:** `ApprovalChatBlock(ChatBlockInput{Kind:"accept_epic", ItemKey:"EPIC-14", Summary:"S", Git: two refs})` == `"### Approval · Finish EPIC-14\n\nS\n\nagent-swarm: swarm/epic-14 at 3f9c2ab\nendurio: swarm/epic-14 at 81d0e44"`. The `request_open` relay for an accept row carries `chat_block`, with the summary read from the integrated checkpoint. `SummaryGate` for an accept ref returns an empty chat block.
  - **`next`:** `NativePromptNextStep("req_A", []string{"auto_merge","manual_merge","request_changes"})` contains `decision:"auto_merge"|"manual_merge"|"request_changes"`. The existing test at `native_test.go:17`, which calls it with the approve pair, still asserts `decision:"approve"|"request_changes"`.
  - **native_answer (`spec:768-774`):** use the `routedAccept` helper extended with a `repos` row for the binding's repo. `openAcceptRow` binds `"git":[]`; add an overload `openAcceptRowGit(t, s, id, kind, key, gitJSON)` and keep the old helper.
    - `auto_merge` on the answer `Create PR, auto-merge when checks pass` → `approved`, `$.merge = "auto"`, and the payload `merge == "auto"`. The root stays `in_review`: seed a real integrated checkpoint as in Task 1's fixture.
    - `approve` on an accept row → `decision for a finish request must be one of: auto_merge, manual_merge, request_changes.`
    - `auto_merge` with the answer `Request changes` → `The user's native answer was "Request changes", not "Create PR, auto-merge when checks pass".`
    - `merge_locally` on a 3-option prompt → the one-of refusal.
    - `request_changes` → `changes_requested` and the root goes to `in_progress`.
    - `auto_merge` on a `msg_` ref or an `approve_plan` → `decision must be approve or request_changes.`
  - **Board Approve (`spec:775-779`):**
    - `Approve(id, {Binding: req.Binding, Merge: "manual", Via: "board"})` → `approved` and `in_review`.
    - Missing merge → `*items.Error{CodeBadRequest}` with `Choose how to finish: merge must be "auto" or "manual".`
    - An all-local item with `auto` → `Choose how to finish: merge must be "local" (no repository has a GitHub remote).`
    - A stale binding → conflict, unchanged.
  - **`FinishLocal`:** `RequestWireByID` returns `FinishLocal == true` only for an all-local accept row.
  - **Resurface (`spec:804-809`, assumption 2):**
    - Approve an agentless accept row with `merge:"auto"`. No `approval_result` message exists yet.
    - `StartOrchestrator` / `resurfaceOpenRequests(ctx, orch, ses, true, time.Time{})` → the row's `agent_id` is set and exactly one `approval_result` has `merge == "auto"`.
    - A second call → still one.
    - After inserting an `item_merges` row for that checkpoint on a fresh agentless approval → zero.
  - **Notify:**
    - `Render("request.accept_epic", {"KEY":"EPIC-14"})` → Title `Finish EPIC-14: create PR?`, Body `EPIC-14: integrated and verified. Pick how to finish.`.
    - A rule whose Title has a placeholder with no arg → error. Add a temporary `Rules["test.title"]` inside the test and remove it with `t.Cleanup`.

  Update in place, never delete: `TestNativePromptForAcceptKinds` (new headers and options), `TestAcceptRowRoutesToLiveRootOrchestrator` (header `Finish epic`, question `Finish EPIC-1 "Build it"? Branch …` or the `git:[]` fallback defined below, `next == NativePromptNextStep("req_accept", PromptDecisions(KindAcceptEpic, np))`), `TestNativeAnswerApprovesRoutedAcceptRow` (answer `Create PR, auto-merge when checks pass`, decision `auto_merge`, payload `merge`), `TestChoreEndToEndAcceptFix` (seed a GitHub `proj` repo row, header `Finish chore`, decision `auto_merge`, the item stays `in_review` after approval), and every `matchDecisionEvidence(x, label, c)` call → `matchDecisionEvidence(x, label, []string{other}, c)`.

  **`git: []` fallback** (the old test binding): the repo list is empty. Treat it as the three-option single-repo shape with question `Finish <KEY> "<title>"? Not pushed.` and `<base>` `the default branch`. This path only occurs in fixtures, and it must not error.

  Run `go test ./internal/runtime ./internal/notify -run 'Finish|Accept|NativeAnswer|NextStep|Render|Resurface' -v`. Expected FAIL: compile errors (`Descriptions`, `Merge` and `PromptDecisions` are undefined).

- [ ] **Step 2: native.go.**
  - `nativePromptFor`, case `KindAcceptEpic, KindAcceptFix`: read `key, title, type`. Unmarshal `req.Binding`'s `git` into `[]GitRef` and call `s.finishReposTx(ctx, tx, rootID, refs)`, with `rootID = req.ItemID` (accept rows sit on the root). Build the header, question, options and descriptions from `spec:565-580`, applying assumption 1 and the `git: []` fallback. Use `sha7 := func(s string) string { if len(s) > 7 { return s[:7] }; return s }` and `capRunes(q, 1000)`.
  - `freezeNativeQuestionTx(ctx, tx, reqID string, np NativePrompt)`: change the signature. Update both callers (`storedNativePromptTx`, and `askApproval`/`askConfirmRepos`, which you find with `grep -n freezeNativeQuestionTx`). When `len(np.Descriptions) > 0`, also `json_set` `$.options` to `json(?)` of the marshalled options and `$.descriptions` likewise. Keep the `$.question IS NULL` guard.
  - `effectiveNativeQuestionTx`: the frozen struct gains `Options []string` and `Descriptions []string`. When frozen, return them if `len(Options) > 0`, else `approveOptions`.
  - `ApprovalChatBlock`: add the case `KindAcceptEpic, KindAcceptFix`. The head is `"### Approval · Finish " + in.ItemKey`. The foot is one `"<repo>: <branch> at <sha7>"` line per `in.Git` entry, joined by `"\n"`.
  - `approvalChatBlockTx`, accept case: read the item key, `SELECT summary FROM checkpoints WHERE id = binding.integrated_checkpoint` into `in.Summary`, and `in.Git` from the binding. If that row is missing (`errors.Is(err, sql.ErrNoRows)`, as with the fixtures' `"ckp_x"`), keep an empty summary and still return the block. Returning an error would fail `relayRequestTx` and every routed-accept test.
  - `finishDecisions`: 3 options → `[auto_merge, manual_merge, request_changes]`; 2 options → `[merge_locally, request_changes]`.
  - `decisionLabels`:
    - Non-accept or `req.ID == ""` (a msg ref): `approve` → `("Approve", ["Request changes"])`; `request_changes` → `("Request changes", ["Approve"])`; anything else → `errors.New("decision must be approve or request_changes.")`.
    - Accept: `ds := finishDecisions(np.Options)`. The index of `decision` in `ds` gives `np.Options[i]` as the label and every other option as `others`. If the decision is not in `ds` → `fmt.Errorf("decision for a finish request must be one of: %s.", strings.Join(ds, ", "))`.
  - `finishDecisionFor`: apply `labelShape` against the three finish labels. `Merge into <x> locally` matches `strings.HasPrefix(fold, "merge into ")`, then the text before any `:` must have the suffix ` locally`.
  - `matchDecisionEvidence`: loop `others` where it used to call `otherApprovalLabel`. Delete `otherApprovalLabel`; `decisionLabel` becomes unused, so delete it as well.
  - `NativePromptNextStep(ref, decisions)`: replace the literal `decision:\"approve\"|\"request_changes\"` with `"decision:" + quoted decisions joined by "|"`, where `quoted` is `strconv.Quote` of each.
  - `NativeAnswerNextStep`: before the Approve/Request-changes checks, `if d := finishDecisionFor(trimmed); d != "" { return fmt.Sprintf("[swarm] Recorded %q for %s. Forward it now: swarm_ask kind:\"native_answer\", ref:%q, decision:%q", trimmed, ref, ref, d) }`. `spec:331-332` gives the exact shape. `trimmed` is the label as the user picked it.
  - `nativeAnswer`:
    - Validate `in.Decision ∈ {approve, request_changes, auto_merge, manual_merge, merge_locally}`; otherwise return `decision must be approve or request_changes.`.
    - Before the evidence tx: for a `req_` ref, load `req` with `RequestByID` and `np` with `effectiveNativeQuestionTx` (inside `s.tx`), then call `decisionLabels(req, np, in.Decision)`. For a `msg_` ref, call `decisionLabels(Request{}, NativePrompt{}, in.Decision)`.
    - Pass `label, others` to `matchDecisionEvidence`.
    - In the approve tail: when `req.Kind` is an accept kind, `m := map[string]string{"auto_merge":"auto","manual_merge":"manual","merge_locally":"local"}[in.Decision]`. Resolve with `approveCheck(in2)`, `bindEvidence`, and an extra after hook `UPDATE requests SET binding_json = json_set(binding_json,'$.merge',?) WHERE id = ?`. The payload adds `"merge": m`.

- [ ] **Step 3: requests.go.**
  - `ApproveInput.Merge`.
  - `Approve`: when `req.Kind` is `accept_epic`/`accept_fix`, validate `in.Merge` inside `check`. Compute `finishLocal` from the binding's git through `finishReposTx`. Local: only `"local"` is allowed. Otherwise: `"auto"` or `"manual"`. Refusals use the exact copy with `CodeBadRequest`. Add the `$.merge` after hook, and add `"merge"` to the payload only for accept kinds. The resolve `check` callback receives `req`, so wrap `approveCheck(in)` in a closure that runs the merge validation first.
  - `relayRequestTx`: `payload["next"] = NativePromptNextStep(req.ID, PromptDecisions(req.Kind, np))`. For accept kinds, set `payload["chat_block"]` from `approvalChatBlockTx`.
  - `RequestWireTx`: for accept kinds, `w.FinishLocal` is true when repos resolve, `len > 0`, and none is GitHub.
  - `resurfaceOpenRequests`, inside the top-level-orchestrator branch after the open-row bind:

```go
	fa, ok, err := s.Items.FinishApprovalTx(ctx, tx, a.RootItemID)
	if err != nil {
		return err
	}
	if ok && fa.Merge != "" && fa.AgentID == "" {
		var rows int
		tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_merges WHERE item_id = ? AND integrated_checkpoint = ?`,
			a.RootItemID, fa.CheckpointID).Scan(&rows)
		if rows == 0 {
			// bind + enqueue the approval_result Approve would have sent (locked decision 12)
			if _, err := tx.ExecContext(ctx, `UPDATE requests SET agent_id = ?, session_id = ? WHERE id = ?`, a.ID, sessionID, fa.RequestID); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"decision": "approved", "merge": fa.Merge, "section_id": "", "section_sha256": ""})
			if _, err := s.enqueue(ctx, tx, Message{Kind: "approval_result", Origin: "user_action", ToAgentID: a.ID,
				RootItemID: a.RootItemID, ItemID: a.RootItemID, RequestID: fa.RequestID, Payload: payload}); err != nil {
				return err
			}
		}
	}
```

  (`items.querier` accepts `*sql.Tx`.)
  - `mcpserver/tools.go:246`: `out["next"] = runtime.NativePromptNextStep(r.ID, runtime.PromptDecisions(r.Kind, *r.NativePrompt))`.

- [ ] **Step 4: Notifications.**
  - Replace the two accept rows with `spec:619-620`: title `Finish {KEY}: create PR?`, body `{KEY}: integrated and verified. Pick how to finish.`.
  - `notify.Render` runs the same `ReplaceAllStringFunc` over `r.Title`, collecting into the same `missing` list.
  - In `notify_test.go:214`, `runtime/agents_test.go:224` and `mcpserver/helpers_test.go:139`, iterate `append(notifyrules.Placeholders(rule.Title), notifyrules.Placeholders(rule.Body)...)`. The notify test also asserts that `r.Title` has no braces.
  - Rewrite any test asserting the old titles `Epic acceptance needed` / `Fix acceptance needed` (grep for them).

- [ ] **Step 5:** Run `go test ./internal/runtime/... ./internal/notify/... ./internal/mcpserver/...`. Expected PASS.

- [ ] **Step 6: Gate and commit.**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/runtime/native.go internal/runtime/requests.go internal/runtime/finish_question_test.go \
  internal/runtime/native_test.go internal/runtime/native_answer_test.go internal/runtime/approval_lane_test.go \
  internal/runtime/requests_test.go internal/runtime/agents_test.go internal/mcpserver/tools.go internal/mcpserver/helpers_test.go \
  internal/notifyrules/notifyrules.go internal/notify/notify.go internal/notify/notify_test.go
# plus any other updated test file, explicitly
git commit -m "feat(runtime): finish question with PR options replaces accept approve"
```

---

### Task 3: PR watcher and surfaces (daemon loop, MCP, HTTP, CLI, skills)

**Files:**
- Modify: `internal/runtime/todos.go:45-50,150-157` (progress-list wording + chore context step), `internal/runtime/todos_test.go`, `internal/runtime/merges.go` (append `WatchMerges`, `WatchMergesLoop`), `cmd/swarm/daemon.go:~364`, `internal/mcpserver/tools.go:104-160,194,212`, `internal/httpapi/runtime.go:50-70,363-368,505-518`, `internal/httpapi/requests.go:89-110`, `internal/httpapi/items.go:283-290`, `cmd/swarm/runtime_cmds.go:501-530`, `skills/swarm-orchestrator/SKILL.md` (lines 20, 54, 63), `internal/install/skills/swarm-orchestrator/SKILL.md` (via `make skills-sync`)
- Test: `internal/runtime/merges_watch_test.go` (new), `internal/mcpserver/tools_test.go` (or the file that asserts `swarm_checkpoint`), `internal/httpapi/{requests,items,runtime}_test.go`, `cmd/swarm/runtime_cmds_test.go`

**Interfaces:**
- Consumes (Task 1): `ghPRView`, `rollupChecks`, `ItemMerge`, `Merges`, `MergeProgressFor`, `items.FinishApprovalTx`, `finishFixture` (merges_test.go), `Finishing`, `CheckpointInput.PRs/Merged`, `FinishPR`, `FinishMerged`. (Task 2): `ApproveInput.Merge`, `PromptDecisions`, `NativePromptNextStep(ref, decisions)`.
- Produces: `func (s *Store) WatchMerges(ctx context.Context) error`, `func (s *Store) WatchMergesLoop(ctx context.Context, every time.Duration)`. Wire shapes: `approveBody.Merge string \`json:"merge"\``, item detail `"merges": ItemMerge[]`, `agentNodeWire.Merge *runtime.MergeProgress \`json:"merge,omitempty"\``.

- [ ] **Step 1: Watcher tests (failing).** In `merges_watch_test.go`, build on `finishFixture(t, "https://github.com/o/proj.git", "auto")` and a successful `finishing` with `ghOpenArmed` (an open row with passing checks). Then swap `s.Exec` to a fresh fake per tick. Cases (`spec:790-799`):
  - `failing` rollup (`[{"name":"test (ubuntu)","status":"COMPLETED","conclusion":"FAILURE"},{"name":"lint","status":"COMPLETED","conclusion":"FAILURE"}]`): exactly one relay whose payload equals `{"event":"pr_checks_failed","item":<key>,"repo":"proj","url":"https://github.com/o/proj/pull/412","number":412,"failing":["test (ubuntu)","lint"]}`, and one `pr.checks_failed` notification. Assert that `Args` hold `KEY`, `repo`, `N="412"` and `checks="test (ubuntu), lint"`.
  - A second failing tick → still one relay. Then passing → failing → a second relay.
  - MERGED → the row is `merged` with `merged_sha`, the root is `done`, and `item.merged` is raised.
  - Two repos (add a second GitHub repo `endurio` to the fixture's integrated checkpoint), MERGED on one → `in_review` and `MergeProgressFor` = `{1, 2}`.
  - CLOSED → the row is `closed`, one `pr_closed` relay goes to `orch.ID` (`{"event":"pr_closed","item":…,"repo":"proj","url":…,"number":412}`), and the root is `in_progress`. No new `accept_fix` row appears. A new `integrated` checkpoint → a new open `accept_fix`.
  - A gh error (`Err: errors.New("gh: exit status 1: boom")`) → the row is unchanged, and `s.Log` captured `merges: gh pr view https://github.com/o/proj/pull/412: …`.
  - A root not in_review: `UPDATE items SET status='in_progress'` → no gh call. The fake has no response, so a call would error, and the test asserts the fake's recorded calls are empty.

  Run `go test ./internal/runtime -run WatchMerges -v`. Expected FAIL: `WatchMerges` is undefined.

- [ ] **Step 2: Implement** `WatchMerges` per `spec:412-437`:
  - Select rows first (id, item_id, root key, repo, url, number, checks) and close the rows cursor.
  - For each row, call `ghPRView` with no tx.
  - Open one `s.tx` per row and re-read `state` and `checks` (skip if no longer `open`).
  - Apply MERGED / CLOSED / OPEN.
    - For CLOSED, read `FinishApprovalTx(ctx, tx, itemID)` *before* `ReconcileTx` (assumption 5).
    - Relays go through `s.enqueueRaw(ctx, tx, Message{Kind: "relay", Origin: "daemon", ToAgentID: fa.AgentID, RootItemID: itemID, ItemID: itemID, Payload: body})`, skipped when `fa.AgentID == ""`.
    - Check the wake class: grep `WakeDue`/`immediate` in `inbox.go`. If relays already wake immediately, set nothing; otherwise set the field that marks `immediate`.
  - `pr.checks_failed` args are `{"KEY":key,"repo":repo,"N":strconv.Itoa(number),"checks":strings.Join(failing,", ")}`.
  - `WatchMergesLoop` is a copy of `ReclaimWorktreesLoop` (`reconcile.go:1585`) with the `merges:` log prefix.
  - Wire it in `daemon.go` next to the reclaim loop: `func(ctx context.Context) { dm.rt.WatchMergesLoop(ctx, time.Minute) }, // PR merge watcher`.

  Run the Step 1 command. Expected PASS.

- [ ] **Step 3: MCP (failing test, then code).**
  - Tests:
    - the `swarm_checkpoint` description equals `spec:441-443`;
    - the schema contains `"prs"` and `"merged"`;
    - calling the handler with `{"kind":"finishing","summary":"s","prs":[{"repo":"proj","url":"u"}]}` reaches `WriteCheckpoint` with `PRs` set. Assert via the refusal text from a non-orchestrator session: `Only the top-level orchestrator can write finishing on its own item.`
    - the `swarm_ask` schema enum lists the 5 decisions.
  - Code:
    - `spec:441-452` verbatim for the description, the schema entries and the `decision` property;
    - the handler struct gains `PRs []runtime.FinishPR \`json:"prs"\`` and `Merged []runtime.FinishMerged \`json:"merged"\``, both passed into `CheckpointInput`.
    - `requestOut` already calls `PromptDecisions` (Task 2), so leave it.

- [ ] **Step 4: HTTP (failing tests, then code).**
  - Tests:
    - `POST /api/requests/{id}/approve` with no `merge` on an accept row → 400 with `Choose how to finish: merge must be "auto" or "manual".`;
    - with `"merge":"manual"` → 200, `state: "approved"`;
    - `GET /api/items/{root}` for a root with an approved finish and no rows → `"merges":[]`. With one row → the `ItemMerge` JSON, key names per `spec:187-198`;
    - an agent node for a top-level orchestrator of that root → `"merge":{"merged":0,"total":1}`;
    - an all-local accept request → `"finish_local":true`.
  - Code:
    - `approveBody.Merge string \`json:"merge"\``, passed as `ApproveInput.Merge`.
    - `items.go`, after `todos`:

```go
		if mp, err := s.RT.MergeProgressFor(ctx, it.ID); err != nil {
			s.writeErr(w, err)
			return
		} else if mp != nil {
			m, err := s.RT.Merges(ctx, it.ID)
			if err != nil {
				s.writeErr(w, err)
				return
			}
			if m == nil {
				m = []runtime.ItemMerge{}
			}
			out["merges"] = m
		}
```

    - `agentNodeOut`, inside the existing `a.Role == RoleOrchestrator && a.ItemID == a.RootItemID` block: `if mp, err := s.RT.MergeProgressFor(ctx, a.ItemID); err == nil { w.Merge = mp }`.

- [ ] **Step 5: CLI (failing test, then code).**
  - Test in `runtime_cmds_test.go`: `run([]string{"approve", "--home", home, "--url", srv.URL, "req_1", "--merge", "auto"}, …)` → the body contains `"merge":"auto"`. With no args → usage `Usage: swarm approve REQ [--merge auto|manual|local]`.
  - Code: `rest, merge, _ := pullValue(rest, "--merge")` after `connect`. Update the usage string. When `merge != ""`, set `body["merge"] = merge`.

- [ ] **Step 6: Skills.** Edit `skills/swarm-orchestrator/SKILL.md` exactly per `spec:642-664`:
  - line 20: swap only the one sentence;
  - line 54: replace that bullet with the six bullets;
  - line 63: replace that bullet.

  Then run `make skills-sync`. Check: `diff skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md` prints nothing. If a test pins skill text (`grep -rn "finishing-a-development-branch" --include='*_test.go' .`), update it.

- [ ] **Step 7: Progress-list wording (failing tests, then code).** Source: spec section "Progress-list wording" (`spec:666-688`).
  - Update `internal/runtime/todos_test.go` in place:
    - `:90`: `Merging and verifying` / `Finishing: PR or merge`;
    - `:157`: `Understanding the request` / `Reviewing the plan and setting up tasks`;
    - add a label assertion for the debug spike's `frame` → `Understanding the problem` in `TestTodosDebugSpikeAndNoListCases`.
  - Rewrite `TestTodosChoreWithZeroTasks`:
    - fresh chore: ids `["context","work","integrate","accept"]`, statuses `[in_progress, pending, pending, pending]`, labels `Gathering context` and `Making the changes`;
    - after `insertCheckpoint(t, s, ses.ID, orch.ID, ch.ID, "progress", nil)` → `[completed, in_progress, pending, pending]`;
    - after `integrated` → `[completed, completed, completed, in_progress]`;
    - after done → `accept` is completed.
  - Add `TestTodosChoreContextCompletesWithATask`: a chore with one non-cancelled task and no checkpoints → ids `["context", <TASK-KEY>, "integrate", "accept"]` with `context` completed and no `work` entry. A chore whose only task is cancelled → `context` in_progress and the `work` entry is back.
  - Run `go test ./internal/runtime -run Todos -v`. Expected FAIL on the labels and the `context` id.
  - Code in `todos.go`:
    - replace the labels in `spikeSteps` and the three fixed entries per the spec table;
    - for `typ == items.Chore`, prepend the context entry:

```go
	if typ == items.Chore {
		var progressed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'progress')`,
			rootID).Scan(&progressed); err != nil {
			return nil, err
		}
		ctxDone := len(out) > 0 || progressed // out = the non-cancelled tasks listed above
		context := Todo{ID: "context", Label: "Gathering context", Status: TodoInProgress}
		if ctxDone {
			context.Status = TodoCompleted
		}
		if len(out) == 0 {
			work := TodoPending
			switch {
			case integrate == TodoCompleted:
				work = TodoCompleted
			case ctxDone:
				work = TodoInProgress
			}
			out = append(out, Todo{ID: "work", Label: "Making the changes", Status: work})
		}
		out = append([]Todo{context}, out...)
	}
```

    This replaces the existing `if typ == items.Chore && len(out) == 0` block. Rename the local `context` if it shadows the imported `context` package; use `ctxTodo`.
  - Skill: in both SKILL.md copies (edit `skills/`, then `make skills-sync`), prefix the chore bullet at line 61 with `Gather context first: read the code, docs and recent history the chore touches, then report a progress checkpoint. `, so the line reads `- Gather context first: … progress checkpoint. Do the work directly. …`.
  - Run `go test ./internal/runtime/... ./internal/install/...`. Expected PASS.

- [ ] **Step 8: Gate and commit.**

```bash
gofmt -l . && go vet ./... && go test ./... && make skills-sync && git diff --exit-code internal/install/skills
git add internal/runtime/todos.go internal/runtime/todos_test.go \
  internal/runtime/merges.go internal/runtime/merges_watch_test.go cmd/swarm/daemon.go \
  internal/mcpserver/tools.go internal/httpapi/runtime.go internal/httpapi/requests.go internal/httpapi/items.go \
  cmd/swarm/runtime_cmds.go cmd/swarm/runtime_cmds_test.go \
  skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md
# plus the exact mcpserver/httpapi test files you touched
git commit -m "feat: PR merge watcher, finishing on MCP/HTTP/CLI, orchestrator finish skill, progress-list wording"
```

---

### Task 4: UI (web Review footer, Awaiting-merge block, menubar)

Runs in parallel with Task 3. It uses only the wire shapes in `spec:456-492`: `Request.finish_local`, `ItemDetail.merges`, the `ApproveBody.merge` request field, and `AgentNode.merge`.

**Files:**
- Modify: `web/src/types.ts:89-96,217-240,378`, `web/src/copy.ts:117-118` (+ new keys in `C`, `T.mergedLocally`), `web/src/logic/review.ts:48-52`, `web/src/panels/Review.tsx:148-160`, `web/src/panels/Details.tsx` (above `{d.todos && <TodoList …/>}`), `web/src/mock/fixtures.ts` (add `merges`/`finish_local` fixtures)
- Create: `web/src/components/MergeList.tsx`, `web/src/components/MergeList.test.tsx`
- Update tests (never delete): `web/src/logic/review.test.ts:35`, `web/src/panels/Review.test.tsx:71-107`, `web/src/panels/Details.test.tsx`, `web/src/copy.test.ts`, `web/src/App.test.tsx`, `web/src/panels/NeedsYou.test.tsx`, `web/src/logic/inbox.test.ts` (every `Accept epic`/`Accept fix` string becomes `Finish epic`/`Finish fix`), `web/src/components/TodoList.test.tsx` (progress-list label)
- Menubar: `apps/menubar/Sources/SwarmBarKit/Wire.swift:79-122`, `AgentActions.swift:263-268`, `Copy.swift:290` (+ `awaitingMerge`). Tests: `Tests/SwarmBarTests/AgentActionsTests.swift:195-206`, `AppModelTests.swift:175,775`, and a Wire decode test (`FixtureTests.swift` or `AppModelTests.swift`, whichever decodes `AgentNode` JSON).

**Interfaces:**
- Consumes: the wire JSON from `spec:456-492`. It uses no Go symbols.
- Produces:
  - `type MergeChoice`, `interface ItemMerge`, `ApproveBody.merge`, `Request.finish_local?: boolean`, `ItemDetail.merges?: ItemMerge[]`
  - `approveBody(r: Request, merge?: MergeChoice): ApproveBody`
  - `export function MergeList({ merges }: { merges: ItemMerge[] })`
  - Swift `MergeProgress`, `AgentNode.merge`, `Copy.awaitingMerge(_:_:)`

- [ ] **Step 1: Failing web tests.**
  - `review.test.ts`:
    - `expect(approveBody(req("req_accept"), "auto")).toEqual({ binding: req("req_accept").binding, merge: "auto" })`;
    - `SCOPE_LABEL.accept_epic === "Finish epic"`.
  - `Review.test.tsx`, rewriting the accept test:
    - the heading is `Finish epic · EPIC-12 › Authentication`;
    - buttons `Create PR + auto-merge`, `Create PR`, `Request changes`;
    - clicking `Create PR + auto-merge` posts a body matching `{ binding: {...}, merge: "auto" }`;
    - clicking `Create PR` on a fresh render posts `merge: "manual"`.
  - A new test with `{ ...req, finish_local: true }`: only `Merge locally` + `Request changes` are shown, and a click posts `merge: "local"`.
  - The stale test clicks `Create PR + auto-merge` instead of `Accept epic`.
  - `accepts a fix…` asserts the `Create PR` button exists.
  - `MergeList.test.tsx` renders:

```ts
const merges: ItemMerge[] = [
  { repo: "agent-swarm", kind: "pr", url: "https://github.com/o/agent-swarm/pull/412", number: 412, base: "main", head: "swarm/epic-14", auto_merge: true, state: "open", checks: "passing" },
  { repo: "endurio", kind: "pr", url: "https://github.com/o/endurio/pull/88", number: 88, base: "main", head: "swarm/epic-14", auto_merge: false, state: "open", checks: "failing" },
  { repo: "docs", kind: "local", base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "", merged_sha: "81d0e44aa" },
  { repo: "web", kind: "pr", url: "https://github.com/o/web/pull/5", number: 5, base: "main", head: "swarm/epic-14", auto_merge: false, state: "merged", checks: "passing" },
];
// expect heading "Awaiting merge"; row texts contain:
//   "agent-swarm", "#412", "✓ passing", "auto-merge on"
//   "endurio", "#88", "✗ failing", "(orchestrator fixing)"
//   "docs", "merged locally 81d0e44"
//   "web", "#5", "merged"
// links: getAllByRole("link") → href of the first = the PR url, target "_blank", rel "noreferrer"
// pending → "… pending"; checks "" on an open PR → "no checks"
```

  - `Details.test.tsx`, overriding `GET /api/items/EPIC-12` the same way the Progress test does:
    - `status: "in_review", merges: [one row], todos: [the Progress test's todos array]` → the `Awaiting merge` heading appears *before* the Progress heading (`compareDocumentPosition`);
    - `merges: []` → the text `Awaiting merge — waiting for the orchestrator to open PRs.`;
    - no `merges` key → neither is shown;
    - `status: "done"` with merges → neither is shown.

  Run `cd web && pnpm test`. Expected FAIL: the `Finish epic` text is missing and `MergeList` is missing.

- [ ] **Step 2: Web implementation.**
  - `types.ts`: per `spec:473-482`.
  - `copy.ts`: per `spec:627-634`. `T.mergedLocally = (sha: string) => \`merged locally ${sha7(sha)}\``; import `sha7` from `./logic/format` if `copy.ts` doesn't have it (check for a cycle first; if one appears, inline `sha.slice(0, 7)`).
  - `review.ts`: `approveBody(r, merge?)` returns `{ binding, merge }` for accept kinds.
  - `Review.tsx` footer, for accept kinds only (the other kinds keep today's button):
    - `finish_local` → one `bg-accent` button, `C.mergeLocally` → `run("approve", "local")`;
    - else → a `bg-accent` `C.createPrAutoMerge` button (`"auto"`) and a `border border-line` `C.createPr` button (`"manual"`).
    - `RequestChanges` stays unchanged.
    - Thread `merge` through `useMutation`: `(api, a: { kind: "approve" | "close"; merge?: MergeChoice }) => a.kind === "approve" ? api.approve(r.id, approveBody(r, a.merge)) : api.closeSpike(r.id)`.
  - `MergeList.tsx`: a `<section aria-label={C.awaitingMerge}>` with `<h3>`, then one `<li>` per row:
    - repo;
    - for a PR, `#N`, then `C.merged` when merged, else the checks text plus `C.autoMergeOn` when `auto_merge`, or `C.orchestratorFixing` when checks are failing;
    - for a local row, `T.mergedLocally(merged_sha)`;
    - `↗` as `<a href={url} target="_blank" rel="noreferrer">` when `url` is set.
    - No progress bar.
  - `Details.tsx`: `{item.status === "in_review" && d.merges && (d.merges.length ? <MergeList merges={d.merges} /> : <p>{C.awaitingOrchestrator}</p>)}`, placed right before the TodoList line.
  - `requestTitle.ts` already maps to `C.acceptEpic`/`C.acceptFix`, so it picks up the new copy.
  - Update the pinned-string tests listed in Files.
  - Progress-list wording (`spec:666-688`): the web renders labels from the wire, so change only the test fixtures.
    - `TodoList.test.tsx`: fixture `Merge + verify` → `Merging and verifying`, and its expected row text `○Merging and verifying`. `queryByRole("button", { name: "Merging and verifying" })` stays null.
    - `Details.test.tsx:37`: the same label.
    - Also update `web/src/mock/fixtures.ts` if it carries todo labels (`grep -n "Merge + verify\|User acceptance" web/src`).
  - Leave `C.epicDone/bugDone/choreDone` unchanged: they are not in the spec's web copy list.

  Run `cd web && pnpm test && pnpm run typecheck`. Expected PASS.

- [ ] **Step 3: Failing menubar tests.**

```swift
// AgentActionsTests
let waiting = AgentNode(name: "o", model: "m", role: .orchestrator, itemKey: "EPIC-14",
                        progress: AgentProgress(done: 6, total: 6, current: ""),
                        merge: MergeProgress(merged: 1, total: 2))
XCTAssertEqual(AgentTree.subtitle(waiting), "Orchestrator · Awaiting merge · 1/2 merged")
// Decoding: {"...agent fields...","merge":{"merged":1,"total":2}} → node.merge == MergeProgress(merged: 1, total: 2); absent → nil
// AppModelTests:175 and :775: "Accept EPIC-12" → "Finish EPIC-12"
XCTAssertEqual(Copy.awaitingMerge(1, 2), "Awaiting merge · 1/2 merged")
```

  (With the default running session the state label is omitted, exactly as in the existing `"Orchestrator · EPIC-1"` assertion. The spec's `· Idle` suffix comes from `DisplayState` for an idle session, which is unchanged.)

  Run `cd apps/menubar && swift test`. Expected FAIL: `MergeProgress` is undefined.

- [ ] **Step 4: Menubar implementation.**
  - `Wire.swift`: add `MergeProgress` (`spec:490`) and `public var merge: MergeProgress?`, with `case merge` in `CodingKeys` and an init param `merge: MergeProgress? = nil` after `progress:`.
  - `Copy.swift`: `acceptItem` returns `"Finish \(key)"`; add `public static func awaitingMerge(_ merged: Int, _ total: Int) -> String { "Awaiting merge · \(merged)/\(total) merged" }`.
  - `AgentActions.subtitle`: `let middle = a.merge.map { Copy.awaitingMerge($0.merged, $0.total) } ?? a.progress.map { … } ?? a.step ?? a.itemKey`.

  Run `swift test`. Expected PASS.

- [ ] **Step 5: Gate and commit.**

```bash
cd web && pnpm test && pnpm run typecheck && cd ../apps/menubar && swift test && cd ../..
git add web/src/types.ts web/src/copy.ts web/src/logic/review.ts web/src/logic/review.test.ts web/src/panels/Review.tsx \
  web/src/panels/Review.test.tsx web/src/panels/Details.tsx web/src/panels/Details.test.tsx web/src/components/MergeList.tsx \
  web/src/components/MergeList.test.tsx web/src/mock/fixtures.ts web/src/copy.test.ts web/src/App.test.tsx \
  web/src/panels/NeedsYou.test.tsx web/src/logic/inbox.test.ts web/src/components/TodoList.test.tsx \
  apps/menubar/Sources/SwarmBarKit/Wire.swift apps/menubar/Sources/SwarmBarKit/AgentActions.swift apps/menubar/Sources/SwarmBarKit/Copy.swift \
  apps/menubar/Tests/SwarmBarTests/AgentActionsTests.swift apps/menubar/Tests/SwarmBarTests/AppModelTests.swift
# drop any path above you did not change; add any other test you updated
git commit -m "feat(ui): finish buttons, awaiting-merge block, menubar merge progress"
```

---

### Task 5: Agent name follows the agent-authored title

**Depends on Task 3.** Task 3 is the last runtime change before this one, and Task 5 edits `checkpoint.go`/`agents_test.go` in the same package. Run Task 5 after Task 3 is committed. It can run alongside Task 4.

**Spec:** the section "Agent name follows the agent-authored title", appended at the end of the spec (`grep -n "Agent name follows" docs/specs/2026-09-29-finish-with-pr.md`).

**Findings.** Everything keyed on `agents.name` was checked, and each item below says what the rename needs.

| Surface | Keyed by | Rename needs |
|---|---|---|
| Messages (`messages.to_agent_id`, `swarm_send`) | agent **id**. `Send` resolves `to` via `agentByNameTx` at send time, and a child uses the alias `"parent"` (`inbox.go:557`) | Nothing. Sends to the new name resolve, and old-name sends get `No agent <old>.` |
| Child → parent | `agents.parent_agent_id` (id). Brief `ParentName` is rendered once at spawn (`text.go:385`) | Nothing. At title time a spike orchestrator normally has no children, and later spawns render the new name |
| Notifications | `notifications.agent_id`, joined to `agents.name` on read (`notify.go:139,188`) | Nothing |
| Events | `publishAgentChanged(ctx, tx, name, rootItemID)` (`agents.go:2492`) | Emit it with the **new** name in the rename tx. Web and menubar refetch state on `agent.changed` |
| tmux session | `sessions.tmux_name` = `a.Name` at start (`agents.go:1412`). Every per-session call uses `ses.TmuxName`. **Menubar attaches `attach -t =<agent name>`** (`Terminals.swift:48-50`, `AppModel.swift:418`), and `Store.Terminal` returns `a.Name` (`agents.go:2213-2225`) | Rename the live tmux session after commit (`rename-session`), then set its `tmux_name` |
| Window title | reconciler `sessionTitle(…, r.AgentName)` → `RenameWindow(r.TmuxName, …)` (`reconcile.go:1199-1200`) | Nothing. The next tick writes the new name |
| Worktree paths and branches | the branch slug (`worktree.go:107,112`), never the agent name | Nothing |
| Adapter | Claude `-n <AgentName>` (`adapter/claude.go:111`), a display label only | Nothing. The next session uses the new name |
| httpapi `termFallback`/`termWait` | keyed by agent name, but transient per click | Nothing |

"Generated" is proven by `items.title_pending = 1`: only `StartSpike` sets it, and only when the typed Name is empty (`agents.go:287-297,327`). No other code path renames agents (`grep -rn "UPDATE agents SET name"` finds nothing). A successful `applyPendingTitle` therefore implies the name was generated.

**Files:**
- Modify:
  - `internal/runtime/checkpoint.go:1192-1213` (title branch) and a new helper next to `applyPendingTitle` (`:130`);
  - `internal/runtime/model.go:327-336` (the `Tmux` interface gains `RenameSession`);
  - `internal/spawn/tmux.go` (implement it);
  - every `Tmux` fake: `internal/runtime/agents_test.go:~112`, `internal/httpapi/helpers_test.go:320`, `internal/mcpserver/helpers_test.go:85`.
- Test: `internal/runtime/checkpoint_test.go` (next to `TestCheckpointTitleAppliesOnceForTheOrchestratorsOwnRootItem`) and `internal/spawn/tmux_test.go`, if that file tests argv (check with `grep -n RenameWindow internal/spawn/*_test.go`).

**Interfaces:**
- Consumes: `applyPendingTitle(ctx, tx, it, title) (bool, error)`, `publishAgentChanged`, `ids.KebabMax`, `ids.Unique`, and the `titlePendingSpike` test fixture (`checkpoint_test.go:81`).
- Produces:
  - `Tmux.RenameSession(ctx context.Context, oldName, newName string) error`
  - `func (s *Store) renameGeneratedAgentTx(ctx context.Context, tx *sql.Tx, a Agent, title string) (newName string, err error)`. It returns `""` when nothing was renamed.

- [ ] **Step 1: Failing tests** in `checkpoint_test.go`:

```go
func TestAcceptedTitleRenamesAGeneratedOrchestrator(t *testing.T) {
	s, tm, _ := newStore(t)
	ctx := context.Background()
	a, ses, _ := titlePendingSpike(t, s) // generated name "fix-the-login-redirect"
	old := a.Name
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "starting",
		Title: "Fix login redirect loop"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "fix-login-redirect-loop" {
		t.Fatalf("name = %q", got.Name)
	}
	if !slices.Contains(tm.sessRenamed, old+"|fix-login-redirect-loop") {
		t.Fatalf("tmux renames = %v", tm.sessRenamed)
	}
	ses2, _ := s.LatestSession(ctx, a.ID)
	if ses2.TmuxName != "fix-login-redirect-loop" {
		t.Fatalf("tmux_name = %q", ses2.TmuxName)
	}
	// agent.changed with the new name
	var n int
	s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE type = 'agent.changed'
		AND json_extract(payload_json,'$.name') = 'fix-login-redirect-loop'`).Scan(&n)
	if n == 0 {
		t.Fatal("no agent.changed for the new name")
	}
}

func TestAcceptedTitleNeverRenamesATypedName(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	_, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Login work", Intent: "feature", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, a.ID)
	res, _ := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "s", Title: "Fix login redirect loop"})
	if res.TitleApplied {
		t.Fatal("typed-name spike has no pending title")
	}
	if got, _ := s.AgentByID(ctx, a.ID); got.Name != "login-work" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestAcceptedTitleRenameGetsASuffixOnCollision(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	if _, _, _, err := s.StartSpike(ctx, SpikeInput{Name: "fix login redirect loop", Intent: "feature", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	a, ses, _ := titlePendingSpike(t, s)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "s", Title: "Fix login redirect loop"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgentByID(ctx, a.ID); got.Name != "fix-login-redirect-loop-2" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestMessagesToTheRenamedOrchestratorReachIt(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	a, ses, key := titlePendingSpike(t, s)
	child, _, err := s.Spawn(ctx, SpawnInput{ItemKey: key, Role: RoleResearcher, Kind: Fake, Model: "fake-1",
		ParentAgentID: a.ID, Brief: BriefInput{Objective: "look around"}})
	if err != nil {
		t.Fatal(err)
	}
	childSes := mustSessionID(t, s, child.ID)
	if _, err := s.WriteCheckpoint(ctx, ses.ID, CheckpointInput{Kind: Accepted, Summary: "s", Title: "Fix login redirect loop"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.Send(ctx, childSes, "fix-login-redirect-loop", "finding", "hi", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var to string
	s.DB.QueryRowContext(ctx, `SELECT to_agent_id FROM messages WHERE id = ?`, id).Scan(&to)
	if to != a.ID {
		t.Fatalf("to_agent_id = %q, want %q", to, a.ID)
	}
	if _, err := s.Send(ctx, childSes, "parent", "finding", "via alias", "", ""); err != nil {
		t.Fatalf("parent alias after rename: %v", err)
	}
	if _, err := s.Send(ctx, childSes, "fix-the-login-redirect", "finding", "old name", "", ""); err == nil ||
		err.Error() != "No agent fix-the-login-redirect." {
		t.Fatalf("old name err = %v", err)
	}
}
```

  `s.AgentByID` exists (`agents.go:2411`). The events table columns are `type` and `payload_json`, so the first test's query uses `WHERE type = 'agent.changed'`.

  Add `sessRenamed []string` to `fakeTmux` plus:

```go
func (f *fakeTmux) RenameSession(ctx context.Context, oldName, newName string) error {
	f.sessRenamed = append(f.sessRenamed, oldName+"|"+newName)
	if e, ok := f.env[oldName]; ok {
		f.env[newName] = e
		delete(f.env, oldName)
	}
	return nil
}
```

  In `internal/httpapi/helpers_test.go` and `internal/mcpserver/helpers_test.go`, add `func (f *…) RenameSession(ctx context.Context, oldName, newName string) error { return nil }`.

  Run `go test ./internal/runtime -run 'AcceptedTitle|RenamedOrchestrator' -v`. Expected FAIL: `RenameSession` is not in the `Tmux` interface, and the name is unchanged.

- [ ] **Step 2: Implement.**
  - `model.go`: add `RenameSession(ctx context.Context, oldName, newName string) error` to `Tmux`.
  - `spawn/tmux.go`: `func (s *Spawner) RenameSession(ctx context.Context, oldName, newName string) error { _, err := s.run(ctx, "rename-session", "-t", "="+oldName, newName); return err }`. Use `=` for an exact match, so `login` never hits `login-2`.
  - `checkpoint.go`:

```go
// renameGeneratedAgentTx gives an orchestrator whose name was generated from the
// request (title_pending was 1) the kebab of the title the agent just chose.
func (s *Store) renameGeneratedAgentTx(ctx context.Context, tx *sql.Tx, a Agent, title string) (string, error) {
	base, err := ids.KebabMax(title, 24)
	if err != nil || base == "" || base == a.Name {
		return "", nil
	}
	name := ids.Unique(base, func(n string) bool {
		var one int
		return tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE name = ? AND id <> ?`, n, a.ID).Scan(&one) == nil
	})
	if name == a.Name {
		return "", nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET name = ? WHERE id = ?`, name, a.ID); err != nil {
		return "", err
	}
	return name, s.publishAgentChanged(ctx, tx, name, a.RootItemID)
}
```

  In the title branch, right after `applied == true`, call `newName, err := s.renameGeneratedAgentTx(ctx, tx, a, title)`. When `newName != ""`, hoist `renameFrom, renameTo, renameSes = a.Name, newName, ses.ID` out of the closure (like `toClose`) and set `a.Name = newName` for the rest of the tx. After the tx commits and `ran` is true:

```go
	if renameTo != "" {
		if err := s.Tmux.RenameSession(ctx, renameFrom, renameTo); err != nil {
			s.logf("rename: tmux rename-session %s → %s: %v", renameFrom, renameTo, err)
		} else if _, err := s.DB.ExecContext(ctx, `UPDATE sessions SET tmux_name = ? WHERE id = ? AND tmux_name = ?`,
			renameTo, renameSes, renameFrom); err != nil {
			s.logf("rename: tmux_name %s: %v", renameSes, err)
		}
	}
```

  Run the Step 1 command. Expected PASS. Then run `go test ./internal/runtime/... ./internal/httpapi/... ./internal/mcpserver/... ./internal/spawn/...`. Update any test that pins an agent name after an `accepted` checkpoint with a title, for example a check that expects the placeholder name. Rewrite it to the new name, never delete it.

- [ ] **Step 3: Gate and commit.**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/runtime/checkpoint.go internal/runtime/checkpoint_test.go internal/runtime/model.go internal/runtime/agents_test.go \
  internal/spawn/tmux.go internal/httpapi/helpers_test.go internal/mcpserver/helpers_test.go
# plus internal/spawn/tmux_test.go if you added an argv test
git commit -m "feat(runtime): rename a generated orchestrator name to the agent-authored title"
```

---

## After all five tasks

1. Full gate: `make web-build && gofmt -l . && go vet ./... && go test ./... && (cd web && pnpm test && pnpm run build) && (cd apps/menubar && swift test) && make skills-sync && git diff --exit-code internal/install/skills`.
2. Migration check on a copy of the live DB, per `spec:751-757`.
3. Live checks after deploy, per `spec:822-829`. Run `gh auth status` in the launchd environment first.
