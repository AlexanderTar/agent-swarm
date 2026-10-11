import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { toast as sonner } from "sonner";
import { App } from "../App";
import { renderWithDaemon } from "../test/render";
import { Sheet } from "./Sheet";
import { Toaster } from "./Toast";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectTrigger, SelectValue } from "./ui/select";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

// Below md every control is 44 px; classes at md and up stay as they were (DS "Mobile / touch").
describe("touch primitives (max-md 44 px)", () => {
  it.each([
    ["default", "max-md:h-11"],
    ["xs", "max-md:h-11"],
    ["sm", "max-md:h-11"],
    ["lg", "max-md:h-11"],
    ["icon", "max-md:size-11"],
    ["icon-xs", "max-md:size-11"],
    ["icon-sm", "max-md:size-11"],
    ["icon-lg", "max-md:size-11"],
  ] as const)("Button size %s has %s and keeps its desktop class", (size, cls) => {
    render(<Button size={size}>go</Button>);
    const button = screen.getByRole("button", { name: "go" });
    expect(button).toHaveClass(cls);
    expect(button.className).toMatch(/\b(h-8|h-6|h-7|h-10|size-8|size-6|size-10)\b/);
  });

  it("Input is 44 px below md and h-8 above", () => {
    render(<Input aria-label="q" />);
    expect(screen.getByLabelText("q")).toHaveClass("h-8", "max-md:h-11");
  });

  it("SelectTrigger is 44 px below md for both sizes", () => {
    render(<Select><SelectTrigger aria-label="t"><SelectValue /></SelectTrigger></Select>);
    const cls = screen.getByRole("combobox", { name: "t" }).className;
    expect(cls).toContain("data-[size=default]:h-8");
    expect(cls).toContain("max-md:data-[size=default]:h-11");
    expect(cls).toContain("max-md:data-[size=sm]:h-11");
  });

  it("TabsTrigger is 44 px below md", () => {
    render(<Tabs defaultValue="a"><TabsList aria-label="v"><TabsTrigger value="a">A</TabsTrigger></TabsList></Tabs>);
    expect(screen.getByRole("tab", { name: "A" })).toHaveClass("max-md:min-h-11");
    expect(screen.getByRole("tablist")).toHaveClass("max-md:group-data-[orientation=horizontal]/tabs:h-auto");
  });
});

describe("responsive shell", () => {
  it("uses the dynamic viewport height and covers the notch", () => {
    const { container } = renderWithDaemon(<App />, { events: false });
    expect(container.firstElementChild).toHaveClass("h-dvh");
    expect(container.firstElementChild).not.toHaveClass("h-screen");
    expect(readFileSync(join(process.cwd(), "index.html"), "utf8")).toContain("viewport-fit=cover");
  });

  it("pads the header by the safe-area insets", () => {
    renderWithDaemon(<App />, { events: false });
    const cls = screen.getByRole("banner").className;
    expect(cls).toContain("pt-[max(0.5rem,env(safe-area-inset-top))]");
    expect(cls).toContain("pl-[max(1rem,env(safe-area-inset-left))]");
    expect(cls).toContain("pr-[max(1rem,env(safe-area-inset-right))]");
  });

  it("pads the sheet footer by the bottom inset", () => {
    render(<Sheet title="T" onClose={() => {}} footer={<span>f</span>}>body</Sheet>);
    expect(document.querySelector("[data-slot=sheet-footer]")!.className).toContain("pb-[max(0.75rem,env(safe-area-inset-bottom))]");
  });

  it("places toasts top-center below sm and bottom-left above", async () => {
    const mq = (matches: boolean) => vi.spyOn(window, "matchMedia").mockImplementation(((query: string) =>
      ({ matches, media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia);
    const toaster = () => document.querySelector("[data-sonner-toaster]");
    mq(true);
    const narrow = render(<Toaster />);
    act(() => { sonner.error("narrow"); });
    await waitFor(() => expect(toaster()).toBeInTheDocument());
    expect(toaster()).toHaveAttribute("data-x-position", "center");
    expect(toaster()).toHaveAttribute("data-y-position", "top");
    narrow.unmount();
    sonner.dismiss();
    mq(false);
    render(<Toaster />);
    act(() => { sonner.error("wide"); });
    await waitFor(() => expect(toaster()).toBeInTheDocument());
    expect(toaster()).toHaveAttribute("data-x-position", "left");
    expect(toaster()).toHaveAttribute("data-y-position", "bottom");
    vi.restoreAllMocks();
  });
});
