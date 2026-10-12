import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { NewSpikeSheet } from "./NewSpikeSheet";

// jsdom has no createImageBitmap, so the decode/re-encode step is replaced by a pass-through.
vi.mock("../logic/requestImages", async (orig) => ({
  ...(await orig<typeof import("../logic/requestImages")>()),
  addImages: vi.fn(async (_n: number, files: { blob: Blob; name: string }[]) => ({
    added: files.map((f) => ({ name: f.name, blob: f.blob })),
  })),
}));

const png = (name = "a.png") => new File(["\x89PNG"], name, { type: "image/png" });
const filesDrag = (files: File[] = []) => ({
  dataTransfer: { types: ["Files"], files, dropEffect: "" },
});
const textDrag = () => ({ dataTransfer: { types: ["text/plain"], files: [], dropEffect: "" } });

async function open() {
  const daemon = createMockDaemon();
  daemon.db.settings.max_concurrent_agents = 8;
  const onCreated = vi.fn();
  const r = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
    daemon,
    events: false,
  });
  const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
  const request = within(sheet).getByRole("textbox", { name: "Request (optional)" });
  return { ...r, sheet, request, onCreated };
}

beforeEach(() => {
  let n = 0;
  URL.createObjectURL = vi.fn(() => `blob:mock/${++n}`);
  URL.revokeObjectURL = vi.fn();
});

describe("NewSpikeSheet request images: paste and drop", () => {
  it("adds a pasted image as a tile and inserts no text", async () => {
    const { sheet, request } = await open();
    fireEvent.paste(request, {
      clipboardData: { files: [png("")], types: ["Files"], getData: () => "" },
    });
    expect(await within(sheet).findByRole("img", { name: "Pasted image.png" })).toBeInTheDocument();
    expect(request).toHaveValue("");
    expect(within(sheet).getByText("1 of 10")).toBeInTheDocument();
  });

  it("keeps a clipboard file's own name and lets plain text paste through", async () => {
    const { sheet, request, user } = await open();
    fireEvent.paste(request, {
      clipboardData: { files: [png("diagram.png")], types: ["Files"], getData: () => "" },
    });
    expect(await within(sheet).findByRole("img", { name: "diagram.png" })).toBeInTheDocument();
    await user.click(request);
    await user.paste("hello");
    expect(request).toHaveValue("hello");
  });

  it("shows the drop overlay for a file drag only, and hides it on leave", async () => {
    const { sheet, request } = await open();
    const zone = request.closest("[data-drop-zone]") as HTMLElement;
    expect(zone).not.toBeNull();
    fireEvent.dragEnter(zone, textDrag());
    expect(within(sheet).queryByText("Drop images to attach")).not.toBeInTheDocument();
    fireEvent.dragEnter(zone, filesDrag());
    expect(within(sheet).getByText("Drop images to attach")).toBeInTheDocument();
    // entering a child then leaving the zone's edge must not flicker the overlay
    fireEvent.dragEnter(request, filesDrag());
    fireEvent.dragLeave(zone, filesDrag());
    expect(within(sheet).getByText("Drop images to attach")).toBeInTheDocument();
    fireEvent.dragLeave(request, filesDrag());
    expect(within(sheet).queryByText("Drop images to attach")).not.toBeInTheDocument();
  });

  it("adds dropped files, naming an unnamed one 'Dropped image.png', and hides the overlay", async () => {
    const { sheet, request } = await open();
    const zone = request.closest("[data-drop-zone]") as HTMLElement;
    const files = [png("one.png"), png("")];
    fireEvent.dragEnter(zone, filesDrag(files));
    fireEvent.drop(zone, filesDrag(files));
    expect(await within(sheet).findByRole("img", { name: "one.png" })).toBeInTheDocument();
    expect(within(sheet).getByRole("img", { name: "Dropped image.png" })).toBeInTheDocument();
    expect(within(sheet).queryByText("Drop images to attach")).not.toBeInTheDocument();
  });

  it("leaves a text drag alone inside the zone", async () => {
    const { request } = await open();
    const zone = request.closest("[data-drop-zone]") as HTMLElement;
    expect(fireEvent.dragOver(zone, textDrag())).toBe(true);
  });

  it("window guard: cancels Files dragover/drop outside the zone, leaves text drags, and detaches on unmount", async () => {
    const { unmount, request } = await open();
    const over = fireEvent.dragOver(document.body, filesDrag());
    expect(over).toBe(false); // preventDefault() was called
    expect(fireEvent.drop(document.body, filesDrag())).toBe(false);
    expect(fireEvent.dragOver(document.body, textDrag())).toBe(true);
    expect(fireEvent.drop(document.body, textDrag())).toBe(true);
    // inside the zone the guard stays out; the zone's own handler decides
    const zone = request.closest("[data-drop-zone]") as HTMLElement;
    const ev = new Event("dragover", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "dataTransfer", {
      value: { types: ["Files"], files: [], dropEffect: "" },
    });
    request.dispatchEvent(ev);
    expect(zone.contains(request)).toBe(true);
    unmount();
    expect(fireEvent.dragOver(document.body, filesDrag())).toBe(true);
    expect(fireEvent.drop(document.body, filesDrag())).toBe(true);
  });

  it("sets dropEffect to none for a Files dragover outside the zone", async () => {
    await open();
    const dt = { types: ["Files"], files: [], dropEffect: "copy" };
    fireEvent.dragOver(document.body, { dataTransfer: dt });
    expect(dt.dropEffect).toBe("none");
  });

  it("shows the paste hint as the Request placeholder on fine pointers only, Mac or other", async () => {
    vi.spyOn(window, "matchMedia").mockImplementation(
      (q) =>
        ({
          matches: q.includes("pointer: fine"),
          media: q,
          onchange: null,
          addEventListener() {},
          removeEventListener() {},
          addListener() {},
          removeListener() {},
          dispatchEvent: () => false,
        }) as MediaQueryList,
    );
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue(
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)",
    );
    const mac = await open();
    expect(mac.request).toHaveAttribute(
      "placeholder",
      "⌘V pastes an image; dropping an image file adds it",
    );
    mac.unmount();
    vi.spyOn(navigator, "userAgent", "get").mockReturnValue("Mozilla/5.0 (X11; Linux x86_64)");
    const other = await open();
    await waitFor(() =>
      expect(other.request).toHaveAttribute(
        "placeholder",
        "Ctrl+V pastes an image; dropping an image file adds it",
      ),
    );
  });

  it("gives phones (coarse pointer) no placeholder", async () => {
    const { request } = await open();
    expect(request).not.toHaveAttribute("placeholder");
  });
});
