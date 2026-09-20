# Claude Usage, Settings Fixes, Fork Budget Enforcement, and Generalist Verification Gate

## Context

Four interconnected issues require fixing in `agent-swarm`:
1. **Claude Menubar Indicator**: The usage indicator currently displays weekly usage (`seven_day`) rather than the 5-hour session usage due to a regression trusting Anthropic's `anthropic-ratelimit-unified-representative-claim` header.
2. **Settings Save Failure**: Attempting to reduce the number of concurrent agents via the menubar fails. The Swift `Settings` struct lacks `max_concurrent_subagents`, resulting in an omitted field during JSON serialization. The Go daemon strictly validates this field (`1..16`) and rejects the payload with a 400 Bad Request. In addition, the `LimitField` UI immediately resets the input value if the limit is exceeded temporarily, confusing users.
3. **Claude Subagent Evasion via Forks**: Claude agents are circumventing the subagent concurrency limits by using newly introduced tools (`Agent`, `Fork`, `fork`) or by launching `claude` subprocesses directly via bash.
4. **Tooling Gate Mismatch in Checkpoint Verification**: The orchestration daemon hardcodes strict TDD dogma (`sawRed && last.Phase == "green" && last.OK`) into `internal/runtime/checkpoint.go:tddOK`. Tasks scoped to writing failing tests (such as TASK-66), reproductions, refactors, benchmarks, or chores get rejected with `"TDD evidence missing"` even after producing verified red-phase test runs. The orchestration layer must not bake a specific development methodology into its database state machine. Instead, it must enforce the generalist invariant: **Evidence Before Completion** (requiring verification commands and results, without dictating red/green phases).

## Locked Decisions
- The menubar usage indicator will **always** prioritize the `"five_hour"` meter if present, irrespective of the `representative-claim` header.
- `maxConcurrentSubagents` will be fully supported in the macOS menubar app's UI and data models.
- Any attempt by Claude to spawn a worker (via `Agent` tool, `Fork` tool, `fork` tool, or `bash` execution of `claude`) will be intercepted and subjected to the same concurrency budget as `invoke_subagent`.
- Sessions started with `source: "fork"` will consume the parent's concurrency budget.
- **Generalist Verification**: The checkpoint completion gate (`internal/runtime/checkpoint.go`) will enforce that verification evidence was recorded (`len(Verification) > 0` with non-empty `Cmd`), replacing the methodology-bound `tddOK` with `verifyOK`. TDD discipline remains enforced at the skill and peer-review level, not in the database gate.

## DB / Settings Schema Changes
None. The Go backend (`internal/settings/settings.go`) already expects and validates `MaxConcurrentSubagents`. The database schema for `checkpoints.verify_json` and `items.tdd_exempt` remains backwards-compatible.

## Model / API Types

**Swift (`apps/menubar/Sources/SwarmBarKit/Wire.swift`)**
```swift
public struct Settings: Codable, Sendable, Equatable {
    // ... existing fields ...
    public var maxConcurrentSubagents: Int = 3
    
    enum CodingKeys: String, CodingKey {
        // ... existing keys ...
        case maxConcurrentSubagents = "max_concurrent_subagents"
    }
}
```

## UI / Menubar Changes
1. **`SettingsModel.swift`**:
   - Simplify `SettingsModel.Limit` enum to only the two real settings: `.subagents` (`1...16`) and `.pauseDeadline` (`30...600`).
   - Remove `.orchestrators`, `.agents`, and `.agentsPerRoot` from `Limit` so only the enforced subagents setting is exposed.
   - Wire `value` and `store` cases for `.subagents` mapping to `settings.maxConcurrentSubagents`.
   - Update `overLimit` to count active subagents under any parent against the new `.subagents` limit.
2. **`SettingsView.swift`**:
   - In `LimitsTab`, display only one subagent limit field:
     ```swift
     LimitField(model: model, title: Copy.maxSubagents, limit: .subagents)
     Text(Copy.subagentsLimitCaption).font(.caption).foregroundStyle(.secondary)
     ```
     followed by `pauseDeadline`.
   - In `LimitField.commit()`, prevent immediate reset of `text` to `model.value(limit)` until the user-intended change is processed or rejected.
