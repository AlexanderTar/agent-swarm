// HEIC and TIFF decoders for browsers whose createImageBitmap cannot read them (Chrome, Firefox).
// Both are reachable only through dynamic import(), so they stay out of the main bundle.
// libheif-js is LGPL-3.0: it ships as its own replaceable chunk, never merged into app code.

export async function decodeHeic(bytes: Uint8Array): Promise<ImageData> {
  const { default: libheif } = await import("libheif-js/wasm-bundle");
  const image = new libheif.HeifDecoder().decode(bytes)[0];
  if (!image) throw new Error("no image in HEIC file");
  const width = image.get_width();
  const height = image.get_height();
  const target = { data: new Uint8ClampedArray(width * height * 4), width, height };
  return new Promise((resolve, reject) =>
    image.display(target, (out) =>
      out
        ? resolve(new ImageData(target.data, width, height))
        : reject(new Error("HEIF processing error")),
    ),
  );
}

export async function decodeTiff(bytes: Uint8Array): Promise<ImageData> {
  const mod = await import("utif2");
  const UTIF = "default" in mod ? (mod.default as typeof mod) : mod;
  const buffer = bytes.buffer.slice(
    bytes.byteOffset,
    bytes.byteOffset + bytes.byteLength,
  ) as ArrayBuffer;
  const ifd = UTIF.decode(buffer)[0];
  if (!ifd) throw new Error("no image in TIFF file");
  UTIF.decodeImage(buffer, ifd);
  return new ImageData(new Uint8ClampedArray(UTIF.toRGBA8(ifd)), ifd.width, ifd.height);
}
