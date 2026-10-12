import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { addImages } from "../logic/requestImages";
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

const png = (name = "a.png") =>
  new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
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

describe("NewSpikeSheet request images: submit", () => {
  const start = (sheet: HTMLElement) =>
    within(sheet).getByRole("button", { name: /^(Start|Queue|Try again)/ });
  const paste = (request: HTMLElement, ...names: string[]) =>
    fireEvent.paste(request, {
      clipboardData: { files: names.map((n) => png(n)), types: ["Files"], getData: () => "" },
    });

  it("posts base64 attachments for the images, none without images", async () => {
    const { sheet, request, user, daemon, onCreated } = await open();
    await user.type(request, "Look at these");
    paste(request, "a.png", "b.png");
    await within(sheet).findByText("2 of 10");
    await user.click(start(sheet));
    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    const body = daemon.calls.find((c) => c.path === "/api/spikes")?.body as {
      attachments?: { name: string; data: string }[];
    };
    expect(body.attachments).toHaveLength(2);
    expect(body.attachments?.[0]).toEqual({ name: "a.png", data: btoa("\x89PNG") });
  });

  it("omits attachments with no images", async () => {
    const { sheet, request, user, daemon, onCreated } = await open();
    await user.type(request, "Just text");
    await user.click(start(sheet));
    await waitFor(() => expect(onCreated).toHaveBeenCalled());
    expect(daemon.calls.find((c) => c.path === "/api/spikes")?.body).not.toHaveProperty(
      "attachments",
    );
  });

  it("keeps Start disabled, without a spinner, while a tile is still being read", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    vi.mocked(addImages).mockImplementationOnce(async (_n, files) => {
      await gate;
      return { added: files };
    });
    const { sheet, request, user } = await open();
    await user.type(request, "Look");
    paste(request, "slow.png");
    expect(start(sheet)).toBeDisabled();
    expect(start(sheet).querySelector("svg.animate-spin")).toBeNull();
    expect(sheet.querySelector('li[aria-hidden="true"]')).not.toBeNull();
    release();
    await within(sheet).findByRole("img", { name: "slow.png" });
    expect(start(sheet)).toBeEnabled();
  });

  it("shows the images-not-saved state, scrolls to the top, and finishes only on Done", async () => {
    const d = createMockDaemon();
    d.db.failAttachments = true;
    d.db.settings.max_concurrent_agents = 8;
    const onCreated = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={onCreated} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Request (optional)" });
    await user.type(request, "Look");
    paste(request, "a.png");
    await within(sheet).findByText("1 of 10");
    const body = sheet.querySelector<HTMLElement>(".overflow-y-auto") as HTMLElement;
    let top = 120;
    Object.defineProperty(body, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    });
    await user.click(start(sheet));
    const alert = await within(sheet).findByText(
      "Started, but the images could not be saved. Share them with the orchestrator another way.",
    );
    expect(alert.closest('[role="alert"]')).toBeInTheDocument();
    expect(top).toBe(0);
    expect(onCreated).not.toHaveBeenCalled();
    const done = within(sheet).getByRole("button", { name: "Done" });
    expect(done).toHaveFocus();
    expect(within(sheet).getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(sheet.querySelector("[data-started]")).toHaveAttribute("inert");
    expect(within(sheet).queryByRole("button", { name: /^(Start|Queue)/ })).not.toBeInTheDocument();
    await user.click(done);
    expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^CHORE-/));
  });

  it.each([
    [
      "Escape",
      async (user: ReturnType<typeof renderWithDaemon>["user"]) => user.keyboard("{Escape}"),
    ],
    [
      "the close button",
      async (user: ReturnType<typeof renderWithDaemon>["user"]) =>
        user.click(screen.getByRole("button", { name: "Close" })),
    ],
  ])("treats %s as Done in the images-not-saved state", async (_n, close) => {
    const d = createMockDaemon();
    d.db.failAttachments = true;
    d.db.settings.max_concurrent_agents = 8;
    const onCreated = vi.fn();
    const onClose = vi.fn();
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={onClose} onCreated={onCreated} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    const request = within(sheet).getByRole("textbox", { name: "Request (optional)" });
    await user.type(request, "Look");
    paste(request, "a.png");
    await within(sheet).findByText("1 of 10");
    await user.click(start(sheet));
    await within(sheet).findByRole("button", { name: "Done" });
    await close(user);
    expect(onCreated).toHaveBeenCalledWith(expect.stringMatching(/^CHORE-/));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("images alone never enable Start", async () => {
    const { sheet, request } = await open();
    paste(request, "a.png");
    await within(sheet).findByText("1 of 10");
    expect(start(sheet)).toBeDisabled();
  });

  it("an image error never disables Start", async () => {
    vi.mocked(addImages).mockImplementationOnce(async () => ({
      added: [],
      error: "bad.png is not an image Swarm can attach.",
    }));
    const { sheet, request, user } = await open();
    await user.type(request, "Look");
    paste(request, "bad.png");
    await within(sheet).findByText("bad.png is not an image Swarm can attach.");
    expect(start(sheet)).toBeEnabled();
  });

  it("recovers when reading a file rejects: error line, Start enabled, later adds still work", async () => {
    vi.mocked(addImages).mockImplementationOnce(async () => {
      throw new Error("read failed");
    });
    const { sheet, request, user } = await open();
    await user.type(request, "Look");
    paste(request, "folder");
    expect(
      await within(sheet).findByText("folder is not an image Swarm can attach."),
    ).toBeInTheDocument();
    expect(sheet.querySelector('li[aria-hidden="true"]')).toBeNull();
    expect(start(sheet)).toBeEnabled();
    paste(request, "ok.png");
    expect(await within(sheet).findByRole("img", { name: "ok.png" })).toBeInTheDocument();
  });

  it("scrolls the body back to the top when the launch fails, so the banner is visible", async () => {
    const d = createMockDaemon();
    d.db.force413 = true;
    d.db.settings.max_concurrent_agents = 8;
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "Look");
    const body = sheet.querySelector<HTMLElement>(".overflow-y-auto") as HTMLElement;
    let top = 120;
    Object.defineProperty(body, "scrollTop", {
      configurable: true,
      get: () => top,
      set: (v: number) => {
        top = v;
      },
    });
    await user.click(start(sheet));
    await within(sheet).findByText("Couldn't start orchestrator. Your entries are saved.");
    expect(top).toBe(0);
  });

  it("lays the failure banner out like the not-saved alert: icon plus one text block", async () => {
    const d = createMockDaemon();
    d.db.force413 = true;
    d.db.settings.max_concurrent_agents = 8;
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "Look");
    await user.click(start(sheet));
    const title = await within(sheet).findByText(
      "Couldn't start orchestrator. Your entries are saved.",
    );
    const alert = title.closest('[role="alert"]') as HTMLElement;
    expect(alert.querySelector(":scope > svg")).not.toBeNull();
    const text = alert.querySelector(":scope > div") as HTMLElement;
    expect(text).toContainElement(title);
    expect(text).toContainElement(within(alert).getByText(/too large to send over remote access/));
  });

  it("maps a 413 to the remote-access banner and offers Try again", async () => {
    const d = createMockDaemon();
    d.db.force413 = true;
    d.db.settings.max_concurrent_agents = 8;
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "Look");
    await user.click(start(sheet));
    expect(
      await within(sheet).findByText(
        "The images are too large to send over remote access. Remove some and try again.",
      ),
    ).toBeInTheDocument();
    expect(
      within(sheet).getByText("Couldn't start orchestrator. Your entries are saved."),
    ).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Try again" })).toBeEnabled();
  });

  it("moves focus to Try again after a 413", async () => {
    const d = createMockDaemon();
    d.db.force413 = true;
    const { user } = renderWithDaemon(<NewSpikeSheet onClose={vi.fn()} onCreated={vi.fn()} />, {
      daemon: d,
      events: false,
    });
    const sheet = await screen.findByRole("dialog", { name: "New orchestrator" });
    await user.type(within(sheet).getByRole("textbox", { name: "Request (optional)" }), "Look");
    // fireEvent leaves focus where it was, as when the disabled Start button drops it to the dialog.
    fireEvent.click(start(sheet));
    const retry = await within(sheet).findByRole("button", { name: "Try again" });
    await waitFor(() => expect(retry).toHaveFocus());
  });
});
