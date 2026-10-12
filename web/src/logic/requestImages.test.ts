import { describe, expect, it, vi } from "vitest";
import { MAX_IMAGES, addImages, sniff } from "./requestImages";

const u8 = (...b: number[]) => new Uint8Array(b);
const ascii = (s: string) => [...s].map((c) => c.charCodeAt(0));

describe("sniff", () => {
  it("recognises each supported format by magic bytes", () => {
    expect(sniff(u8(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a))).toBe("png");
    expect(sniff(u8(0xff, 0xd8, 0xff, 0xe0))).toBe("jpeg");
    expect(sniff(u8(...ascii("GIF89a")))).toBe("gif");
    expect(sniff(u8(...ascii("RIFF"), 0, 0, 0, 0, ...ascii("WEBPVP8 ")))).toBe("webp");
    for (const brand of ["heic", "heix", "mif1", "msf1"]) {
      expect(sniff(u8(0, 0, 0, 0x18, ...ascii("ftyp"), ...ascii(brand)))).toBe("heic");
    }
    expect(sniff(u8(0x49, 0x49, 0x2a, 0x00, 8, 0, 0, 0))).toBe("tiff");
    expect(sniff(u8(0x4d, 0x4d, 0x00, 0x2a, 0, 0, 0, 8))).toBe("tiff");
  });

  it("returns null for anything else", () => {
    expect(sniff(u8())).toBeNull();
    expect(sniff(u8(...ascii("%PDF-1.7")))).toBeNull();
    expect(sniff(u8(...ascii("RIFF"), 0, 0, 0, 0, ...ascii("WAVEfmt ")))).toBeNull();
    expect(sniff(u8(0, 0, 0, 0x18, ...ascii("ftyp"), ...ascii("mp42")))).toBeNull();
  });
});

describe("addImages", () => {
  const png = (name: string) => ({
    name,
    blob: new Blob([u8(0x89, 0x50, 0x4e, 0x47, 0, 0, 0, 0)], { type: "image/png" }),
  });
  const bmp = { width: 10, height: 10 } as ImageBitmap;
  const decode = vi.fn(async () => bmp);

  it("exposes the 10 image limit", () => expect(MAX_IMAGES).toBe(10));

  it("adds every image of a clean batch", async () => {
    const r = await addImages(0, [png("a.png"), png("b.png")], { decode });
    expect(r.added.map((i) => i.name)).toEqual(["a.png", "b.png"]);
    expect(r.error).toBeUndefined();
  });

  it("stops at the 10th image with the too-many error", async () => {
    const r = await addImages(9, [png("a.png"), png("b.png"), png("c.png")], { decode });
    expect(r.added.map((i) => i.name)).toEqual(["a.png"]);
    expect(r.error).toBe("Up to 10 images.");
  });

  it("reports the first error of a batch and keeps the images that worked", async () => {
    const failing = vi.fn(async (b: Blob) => {
      if ((b as Blob & { bad?: boolean }).bad) throw new Error("undecodable");
      return bmp;
    });
    const bad = (name: string) => Object.assign(png(name).blob, { bad: true });
    const r = await addImages(
      0,
      [
        png("a.png"),
        { name: "one.pdf", blob: bad("one.pdf") },
        { name: "two.pdf", blob: bad("two.pdf") },
        png("b.png"),
      ],
      { decode: failing },
    );
    expect(r.added.map((i) => i.name)).toEqual(["a.png", "b.png"]);
    expect(r.error).toBe("one.pdf is not an image Swarm can attach.");
  });

  it("lets the too-many error win over a later unsupported file", async () => {
    const r = await addImages(10, [png("a.png")], { decode });
    expect(r.added).toEqual([]);
    expect(r.error).toBe("Up to 10 images.");
  });
});
