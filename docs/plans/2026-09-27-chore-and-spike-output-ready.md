# Spike Output Lands Ready, and Chores Are Chores: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Materialized epic and bug roots land Ready and can be accepted. A user-created chore is a Ready `CHORE-N` root, not a spike, and it finishes through `accept_fix`.

**Architecture:** Nearly every behavior change sits in `internal/items` (transition rules and create validation) and in `internal/runtime` (materialize, `StartSpike`, `StartOrchestrator`, native prompt). The CLI and the web add chore as a creation path over the unchanged `POST /api/spikes` wire. No DB change, no new request kind, no menubar change.

**Tech Stack:** Go 1.x (`go test`), SQLite via `internal/db`, React and TypeScript with vitest (`pnpm`), Markdown skills synced with `make skills-sync`.

**Spec:** `docs/specs/2026-09-27-chore-and-spike-output-ready.md`. Read it first. The §/E numbers below refer to it.

## Global Constraints

- Work only in `/Users/alexandertar/GitHub/agent-swarm-chore-ready` (branch `fix/chore-and-spike-output-ready`). Never touch the primary checkout, `~/.swarm`, the live daemon, launchd, `tmux -L swarm`, `~/.claude*` or `~/.codex`.
- Stage explicit paths only. Never `git add -A`, never `--amend`.
- Never delete a test. Port it to the new behavior (tests listed in spec §9).
- Every commit ends with the trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Do not edit any `apps/menubar` file, least of all `apps/menubar/Sources/SwarmBarUI/NewOrchestratorView.swift`.
- After editing `skills/*`, run `make skills-sync` and commit the synced copy.
- Copy is verbatim from spec §6. In particular:
  - `A chore isn't a spike. Create type chore.`
  - `A chore works only on its own scope. It can't propose top-level items.`
  - `Accept this chore to mark it Done.`
  - `Review the chore and accept it.`
  - `Accept chore`
  - `Creates a chore orchestrator that works on the scope you describe.`
  - `The orchestrator asks you to confirm repositories before it starts work.`
  - `New chore`
- Quote globs under zsh (`--include='*.go'`).

## Build batches

- **Batch 1, Go, CLI and skills:** Tasks 1–8. Gate: `go vet ./... && go test ./...` green, and `make skills-sync` leaves no diff.
- **Batch 2, Web:** Tasks 9–11. Gate: `cd web && pnpm test && pnpm typecheck` green, then `make web-build && go build ./...`.

## File map

| File | Responsibility in this change |
|---|---|
| `internal/items/transition.go` | chore root lifecycle; Draft→Ready rule for earlier accepted |
| `internal/items/store.go` | spike+chore refusal; refusal when a chore-rooted orchestrator proposes |
| `internal/runtime/materialize.go` | root created Ready |
| `internal/runtime/agents.go` | `StartSpike` chore branch; `promoteDraftRoot` |
| `internal/runtime/native.go` | chore accept native prompt |
| `cmd/swarm/runtime_cmds.go`, `cmd/swarm/main.go`, `README.md` | `--intent chore` |
| `skills/swarm-orchestrator/SKILL.md` (+ synced copy) | Chores section |
| `web/src/copy.ts`, `logic/transitions.ts`, `logic/tree.ts`, `state/url.ts`, `components/Header.tsx` | board CHORE support |
| `web/src/types.ts`, `logic/spawnForm.ts`, `mock/daemon.ts` | chore on the spike wire |
| `web/src/panels/NewSpikeSheet.tsx`, `views/props.ts`, `App.tsx` | New item → Chore sheet |

---

# Batch 1: Go, CLI, skills

### Task 1: A chore root reaches Done through `accept_fix`

**Files:**
- Modify: `internal/items/transition.go:223` (check), `:336-339` (checkRoot denial), `:471` (rootState), `:534` (ReconcileTx), `:615-618` and `:679-682` (reconcileRoot)
- Test: `internal/items/transition_test.go` (append)

**Interfaces:**
- Produces: `func isAcceptRoot(t Type) bool` and `func acceptKind(t Type) string` (unexported, `internal/items`). Task 2 uses `isAcceptRoot`.

- [ ] **Step 1: Write the failing tests** (append to `internal/items/transition_test.go`; this file already has the helpers `mk`, `setStatus`, `seedCheckpoint`, `later`, `acceptRequests`, `count`, `wantStatus`, `wantDenied`, `move`, `exec`, `gitJSON` and `user`)

```go
// Spec E8/E10/decision 4: a chore with a task finishes through accept_fix.
func TestChoreAcceptanceFlow(t *testing.T) {
	s := newStore(t)
	ch := mk(t, s, items.Chore, "", "Bump deps")
	task := mk(t, s, items.Task, ch.Key, "Bump go deps")
	setStatus(t, s, ch, items.Ready)
	seedCheckpoint(t, s.DB, ch, "accepted", 1, later(s), "")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)

	setStatus(t, s, task, items.Done)
	ckp := seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
	states, b := acceptRequests(t, s, ch)
	if len(states) != 1 || count(states, "accept_fix:open") != 1 || b.IntegratedCheckpoint != ckp {
		t.Fatalf("requests = %v, binding = %+v", states, b)
	}
	var prompt string
	if err := s.DB.QueryRow(`SELECT prompt FROM requests WHERE item_id = ?`, ch.ID).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if prompt != "Review the chore and accept it." {
		t.Fatalf("prompt = %q", prompt)
	}
	wantDenied(t, move(t, s, ch.Key, items.Done, user), "Accept this chore to mark it Done.")
	wantDenied(t, move(t, s, ch.Key, items.Done, items.Daemon()), "Accept this chore to mark it Done.")

	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ?`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.Done)
}

