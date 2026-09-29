import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { expect, it } from "vitest";

it("uses themed controls and no legacy token aliases in production TSX", () => {
  const root = join(process.cwd(), "src");
  const violations: string[] = [];
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (path !== join(root, "components", "ui")) walk(path);
      } else if (entry.name.endsWith(".tsx") && !entry.name.endsWith(".test.tsx")) {
        const source = readFileSync(path, "utf8");
        if (/<(?:button|input|textarea|select|details)\b/.test(source) || /\b(?:bg|text|border|ring|divide)-(?:canvas|panel|raised|line|ink|ok|warn|bad)\b/.test(source)) {
          violations.push(relative(root, path));
        }
      }
    }
  };
  walk(root);
  expect(violations).toEqual([]);
});