3. **`Copy.swift`**:
   - Add `public static let maxSubagents = "Max concurrent subagents"`
   - Add `public static let subagentsLimitCaption = "Maximum subagents each parent agent may run concurrently."`

## Runtime & Hook Handler Changes
1. **`internal/usage/claude.go`**:
   - Update lines 132-135: Explicitly check for `"five_hour"` in `meters` and set it as `HeadlineID` whenever present.
2. **`internal/hook/handler.go`**:
   - In `case "PreToolUse":`:
     - Unconditionally block all native fork and subagent tools (`Agent`, `Task`, `Fork`, `fork`, `invoke_subagent`, `subagent`, `dispatch_agent`, `spawn_agent`):
       ```go
       isNativeFork := in.ToolName == "Agent" ||
           in.ToolName == "Task" ||
           in.ToolName == "Fork" ||
           in.ToolName == "fork" ||
           in.ToolName == "invoke_subagent" ||
           in.ToolName == "subagent" ||
           in.ToolName == "dispatch_agent" ||
           in.ToolName == "spawn_agent"

       if isNativeFork {
           return adapter.HookDecision{
               Block:  true,
               Reason: "[swarm] Native forks and subagents are disabled. Use swarm_spawn to delegate work to Swarm-managed agents, or execute tasks sequentially in this session.",
           }, nil
       }
       ```
     - In `in.Command != ""`:
       Block shell commands attempting to invoke `claude` (e.g. `claude `, `claude\n`, `claude -p`, `claude --fork`) to prevent launching unmanaged subprocesses:
       ```go
       if isClaudeCLI(in.Command) {
           return adapter.HookDecision{
               Block:  true,
               Reason: "[swarm] Nested agent invocations via shell are disabled. Use swarm_spawn to delegate work.",
           }, nil
       }
       ```
     - For Swarm's own `swarm_spawn` tool, enforce `max_concurrent_subagents` budget against DB-tracked child agents as before.
   - In `case "SessionStart":`:
     - If `in.Source == "fork"`, block/terminate the forked session immediately:
       ```go
       if in.Source == "fork" {
           return adapter.HookDecision{
               Block:  true,
               Reason: "[swarm] Forked sessions are disabled. Work must run within the assigned Swarm session.",
           }, nil
       }
       ```
3. **`internal/runtime/checkpoint.go`**:
   - Replace `tddMissing` constant with:
     ```go
     const verifyMissing = "Verification evidence missing: record what was run to verify this work before completing."
     ```
   - Replace `tddOK(prior, now []Verify) bool` with:
     ```go
     func verifyOK(prior, now []Verify) bool {
         all := append(append([]Verify{}, prior...), now...)
         if len(all) == 0 {
             return false
         }
         for _, v := range all {
             if strings.TrimSpace(v.Cmd) != "" {
                 return true
             }
         }
         return false
     }
     ```
   - In `WriteCheckpoint`, gate `CompletedCkp` on `verifyOK` when `it.TddExempt == ""` and `gated`.
   - `it.TddExempt != ""` continues to skip verification for non-code items (e.g. pure docs/spikes).

## Verification Scenarios
1. **Claude Usage Indicator**: Test with headers containing both `5h` and `7d` meters where `representative-claim` is `seven_day`. Verify `HeadlineID` is `"five_hour"`.
2. **Settings Save**: In menubar, modify `maxAgents` and `maxConcurrentSubagents`. Verify save returns HTTP 200 and persists to daemon database without 400 error.
3. **Fork Budget Interception**:
   - Tool `Agent` with `subagent_type: "fork"` is blocked when subagents >= limit.
   - Command `claude -p ...` in Bash is blocked when subagents >= limit.
4. **Generalist Verification Gate**:
   - A task completing with red-only test verification (`phase: "red", ok: false`) succeeds and transitions to `InReview`.
   - A task completing with standard green verification succeeds and transitions to `InReview`.
   - A task completing with zero verification records is rejected with `Verification evidence missing`.

## Out of Scope
- Altering the SQLite table schema for `items` or `checkpoints`.
- Eliminating peer review (`InReview` status transition).