// Spec E9/E10: a chore needs no child; a declined acceptance goes back to work
// and a fresh integration asks again. An epic with no children still waits.
func TestChoreWithNoTasksReachesDone(t *testing.T) {
	s := newStore(t)
	ch := mk(t, s, items.Chore, "", "Rotate cert")
	setStatus(t, s, ch, items.Ready)
	seedCheckpoint(t, s.DB, ch, "accepted", 1, later(s), "")
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)
	seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)

	exec(t, s.DB, `UPDATE requests SET state = 'changes_requested' WHERE item_id = ?`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InProgress)
	seedCheckpoint(t, s.DB, ch, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.InReview)
	states, _ := acceptRequests(t, s, ch)
	if count(states, "accept_fix:open") != 1 {
		t.Fatalf("requests = %v", states)
	}
	exec(t, s.DB, `UPDATE requests SET state = 'approved' WHERE item_id = ? AND state = 'open'`, ch.ID)
	s.Reconcile(ctx, ch.Key)
	wantStatus(t, s, ch.Key, items.Done)

	e := mk(t, s, items.Epic, "", "Empty epic")
	setStatus(t, s, e, items.Ready)
	seedCheckpoint(t, s.DB, e, "accepted", 1, later(s), "")
	s.Reconcile(ctx, e.Key)
	seedCheckpoint(t, s.DB, e, "integrated", 1, later(s), gitJSON)
	s.Reconcile(ctx, e.Key)
	wantStatus(t, s, e.Key, items.InProgress)
	if states, _ := acceptRequests(t, s, e); len(states) != 0 {
		t.Fatalf("a childless epic must not ask for acceptance: %v", states)
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/items/ -run 'TestChore' -v`
Expected: FAIL. The chore stays `ready`, because `ReconcileTx` has no chore case.

- [ ] **Step 3: Implement** in `internal/items/transition.go`

Add these near `orGeneric`:

```go
// isAcceptRoot reports whether t is a root type that finishes through
// accept_epic/accept_fix: epic, bug, chore (2026-09-27 chore spec).
func isAcceptRoot(t Type) bool { return t == Epic || t == Bug || t == Chore }

// acceptKind is the acceptance request kind for a root type.
func acceptKind(t Type) string {
	if t == Epic {
		return "accept_epic"
	}
	return "accept_fix" // bug and chore (chore spec decision 4)
}
```

In `check`, change `case Epic, Bug:` to `case Epic, Bug, Chore:`.

In `checkRoot`, replace the tail of the `to == Done` case:

```go
		switch it.Type {
		case Epic:
			return deny("Accept this epic to mark it Done.")
		case Chore:
			return deny("Accept this chore to mark it Done.")
		}
		return deny("Accept this fix to mark it Done.")
```

In `rootState`, change the finished line to:

```go
	st.finished = fin == n && (n > 0 || it.Type == Chore) // a chore may have no tasks
```

In `ReconcileTx`, change `case Epic, Bug:` to `case Epic, Bug, Chore:`.

In `reconcileRoot`, replace

```go
	kind := "accept_epic"
	if it.Type == Bug {
		kind = "accept_fix"
	}
```

with `kind := acceptKind(it.Type)`. Replace the prompt block with:

```go
	prompt := "Review completed work and accept the epic."
	switch it.Type {
	case Bug:
		prompt = "Review the fix and accept it."
	case Chore:
		prompt = "Review the chore and accept it."
	}
```

- [ ] **Step 4: Run the package tests and watch them pass**

Run: `go test ./internal/items/...`
Expected: PASS, including the existing `TestEpicAcceptanceFlow` and `TestCancelStalesTheOpenAcceptRequest`.

- [ ] **Step 5: Commit**

```bash
git add internal/items/transition.go internal/items/transition_test.go
git commit -m "fix(items): a chore root finishes through accept_fix, with or without tasks

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 2: Promoting a Draft root honors an earlier `accepted`

**Files:**
- Modify: `internal/items/transition.go:55-73` (`TransitionTx`)
- Test: `internal/items/transition_test.go` (append)

**Interfaces:**
- Consumes: `isAcceptRoot` from Task 1.
- Produces: `TransitionTx(ctx, tx, key, Ready, by)` on a Draft epic/bug/chore root that has any `accepted` checkpoint now ends in InProgress, or further once reconciled. Task 5 relies on this.

- [ ] **Step 1: Write the failing test**

```go
// Spec decision 6 / E5: BUG-2-style stuck roots recover with one move to Ready.
func TestDraftRootPromotionHonorsEarlierAccepted(t *testing.T) {
	s := newStore(t)
	b := mk(t, s, items.Bug, "", "Crash") // Draft, as materialize used to leave it
	task := mk(t, s, items.Task, b.Key, "Fix")
	seedCheckpoint(t, s.DB, b, "accepted", 1, later(s), "")
	s.Reconcile(ctx, b.Key)
	wantStatus(t, s, b.Key, items.Draft) // the live stuck state
	setStatus(t, s, task, items.Done)
	seedCheckpoint(t, s.DB, b, "integrated", 1, later(s), gitJSON)

	if err := move(t, s, b.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, b.Key, items.InReview)
	if states, _ := acceptRequests(t, s, b); count(states, "accept_fix:open") != 1 {
		t.Fatalf("requests = %v", states)
	}

	fresh := mk(t, s, items.Epic, "", "Fresh proposal") // no accepted yet
	if err := move(t, s, fresh.Key, items.Ready, user); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, s, fresh.Key, items.Ready)
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/items/ -run TestDraftRootPromotionHonorsEarlierAccepted -v`
Expected: FAIL with `BUG-1 status = ready, want in_review`.

- [ ] **Step 3: Implement.** In `TransitionTx`, insert this block after the reopen/cancel `staleAccepts` block and before `s.ReconcileTx`:

```go
	// Chore spec decision 6: nothing ever moves back into Draft, so an
	// accepted checkpoint on a Draft root belongs to this lifecycle. It
	// counts on promotion, instead of parking the root at Ready forever
	// (acceptedSince only sees checkpoints newer than updated_at).
	if from == Draft && to == Ready && isAcceptRoot(it.Type) && it.ID == it.RootID {
		accepted, err := exists(ctx, tx, `SELECT 1 FROM checkpoints WHERE item_id = ? AND kind = 'accepted'`, it.ID)
		if err != nil {
			return Item{}, err
		}
		if accepted {
			if err := s.setStatus(ctx, tx, &it, InProgress); err != nil {
				return Item{}, err
			}
		}
	}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/items/...`
Expected: PASS. `TestEpicManualMoves` still moves a fresh Draft epic to Ready, and the reopen path in `TestEpicAcceptanceFlow` still stays Ready.

- [ ] **Step 5: Commit**

```bash
git add internal/items/transition.go internal/items/transition_test.go
git commit -m "fix(items): promoting a Draft root counts its earlier accepted checkpoint

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 3: Refuse spike+chore, and refuse proposals from a chore-rooted orchestrator

**Files:**
- Modify: `internal/items/store.go:377-379` (intent check) and the top-level orchestrator branch (`if by.isOrchestrator() {` under `if in.ParentKey == ""`, around `:405`)
- Test: `internal/items/store_test.go:713` (port `TestCreateSpikeWithChoreIntent`), plus a new test

**Interfaces:**
- Produces: `CreateTx` errors (`CodeBadRequest`) with the two copies above. Task 4 relies on the spike refusal: it can't create a chore spike through `CreateInput`.

- [ ] **Step 1: Port and write the failing tests.** Replace the body of `TestCreateSpikeWithChoreIntent`. The behavior was changed on purpose by spec decision 5, so this is a port, not a deletion:

```go
// Chore spec decision 5: a chore is never a spike, whoever asks.
func TestCreateSpikeWithChoreIntent(t *testing.T) {
	s := newStore(t)
	root := mk(t, s, items.Epic, "", "Root")
	for _, by := range []items.Actor{items.User("test"), items.Daemon(), items.Orchestrator("agt_1", root.ID)} {
		_, err := s.Create(ctx, items.CreateInput{Type: items.Spike, Title: "Maintenance chore", SpikeIntent: "chore"}, by)
		if code(err) != items.CodeBadRequest || err.Error() != "A chore isn't a spike. Create type chore." {
			t.Fatalf("%s: err = %v", by.Kind, err)
		}
	}
}

// Chore spec decision 3 / E11: a chore works only on its own scope.
func TestChoreRootCannotProposeTopLevel(t *testing.T) {
	s := newStore(t)
	ch := mk(t, s, items.Chore, "", "Bump deps")
	orch := items.Orchestrator("agt_1", ch.ID)
	for _, in := range []items.CreateInput{
		{Type: items.Epic, Title: "E"}, {Type: items.Bug, Title: "B"},
		{Type: items.Chore, Title: "C"}, {Type: items.Spike, Title: "S", SpikeIntent: "feature"},
	} {
		_, err := s.Create(ctx, in, orch)
		if code(err) != items.CodeBadRequest ||
			err.Error() != "A chore works only on its own scope. It can't propose top-level items." {
			t.Fatalf("%s: err = %v", in.Type, err)
		}
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("items = %d (%v), want only the chore", n, err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/items/ -run 'TestCreateSpikeWithChoreIntent|TestChoreRootCannotProposeTopLevel|TestOrchestratorCanProposeRoot' -v`
Expected: FAIL on both new assertions. `TestOrchestratorCanProposeRoot` passes.

- [ ] **Step 3: Implement** in `internal/items/store.go`. Replace

```go
	if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" && in.SpikeIntent != "chore" {
```

with

```go
	if in.Type == Spike && in.SpikeIntent == "chore" {
		return Item{}, errf(CodeBadRequest, "A chore isn't a spike. Create type chore.")
	}
	if in.Type == Spike && in.SpikeIntent != "feature" && in.SpikeIntent != "debug" {
```

(the body and copy `Spikes start with an intent. Use New spike.` stay). Then, as the first statements inside the top-level `if by.isOrchestrator() {` branch, before the `in.Status == Ready` check:

```go
			own, err := s.getByID(ctx, tx, by.RootID)
			if err != nil {
				return Item{}, err
			}
			if own.Type == Chore {
				return Item{}, errf(CodeBadRequest, "A chore works only on its own scope. It can't propose top-level items.")
			}
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `grep -rn 'items.Orchestrator("' --include='*_test.go' internal` first. Any test whose second argument is not a real item id and that creates a root now fails with `sql: no rows`; point it at a real root. Then run `go test ./internal/items/... ./internal/mcpserver/...`
Expected: PASS. The table in `orchestrator_test.go:2267` proposes a chore **type** from an epic root and still passes.

- [ ] **Step 5: Commit**

```bash
git add internal/items/store.go internal/items/store_test.go
git commit -m "fix(items): a chore is never a spike, and a chore can't propose top-level items

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 4: Materialize creates the root Ready (and legacy chore spikes still work)

**Files:**
- Modify: `internal/runtime/materialize.go:154`
- Test: `internal/runtime/materialize_test.go:150` (port), plus new tests and a fixture in the same file

**Interfaces:**
- Consumes: `approvedFeatureSpike`, `approvedDebugSpike`, `seedRepo`, `writeFile` (existing test helpers). `s.Materialize(ctx, sessionID, spikeKey, specID, planID, reportID, requestID string) (MaterializeResult, error)`.

- [ ] **Step 1: Port and write the failing tests.** In `TestMaterializeBuildsTheEpicTree`, change:

```go
	if root.Status != items.Ready {
		t.Fatalf("the root starts as ready, got %s", root.Status)
	}
```

At the end of the same test, add the reported bug's own path (E1). The user starts the root's orchestrator, and its `accepted` must move the root on. The conflict check is keyed on the new epic's own `root_item_id`, so the still-active SPIKE-1 orchestrator doesn't collide:

```go
	// The reported bug: the new root's orchestrator could never leave Draft.
	orch, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: res.Root, Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteCheckpoint(ctx, mustSessionID(t, s, orch.ID), CheckpointInput{Kind: Accepted, Summary: "on it"}); err != nil {
		t.Fatal(err)
	}
	if root, _ = s.Items.Get(ctx, res.Root); root.Status != items.InProgress {
		t.Fatalf("after accepted: %s", root.Status)
	}
```

Append:

```go
// Spec E2.
func TestMaterializeDebugRootIsReady(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	ses, reportID := approvedDebugSpike(t, s)
	res, err := s.Materialize(ctx, ses.ID, "SPIKE-1", "", "", reportID, "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.Items.Get(ctx, res.Root)
	if root.Type != items.Bug || root.Status != items.Ready {
		t.Fatalf("root = %s %s, want a ready bug", root.Type, root.Status)
	}
}

// choreTreeBody is a plan whose swarm-tree has a chore root (legacy chore spikes).
const choreTreeBody = "# Plan\n\n## Work breakdown\n\n" +
	"```swarm-tree\n" +
	`{"root":{"type":"chore","title":"Bump deps","brief":"","acceptance":["Deps are current."]},
 "children":[{"ref":"t1","type":"task","title":"Bump go deps","brief":"","acceptance":[],"role_hint":"coder","tdd_exempt":null,"repos":["chat"],"workflow":{"template":"tdd-reviewed"},"steps":["Write test","Bump"],"verify":["go test ./..."],"solo":"focused"}],
 "deps":[]}` + "\n```\n\n## Verification\n\ngo test ./...\n"

// Spec decision 5 / E14: a live spike with intent chore (SPIKE-20) still
// materializes. The row is set directly because CreateTx now refuses it.
func TestMaterializeLegacyChoreSpike(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "feature",
		Kind: Fake, Model: "fake-1", Repos: []string{repo}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET spike_intent = 'chore' WHERE key = ?`, key); err != nil {
		t.Fatal(err)
	}
	ses, err := s.LatestSession(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.Ask(ctx, ses.ID, AskInput{Kind: "confirm_repos", Prompt: "chat only",
		Repos: []ReposProposal{{Repo: repo, Reason: "the deps live here"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmRepos(ctx, req.ID, []string{repo}, "", 0, "board", ""); err != nil {
		t.Fatal(err)
	}
	plan, err := s.RegisterArtifact(ctx, ses.ID, "register", key, "plan", writeFile(t, choreTreeBody), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Ask(ctx, ses.ID, AskInput{Kind: "approval", ArtifactID: plan.ArtifactID, Prompt: "Approve the plan."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, r.ID, ApproveInput{ArtifactRevision: plan.Revision, Via: "board"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Materialize(ctx, ses.ID, key, "", plan.ArtifactID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.Items.Get(ctx, res.Root)
	if root.Type != items.Chore || root.Status != items.Ready || !strings.HasPrefix(root.Key, "CHORE-") {
		t.Fatalf("root = %+v", root)
	}
	children, _ := s.Items.Children(ctx, root.Key)
	if len(children) != 1 || children[0].Type != items.Task || children[0].Status != items.Ready {
		t.Fatalf("children = %+v", children)
	}
	if sp, _ := s.Items.Get(ctx, key); sp.Status != items.Done {
		t.Fatalf("spike = %s, want done", sp.Status)
	}
}
```

(`Materialize` moves the spike to Done in the same tx, `materialize.go:345`; the feature test asserts the same at `materialize_test.go:168`.)

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runtime/ -run 'TestMaterialize' -v`
Expected: FAIL. The root is `draft` in all three tests.

- [ ] **Step 3: Implement.** In `internal/runtime/materialize.go:154`, change `Acceptance: tree.Root.Acceptance, Status: items.Draft,` to `Acceptance: tree.Root.Acceptance, Status: items.Ready,`. Update the doc comment on `Materialize` to read "turns an approved spike into a Ready epic (feature) or bug (debug)".

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/runtime/ -run 'TestMaterialize' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/materialize.go internal/runtime/materialize_test.go
git commit -m "fix(runtime): materialized roots start Ready so their orchestrator can accept them

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 5: `StartSpike` creates a chore for intent chore; `StartOrchestrator` promotes a Draft root

**Files:**
- Modify: `internal/runtime/agents.go:287-297` (`StartSpike` create), `:457-462` (`StartOrchestrator` head), plus the new helper `promoteDraftRoot`
- Test: `internal/runtime/agents_test.go:344` (port `TestStartSpikeWithChoreIntentAndLongBrief`), plus new tests

**Interfaces:**
- Consumes: Task 3's spike+chore refusal, which means `StartSpike` must branch **before** `Items.Create`. Also Task 2's promotion rule.
- Produces: `func (s *Store) promoteDraftRoot(ctx context.Context, it items.Item) (items.Item, error)`. After this, `StartSpike(ctx, SpikeInput{Intent: "chore", ...})` returns a `CHORE-N` key. Tasks 6 and 7 rely on that.

- [ ] **Step 1: Port and write the failing tests.** Replace `TestStartSpikeWithChoreIntentAndLongBrief` with:

```go
// Chore spec decision 2 / E7: intent chore creates a Ready chore, not a spike.
func TestStartSpikeWithChoreIntentCreatesAReadyChore(t *testing.T) {
	s, _, fa := newStore(t)
	ctx := context.Background()
	repo := seedRepo(t, s, "chat")
	longBrief := strings.Repeat("Detailed maintenance instructions. ", 60) // ~2160 chars (> 600)
	key, a, _, err := s.StartSpike(ctx, SpikeInput{Name: "First pass cleanup", Intent: "chore",
		Kind: Fake, Model: "fake-1", Request: longBrief, Repos: []string{repo}})
	if err != nil {
		t.Fatalf("expected a chore with a long brief to succeed, got: %v", err)
	}
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "CHORE-") || it.Type != items.Chore || it.Status != items.Ready || it.SpikeIntent != "" {
		t.Fatalf("item = %s %s %s intent %q", key, it.Type, it.Status, it.SpikeIntent)
	}
	if it.Brief != longBrief {
		t.Fatalf("expected brief to match longBrief exactly, got len %d vs %d", len(it.Brief), len(longBrief))
	}
	if !slices.Equal(it.SuggestedRepos, []string{repo}) || len(it.Repos) != 0 {
		t.Fatalf("repos = %v, suggested = %v", it.Repos, it.SuggestedRepos)
	}
	if a.Name != "first-pass-cleanup" || a.Role != RoleOrchestrator || a.ItemID != it.ID {
		t.Fatalf("agent = %+v", a)
	}
	if !strings.Contains(fa.LastSpec.Kickoff, "swarm-orchestrator") || strings.Contains(fa.LastSpec.Kickoff, "swarm-spike") {
		t.Fatalf("kickoff must name swarm-orchestrator, not swarm-spike:\n%s", fa.LastSpec.Kickoff)
	}
}

// Chore spec decision 1 / E3.
func TestStartOrchestratorPromotesDraftRoot(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s) // EPIC-1 is Draft
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, "EPIC-1"); it.Status != items.Ready {
		t.Fatalf("EPIC-1 = %s, want ready", it.Status)
	}
}

// Chore spec decision 7 / E4: promotion comes first, so a refused start still promotes.
func TestStartOrchestratorPromotesEvenWhenRefused(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	seedEpicWithTask(t, s)
	if _, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE items SET status = 'draft' WHERE key = 'EPIC-1'`); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.StartOrchestrator(ctx, OrchestratorInput{ItemKey: "EPIC-1", Kind: Fake, Model: "fake-1"})
	if err == nil || err.Error() != "This item already has an orchestrator." {
		t.Fatalf("err = %v", err)
	}
	if it, _ := s.Items.Get(ctx, "EPIC-1"); it.Status != items.Ready {
		t.Fatalf("EPIC-1 = %s, want ready", it.Status)
	}
}
```

Add `"slices"` to the imports in `agents_test.go` if it isn't there.

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runtime/ -run 'TestStartSpikeWithChoreIntent|TestStartOrchestratorPromotes' -v`
Expected: FAIL. StartSpike errors with `A chore isn't a spike. Create type chore.` (Task 3), and EPIC-1 stays `draft`.

- [ ] **Step 3: Implement** in `internal/runtime/agents.go`. Replace the `s.Items.Create(ctx, items.CreateInput{Type: items.Spike, ...})` call in `StartSpike` with:

```go
	ci := items.CreateInput{
		Type:           items.Spike,
		Title:          in.Name,
		SpikeIntent:    in.Intent,
		Brief:          in.Request,
		SuggestedRepos: in.Repos,
	}
	if in.Intent == "chore" { // chore spec decision 2: a chore is not a spike
		ci.Type, ci.SpikeIntent, ci.Status = items.Chore, "", items.Ready
	}
	it, err := s.Items.Create(ctx, ci, items.User("board"))
```

Add the helper right above `StartOrchestrator`:

```go
// promoteDraftRoot moves a Draft epic/bug/chore root to Ready as the user
// starting its orchestrator (chore spec decisions 1 and 7). It runs before
// anything else, so the recover-in-place path is covered too, and a start
// that is then refused still leaves the root Ready (a Ready root never
// auto-spawns). Anything else is a no-op.
func (s *Store) promoteDraftRoot(ctx context.Context, it items.Item) (items.Item, error) {
	if it.ID != it.RootID || it.Status != items.Draft ||
		(it.Type != items.Epic && it.Type != items.Bug && it.Type != items.Chore) {
		return it, nil
	}
	return s.Items.Transition(ctx, it.Key, items.Ready, items.User("board"))
}
```

At the top of `StartOrchestrator`, right after `it, err := s.Items.Get(ctx, in.ItemKey)` and its error check:

```go
	if it, err = s.promoteDraftRoot(ctx, it); err != nil {
		return Agent{}, false, err
	}
```

- [ ] **Step 4: Run the whole runtime package and watch it pass**

Run: `go test ./internal/runtime/... ./internal/httpapi/... ./internal/mcpserver/...`
Expected: PASS. `worker()` and every other `StartOrchestrator` fixture now promotes EPIC-1 to Ready, and its orchestrator's `accepted` moves it to InProgress. If an existing test asserts EPIC-1's status or `item_revision` after that, port the assertion to the new value and add a comment citing chore spec decision 1. Do not delete it. Stop and report if more than 5 tests need porting.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/agents.go internal/runtime/agents_test.go
git commit -m "feat(runtime): intent chore creates a Ready chore; starting an orchestrator promotes a Draft root

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

(also add any test file ported in Step 4, by explicit path)

### Task 6: Chore accept native prompt, and the chore end to end

**Files:**
- Modify: `internal/runtime/native.go:143-153`
- Test: `internal/runtime/approval_lane_test.go` (extend `TestNativePromptForAcceptKinds`, add `TestChoreEndToEndAcceptFix`)

**Interfaces:**
- Consumes: Tasks 1 and 5. Test helpers `relayFor`, `decodeNP`, `hookSimulate`, `mustSessionID` (existing).

- [ ] **Step 1: Write the failing tests.** Inside `TestNativePromptForAcceptKinds`, create the chore before `s.tx`:

```go
	chore, err := s.Items.Create(ctx, items.CreateInput{Type: items.Chore, Title: "Bump deps"}, items.User("board"))
	if err != nil {
		t.Fatal(err)
	}
```

Before the closure's final `return nil`, add:

```go
		got, err = s.nativePromptFor(ctx, tx, Request{ID: "req_c1", Kind: KindAcceptFix, ItemID: chore.ID}, "", nil)
		if err != nil {
			return err
		}
		want = NativePrompt{Header: "Accept chore",
			Question: fmt.Sprintf(`Accept %s "Bump deps" as done? ⟦swarm:req_c1⟧`, chore.Key), Options: approveOptions}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("chore accept_fix prompt = %+v, want %+v", got, want)
		}
```

Append:

```go
// Chore spec E9 end to end: a zero-task chore goes accepted -> integrated ->
// accept_fix routed natively to its orchestrator -> approve -> Done.
func TestChoreEndToEndAcceptFix(t *testing.T) {
	s, _, _ := newStore(t)
	ctx := context.Background()
	key, orch, _, err := s.StartSpike(ctx, SpikeInput{Name: "Bump deps", Intent: "chore", Kind: Fake, Model: "fake-1"})
	if err != nil {
		t.Fatal(err)
	}
	ses := mustSessionID(t, s, orch.ID)
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Accepted, Summary: "bumping deps"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Items.Get(ctx, key); it.Status != items.InProgress {
		t.Fatalf("after accepted: %s", it.Status)
	}
	if _, err := s.WriteCheckpoint(ctx, ses, CheckpointInput{Kind: Integrated, Summary: "merged",
		Git:          []GitRef{{Repo: "proj", Branch: "main", SHA: "deadbee"}},
		Verification: []Verify{{Cmd: "go test ./...", Phase: "green", OK: true}}}); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Items.Get(ctx, key)
	if it.Status != items.InReview {
		t.Fatalf("after integrated: %s", it.Status)
	}
	var reqID string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM requests WHERE item_id = ? AND kind = 'accept_fix'
		AND state = 'open'`, it.ID).Scan(&reqID); err != nil {
		t.Fatalf("no open accept_fix: %v", err)
	}
	p, n := relayFor(t, s, orch.ID, reqID)
	if n != 1 {
		t.Fatalf("%d relays, want 1", n)
	}
	np := decodeNP(t, p)
	if np.Header != "Accept chore" {
		t.Fatalf("native prompt = %+v", np)
	}
	hookSimulate(t, s, ses, np, "Approve")
	out, err := s.Ask(ctx, ses, AskInput{Kind: "native_answer", Ref: reqID, Decision: "approve"})
	if err != nil || out.State != "approved" {
		t.Fatalf("native_answer = %+v, %v", out, err)
	}
	if it, _ := s.Items.Get(ctx, key); it.Status != items.Done {
		t.Fatalf("after approval: %s", it.Status)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./internal/runtime/ -run 'TestNativePromptForAcceptKinds|TestChoreEndToEndAcceptFix' -v`
Expected: FAIL. The header is `Accept fix`.

- [ ] **Step 3: Implement** in `internal/runtime/native.go`, case `KindAcceptEpic, KindAcceptFix`:

```go
	case KindAcceptEpic, KindAcceptFix:
		var key, title, typ string
		if err := tx.QueryRowContext(ctx, `SELECT key, title, type FROM items WHERE id = ?`, req.ItemID).Scan(&key, &title, &typ); err != nil {
			return NativePrompt{}, err
		}
		if req.Kind == KindAcceptFix && typ == string(items.Chore) {
			q := fmt.Sprintf("Accept %s %q as done?", key, title)
			return NativePrompt{Header: "Accept chore", Question: truncateWithToken(q, req.ID), Options: approveOptions}, nil
		}
		if req.Kind == KindAcceptFix {
			// … unchanged
```

(`native.go` already imports `internal/items`, line 14.)

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./internal/runtime/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/native.go internal/runtime/approval_lane_test.go
git commit -m "feat(runtime): chore acceptance asks 'Accept chore' through the native approval lane

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 7: `swarm new --intent chore`

**Files:**
- Modify: `cmd/swarm/runtime_cmds.go:108,118-121`, `cmd/swarm/main.go:29`, `README.md:149`
- Test: `cmd/swarm/runtime_cmds_test.go` (extend `TestNewRequiresNameAndIntent` at `:149`, add `TestNewChore`)

- [ ] **Step 1: Write the failing tests.** In `TestNewRequiresNameAndIntent`, replace the `sideways` block with:

```go
	var errOut bytes.Buffer
	if code := run([]string{"new", "--home", home, "--url", srv.URL,
		"--intent", "sideways", "--name", "x"}, &out, &errOut); code == 0 {
		t.Fatal("an unknown intent must be refused before the request")
	}
	if !strings.Contains(errOut.String(), "--intent must be feature, debug or chore") {
		t.Fatalf("stderr = %q", errOut.String())
	}
```

Append:

```go
// Chore spec E15.
func TestNewChore(t *testing.T) {
	srv, got, home := stubDaemon(t, map[string]string{
		"POST /api/spikes": `{"item":{"key":"CHORE-3","title":"Bump deps"},
			"agent":{"name":"bump-deps","state":"active","session":{"state":"spawning"}},"queued":false}`,
	})
	defer srv.Close()
	var out, errOut bytes.Buffer
	if code := run([]string{"new", "--home", home, "--url", srv.URL, "--name", "Bump deps", "--intent", "chore"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	var body string
	for _, c := range *got {
		if c.path == "/api/spikes" {
			body = c.body
		}
	}
	if !strings.Contains(body, `"intent":"chore"`) {
		t.Fatalf("body = %s", body)
	}
	if out.String() != "CHORE-3 created. Agent bump-deps is active.\n" {
		t.Fatalf("stdout = %q", out.String())
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./cmd/swarm/ -run 'TestNewRequiresNameAndIntent|TestNewChore' -v`
Expected: FAIL (chore is refused with exit code 2).

- [ ] **Step 3: Implement.**
  - `runtime_cmds.go:108`: `intent = fs.String("intent", "", "feature, debug or chore")`.
  - `:118-119`:
    ```go
    	if *name == "" || (*intent != "feature" && *intent != "debug" && *intent != "chore") {
    		fmt.Fprintln(stderr, "swarm: --name is required and --intent must be feature, debug or chore")
    ```
  - `main.go:29` and `README.md:149`: `feature|debug` → `feature|debug|chore`.

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./cmd/swarm/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/swarm/runtime_cmds.go cmd/swarm/runtime_cmds_test.go cmd/swarm/main.go README.md
git commit -m "feat(cli): swarm new --intent chore creates a chore

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 8: Orchestrator skill Chores section, then the Batch 1 gate

**Files:**
- Modify: `skills/swarm-orchestrator/SKILL.md:33`, `:53`, plus a new `## Chores` section between `## Owning an item` and `## Spikes`
- Modify (generated): `internal/install/skills/swarm-orchestrator/SKILL.md` via `make skills-sync`

- [ ] **Step 1: Edit the skill** exactly as spec §6.1 says:
  - Line 33: `` (`intent` is required for a spike — `feature`, `debug` or `chore`) `` → `` (`intent` is required for a spike — `feature` or `debug`; a chore is `type: "chore"`, never a spike) ``.
  - Line 53: `` `accept_epic` (`accept_fix` for a bug) `` → `` `accept_epic` (`accept_fix` for a bug or a chore) ``.
  - Insert before `## Spikes`:

```markdown
## Chores
- A chore (`CHORE-N`) is the user's own scope, not a proposal. It has no spec, plan or report to approve and no `swarm_materialize`.
- Confirm repositories first with `swarm_ask kind: "confirm_repos"`, exactly as for a spike: keep or drop (with a reason) every repo the user suggested.
- Do the work directly. For a small scope, do it yourself in your own worktree. Otherwise create tasks under the chore (`swarm_items create` with `parent: <CHORE-KEY>`, `type: "task"`, a workflow) and run them as usual. A chore has no stories; zero tasks is fine.
- Never propose top-level items from a chore — Swarm refuses it. Put out-of-scope findings in your final summary instead.
- When the work is merged and verified, write `integrated` on the chore with the merged sha per repo and the verification results. The daemon opens `accept_fix` for the chore and sends you a `request_open` relay; ask and forward it like a bug's acceptance. On approval the chore moves to Done.
```

- [ ] **Step 2: Sync and run the Batch 1 gate**

Run: `make skills-sync && go vet ./... && go test ./...`
Expected: PASS. If any install test compares skill content, it passes because the copy is synced.

- [ ] **Step 3: Commit**

```bash
git add skills/swarm-orchestrator/SKILL.md internal/install/skills/swarm-orchestrator/SKILL.md
git commit -m "docs(skills): chores confirm repos, work their own scope and finish through accept_fix

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

If `git status` shows other files changed under `internal/install/skills` after the sync, stop and report them. The sync must only touch the orchestrator skill.

---

# Batch 2: Web

Run the web commands from `web/`. Install once with `pnpm install --frozen-lockfile`.

### Task 9: Board CHORE support (copy, Done hint, levels, parents, filters)

**Files:**
- Modify: `web/src/copy.ts` (C block near `epicDone`), `web/src/logic/transitions.ts:25-38`, `web/src/logic/tree.ts:8,16`, `web/src/state/url.ts:22`, `web/src/components/Header.tsx:9`
- Test: `web/src/copy.test.ts` (the §17.3 block near `:68`), `web/src/logic/transitions.test.ts`, `web/src/logic/newItem.test.ts:10` (port), `web/src/state/url.test.ts`

**Interfaces:**
- Produces: `C.newChore`, `C.choreCaption`, `C.choreReposCaption`, `C.choreDone`. Tasks 10 and 11 use them.

- [ ] **Step 1: Write the failing tests**
  - `copy.test.ts`, in the object literal that holds `bugDone: "Accept this fix to mark it Done.",`, add:
    ```ts
          choreDone: "Accept this chore to mark it Done.",
          newChore: "New chore",
          choreCaption: "Creates a chore orchestrator that works on the scope you describe.",
          choreReposCaption: "The orchestrator asks you to confirm repositories before it starts work.",
    ```
  - `transitions.test.ts`, inside `it("routes epic and bug Done to acceptance", …)`:
    ```ts
        expect(checkMove(it_("chore", "in_review"), "done")).toEqual({ ok: false, reason: "Accept this chore to mark it Done.", special: "accept" });
    ```
  - `newItem.test.ts:10` (port): `task: ["story", "bug", "spike", "chore"]`.
  - `url.test.ts`, add to `cases`: `["#/hierarchy?type=chore", { type: "chore" }],`
  - `tree.test.ts`, add:
    ```ts
    it("puts chores at the top level and lets them hold tasks", () => {
      expect(LEVEL_TYPES.top).toEqual(["epic", "bug", "spike", "chore"]);
      expect(PARENT_TYPES.task).toEqual(["story", "bug", "spike", "chore"]);
    });
    ```
    (import `LEVEL_TYPES` and `PARENT_TYPES` from `./tree` if they aren't imported already).

- [ ] **Step 2: Run them and watch them fail**

Run: `pnpm vitest run src/copy.test.ts src/logic/transitions.test.ts src/logic/newItem.test.ts src/state/url.test.ts src/logic/tree.test.ts`
Expected: FAIL on every new assertion.

- [ ] **Step 3: Implement**
  - `copy.ts` C: next to `bugDone`, add `choreDone: "Accept this chore to mark it Done.",`. Next to `newSpike`, add `newChore: "New chore",`. Next to `debugCaption`, add `choreCaption: "Creates a chore orchestrator that works on the scope you describe.",`. Next to `reposCaption`, add `choreReposCaption: "The orchestrator asks you to confirm repositories before it starts work.",`.
  - `transitions.ts`, in the `to === "done"` switch after `case "bug":`:
    ```ts
          case "chore":
            return { ok: false, reason: C.choreDone, special: "accept" };
    ```
  - `tree.ts`: `top: ["epic", "bug", "spike", "chore"],` and `task: ["story", "bug", "spike", "chore"],`.
  - `url.ts:22` and `Header.tsx:9`: append `"chore"` to `TYPES`.

- [ ] **Step 4: Run the whole web suite and watch it pass**

Run: `pnpm test && pnpm typecheck`
Expected: PASS. If a test snapshots the type filter options or the kanban top-level card list, port it to include chore. Don't delete it.

- [ ] **Step 5: Commit**

```bash
git add web/src/copy.ts web/src/copy.test.ts web/src/logic/transitions.ts web/src/logic/transitions.test.ts web/src/logic/tree.ts web/src/logic/tree.test.ts web/src/logic/newItem.test.ts web/src/state/url.ts web/src/state/url.test.ts web/src/components/Header.tsx
git commit -m "feat(web): chores show at the top level, filter, hold tasks and route Done to acceptance

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 10: Chore on the spike wire (types, payload, mock daemon)

**Files:**
- Modify: `web/src/types.ts:348`, `web/src/logic/spawnForm.ts:37`, `web/src/mock/daemon.ts:285-300`
- Test: `web/src/mock/daemon.test.ts:184` (extend)

**Interfaces:**
- Produces: `CreateSpikeBody.intent: "feature" | "debug" | "chore"` and `SpikeFormState.intent: "feature" | "debug" | "chore"`. The mock returns `CHORE-n` Ready items. Task 11 uses all three.

- [ ] **Step 1: Write the failing test.** In `it("starts orchestrators and spikes and refuses duplicates", …)`, right after the `s3` expectation:

```ts
    const c = call(d, "POST", "/api/spikes", { request_id: "s5", name: "Bump deps", intent: "chore", agent: "claude", model: "opus" });
    expect(c.body).toMatchObject({ item: { type: "chore", status: "ready", spike_intent: null }, agent: { name: "bump-deps" } });
    expect((c.body as { item: { key: string } }).item.key).toMatch(/^CHORE-\d+$/);
```

- [ ] **Step 2: Run it and watch it fail**

Run: `pnpm vitest run src/mock/daemon.test.ts`
Expected: FAIL (the type is `spike`).

- [ ] **Step 3: Implement**
  - `types.ts`: `intent: "feature" | "debug" | "chore";` in `CreateSpikeBody`.
  - `spawnForm.ts`: `export interface SpikeFormState extends SpawnFormState { intent: "feature" | "debug" | "chore"; request: string }`.
  - `mock/daemon.ts` `createSpike`, replacing the `key` and `makeItem` lines:
    ```ts
          // Mirrors StartSpike (chore spec decision 2): intent chore is a Ready chore, not a spike.
          const chore = body.intent === "chore";
          const key = chore ? `CHORE-${++counter}` : `SPIKE-${++counter}`;
          const it = makeItem({
            key, title: body.name, brief: String(body.request ?? ""), status: chore ? "ready" : "draft",
            spike_intent: chore ? null : body.intent, repos: body.repos ?? [], created_at: NOW,
          });
    ```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `pnpm test && pnpm typecheck`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/types.ts web/src/logic/spawnForm.ts web/src/mock/daemon.ts web/src/mock/daemon.test.ts
git commit -m "feat(web): the spike wire carries intent chore; the mock creates a Ready chore

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

### Task 11: New item → Chore opens the New chore sheet

**Files:**
- Modify: `web/src/components/Header.tsx:10`, `web/src/views/props.ts:41-45`, `web/src/App.tsx:53-54,133`, `web/src/panels/NewSpikeSheet.tsx`
- Test: `web/src/App.test.tsx:130` (port), `web/src/App.flows.test.tsx` (add), `web/src/panels/NewSpikeSheet.test.tsx` (add)

**Interfaces:**
- Consumes: `C.newChore`, `C.choreCaption`, `C.choreReposCaption` (Task 9), `SpikeFormState.intent` including chore (Task 10).
- Produces: `NewSpikeSheet(p: { caption?: string; chore?: boolean; onClose(): void; onCreated(key: string): void })` and `SheetState` `{ kind: "spike"; caption?: string; chore?: boolean }`.

- [ ] **Step 1: Write the failing tests**
  - `App.test.tsx:130` (port): the menu list becomes `"Epic", "Bug", "Story", "Task", "Spike", "Chore",`.
  - `App.flows.test.tsx`, add inside `describe("App flows")`:
    ```tsx
      it("creates a chore from New item and selects it (chore spec E16)", async () => {
        const { user } = renderWithDaemon(<App />, { daemon: roomy() });
        await user.click(await screen.findByRole("button", { name: "New item" }));
        await user.click(screen.getByRole("menuitem", { name: "Chore" }));
        const sheet = await screen.findByRole("dialog", { name: "New chore" });
        expect(within(sheet).queryByText("Spikes start with an intent. Use New spike.")).not.toBeInTheDocument();
        await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Bump deps");
        await user.click(await within(sheet).findByRole("button", { name: "Start orchestrator" }));
        await waitFor(() => expect(window.location.hash).toMatch(/item=CHORE-\d+/));
        expect(screen.queryByRole("dialog", { name: "New chore" })).not.toBeInTheDocument();
      });
    ```
  - `NewSpikeSheet.test.tsx`, add:
    ```tsx
      it("creates a chore in chore mode, with no intent choice", async () => {
        const d = createMockDaemon();
        d.db.settings.max_concurrent_agents = 8;
        const onCreated = vi.fn();
        const { user } = renderWithDaemon(<NewSpikeSheet chore onClose={vi.fn()} onCreated={onCreated} />, { daemon: d, events: false });
        const sheet = await screen.findByRole("dialog", { name: "New chore" });
        expect(within(sheet).getByText("Creates a chore orchestrator that works on the scope you describe.")).toBeInTheDocument();
        expect(within(sheet).getByText("The orchestrator asks you to confirm repositories before it starts work.")).toBeInTheDocument();
        expect(within(sheet).queryByRole("radio", { name: "Feature spike" })).not.toBeInTheDocument();
        expect(within(sheet).queryByRole("radio", { name: "Debug spike" })).not.toBeInTheDocument();
        await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Bump deps");
        await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
        await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^CHORE-/)));
        expect(d.calls.find((c) => c.path === "/api/spikes")?.body).toMatchObject({ name: "Bump deps", intent: "chore" });
      });
    ```

- [ ] **Step 2: Run them and watch them fail**

Run: `pnpm vitest run src/App.test.tsx src/App.flows.test.tsx src/panels/NewSpikeSheet.test.tsx`
Expected: FAIL (no Chore menu item, no "New chore" dialog).

- [ ] **Step 3: Implement**
  - `Header.tsx:10`: `const NEW_TYPES: ItemType[] = ["epic", "bug", "story", "task", "spike", "chore"];`
  - `views/props.ts`: `| { kind: "spike"; caption?: string; chore?: boolean }`.
  - `App.tsx:53-54`:
    ```tsx
      const newItem = (type: ItemType, parentKey?: string) =>
        setSheet(type === "chore" ? { kind: "spike", chore: true }
          : type === "spike" ? { kind: "spike", caption: C.spikeViaNewItem }
          : { kind: "item", type, parentKey });
    ```
    and `:133`: `<NewSpikeSheet caption={sheet.caption} chore={sheet.chore} onClose={closeSheet} onCreated={created} />`.
  - `NewSpikeSheet.tsx`:
    - `Form` props gain `chore?: boolean`, and `NewSpikeSheet`'s props gain `chore?: boolean` (they already pass through `{...p}`).
    - Submit: `spikePayload({ name, intent: p.chore ? "chore" : intent, repos, request, fields }, p.settings, p.catalog, requestId)`.
    - Both `<Sheet title=…>` (form and load-error): `title={p.chore ? C.newChore : C.newSpike}`.
    - Replace the intent `<div className="space-y-1">…</div>` with:
      ```tsx
            {p.chore ? (
              <p className="text-muted">{C.choreCaption}</p>
            ) : (
              <div className="space-y-1">
                {/* existing Intent label, Segmented and caption, unchanged */}
              </div>
            )}
      ```
    - `<RepoPicker label={C.repositoriesOptional} caption={p.chore ? C.choreReposCaption : C.reposCaption} selected={repos} onChange={setRepos} />`

- [ ] **Step 4: Run the Batch 2 gate**

Run: `pnpm test && pnpm typecheck && cd .. && make web-build && go build ./...`
Expected: PASS. `make build` is optional, because it codesigns with `SWARM_SIGN_IDENTITY` and can fail or prompt without one. Never run `make install-daemon`.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/Header.tsx web/src/views/props.ts web/src/App.tsx web/src/App.test.tsx web/src/App.flows.test.tsx web/src/panels/NewSpikeSheet.tsx web/src/panels/NewSpikeSheet.test.tsx
git commit -m "feat(web): New item → Chore opens a New chore sheet that creates a Ready chore

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

If `make web-build` changes checked-in build output (for example an embedded web bundle), stage those paths explicitly in a separate `chore(web): rebuild bundle` commit.

---

## Final verification (orchestrator, after both batches)

1. `go vet ./... && go test ./...`
2. `make skills-sync && git diff --exit-code internal/install/skills`
3. `cd web && pnpm test && pnpm typecheck`
4. `git log --oneline origin/main..HEAD` shows 11 or 12 commits, each with the trailer.
5. Map spec §8 E1–E17 to tests:
   - E1: `TestMaterializeBuildsTheEpicTree` (start and accepted reach InProgress) plus `TestEpicAcceptanceFlow`
   - E2: `TestMaterializeDebugRootIsReady`
   - E3: `TestStartOrchestratorPromotesDraftRoot`
   - E4: `TestStartOrchestratorPromotesEvenWhenRefused`
   - E5: `TestDraftRootPromotionHonorsEarlierAccepted`
   - E6: `TestEpicAcceptanceFlow`
   - E7: `TestStartSpikeWithChoreIntentCreatesAReadyChore`
   - E8: `TestChoreAcceptanceFlow`
   - E9: `TestChoreWithNoTasksReachesDone` and `TestChoreEndToEndAcceptFix`
   - E10: `TestChoreWithNoTasksReachesDone`
   - E11: `TestChoreRootCannotProposeTopLevel`
   - E12: the existing `allowedParents` tests
   - E13: `TestCreateSpikeWithChoreIntent`
   - E14: `TestMaterializeLegacyChoreSpike`
   - E15: `TestNewChore` and `TestNewRequiresNameAndIntent`
   - E16: `App.flows` and `NewSpikeSheet` tests
   - E17: the `transitions` test
