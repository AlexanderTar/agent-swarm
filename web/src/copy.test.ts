import { describe, expect, it } from "vitest";
import { AGENT_LABEL, AGENT_LOGIN_CMD, C, ITEM_TYPE_LABEL, ROLE_LABEL, SESSION_LABEL, STATUS_LABEL, T, TYPE_PLURAL } from "./copy";

describe("copy (§17)", () => {
  it("has the item status labels (§17.2)", () => {
    expect(Object.values(STATUS_LABEL)).toEqual([
      "Draft", "Ready", "In progress", "Blocked", "In review", "Awaiting approval", "Done", "Cancelled",
    ]);
  });

  it("has the session state labels (§16.2, §17.2)", () => {
    expect(SESSION_LABEL).toMatchObject({
      running: "Running", spawning: "Starting", pause_requested: "Pause requested",
      quiescing: "Finishing current step", paused: "Paused", interrupted: "Interrupted",
      crashed: "Crashed", failed: "Failed", waiting: "Waiting", stopping: "Stopping",
      queued: "Queued", stale: "No activity for 30 min", completed: "Completed", cancelled: "Cancelled",
    });
  });

  it("has the role, type and agent labels", () => {
    expect(ROLE_LABEL).toMatchObject({ ui_reviewer: "UI reviewer", orchestrator: "Orchestrator", advisor: "Advisor", designer: "Designer" });
    expect(ROLE_LABEL.designer).toBe("Designer");
    expect(ITEM_TYPE_LABEL.chore).toBe("Chore");
    expect(TYPE_PLURAL.spike).toBe("Spikes");
    expect(AGENT_LABEL).toMatchObject({ claude: "Claude", codex: "Codex", agy: "Antigravity", cursor: "Cursor", muse: "Muse" });
  });

  // Added by the fix-round-2 rulings; queued for the P5 §17 spec pass.
  it("has the empty-agent and missing-timestamp copy", () => {
    expect(C).toMatchObject({
      chooseAgent: "Choose an agent.",
      neverScanned: "Never scanned",
      neverFetched: "Never fetched",
    });
    expect(T.catalogNeverFetched("timeout")).toBe("Never fetched. Couldn't refresh: timeout");
  });

  it("has the in-flight pause/resume labels", () => {
    expect(C).toMatchObject({ pausing: "Pausing…", resuming: "Resuming…" });
  });

  it("has the Needs-you generic row copy", () => {
    expect(C.needsYouMessage).toBe("Waiting for your input");
    expect(C.openAgentTerminal).toBe("Open agent terminal");
  });

  it("has the §17.3 and §17.4 static copy", () => {
    expect(C).toMatchObject({
      search: "Search name or key…",
      moveTo: "Move to…",
      updating: "Updating…",
      depLegend: "A → B: B waits for A",
      back: "← Back",
      addStory: "Add story",
      addTask: "Add task",
      nameEmpty: "Enter a name containing a letter or number.",
      nameTaken: "This agent name is already in use.",
      chooseRepo: "Choose at least one repository.",
      scanning: "Scanning your home folder…",
      repoMissing: "Repository is unavailable. Choose another location.",
      repoDirty: "Has uncommitted changes. The orchestrator works in its own worktree.",
      notARepo: "No git repository found in this folder.",
      modelUnavailable: "Choose a model available for this agent.",
      launchFailure: "Couldn't start orchestrator. Your entries are saved.",
      queuedCaption: "Starts when an agent slot becomes available.",
      orchestratorExists: "This item already has an orchestrator.",
      epicDone: "Accept this epic to mark it Done.",
      bugDone: "Accept this fix to mark it Done.",
      choreDone: "Accept this chore to mark it Done.",
      newChore: "New chore",
      choreCaption: "Creates a top-level chore orchestrator for maintenance, refactoring, or general work.",
      choreReposCaption: "The orchestrator asks you to confirm repositories before it starts work.",
      spikeDone: "This spike reaches Done after materialization.",
      awaitingNonSpike: "Only spikes can await approval.",
      cycle: "This dependency would create a cycle.",
      depHierarchy: "A task can't depend on its own story or epic.",
      spikeViaNewItem: "Spikes start with an intent. Use New spike.",
      staleRevision: "This item changed elsewhere. Showing its latest status.",
      staleApproval: "This request changed. Review the latest version.",
      emptyChange: "Add a comment describing what to change.",
      daemonDown: "Connection lost. Status changes are unavailable.",
      noItems: "No work items yet.",
      filteredNone: "No items match these filters.",
      kanbanNoTasks: "No tasks yet. Open Hierarchy to add or inspect work.",
      kanbanNoStories: "No stories match. Stories belong to epics.",
      kanbanFiltered: "No tasks match these filters.",
      emptyColumn: "No items",
      noDeps: "No dependencies for this item.",
      noCheckpoints: "No checkpoints yet.",
      resolved: "Already resolved.",
      outsideView: "This item is outside the current view.",
      inboxEmpty: "Nothing needs your attention.",
      hiddenByFilters: "Hidden by the current filters.",
      clearFilters: "Clear filters",
      featureCaption: "Creates a spike to explore this request and turn it into an epic.",
      debugCaption: "Creates a spike to find the root cause and turn it into a bug with a fix plan.",
      reposCaption: "The spike suggests repositories and asks you to confirm them.",
      defaultClaudeCode: "Default (Claude Code)",
      notSupported: "Not supported",
      storiesCaption: "Stories within epics",
      unassignedLane: "Unassigned · Parent missing",
    });
  });

  it("fills the templates", () => {
    expect(T.matches(12)).toBe("12 matches");
    expect(T.storyDoneDenied("STORY-40")).toBe("Couldn't move STORY-40 to Done. Complete all child tasks and their checkpoints first.");
    expect(T.taskDoneDenied("TASK-7")).toBe("Couldn't move TASK-7 to Done. No agent has reported it complete.");
    expect(T.genericStatus("In progress")).toBe("Couldn't update status. The item remains In progress.");
    expect(T.agentNotInstalled("Cursor")).toBe("Cursor isn't installed on this Mac.");
    expect(T.agentNotSignedIn("Codex", "codex login")).toBe("Codex isn't signed in. Run `codex login` in a terminal.");
    expect(T.superpowersMissing("agy")).toBe("Install the superpowers plugin for agy to run orchestrators.");
    expect(T.catalogStale("3h", "timeout")).toBe("Model list from 3h ago. Couldn't refresh: timeout");
    expect(T.modelGone("claude-opus-4-1", "Claude")).toBe("claude-opus-4-1 is no longer offered by Claude.");
    expect(T.effortUnavailable("xhigh", "Sonnet 4.6")).toBe("xhigh isn't available for Sonnet 4.6; using the default.");
    expect(T.cancelOrchestrator("auth-epic-orchestrator", 3)).toBe("Cancel auth-epic-orchestrator and its 3 agents?");
    expect(T.defaultLevel("high")).toBe("Default (high)");
    expect(T.agentName("investigate-login-crash")).toBe("Agent name: investigate-login-crash");
    expect(T.selected("endurio-chat, endurio-app")).toBe("Selected: endurio-chat, endurio-app");
    expect(T.scannedAgo("2h")).toBe("Scanned 2h ago");
    expect(T.startedFrom("SPIKE-3")).toBe("Started from SPIKE-3");
    expect(T.finished(2)).toBe("Finished (2)");
    expect(T.colHeader("In progress", 8)).toBe("In progress · 8");
    expect(T.colTooltip(8, 13)).toBe("8 of 13 items");
    expect(T.agentsTooltip(4)).toBe("4 agents working in this item and its children.");
    expect(T.typeHint("Spikes")).toBe("Spikes have no task cards yet. Switch Card level to Top-level items to see them.");
    expect(T.laneFinished(6, 4, 2, "tasks")).toBe("All 6 tasks finished · 4 done, 2 cancelled.");
    expect(T.laneDone(6, "tasks")).toBe("All 6 tasks done.");
    expect(T.progress(3, 5, "tasks")).toBe("3 of 5 tasks done");
    expect(T.blockedBy("TASK-98", 1)).toBe("Blocked by TASK-98 +1");
    expect(T.blockedBy("TASK-98", 0)).toBe("Blocked by TASK-98");
    expect(T.needs(1)).toBe("Needs 1");
    expect(T.needsYouButton(3)).toBe("Needs you 3");
    expect(T.laneHeader(9, "tasks", 2, 1)).toBe("9 tasks · 2 need you · 1 blocked");
    expect(T.laneHeader(3, "tasks", 0, 0)).toBe("3 tasks");
    expect(T.confirmNRepos(2)).toBe("Confirm 2 repositories");
    expect(T.approveSectionRow("Data model")).toBe('Approve "Data model"');
    expect(T.duplicateOf("EPIC-4")).toBe("Duplicate of EPIC-4");
    expect(T.reason("the sync queue lives in the app's data layer.")).toBe("Reason: the sync queue lives in the app's data layer.");
    expect(T.requestedBy("offline-spike-orchestrator", "12 min ago")).toBe("Requested by offline-spike-orchestrator · 12 min ago");
    expect(T.proposedBy("offline-spike-orchestrator", "3 min ago")).toBe("Proposed by offline-spike-orchestrator · 3 min ago");
    expect(T.advice("codex/gpt-6-astra (high)", "9s", "12.3k", "$0.04")).toBe("Advice · codex/gpt-6-astra (high) · 9s · 12.3k · $0.04");
    expect(T.advice("claude/claude-fable-5-1", "", "", "")).toBe("Advice · claude/claude-fable-5-1");
  });
});

