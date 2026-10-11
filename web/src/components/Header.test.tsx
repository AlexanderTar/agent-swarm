import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DEFAULT_URL, type BoardUrl } from "../state/url";
import { pickOption } from "../test/select";
import { Header } from "./Header";

const narrow = (matches: boolean) =>
  vi.spyOn(window, "matchMedia").mockImplementation(((query: string) =>
    ({ matches: query.includes("639px") ? matches : false, media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia);

function setup(url: Partial<BoardUrl> = {}, matches: number | null = null) {
  const setUrl = vi.fn();
  const user = userEvent.setup();
  render(<Header url={{ ...DEFAULT_URL, ...url }} setUrl={setUrl} matches={matches} needsYou={0} onNewSpike={() => {}} onNewItem={() => {}} />);
  return { setUrl, user };
}

afterEach(() => vi.restoreAllMocks());

describe("Header filters below sm", () => {
  it("collapses Type and Status into a Filters popover", async () => {
    narrow(true);
    const { user } = setup();
    expect(screen.getByRole("searchbox", { name: "Search name or key…" })).toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Type" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Filters" }));
    const panel = await screen.findByRole("dialog", { name: "Filters" });
    expect(within(panel).getByRole("combobox", { name: "Type" })).toBeInTheDocument();
    expect(within(panel).getByRole("combobox", { name: "Status" })).toBeInTheDocument();
  });

  it("changes a filter from inside the popover", async () => {
    narrow(true);
    const { user, setUrl } = setup();
    await user.click(screen.getByRole("button", { name: "Filters" }));
    await pickOption(user, "Type", "Tasks");
    expect(setUrl).toHaveBeenCalledWith({ type: "task" });
  });

  it("shows the active filter count on the button", () => {
    narrow(true);
    setup({ type: "task", status: "done" });
    const button = screen.getByRole("button", { name: "Filters, 2 active" });
    expect(within(button).getByText("2")).toHaveAttribute("data-slot", "badge");
  });

  it("counts a search query in the badge, since Clear filters also clears it", () => {
    narrow(true);
    setup({ q: "abc" });
    expect(screen.getByRole("button", { name: "Filters, 1 active" })).toBeInTheDocument();
  });

  it("uses a short placeholder on phones and keeps the full accessible name", () => {
    narrow(true);
    setup();
    expect(screen.getByRole("searchbox", { name: "Search name or key…" })).toHaveAttribute("placeholder", "Search");
  });

  it("has no badge without active filters", () => {
    narrow(true);
    setup();
    expect(screen.getByRole("button", { name: "Filters" })).not.toHaveTextContent(/\d/);
  });

  it("puts card level and group-by in the popover on Kanban", async () => {
    narrow(true);
    const { user } = setup({ view: "kanban" });
    expect(screen.queryByRole("combobox", { name: "Card level" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Filters" }));
    const panel = await screen.findByRole("dialog", { name: "Filters" });
    expect(within(panel).getByRole("combobox", { name: "Card level" })).toBeInTheDocument();
    expect(within(panel).getByRole("combobox", { name: "Group by" })).toBeInTheDocument();
  });

  it("offers Clear filters and the match count in the popover", async () => {
    narrow(true);
    const { user, setUrl } = setup({ type: "task" }, 3);
    await user.click(screen.getByRole("button", { name: "Filters, 1 active" }));
    const panel = await screen.findByRole("dialog", { name: "Filters" });
    expect(within(panel).getByText("3 matches")).toBeInTheDocument();
    await user.click(within(panel).getByRole("button", { name: "Clear filters" }));
    expect(setUrl).toHaveBeenCalledWith({ q: "", type: "", status: "" });
  });

  it("moves New orchestrator into the New item menu", async () => {
    narrow(true);
    const onNewSpike = vi.fn();
    const user = userEvent.setup();
    render(<Header url={DEFAULT_URL} setUrl={() => {}} matches={null} needsYou={0} onNewSpike={onNewSpike} onNewItem={() => {}} />);
    expect(screen.queryByRole("button", { name: "New orchestrator" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "New item" }));
    await user.click(await screen.findByRole("menuitem", { name: "New orchestrator" }));
    expect(onNewSpike).toHaveBeenCalledTimes(1);
  });

  it("keeps the app title for screen readers", () => {
    narrow(true);
    setup();
    expect(screen.getByRole("heading", { name: "Agent Swarm" })).toHaveClass("sr-only");
  });

  it("keeps the view tabs outside the popover", () => {
    narrow(true);
    setup();
    expect(screen.getByRole("tab", { name: "Kanban" })).toBeInTheDocument();
  });
});

describe("Header at sm and up", () => {
  it("keeps the inline filters and has no Filters button", () => {
    narrow(false);
    setup({ view: "kanban" });
    expect(screen.queryByRole("button", { name: /^Filters/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New orchestrator" })).toBeInTheDocument();
    for (const name of ["Type", "Status", "Card level", "Group by"]) expect(screen.getByRole("combobox", { name })).toBeInTheDocument();
  });
});
