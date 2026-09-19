import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { prefill } from "../logic/catalog";
import { seed } from "../mock/fixtures";
import type { AgentCatalogEntry } from "../types";
import { AgentFields, type AgentFieldsValue } from "./AgentFields";

function Host({ catalog, initial }: { catalog?: AgentCatalogEntry[]; initial?: Partial<AgentFieldsValue["choice"]> }) {
  const db = seed();
  const start = prefill(db.settings);
  const [v, setV] = useState<AgentFieldsValue>({ ...start, choice: { ...start.choice, ...initial } });
  return <AgentFields value={v} onChange={setV} settings={db.settings} catalog={catalog ?? db.catalog} />;
}

const select = (name: string) => screen.getByRole("combobox", { name }) as HTMLSelectElement;
const selectedText = (name: string) => select(name).selectedOptions[0]?.textContent;

describe("AgentFields (§16.3, §16.10)", () => {
  it("prefills from Settings", () => {
    render(<Host />);
    expect(selectedText("Agent")).toBe("Claude");
    expect(selectedText("Model")).toBe("Opus (latest)");
    expect(selectedText("Effort")).toBe("Default (high)");
    expect(selectedText("Advisor")).toBe("Claude · Fable (latest)");
    expect(screen.getByText("Defaults from Settings")).toBeInTheDocument();
  });

  it("lists only enabled agents and advisor-capable Claude models", () => {
    render(<Host />);
    expect([...select("Agent").options].map((o) => o.textContent)).toEqual(["Claude", "Codex"]);
    const advisors = [...select("Advisor").options].map((o) => o.textContent);
    expect(advisors).toContain("Codex · GPT-6 Astra");
    expect(advisors).not.toContain("Claude · Haiku 4.5");
    expect(advisors.at(-1)).toBe("No advisor");
  });

  it("re-checks the model when the agent changes", async () => {
    const user = userEvent.setup();
    render(<Host />);
    await user.selectOptions(select("Agent"), "codex");
    expect(select("Model").value).toBe("");
    expect(screen.getByText("Choose a model available for this agent.")).toBeInTheDocument();
    await user.selectOptions(select("Model"), "gpt-6-astra");
    expect(screen.queryByText("Choose a model available for this agent.")).not.toBeInTheDocument();
    expect(selectedText("Effort")).toBe("Default (medium)");
  });

  it("hides effort for models without it and resets unsupported levels with a note", async () => {
    const user = userEvent.setup();
    render(<Host initial={{ effort: "xhigh" }} />);
    expect(select("Effort").value).toBe("xhigh");
    await user.selectOptions(select("Model"), "claude-sonnet-4-6");
    expect(select("Effort").value).toBe("");
    expect(screen.getByText("xhigh isn't available for Sonnet 4.6; using the default.")).toBeInTheDocument();
    await user.selectOptions(select("Model"), "haiku");
    expect(screen.queryByRole("combobox", { name: "Effort" })).not.toBeInTheDocument();
  });

  it("shows a vanished model and a stale catalog", () => {
    const db = seed();
    const claude = db.catalog[0] as AgentCatalogEntry;
    const stale = [{ ...claude, catalog_stale: true, catalog_error: "timeout", catalog_fetched_at: Date.now() - 3 * 3_600_000 }, ...db.catalog.slice(1)];
    render(<Host catalog={stale} initial={{ model: "claude-opus-4-1" }} />);
    expect(selectedText("Model")).toBe("claude-opus-4-1");
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
    await user.selectOptions(select("Advisor"), "none");
    expect(selectedText("Advisor")).toBe("No advisor");
  });
});
