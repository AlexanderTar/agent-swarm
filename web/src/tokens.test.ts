import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const walk = (d: string): string[] => readdirSync(d).flatMap((f) => {
  const p = join(d, f);
  return statSync(p).isDirectory() ? walk(p) : p.endsWith(".tsx") ? [p] : [];
});

describe("design tokens", () => {
  it("no component uses the pre-shadcn colour names", () => {
    const legacy = /\b(?:bg|text|border|ring|divide)-(?:canvas|panel|raised|line|ink|ok|warn|bad)\b/;
    const hits = walk(join(process.cwd(), "src")).filter((f) => legacy.test(readFileSync(f, "utf8")));
    expect(hits).toEqual([]);
  });
});
