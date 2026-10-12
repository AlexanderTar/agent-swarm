import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it, vi } from "vitest";
import {
  type Deps,
  MAX_IMAGES,
  addImages,
  prepareImage,
  sniff,
  toAttachment,
} from "./requestImages";

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

describe("prepareImage", () => {
  const MB = 1 << 20;
  const PNG = [0x89, 0x50, 0x4e, 0x47];
  const JPEG = [0xff, 0xd8, 0xff, 0xe0];
  const BMP = [0x42, 0x4d, 0, 0];
  const sized = (bytes: number, magic: number[]) => {
    const b = new Uint8Array(bytes);
    b.set(magic);
    return new Blob([b]);
  };
  const decodeTo =
    (width: number, height: number): NonNullable<Deps["decode"]> =>
    async () =>
      ({ width, height }) as ImageBitmap;
  const encodeTo = (...sizes: (number | null)[]) => {
    const calls: unknown[][] = [];
    const encode = vi.fn(async (...args: unknown[]) => {
      calls.push(args.slice(1));
      const n = sizes[Math.min(calls.length, sizes.length) - 1] ?? null;
      return n === null ? null : new Blob([new Uint8Array(n)]);
    });
    return { encode: encode as unknown as NonNullable<Deps["encode"]>, calls };
  };

  it("keeps a small PNG byte for byte whatever its pixel size", async () => {
    const file = sized(1 * MB, PNG);
    const { encode, calls } = encodeTo(1);
    const r = await prepareImage(file, "shot.png", { decode: decodeTo(2880, 1800), encode });
    expect(r).toEqual({ blob: file });
    expect(calls).toEqual([]);
  });

  it("re-encodes a JPEG over 2 MB as JPEG at 2560 px and 0.85", async () => {
    const file = sized(3 * MB, JPEG);
    const { encode, calls } = encodeTo(MB);
    const r = await prepareImage(file, "photo.jpg", { decode: decodeTo(3000, 2000), encode });
    expect("blob" in r && r.blob.size).toBe(MB);
    expect(calls).toEqual([[2560, "image/jpeg", 0.85]]);
  });

  it("never upscales", async () => {
    const { encode, calls } = encodeTo(MB);
    await prepareImage(sized(3 * MB, JPEG), "p.jpg", { decode: decodeTo(2000, 1000), encode });
    expect(calls[0]?.[0]).toBe(2000);
  });

  it("re-encodes a big PNG as PNG when that lands under 2 MB", async () => {
    const { encode, calls } = encodeTo(MB);
    const r = await prepareImage(sized(3 * MB, PNG), "shot.png", {
      decode: decodeTo(4000, 3000),
      encode,
    });
    expect("blob" in r && r.blob.size).toBe(MB);
    expect(calls).toEqual([[2560, "image/png"]]);
  });

  it("falls back to JPEG when the PNG re-encode is still over 2 MB", async () => {
    const { encode, calls } = encodeTo(3 * MB, MB);
    const r = await prepareImage(sized(3 * MB, PNG), "shot.png", {
      decode: decodeTo(4000, 3000),
      encode,
    });
    expect("blob" in r && r.blob.size).toBe(MB);
    expect(calls).toEqual([
      [2560, "image/png"],
      [2560, "image/jpeg", 0.85],
    ]);
  });

  it("converts a format the browser decodes but we cannot send as is (BMP) to PNG", async () => {
    const { encode, calls } = encodeTo(MB);
    const r = await prepareImage(sized(MB, BMP), "a.bmp", { decode: decodeTo(100, 100), encode });
    expect("blob" in r && r.blob.size).toBe(MB);
    expect(calls).toEqual([[100, "image/png"]]);
  });

  it("rejects a non-web format when the encoder fails", async () => {
    const { encode } = encodeTo(null);
    const r = await prepareImage(sized(MB, BMP), "a.bmp", { decode: decodeTo(100, 100), encode });
    expect(r).toEqual({ error: "a.bmp is not an image Swarm can attach." });
  });

  it("falls back to the original when the encoder returns null, then enforces 10 MB", async () => {
    const { encode } = encodeTo(null);
    const small = sized(3 * MB, JPEG);
    expect(await prepareImage(small, "a.jpg", { decode: decodeTo(3000, 2000), encode })).toEqual({
      blob: small,
    });
    const r = await prepareImage(sized(12 * MB, JPEG), "Panorama.jpg", {
      decode: decodeTo(9000, 3000),
      encode,
    });
    expect(r).toEqual({ error: "Panorama.jpg is larger than 10 MB." });
  });

  it("falls back to the original when the encoder throws", async () => {
    const encode = vi.fn().mockRejectedValue(new Error("out of memory"));
    const file = sized(3 * MB, JPEG);
    expect(await prepareImage(file, "a.jpg", { decode: decodeTo(3000, 2000), encode })).toEqual({
      blob: file,
    });
  });

  it("rejects an encode result over 10 MB", async () => {
    const { encode } = encodeTo(11 * MB);
    const r = await prepareImage(sized(3 * MB, JPEG), "a.jpg", {
      decode: decodeTo(3000, 2000),
      encode,
    });
    expect(r).toEqual({ error: "a.jpg is larger than 10 MB." });
  });

  it("rejects an undecodable blob", async () => {
    const decode = vi.fn().mockRejectedValue(new Error("nope"));
    const r = await prepareImage(sized(100, PNG), "x.png", { decode });
    expect(r).toEqual({ error: "x.png is not an image Swarm can attach." });
  });
});

