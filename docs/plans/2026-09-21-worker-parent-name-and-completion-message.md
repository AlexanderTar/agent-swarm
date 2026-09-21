# Implementation Plan: Worker Parent Name & Completion Messaging

## Tasks

### Task 1: Populate `in.Brief.ParentName` in `Store.Spawn`
- **Files**:
  - `internal/runtime/agents_test.go`
  - `internal/runtime/agents.go`
- **Steps**:
  1. In `internal/runtime/agents_test.go`:
     Add test `TestSpawnWorkerPopulatesParentNameInBrief`:
     ```go
     func TestSpawnWorkerPopulatesParentNameInBrief(t *testing.T) {
         s, _, _ := newStore(t)
         ctx := context.Background()
         key, orch, _, err := s.StartSpike(ctx, SpikeInput{
             Name:   "Parent Spike",
             Intent: "feature",
             Kind:   Fake,
             Model:  "fake-1",
         })
         if err != nil {
             t.Fatal(err)
         }
         worker, _, err := s.Spawn(ctx, SpawnInput{
             ItemKey:       key,
             Role:          RoleCoder,
             Kind:          Fake,
             Model:         "fake-1",
             ParentAgentID: orch.ID,
             Name:          "child-coder",
             Brief:         BriefInput{Objective: "Implement feature"},
         })
         if err != nil {
             t.Fatal(err)
         }
         if !strings.Contains(worker.Brief, fmt.Sprintf("parent: %s", orch.Name)) {
             t.Fatalf("expected brief to contain 'parent: %s', got:\n%s", orch.Name, worker.Brief)
         }
     }
     ```
  2. Run `go test -v -run TestSpawnWorkerPopulatesParentNameInBrief ./internal/runtime/...` and observe failure (`parent: none`).
  3. In `internal/runtime/agents.go` (`Spawn`):
     ```go
     var parentID string
     switch p := in.ParentAgentID.(type) {
     case string:
         parentID = p
     case items.Item:
         parentID = p.ID
     }
     var parentParam *string
     var parentName string
     if parentID != "" {
         if s.DB.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, parentID).Scan(&parentName) == nil {
             parentParam = &parentID
         }
     }

     in.Brief.Key = it.Key
     in.Brief.Title = it.Title
     in.Brief.Name = name
     in.Brief.Role = in.Role
     in.Brief.RootKey = it.RootKey
     in.Brief.ParentName = parentName
     ```
     Also export `AgentByID`:
     ```go
     func (s *Store) AgentByID(ctx context.Context, id string) (Agent, error) {
         return s.agentByID(ctx, id)
     }
     ```
  4. Run `go test -v -run TestSpawnWorkerPopulatesParentNameInBrief ./internal/runtime/...` and verify pass.

---

### Task 2: Include `parent` in `swarm_read` (`agentOut`)
- **Files**:
  - `internal/mcpserver/tools_test.go`
  - `internal/mcpserver/tools.go`
- **Steps**:
  1. In `internal/mcpserver/tools_test.go`:
     Add or update test verifying `swarm_read` returns `parent` on worker agents.
  2. In `internal/mcpserver/tools.go`:
     Update `agentOut(s *Server, ctx context.Context, a runtime.Agent) map[string]any` or `agentOut(s *Server, a runtime.Agent)`:
     When `a.ParentAgentID != ""`, query the parent agent's name via `s.RT.AgentByID(ctx, a.ParentAgentID)` and set `out["parent"] = parent.Name`. If no parent, set `out["parent"] = nil`.
  3. Run `go test -v ./internal/mcpserver/...` and verify pass.

---

### Task 3: Update `skills/swarm/SKILL.md` & Sync
- **Files**:
  - `skills/swarm/SKILL.md`
  - `internal/install/skills/swarm/SKILL.md`
- **Steps**:
  1. Update Rule 3 in `skills/swarm/SKILL.md` to instruct sending a completion finding to `to: "parent"`.
  2. Run `make skills-sync` in the worktree.
  3. Verify all tests pass: `go test ./internal/...`.
