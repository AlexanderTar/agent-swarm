import { describe, expect, it } from "vitest";
import { agentActionToast, startedToast } from "./toasts";

describe("toast builders", () => {
  it("names each agent action", () => {
    expect(agentActionToast("pause", "coder-1", "session")).toBe("Pausing coder-1");
    expect(agentActionToast("pause", "orch", "subtree")).toBe("Pausing orch and its agents");
    expect(agentActionToast("resume", "coder-1")).toBe("Resumed coder-1");
    expect(agentActionToast("cancel", "coder-1")).toBe("Cancelled coder-1");
    expect(agentActionToast("ack", "coder-1")).toBe("Acknowledged coder-1");
    expect(agentActionToast("retry", "coder-1")).toBe("Retrying coder-1");
    expect(agentActionToast("terminal", "coder-1")).toBe("Opening terminal for coder-1");
  });

  it("distinguishes queued from started", () => {
    expect(startedToast({ name: "o", state: "queued" }, "EPIC-3")).toBe("Queued o. It starts when an agent slot becomes available.");
    expect(startedToast({ name: "o", state: "active" }, "EPIC-3")).toBe("Started o on EPIC-3");
  });
});