describe("prepareImage decoder fallback", () => {
  const fixture = (name: string) =>
    new Blob([readFileSync(join(process.cwd(), "src/test/fixtures", name))]);
  const nativeFails = vi.fn().mockRejectedValue(new Error("unsupported"));
  const decoded = { width: 64, height: 64, data: new Uint8ClampedArray(64 * 64 * 4) } as ImageData;
  const pngOut = () => new Blob([new Uint8Array(500)]);
  const mkDeps = (decode: NonNullable<Deps["decode"]>) => {
    const decoders = { heic: vi.fn(async () => decoded), tiff: vi.fn(async () => decoded) };
    const encode = vi.fn(async () => pngOut());
    return { decoders, encode, deps: { decode, encode, decoders } satisfies Deps };
  };

  it.each([
    ["tiny.heic", "heic"],
    ["tiny.tiff", "tiff"],
  ] as const)("decodes %s through the lazy decoder and sends a PNG", async (file, kind) => {
    const { decoders, encode, deps } = mkDeps(nativeFails);
    const r = await prepareImage(fixture(file), file, deps);
    expect("blob" in r && r.blob.size).toBe(500);
    expect(decoders[kind]).toHaveBeenCalledOnce();
    expect(decoders[kind === "heic" ? "tiff" : "heic"]).not.toHaveBeenCalled();
    expect(encode).toHaveBeenCalledWith(decoded, 64, "image/png");
  });

  it("never calls a decoder for a PNG or JPEG the browser cannot decode", async () => {
    const { decoders, deps } = mkDeps(nativeFails);
    for (const magic of [
      [0x89, 0x50, 0x4e, 0x47],
      [0xff, 0xd8, 0xff, 0xe0],
    ]) {
      const r = await prepareImage(new Blob([new Uint8Array(magic)]), "x", deps);
      expect(r).toEqual({ error: "x is not an image Swarm can attach." });
    }
    expect(decoders.heic).not.toHaveBeenCalled();
    expect(decoders.tiff).not.toHaveBeenCalled();
  });

  it("does not call a decoder when the browser decodes HEIC/TIFF itself (Safari)", async () => {
    const { decoders, deps } = mkDeps(async () => ({ width: 16, height: 16 }) as ImageBitmap);
    expect("blob" in (await prepareImage(fixture("tiny.heic"), "a.heic", deps))).toBe(true);
    expect("blob" in (await prepareImage(fixture("tiny.tiff"), "a.tiff", deps))).toBe(true);
    expect(decoders.heic).not.toHaveBeenCalled();
    expect(decoders.tiff).not.toHaveBeenCalled();
  });

  it("reports unsupported when the lazy decoder itself fails", async () => {
    const { decoders, deps } = mkDeps(nativeFails);
    decoders.heic.mockRejectedValue(new Error("corrupt"));
    const r = await prepareImage(fixture("tiny.heic"), "bad.heic", deps);
    expect(r).toEqual({ error: "bad.heic is not an image Swarm can attach." });
  });
});

describe("toAttachment", () => {
  it("returns plain base64 with no data: prefix, also for large blobs", async () => {
    const small = await toAttachment({
      name: "a.png",
      blob: new Blob([new Uint8Array([1, 2, 3, 4])], { type: "image/png" }),
    });
    expect(small).toEqual({ name: "a.png", data: "AQIDBA==" });
    const big = await toAttachment({
      name: "b.png",
      blob: new Blob([new Uint8Array(200_000).fill(65)]),
    });
    expect(big.data).toBe(btoa("A".repeat(200_000)));
  });
});
