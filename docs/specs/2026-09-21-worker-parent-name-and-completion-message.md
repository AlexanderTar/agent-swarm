# Fix Worker Parent Name in Brief and Completion Messaging

## Context

A child agent (`s2-fixes`) spawned to implement fixes on a task completed its work, but when asked why it did not send a message or disclosures to its orchestrator, it responded:
> "No, I didn't message an orchestrator. The brief listed `parent: none`, so there was no parent to `swarm_send` to. I reported through `swarm_checkpoint`: several `progress` checkpoints and a final `completed` one..."

### Root Cause Analysis

1. **Brief Generation in `internal/runtime/agents.go` (`Store.Spawn`)**:
   When an orchestrator spawns a child worker agent via `swarm_spawn`, it passes `ParentAgentID: a.ID`.
   In `Store.Spawn` (lines 630-653):
   ```go
   var parentID string
   switch p := in.ParentAgentID.(type) {
   case string:
       parentID = p
   case items.Item:
       parentID = p.ID
   }
   var parentParam *string
   if parentID != "" {
       var one int
       if s.DB.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, parentID).Scan(&one) == nil {
           parentParam = &parentID
       }
   }

   in.Brief.Key = it.Key
   in.Brief.Title = it.Title
   in.Brief.Name = name
   in.Brief.Role = in.Role
   in.Brief.RootKey = it.RootKey
   briefText, err := RenderBrief(in.Brief)
   ```
   `in.Brief.ParentName` is never assigned.
   In `RenderBrief(in BriefInput)` (`internal/runtime/text.go:74`):
   ```go
   parent := in.ParentName
   if parent == "" {
       parent = "none"
   }
   fmt.Fprintf(&b, "agent: %s · role: %s · parent: %s · root: %s\n", in.Name, in.Role, parent, in.RootKey)
   ```
   Because `in.Brief.ParentName` was left empty, the brief generated for the child agent contains `parent: none`.
   When the child receives its initial `assignment` message, it reads `parent: none` and concludes that it has no parent orchestrator to communicate with.

2. **Agent Representation in `swarm_read` (`internal/mcpserver/tools.go`)**:
   In `agentOut`:
   ```go
   func agentOut(a runtime.Agent) map[string]any {
       return map[string]any{"name": a.Name, "kind": a.Kind, "model": a.Model, "role": a.Role, "state": a.State}
   }
   ```
   The `parent` / `parent_name` field is completely omitted when agents inspect agent records via `swarm_read`.

3. **Completion Instructions in `skills/swarm/SKILL.md`**:
   Rule 4 tells agents to message the parent on RESUME (`swarm_send(to: "parent", kind: "finding", ...)`).
   Rule 5 tells agents to message the parent when ASKING A QUESTION (`swarm_send(to: "parent", kind: "question", ...)`).
   However, Rule 3 (checkpoints & completion) does not explicitly remind child agents to send their completion summary and disclosures to `to: "parent"` when finishing work.

---

## Locked Decisions

1. **Populate `in.Brief.ParentName` in `Store.Spawn`**:
   - In `internal/runtime/agents.go`, look up the parent agent's name when `parentID != ""` and assign:
     `in.Brief.ParentName = parentName`
   - The generated brief will now show:
     `agent: <child> · role: <role> · parent: <parent_name> · root: <root>`
   - The child agent will immediately know its parent's name from its initial assignment brief.

2. **Include `"parent": <parent_name>` in `agentOut` (`internal/mcpserver/tools.go`)**:
   - In `internal/mcpserver/tools.go`, when `a.ParentAgentID != ""`, resolve the parent agent's name and include it as `"parent"` in the dictionary returned by `agentOut`. If there is no parent, include `"parent": nil`.
   - Any agent calling `swarm_read` on agents will see the parent's name.

3. **Update `skills/swarm/SKILL.md` for Completion Messaging**:
   - In Rule 3: Add explicit instruction that upon completing their assignment (`completed`), child agents with a parent orchestrator must send a `swarm_send` to `to: "parent"` with `kind: "finding"` containing their completion summary and any disclosures/notes.
   - Run `make skills-sync` to keep `internal/install/skills/` in sync.

---

## File List

### Modified Files
- `internal/runtime/agents.go`: Query parent name and assign `in.Brief.ParentName = parentName` in `Spawn`. Expose `AgentByID(ctx, id)`.
- `internal/runtime/agents_test.go`: Add test verifying that spawning a worker populates `in.Brief.ParentName` and renders the parent's name in `a.Brief`.
- `internal/mcpserver/tools.go`: Include `"parent": parentName` in `agentOut`.
- `internal/mcpserver/tools_test.go`: Add test verifying `swarm_read` includes `parent` in agent output.
- `skills/swarm/SKILL.md`: Update Rule 3 to instruct completion messaging to parent.
- `internal/install/skills/swarm/SKILL.md`: Synced via `make skills-sync`.

---

## Verification

1. `go test -v -run TestSpawnWorkerPopulatesParentNameInBrief ./internal/runtime/...`
2. `go test -v -run TestAgentOutIncludesParent ./internal/mcpserver/...`
3. Run all runtime and mcpserver tests: `go test ./internal/runtime/... ./internal/mcpserver/...`
4. `make build && make install-daemon`
