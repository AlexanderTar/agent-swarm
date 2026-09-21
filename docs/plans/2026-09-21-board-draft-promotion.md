# Board draft promotion — implementation plan

Spec: `docs/specs/2026-09-21-board-draft-promotion.md`. Worktree: `../agent-swarm--board-draft-promotion`, branch `fix/board-draft-promotion`. Go, `internal/mcpserver`. Fixtures: `newOrchestratorServer(t)` (`helpers_test.go:467`) seeds Epic > Story > two Tasks, all `draft`, plus an orchestrator `seed.Caller`. Call tools with `s.call(ctx, seed.Caller, "<tool>", jsonArgs)`. Read items with `s.RT.Items.Get(ctx, key)`.

Stage explicit paths only (`git add <file>`); never `git add -A`, never `--amend`.

## Task 1: spawn promotes Draft task and Draft parent story
Consumes: `items.Orchestrator(agentID, rootID string) items.Actor`; `(*items.Store).Transition(ctx, key, to, actor) (Item, error)`; `callerAgent(ctx, s, c)` returns `a` with `a.ID`, `a.RootItemID`.
Produces: `promoteDraft(ctx, s *Server, it items.Item, actor items.Actor) error` in `internal/mcpserver/orchestrator.go`.

1. Add to `orchestrator_test.go`:
```go
func spawnArgs(item string) string {
	return `{"item":"` + item + `","role":"coder","brief":{"objective":"x"},"worktrees":[]}`
}

func statusOf(t *testing.T, s *Server, key string) items.Status {
	t.Helper()
	it, err := s.RT.Items.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return it.Status
}

func TestSpawnPromotesDraftTaskAndParentStory(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if statusOf(t, s, seed.TaskKey) != items.Draft || statusOf(t, s, seed.StoryKey) != items.Draft {
		t.Fatal("fixture must start Draft")
	}
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, s, seed.TaskKey); got != items.Ready {
		t.Fatalf("task = %s, want ready", got)
	}
	if got := statusOf(t, s, seed.StoryKey); got != items.Ready {
		t.Fatalf("story = %s, want ready", got)
	}
	if got := statusOf(t, s, seed.OtherTaskKey); got != items.Draft {
		t.Fatalf("sibling task = %s, must stay draft", got)
	}
}

func TestSpawnOnReadyTaskChangesNothing(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	ctx := context.Background()
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.RT.Items.Get(ctx, seed.TaskKey)
	if _, err := s.call(ctx, seed.Caller, "swarm_spawn", spawnArgs(seed.TaskKey)); err != nil {
		t.Fatal(err)
	}
	after, _ := s.RT.Items.Get(ctx, seed.TaskKey)
	if after.Revision != before.Revision {
		t.Fatalf("revision %d -> %d: a second spawn must not re-promote", before.Revision, after.Revision)
	}
}
```
(add the `items` import if missing.)
2. Run `go test ./internal/mcpserver/ -run 'TestSpawnPromotes|TestSpawnOnReady' -v`. Expect FAIL (`task = draft`).
3. In `spawnTool`, immediately after `a, err := callerAgent(ctx, s, c)` and its error check, add:
```go
if err := promoteDraft(ctx, s, it, items.Orchestrator(a.ID, a.RootItemID)); err != nil {
	return nil, err
}
```
and add near `itemsTool`:
```go
func promoteDraft(ctx context.Context, s *Server, it items.Item, actor items.Actor) error {
	if it.Type != items.Task || it.Status != items.Draft {
		return nil
	}
	if _, err := s.RT.Items.Transition(ctx, it.Key, items.Ready, actor); err != nil {
		return err
	}
	if it.ParentKey == "" {
		return nil
	}
	parent, err := s.RT.Items.Get(ctx, it.ParentKey)
	if err != nil {
		return err
	}
	if parent.Type == items.Story && parent.Status == items.Draft {
		_, err = s.RT.Items.Transition(ctx, parent.Key, items.Ready, actor)
	}
	return err
}
```
4. Re-run the command from step 2: PASS. Run `go test ./internal/mcpserver/` all green.
5. `git add internal/mcpserver/orchestrator.go internal/mcpserver/orchestrator_test.go && git commit -m "fix(mcpserver): promote draft task and story to ready on swarm_spawn"`

## Task 2: swarm_items create accepts status
Produces: `status` honoured on `op: "create"`.

1. Add tests:
```go
func TestItemsCreateStatusReady(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	out, err := s.call(context.Background(), seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"Ready task","status":"ready"}`)
	if err != nil {
		t.Fatal(err)
	}
	var it struct{ Status string `json:"status"` }
	json.Unmarshal(mustJSON(out), &it)
	if it.Status != "ready" {
		t.Fatalf("status = %q, want ready", it.Status)
	}
}