describe("re-dress copy", () => {
  it("has the menubar parity labels and selection notice", () => {
    expect(C.newOrchestrator).toBe("New orchestrator");
    expect(C.choreIntent).toBe("Chore");
    expect(C.choreCaption).toBe("Creates a top-level chore orchestrator for maintenance, refactoring, or general work.");
    expect(C.agentEffort).toBe("Agent Effort");
    expect(C.advisorModel).toBe("Advisor model");
    expect(C.reposEmpty).toBe("No repositories found.");
    expect(C.reposScanning).toBe("Scanning repositories…");
    expect(C.reposUnavailable).toBe("Repositories unavailable.");
    expect(C.selectRepoHint).toBe("Click to select one or more repositories.");
    expect(C.close).toBe("Close");
    expect(C.openDetails).toBe("Item details");
    expect(T.reposNoLonger(1)).toBe("1 selected repository is no longer available.");
    expect(T.reposNoLonger(2)).toBe("2 selected repositories are no longer available.");
  });

  it("names Antigravity while keeping its login command", () => {
    expect(AGENT_LABEL.agy).toBe("Antigravity");
    expect(AGENT_LOGIN_CMD.agy).toBe("agy");
  });

  it("has the toast messages", () => {
    expect(T.toastItemCreated("TASK-9")).toBe("Created TASK-9");
    expect(T.toastStarted("auth-orch", "EPIC-3")).toBe("Started auth-orch on EPIC-3");
    expect(T.toastQueued("auth-orch")).toBe("Queued auth-orch. It starts when an agent slot becomes available.");
    expect(T.toastMoved("TASK-9", "In review")).toBe("Moved TASK-9 to In review");
    expect(T.toastDepAdded("TASK-9", "TASK-2")).toBe("TASK-9 is now blocked by TASK-2");
    expect(T.toastPausedGroup("o")).toBe("Pausing o and its agents");
    expect(T.toastReposConfirmed(1)).toBe("Confirmed 1 repository");
    expect(T.toastReposConfirmed(3)).toBe("Confirmed 3 repositories");
    expect(T.toastRescanned(12, 0)).toBe("Found 12 repositories");
    expect(T.toastRescanned(12, 1)).toBe("Found 12 repositories · 1 missing");
  });
});
