import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { type RequestImage, RequestImages } from "./RequestImages";

const img = (n: number): RequestImage => ({
  id: `i${n}`,
  name: `shot-${n}.png`,
  blob: new Blob(["x"]),
  url: `blob:mock/${n}`,
});
const imgs = (n: number) => Array.from({ length: n }, (_, i) => img(i + 1));

function Harness(p: {
  initial: RequestImage[];
  pending?: number;
  error?: string;
  onAdd?(f: File[]): void;
}) {
  const [images, setImages] = useState(p.initial);
  return (
    <RequestImages
      images={images}
      pending={p.pending ?? 0}
      error={p.error}
      onAdd={p.onAdd ?? (() => {})}
      onRemove={(id) => setImages((l) => l.filter((i) => i.id !== id))}
    />
  );
}

const media = (matches: boolean) =>
  vi
    .spyOn(window, "matchMedia")
    .mockImplementation(
      (q) =>
        ({
          matches,
          media: q,
          onchange: null,
          addEventListener() {},
          removeEventListener() {},
          addListener() {},
          removeListener() {},
          dispatchEvent: () => false,
        }) as MediaQueryList,
    );

describe("RequestImages", () => {
  it("hides the counter at 0 and shows 'N of 10' otherwise", () => {
    const { rerender } = render(
      <RequestImages images={[]} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    expect(screen.queryByText(/of 10$/)).not.toBeInTheDocument();
    rerender(<RequestImages images={imgs(3)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByText("3 of 10")).toBeInTheDocument();
  });

  it("disables Add images… at 10", () => {
    const { rerender } = render(
      <RequestImages images={imgs(9)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    expect(screen.getByRole("button", { name: "Add images…" })).toBeEnabled();
    rerender(<RequestImages images={imgs(10)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Add images…" })).toBeDisabled();
    expect(screen.getByText("10 of 10")).toBeInTheDocument();
  });

  it("uses a hidden multi-file image input without capture, and opens it from the button", async () => {
    const { container } = render(
      <RequestImages images={[]} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    const input = container.querySelector<HTMLInputElement>('input[type="file"]');
    expect(input).toHaveAttribute("accept", "image/*");
    expect(input).toHaveAttribute("multiple");
    expect(input).not.toHaveAttribute("capture");
    expect(input).toHaveAttribute("hidden");
    expect(input).toHaveAttribute("aria-hidden", "true");
    expect(input).toHaveAttribute("tabindex", "-1");
    const click = vi.spyOn(input as HTMLInputElement, "click");
    await userEvent.click(screen.getByRole("button", { name: "Add images…" }));
    expect(click).toHaveBeenCalled();
  });

  it("passes picked files to onAdd and resets the input so the same file can be picked again", async () => {
    const onAdd = vi.fn();
    const { container } = render(
      <RequestImages images={[]} pending={0} onAdd={onAdd} onRemove={vi.fn()} />,
    );
    const input = container.querySelector<HTMLInputElement>(
      'input[type="file"]',
    ) as HTMLInputElement;
    const f = new File(["x"], "a.png", { type: "image/png" });
    await userEvent.upload(input, f);
    expect(onAdd).toHaveBeenCalledWith([f]);
    expect(input.files).toHaveLength(0);
    expect(input.value).toBe("");
  });

  it("labels each remove button and calls onRemove with the image id", async () => {
    const onRemove = vi.fn();
    render(<RequestImages images={imgs(2)} pending={0} onAdd={vi.fn()} onRemove={onRemove} />);
    expect(screen.getByRole("img", { name: "shot-1.png" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove shot-2.png" }));
    expect(onRemove).toHaveBeenCalledWith("i2");
  });

  it("renders a pulse tile per pending file, hidden from assistive tech", () => {
    const { container } = render(
      <RequestImages images={imgs(1)} pending={2} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    const tiles = container.querySelectorAll('li[aria-hidden="true"]');
    expect(tiles).toHaveLength(2);
    expect(tiles[0]).toHaveClass("motion-safe:animate-pulse");
  });

  it("keeps the polite error line mounted, empty when there is no error", () => {
    const { container, rerender } = render(
      <RequestImages images={[]} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    const p = container.querySelector('p[aria-live="polite"]');
    expect(p).toBeInTheDocument();
    expect(p).toBeEmptyDOMElement();
    rerender(
      <RequestImages
        images={[]}
        pending={0}
        error="Up to 10 images."
        onAdd={vi.fn()}
        onRemove={vi.fn()}
      />,
    );
    expect(container.querySelector('p[aria-live="polite"]')).toHaveTextContent("Up to 10 images.");
    expect(container.querySelector('p[aria-live="polite"]')).toBe(p);
  });

  it("revokes an image's object URL on remove and the rest on unmount", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    const { unmount } = render(<Harness initial={imgs(3)} />);
    await userEvent.click(screen.getByRole("button", { name: "Remove shot-2.png" }));
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(revoke).toHaveBeenCalledWith("blob:mock/2");
    unmount();
    expect(revoke).toHaveBeenCalledTimes(3);
    expect(revoke).toHaveBeenCalledWith("blob:mock/1");
    expect(revoke).toHaveBeenCalledWith("blob:mock/3");
  });

  it("moves focus after a remove: next remove button, else previous, else Add images…", async () => {
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    render(<Harness initial={imgs(3)} />);
    await userEvent.click(screen.getByRole("button", { name: "Remove shot-2.png" }));
    expect(screen.getByRole("button", { name: "Remove shot-3.png" })).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Remove shot-3.png" }));
    expect(screen.getByRole("button", { name: "Remove shot-1.png" })).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Remove shot-1.png" }));
    expect(screen.getByRole("button", { name: "Add images…" })).toHaveFocus();
  });

  it("orders the DOM like the visual order: button row first on phone, strip first from md", () => {
    media(false);
    const phone = render(
      <RequestImages images={imgs(2)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    const add = screen.getByRole("button", { name: "Add images…" });
    const first = screen.getByRole("button", { name: "Remove shot-1.png" });
    expect(add.compareDocumentPosition(first) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(phone.container.querySelector("ul")).toHaveClass(
      "grid",
      "grid-cols-[repeat(5,minmax(0,72px))]",
    );
    phone.unmount();
    media(true);
    const desk = render(
      <RequestImages images={imgs(2)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    expect(
      screen
        .getByRole("button", { name: "Remove shot-1.png" })
        .compareDocumentPosition(screen.getByRole("button", { name: "Add images…" })) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(desk.container.querySelector("ul")).toHaveClass("flex", "overflow-x-auto");
  });

  it("sizes the remove target 24 px from md and 44 px below, with an 18 px disc", () => {
    render(<RequestImages images={imgs(1)} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />);
    const btn = screen.getByRole("button", { name: "Remove shot-1.png" });
    expect(btn).toHaveClass("size-6", "max-md:size-11");
    expect(btn.querySelector("span")).toHaveClass("size-[18px]", "max-md:size-6");
  });

  it("does not render the strip when there is nothing to show", () => {
    media(true);
    const { container } = render(
      <RequestImages images={[]} pending={0} onAdd={vi.fn()} onRemove={vi.fn()} />,
    );
    expect(container.querySelector("ul")).toBeNull();
  });
});