func TestItemsCreateRejectsInProgressStatus(t *testing.T) {
	s, seed := newOrchestratorServer(t)
	if _, err := s.call(context.Background(), seed.Caller, "swarm_items",
		`{"op":"create","type":"task","parent":"`+seed.StoryKey+`","title":"x","status":"in_progress"}`); err == nil {
		t.Fatal("a new item can only start draft or ready")
	}
}
```
2. Run `go test ./internal/mcpserver/ -run 'TestItemsCreateStatus|TestItemsCreateRejects' -v`. Expect `TestItemsCreateStatusReady` FAIL (status = "draft").
3. In `itemsTool`, `case "create"`, add `Status: items.Status(in.Status),` to the `items.CreateInput{...}` literal.
4. Re-run: PASS. `go test ./internal/mcpserver/` green.
5. `git add internal/mcpserver/orchestrator.go internal/mcpserver/orchestrator_test.go && git commit -m "feat(mcpserver): let swarm_items create set draft or ready status"`

## Task 3: skill doc
Edit `skills/swarm-orchestrator/SKILL.md`, "Owning an item" list:
- After the `swarm_spawn` bullet, add: `- Spawn workers on **task** keys, not stories. A worker's checkpoints attach to its assigned item, and stories only derive their status from their tasks. If one worker covers several tasks, tell it to checkpoint each with `item: "<TASK-KEY>"`.`
- Add: `- Items you create with `swarm_items create` start as Draft. Pass `status: "ready"` for work you are about to delegate; `swarm_spawn` also moves a Draft task and its Draft story to Ready, but a Draft task can't move to In progress on its own.`
Then `git add skills/swarm-orchestrator/SKILL.md && git commit -m "docs(skills): tell orchestrators to spawn on tasks and create ready items"`.

## Task 4: full verification
`go build ./... && go vet ./... && go test ./internal/mcpserver/ ./internal/items/ ./internal/runtime/ ./internal/httpapi/` — all green. Report the outputs.

## Task 5 (addendum): promote the story even when the task is already Ready
Test first, in `orchestrator_test.go`: create a Ready task under a Draft story (`swarm_items create ... status:"ready"` on the seeded story, or `s.RT.Items.Transition(ctx, seed.TaskKey, items.Ready, items.Orchestrator(...))` directly), spawn on it, assert the story is `ready`. Run it and watch it FAIL (story stays `draft`).
Then change `promoteDraft` so only the task transition is conditional:
```go
func promoteDraft(ctx context.Context, s *Server, it items.Item, actor items.Actor) error {
	if it.Type != items.Task {
		return nil
	}
	if it.Status == items.Draft {
		if _, err := s.RT.Items.Transition(ctx, it.Key, items.Ready, actor); err != nil {
			return err
		}
	}
	if it.ParentKey == "" {
		return nil
	}
	parent, err := s.RT.Items.Get(ctx, it.ParentKey)
	if err != nil {
		return err
	}
	if parent.Type == items.Story && parent.Status == items.Draft {
		_, err = s.RT.Items.Transition(ctx, parent.Key, items.Ready, actor)
	}
	return err
}
```
Existing tests `TestSpawnPromotesDraftTaskAndParentStory` and `TestSpawnOnReadyTaskChangesNothing` must still pass (the second spawn finds task and story already Ready, so no revision change). Commit: `fix(mcpserver): promote draft parent story when the spawned task is already ready`.

## Task 6 (addendum): skill text
In `skills/swarm-orchestrator/SKILL.md` replace the two bullets added in Task 3 with:
- `- Break every story into tasks, even if it has only one. Spawn implementation workers (`coder`) on **task** keys: a worker's checkpoints attach to its assigned item, and stories only derive their status from their tasks. If one worker covers several tasks, tell it to checkpoint each with `item: "<TASK-KEY>"`. Spawn on a story only for story-wide work such as a review spanning all its tasks.`
- `- Items you create with `swarm_items create` start as Draft. Pass `status: "ready"` for stories and tasks you are about to delegate; `swarm_spawn` also moves a Draft task and its Draft story to Ready. After a task's review passes, mark it done with `swarm_items update status: "done"`. Do not mark stories done by hand: a story becomes Done when all its tasks are Done.`
Then run `make skills-sync` and commit both skill files: `docs(skills): coders on tasks, story spawns for story-wide review, stories close from their tasks`.

## Task 7 (addendum): verify
`go build ./... && go vet ./... && go test -count=1 ./internal/mcpserver/ ./internal/items/ ./internal/runtime/ ./internal/install/`. `httpapi` needs `web/dist`; skip `TestBoardServedAtRoot` in the worktree.
