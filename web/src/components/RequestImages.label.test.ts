import { describe, expect, it } from "vitest";
import { extLabel } from "./RequestImages";

// Must match the menubar's AttachmentTile.label(for:).
describe("extLabel", () => {
  it.each([
    ["spec.pdf", "PDF"],
    ["notes.md", "MD"],
    ["Report.final.Docx", "DOCX"],
    ["LICENSE", "FILE"],
    [".env", "FILE"],
    ["archive.", "FILE"],
    ["main.c++", "C"],
    ["odd.$$", "FILE"],
  ])("%s -> %s", (name, label) => expect(extLabel(name)).toBe(label));
});
