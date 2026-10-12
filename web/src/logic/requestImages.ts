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

export interface Deps {
  decode?: (b: Blob) => Promise<ImageBitmap | ImageData>;
}

const nativeDecode = (b: Blob) => createImageBitmap(b, { imageOrientation: "from-image" });

export async function prepareImage(
  file: Blob,
  name: string,
  deps: Deps = {},
): Promise<{ blob: Blob } | { error: string }> {
  try {
    await (deps.decode ?? nativeDecode)(file);
  } catch {
    return { error: T.imageUnsupported(name) };
  }
  return { blob: file };
}

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
