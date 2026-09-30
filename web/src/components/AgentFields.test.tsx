import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { prefill } from "../logic/catalog";
import { seed } from "../mock/fixtures";
import type { AgentCatalogEntry } from "../types";
import { comboText, optionTexts, pickOption } from "../test/select";
import { AgentFields, type AgentFieldsValue } from "./AgentFields";

function Host({ catalog, initial }: { catalog?: AgentCatalogEntry[]; initial?: Partial<AgentFieldsValue["choice"]> }) {
  const db = seed();
  const start = prefill(db.settings);
  const [v, setV] = useState<AgentFieldsValue>({ ...start, choice: { ...start.choice, ...initial } });
  return <AgentFields value={v} onChange={setV} settings={db.settings} catalog={catalog ?? db.catalog} />;
}

describe("AgentFields (§16.3, §16.10)", () => {
  it("prefills from Settings", () => {
    render(<Host />);
    expect(comboText("Agent")).toContain("Claude");
    expect(comboText("Model")).toBe("Opus (latest)");
    expect(comboText("Effort")).toBe("Default (high)");
    expect(comboText("Advisor")).toContain("Claude");
    expect(comboText("Advisor model")).toBe("Fable (latest)");
    expect(screen.getByText("Defaults from Settings")).toBeInTheDocument();
  });

  it("splits advisor into agent and model", async () => {
    const user = userEvent.setup();
    render(<Host />);
    expect(await optionTexts(user, "Agent")).toEqual(["Claude", "Codex"]);
    expect(await optionTexts(user, "Advisor")).toEqual(["Claude", "Codex", "No advisor"]);
    expect(await optionTexts(user, "Advisor model")).not.toContain("Haiku 4.5");
    await pickOption(user, "Advisor", "No advisor");
    expect(screen.getByRole("combobox", { name: "Advisor model" })).toBeDisabled();
    expect(comboText("Advisor model")).toBe("—");
    await pickOption(user, "Advisor", "Codex");
    expect(comboText("Advisor model")).toContain("GPT-6 Astra");
  });

  it("re-checks the model when the agent changes", async () => {
    const user = userEvent.setup();
    render(<Host />);
    await pickOption(user, "Agent", "Codex");
    expect(comboText("Model")).toBe("");
    expect(screen.getByText("Choose a model available for this agent.")).toBeInTheDocument();
    await pickOption(user, "Model", "GPT-6 Astra");
    expect(screen.queryByText("Choose a model available for this agent.")).not.toBeInTheDocument();
    expect(comboText("Effort")).toBe("Default (medium)");
  });

  it("hides effort for models without it and resets unsupported levels with a note", async () => {
    const user = userEvent.setup();
    render(<Host initial={{ effort: "xhigh" }} />);
    expect(comboText("Effort")).toBe("xhigh");
    await pickOption(user, "Model", "Sonnet 4.6");
    expect(comboText("Effort")).toBe("Default (high)");
    expect(screen.getByText("xhigh isn't available for Sonnet 4.6; using the default.")).toBeInTheDocument();
    await pickOption(user, "Model", "Haiku 4.5");
    expect(screen.queryByRole("combobox", { name: "Effort" })).not.toBeInTheDocument();
  });

  it("shows a vanished model and a stale catalog", () => {
    const db = seed();
    const claude = db.catalog[0] as AgentCatalogEntry;
    const stale = [{ ...claude, catalog_stale: true, catalog_error: "timeout", catalog_fetched_at: Date.now() - 3 * 3_600_000 }, ...db.catalog.slice(1)];
    render(<Host catalog={stale} initial={{ model: "claude-opus-4-1" }} />);
    expect(comboText("Model")).toBe("claude-opus-4-1");
    expect(screen.getByText("claude-opus-4-1 is no longer offered by Claude.")).toBeInTheDocument();
    expect(screen.getByText("Model list from 3h ago. Couldn't refresh: timeout")).toBeInTheDocument();
  });

  it("explains an agent that can't run orchestrators", () => {
    const db = seed();
    const claude = db.catalog[0] as AgentCatalogEntry;
    render(<Host catalog={[{ ...claude, superpowers: false }, ...db.catalog.slice(1)]} />);
    expect(screen.getByText("Install the superpowers plugin for Claude to run orchestrators.")).toBeInTheDocument();
  });

  it("picks no advisor", async () => {
    const user = userEvent.setup();
    render(<Host />);
    await pickOption(user, "Advisor", "No advisor");
    expect(comboText("Advisor")).toBe("No advisor");
  });

  it("expands worker roles", async () => {
    const user = userEvent.setup();
    render(<Host />);
    const trigger = screen.getByRole("button", { name: "Worker Roles" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("group", { name: "Coder" })).toBeInTheDocument();
  });

  it("links visible labels to their select triggers", async () => {
    const user = userEvent.setup();
    render(<Host />);
    const check = (label: HTMLElement | undefined, name: string) => {
      expect(label).toBeDefined();
      expect(label!).toHaveAttribute("for", screen.getByRole("combobox", { name }).id);
    };
    const labels = screen.getAllByText("Agent", { selector: "label" });
    check(labels[0], "Agent");
    check(screen.getAllByText("Model", { selector: "label" })[0], "Model");
    check(screen.getAllByText("Advisor", { selector: "label" })[0], "Advisor");
    check(screen.getAllByText("Model", { selector: "label" })[1], "Advisor model");
    await user.click(screen.getByRole("button", { name: "Worker Roles" }));
    check(screen.getAllByText("Agent", { selector: "label" })[1], "Coder Agent");
    check(screen.getAllByText("Model", { selector: "label" })[2], "Coder Model");
  });
});
