import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { NewSpikeSheet } from "./NewSpikeSheet";

describe("NewSpikeSheet (§16.3, I15)", () => {
  it("previews the agent name, validates it and switches the intent caption", async () => {
    const { user } = renderWithDaemon(<NewSpikeSheet caption="Spikes start with an intent. Use New spike." onClose={vi.fn()} onCreated={vi.fn()} />, { events: false });
    const sheet = await screen.findByRole("dialog", { name: "New spike" });
    expect(within(sheet).getByText("Spikes start with an intent. Use New spike.")).toBeInTheDocument();
    expect(within(sheet).getByText("Creates a spike to explore this request and turn it into an epic.")).toBeInTheDocument();
    expect(within(sheet).getByText("Repositories (optional)")).toBeInTheDocument();
    expect(within(sheet).getByText("The spike suggests repositories and asks you to confirm them.")).toBeInTheDocument();
    const name = within(sheet).getByRole("textbox", { name: "Name" });
    await user.type(name, "Investigate login crash");
    expect(within(sheet).getByText("Agent name: investigate-login-crash")).toBeInTheDocument();
    expect(name).toHaveValue("Investigate login crash");
    await user.clear(name);
    await user.type(name, "???");
    expect(within(sheet).getByText("Enter a name containing a letter or number.")).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Queue orchestrator" })).toBeDisabled();
    await user.click(within(sheet).getByRole("radio", { name: "Debug spike" }));
    expect(within(sheet).getByText("Creates a spike to find the root cause and turn it into a bug with a fix plan.")).toBeInTheDocument();
  });

  it("creates the spike with intent, repos, request and agent choice", async () => {
    const d = createMockDaemon();
    d.db.settings.max_concurrent_agents = 8;
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, { daemon: d, events: false });
    const sheet = await screen.findByRole("dialog", { name: "New spike" });
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Offline sync");
    await user.click(within(sheet).getByRole("radio", { name: "Debug spike" }));
    await user.click(within(await within(sheet).findByRole("group", { name: "Recent" })).getByRole("checkbox", { name: /endurio-chat/ }));
    await user.selectOptions(within(sheet).getByRole("combobox", { name: "Effort" }), "max");
    await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "App loses messages offline");
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^SPIKE-/)));
    expect(d.calls.find((c) => c.path === "/api/spikes")?.body).toMatchObject({
      name: "Offline sync", intent: "debug", repos: ["repo_chat"], agent: "claude", model: "opus", effort: "max",
      advisor: { agent: "claude", model: "fable" }, request: "App loses messages offline",
    });
  });

  it("drops a stale effort Settings holds for a model that no longer offers it (progress.md:77 carry)", async () => {
    // Same repro as SpawnSheet: claude-haiku-4-5-20251001 has efforts: [], so a stored "high" is
    // stale. The Effort control is correctly hidden, but the submit must not still carry it.
    const d = createMockDaemon();
    d.db.settings.max_concurrent_agents = 8;
    d.db.settings.roles.orchestrator = { agent: "claude", model: "haiku", effort: "high" };
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, { daemon: d, events: false });
    const sheet = await screen.findByRole("dialog", { name: "New spike" });
    expect(within(sheet).queryByRole("combobox", { name: "Effort" })).not.toBeInTheDocument();
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Offline sync");
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    const post = d.calls.find((c) => c.path === "/api/spikes");
    expect(post?.body).toMatchObject({ agent: "claude", model: "haiku" });
    expect(post?.body).not.toHaveProperty("effort");
  });

  it("shows a taken name under the field", async () => {
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, { events: false });
    await user.type(await screen.findByRole("textbox", { name: "Name" }), "Login review");
    await user.click(screen.getByRole("button", { name: "Queue orchestrator" }));
    expect(await screen.findByText("This agent name is already in use.")).toBeInTheDocument();
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry, never a
  // silently-empty sheet. The brief's NewSpikeSheet returned `null` forever on any of its three
  // queries failing.
  it("surfaces a failed load with a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    const real = d.handle({ method: "GET", url: "/api/settings", headers: { authorization: `Bearer ${d.db.token}` } });
    let attempt = 0;
    d.override("GET /api/settings", () => {
      attempt += 1;
      if (attempt === 1) return { status: 500, body: { error: { code: "internal", message: "x", reason: "Couldn't load settings." } } };
      return real;
    });
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, { daemon: d, events: false });
    // Note: the error state (NewSpikeSheet -> Sheet directly) and the loaded state (NewSpikeSheet ->
    // Form -> Sheet) are different element types at the same tree position, so React unmounts and
    // remounts the whole <Sheet> (a fresh dialog DOM node) once the retry succeeds. Re-query via
    // `screen` after the retry instead of reusing a captured dialog reference, which would go stale.
    expect(await screen.findByText("Couldn't load settings.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Couldn't load settings.")).not.toBeInTheDocument());
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeInTheDocument();
  });
});
