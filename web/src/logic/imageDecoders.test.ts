import { readFileSync } from "node:fs";
import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { decodeHeic, decodeTiff } from "./imageDecoders";

// jsdom has no ImageData; the decoders only need a constructor with the same shape.
class FakeImageData {
  constructor(
    readonly data: Uint8ClampedArray,
    readonly width: number,
    readonly height: number,
  ) {}
}

const fixture = (name: string) =>
  new Uint8Array(readFileSync(join(process.cwd(), "src/test/fixtures", name)));

describe("imageDecoders", () => {
  beforeEach(() => {
    vi.stubGlobal("ImageData", FakeImageData);
  });

  it("decodes a TIFF with utif2", async () => {
    const img = await decodeTiff(fixture("tiny.tiff"));
    expect([img.width, img.height]).toEqual([64, 64]);
    expect(img.data).toHaveLength(64 * 64 * 4);
  });

  it("decodes a HEIC with libheif-js", async () => {
    const img = await decodeHeic(fixture("tiny.heic"));
    expect([img.width, img.height]).toEqual([64, 64]);
    expect(img.data).toHaveLength(64 * 64 * 4);
  });
});
