import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { pickOption } from "../test/select";
import { NewSpikeSheet } from "./NewSpikeSheet";

describe("NewSpikeSheet (§16.3, I15)", () => {
  it("stacks label over a wrapping Intent control and the footer below sm", async () => {
    renderWithDaemon(<NewSpikeSheet intent="feature" onClose={vi.fn()} onCreated={vi.fn()} />, {
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    const group = within(sheet).getByRole("radiogroup", { name: "Intent" });
    expect(group).toHaveClass("max-sm:flex-wrap");
    expect(group.parentElement).toHaveClass("max-sm:grid-cols-1");
    expect(within(sheet).getByRole("button", { name: "Cancel" }).parentElement).toHaveClass(
      "max-sm:flex-col-reverse",
      "max-sm:items-stretch",
    );
    // Agent rows stack label over control too, not just Intent.
    expect(within(sheet).getByRole("combobox", { name: "Agent" }).parentElement).toHaveClass(
      "max-sm:grid-cols-1",
    );
  });

  it("previews the agent name, validates it and switches the intent caption", async () => {
    const { user } = renderWithDaemon(
      <NewSpikeSheet intent="feature" onClose={vi.fn()} onCreated={vi.fn()} />,
      { events: false },
    );
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    expect(
      within(sheet).getByText("Creates a spike to explore this request and turn it into an epic."),
    ).toBeInTheDocument();
    expect(within(sheet).getByText("Repositories (optional)")).toBeInTheDocument();
    expect(
      within(sheet).getByText("The spike suggests repositories and asks you to confirm them."),
    ).toBeInTheDocument();
    const name = within(sheet).getByRole("textbox", { name: "Name" });
    await user.type(name, "Investigate login crash");
    expect(within(sheet).getByText("Agent name: investigate-login-crash")).toBeInTheDocument();
    expect(name).toHaveValue("Investigate login crash");
    await user.clear(name);
    await user.type(name, "???");
    expect(
      within(sheet).getByText("Enter a name containing a letter or number."),
    ).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Queue orchestrator" })).toBeDisabled();
    await user.click(within(sheet).getByRole("radio", { name: "Debug spike" }));
    expect(
      within(sheet).getByText(
        "Creates a spike to find the root cause and turn it into a bug with a fix plan.",
      ),
    ).toBeInTheDocument();
  });

  it("creates the spike with intent, repos, request and agent choice", async () => {
    const d = createMockDaemon();
    d.db.settings.max_concurrent_agents = 8;
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Offline sync");
    await user.click(within(sheet).getByRole("radio", { name: "Debug spike" }));
    await user.click(within(sheet).getByRole("option", { name: /endurio-chat/ }));
    await pickOption(user, "Effort", "max");
    await user.type(
      within(sheet).getByRole("textbox", { name: "Request (optional)" }),
      "App loses messages offline",
    );
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^SPIKE-/)));
    expect(d.calls.find((c) => c.path === "/api/spikes")?.body).toMatchObject({
      name: "Offline sync",
      intent: "debug",
      repos: ["repo_chat"],
      agent: "claude",
      model: "opus",
      effort: "max",
      advisor: { agent: "claude", model: "fable" },
      request: "App loses messages offline",
    });
  });

  it("drops a stale effort Settings holds for a model that no longer offers it (progress.md:77 carry)", async () => {
    // Same repro as SpawnSheet: claude-haiku-4-5-20251001 has efforts: [], so a stored "high" is
    // stale. The Effort control is correctly hidden, but the submit must not still carry it.
    const d = createMockDaemon();
    d.db.settings.max_concurrent_agents = 8;
    d.db.settings.roles.orchestrator = { agent: "claude", model: "haiku", effort: "high" };
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    expect(within(sheet).queryByRole("combobox", { name: "Effort" })).not.toBeInTheDocument();
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Offline sync");
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    const post = d.calls.find((c) => c.path === "/api/spikes");
    expect(post?.body).toMatchObject({ agent: "claude", model: "haiku" });
    expect(post?.body).not.toHaveProperty("effort");
  });

  it("shows a taken name under the field", async () => {
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      events: false,
    });
    await user.type(await screen.findByRole("textbox", { name: "Name" }), "Login review");
    await user.click(screen.getByRole("button", { name: "Queue orchestrator" }));
    expect(await screen.findByText("This agent name is already in use.")).toBeInTheDocument();
  });

  // Standing rule (flagged, not in the brief): a failed query gets a message + retry, never a
  // silently-empty sheet. The brief's NewSpikeSheet returned `null` forever on any of its three
  // queries failing.
  it("surfaces a failed load with a working retry (standing rule)", async () => {
    const d = createMockDaemon();
    const real = d.handle({
      method: "GET",
      url: "/api/settings",
      headers: { authorization: `Bearer ${d.db.token}` },
    });
    let attempt = 0;
    d.override("GET /api/settings", () => {
      attempt += 1;
      if (attempt === 1)
        return {
          status: 500,
          body: { error: { code: "internal", message: "x", reason: "Couldn't load settings." } },
        };
      return real;
    });
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    // Note: the error state (NewSpikeSheet -> Sheet directly) and the loaded state (NewSpikeSheet ->
    // Form -> Sheet) are different element types at the same tree position, so React unmounts and
    // remounts the whole <Sheet> (a fresh dialog DOM node) once the retry succeeds. Re-query via
    // `screen` after the retry instead of reusing a captured dialog reference, which would go stale.
    expect(await screen.findByText("Couldn't load settings.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(screen.queryByText("Couldn't load settings.")).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeInTheDocument();
  });

  it("offers Chore and sends it", async () => {
    const d = createMockDaemon();
    d.db.settings.max_concurrent_agents = 8;
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Bump deps");
    await user.click(within(sheet).getByRole("radio", { name: "Chore" }));
    expect(
      within(sheet).getByText(
        "Creates a top-level chore orchestrator for maintenance, refactoring, or general work.",
      ),
    ).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Start orchestrator" }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^CHORE-/)));
    expect(d.calls.find((c) => c.path === "/api/spikes")?.body).toMatchObject({
      name: "Bump deps",
      intent: "chore",
    });
    expect(await screen.findByText(/^(Started|Queued) /)).toBeInTheDocument();
  });

  it("presets the intent", async () => {
    renderWithDaemon(<NewSpikeSheet intent="chore" onClose={vi.fn()} onCreated={vi.fn()} />, {
      events: false,
    });
    expect(await screen.findByRole("radio", { name: "Chore" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  describe("optional name (menu bar parity)", () => {
    const startBtn = (sheet: HTMLElement) =>
      within(sheet).getByRole("button", { name: /^(Start|Queue) orchestrator$/ });
    const open = async (daemon = createMockDaemon(), onCreated = vi.fn()) => {
      daemon.db.settings.max_concurrent_agents = 8;
      const r = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
        daemon,
        events: false,
      });
      const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
      return { ...r, sheet, onCreated };
    };

    it("defaults the intent to Chore", async () => {
      const { sheet } = await open();
      expect(within(sheet).getByRole("radio", { name: "Chore" })).toHaveAttribute(
        "aria-checked",
        "true",
      );
    });

    it("enables Start by Name or Request, never by neither", async () => {
      const { sheet, user } = await open();
      expect(startBtn(sheet)).toBeDisabled();
      await user.type(
        within(sheet).getByRole("textbox", { name: "Request (optional)" }),
        "Fix login",
      );
      expect(startBtn(sheet)).toBeEnabled();
      await user.clear(within(sheet).getByRole("textbox", { name: "Request (optional)" }));
      expect(startBtn(sheet)).toBeDisabled();
      await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "Fix login");
      expect(startBtn(sheet)).toBeEnabled();
    });

    it("shows the name-or-request error only after Name was typed then cleared", async () => {
      const { sheet, user } = await open();
      const name = within(sheet).getByRole("textbox", { name: "Name" });
      expect(within(sheet).queryByText("Give a name or a request.")).not.toBeInTheDocument();
      await user.type(name, "a");
      expect(within(sheet).queryByText("Give a name or a request.")).not.toBeInTheDocument();
      await user.clear(name);
      expect(within(sheet).getByText("Give a name or a request.")).toBeInTheDocument();
      await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "x");
      expect(within(sheet).queryByText("Give a name or a request.")).not.toBeInTheDocument();
    });

    it("hides the Agent name preview until a kebab name exists, with the Optional placeholder", async () => {
      const { sheet, user } = await open();
      const name = within(sheet).getByRole("textbox", { name: "Name" });
      expect(name).toHaveAttribute("placeholder", "Optional if you write a request");
      expect(within(sheet).queryByText(/^Agent name:/)).not.toBeInTheDocument();
      await user.type(name, "???");
      expect(within(sheet).queryByText(/^Agent name:/)).not.toBeInTheDocument();
      await user.clear(name);
      await user.type(name, "Fix It");
      expect(within(sheet).getByText("Agent name: fix-it")).toHaveClass("font-mono");
    });

    it('posts name "" and a trimmed request when only a Request is written, titled from it', async () => {
      const d = createMockDaemon();
      const { sheet, user, onCreated } = await open(d);
      await user.type(
        within(sheet).getByRole("textbox", { name: "Request (optional)" }),
        "  \n  Fix the login loop  \nmore",
      );
      await user.click(startBtn(sheet));
      await waitFor(() => expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^CHORE-/)));
      const body = d.calls.find((c) => c.path === "/api/spikes")?.body;
      expect(body).toMatchObject({
        name: "",
        intent: "chore",
        request: "Fix the login loop  \nmore",
      });
      expect(d.db.items.find((i) => i.key === onCreated.mock.calls[0]?.[0])?.title).toBe(
        "Fix the login loop",
      );
    });

    it("shows a spinner on Start while sending and keeps the label", async () => {
      const d = createMockDaemon();
      let release: () => void = () => {};
      const gate = new Promise<void>((r) => {
        release = r;
      });
      const { sheet, user } = await open(d);
      const real = globalThis.fetch;
      vi.stubGlobal("fetch", async (...a: Parameters<typeof fetch>) => {
        if (String(a[0]).endsWith("/api/spikes")) await gate;
        return real(...a);
      });
      await user.type(within(sheet).getByRole("textbox", { name: "Name" }), "x");
      await user.click(startBtn(sheet));
      expect(startBtn(sheet)).toBeDisabled();
      expect(startBtn(sheet).querySelector("svg.animate-spin")).not.toBeNull();
      release();
    });
  });
});
