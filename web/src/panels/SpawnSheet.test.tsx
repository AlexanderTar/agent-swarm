import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { SpawnSheet } from "./SpawnSheet";

const roomy = () => {
  const d = createMockDaemon();
  d.db.settings.max_orchestrators = 8;
  return d;
};

describe("SpawnSheet (§16.10)", () => {
  it("prefills repos, agent fields and the name", async () => {
    renderWithDaemon(<SpawnSheet itemKey="EPIC-12" onClose={vi.fn()} />, { daemon: roomy(), events: false });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    expect(within(sheet).getByText("EPIC-12 · Authentication")).toBeInTheDocument();
    expect(await within(sheet).findByText("Selected: endurio-chat")).toBeInTheDocument();
    expect((within(sheet).getByRole("combobox", { name: "Model" }) as HTMLSelectElement).selectedOptions[0]?.textContent).toBe("Opus (latest)");
    expect(within(sheet).getByRole("textbox", { name: "Name" })).toHaveValue("authentication-orchestrator");
    expect(within(sheet).getByRole("button", { name: "Start orchestrator" })).toBeEnabled();
  });

  it("uses the queued label at the orchestrator limit", async () => {
    renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, { events: false });
    expect(await screen.findByRole("button", { name: "Queue orchestrator" })).toBeInTheDocument();
    expect(screen.getByText("Starts when an agent slot becomes available.")).toBeInTheDocument();
  });

  it("re-checks the model when the agent changes and blocks submit", async () => {
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, { daemon: roomy(), events: false });
    await user.selectOptions(await screen.findByRole("combobox", { name: "Agent" }), "codex");
    expect(screen.getByText("Choose a model available for this agent.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start orchestrator" })).toBeDisabled();
  });

  it("submits the payload and closes", async () => {
    const onClose = vi.fn();
    const { user, daemon } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, { daemon: roomy(), events: false });
    const sheet = await screen.findByRole("dialog", { name: "Start orchestrator" });
    await user.click(within(await within(sheet).findByRole("group", { name: "All" })).getByRole("checkbox", { name: /agent-swarm/ }));
    const name = within(sheet).getByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "billing-orchestrator");
    await user.dblClick(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const posts = daemon.calls.filter((c) => c.path === "/api/items/EPIC-20/orchestrator");
    expect(posts[0]?.body).toMatchObject({ agent: "claude", model: "opus", repos: ["repo_swarm"], repos_version: 0, name: "billing-orchestrator", advisor: { agent: "claude", model: "fable" } });
    expect(new Set(posts.map((c) => (c.body as { request_id: string }).request_id)).size).toBe(1);
  });

  it("keeps entries after a failure and retries with a new request id", async () => {
    const d = roomy();
    let calls = 0;
    d.override("POST /api/items/EPIC-20/orchestrator", () => (++calls === 1
      ? { status: 422, body: { error: { code: "preflight_failed", message: "Commit signing is off for agent-swarm. Enable it in git config." } } }
      : { status: 200, body: d.db.agents[0] }));
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={onClose} />, { daemon: d, events: false });
    const name = await screen.findByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "kept-name");
    await user.click(screen.getByRole("button", { name: "Start orchestrator" }));
    expect(await screen.findByText("Couldn't start orchestrator. Your entries are saved.")).toBeInTheDocument();
    expect(screen.getByText("Commit signing is off for agent-swarm. Enable it in git config.")).toBeInTheDocument();
    expect(name).toHaveValue("kept-name");
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const ids = d.calls.filter((c) => c.path === "/api/items/EPIC-20/orchestrator").map((c) => (c.body as { request_id: string }).request_id);
    expect(new Set(ids).size).toBe(2);
  });

  it("shows a taken name under the field", async () => {
    const d = roomy();
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-20" onClose={vi.fn()} />, { daemon: d, events: false });
    const name = await screen.findByRole("textbox", { name: "Name" });
    await user.clear(name);
    await user.type(name, "login-review");
    await user.click(screen.getByRole("button", { name: "Start orchestrator" }));
    expect(await screen.findByText("This agent name is already in use.")).toBeInTheDocument();
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry, never a
  // silently-empty sheet. The brief's SpawnSheet returned `null` forever on any of its four queries
  // failing.
  it("surfaces a failed load with a working retry (standing rule)", async () => {
    const d = roomy();
    const real = d.handle({ method: "GET", url: "/api/items/EPIC-12", headers: { authorization: `Bearer ${d.db.token}` } });
    let attempt = 0;
    d.override("GET /api/items/EPIC-12", () => {
      attempt += 1;
      if (attempt === 1) return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load the item." } } };
      return real;
    });
    const { user } = renderWithDaemon(<SpawnSheet itemKey="EPIC-12" onClose={vi.fn()} />, { daemon: d, events: false });
    // Note: the error state (SpawnSheet -> Sheet directly) and the loaded state (SpawnSheet -> Form
    // -> Sheet) are different element types at the same tree position, so React unmounts and
    // remounts the whole <Sheet> (a fresh dialog DOM node) once the retry succeeds. Re-query via
    // `screen` after the retry instead of reusing a captured dialog reference, which would go stale.
    expect(await screen.findByText("Couldn't load the item.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Couldn't load the item.")).not.toBeInTheDocument());
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeInTheDocument();
  });
});
