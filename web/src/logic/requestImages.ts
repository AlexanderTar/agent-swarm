import { C, T } from "../copy";

export const MAX_IMAGES = 10;
export const MAX_IMAGE_BYTES = 10 << 20;

export type ImageFormat = "png" | "jpeg" | "gif" | "webp" | "heic" | "tiff";

const startsWith = (b: Uint8Array, at: number, sig: number[]) =>
  sig.every((v, i) => b[at + i] === v);
const ascii = (s: string) => [...s].map((c) => c.charCodeAt(0));
const HEIC_BRANDS = ["heic", "heix", "mif1", "msf1"].map(ascii);

export function sniff(b: Uint8Array): ImageFormat | null {
  if (startsWith(b, 0, [0x89, 0x50, 0x4e, 0x47])) return "png";
  if (startsWith(b, 0, [0xff, 0xd8, 0xff])) return "jpeg";
  if (startsWith(b, 0, [0x47, 0x49, 0x46, 0x38])) return "gif";
  if (startsWith(b, 0, ascii("RIFF")) && startsWith(b, 8, ascii("WEBP"))) return "webp";
  if (startsWith(b, 4, ascii("ftyp")) && HEIC_BRANDS.some((brand) => startsWith(b, 8, brand)))
    return "heic";
  if (startsWith(b, 0, [0x49, 0x49, 0x2a, 0x00]) || startsWith(b, 0, [0x4d, 0x4d, 0x00, 0x2a]))
    return "tiff";
  return null;
}

export const DOWNSCALE_BYTES = 2 << 20;
export const DOWNSCALE_EDGE = 2560;
const JPEG_QUALITY = 0.85;

export type Encoder = (
  src: ImageBitmap | ImageData,
  edge: number,
  type: "image/png" | "image/jpeg",
  quality?: number,
) => Promise<Blob | null>;
export interface Deps {
  decode?: (b: Blob) => Promise<ImageBitmap | ImageData>;
  encode?: Encoder;
}

const nativeDecode = (b: Blob) => createImageBitmap(b, { imageOrientation: "from-image" });

const canvasEncode: Encoder = async (src, edge, type, quality) => {
  const scale = Math.min(1, edge / Math.max(src.width, src.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(src.width * scale));
  canvas.height = Math.max(1, Math.round(src.height * scale));
  const ctx = canvas.getContext("2d");
  if (!ctx) return null;
  if (type === "image/jpeg") {
    ctx.fillStyle = "#fff"; // JPEG has no alpha
    ctx.fillRect(0, 0, canvas.width, canvas.height);
  }
  ctx.drawImage(
    "data" in src ? await createImageBitmap(src) : src,
    0,
    0,
    canvas.width,
    canvas.height,
  );
  return new Promise((resolve) => canvas.toBlob(resolve, type, quality));
};

const WEB_FORMATS: (ImageFormat | null)[] = ["png", "jpeg", "gif", "webp"];

// Downscaling is triggered by file size only: a small Retina screenshot stays the user's exact bytes.
export async function prepareImage(
  file: Blob,
  name: string,
  deps: Deps = {},
): Promise<{ blob: Blob } | { error: string }> {
  const unsupported = { error: T.imageUnsupported(name) };
  let bmp: ImageBitmap | ImageData;
  try {
    bmp = await (deps.decode ?? nativeDecode)(file);
  } catch {
    return unsupported;
  }
  const fmt = sniff(new Uint8Array(await file.slice(0, 16).arrayBuffer()));
  const web = WEB_FORMATS.includes(fmt);
  if (web && file.size <= DOWNSCALE_BYTES) return finish(file, name);

  const encode = deps.encode ?? canvasEncode;
  const edge = Math.min(Math.max(bmp.width, bmp.height), DOWNSCALE_EDGE);
  const jpeg = () => encode(bmp, edge, "image/jpeg", JPEG_QUALITY);
  let out: Blob | null = null;
  try {
    if (fmt === "jpeg") out = await jpeg();
    else {
      out = await encode(bmp, edge, "image/png");
      if (out && out.size > DOWNSCALE_BYTES) out = await jpeg();
    }
  } catch {
    out = null; // iOS Safari can refuse a canvas under memory pressure
  }
  if (!out) return web ? finish(file, name) : unsupported;
  return finish(out, name);
}

const finish = (blob: Blob, name: string) =>
  blob.size > MAX_IMAGE_BYTES ? { error: T.imageTooLarge(name) } : { blob };

export async function addImages(
  current: number,
  files: { blob: Blob; name: string }[],
  deps?: Deps,
): Promise<{ added: { name: string; blob: Blob }[]; error?: string }> {
  const added: { name: string; blob: Blob }[] = [];
  let error: string | undefined;
  for (const f of files) {
    if (current + added.length >= MAX_IMAGES) {
      error ??= C.imageTooMany;
      break;
    }
    const r = await prepareImage(f.blob, f.name, deps);
    if ("error" in r) error ??= r.error;
    else added.push({ name: f.name, blob: r.blob });
  }
  return error ? { added, error } : { added };
}
