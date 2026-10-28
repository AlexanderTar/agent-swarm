#!/usr/bin/env node
// Diffs design/ against a downloaded Design System artifact project/ folder.
// Usage: node scripts/ds-artifact-check.mjs [--design DIR] <project-dir>
// design/<path> maps to project/<path>; repo-only files are skipped, and
// artifact-only extras (archived/, uploads/) are ignored. Exits 1 on mismatch.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";

// Repo-only: never published to the artifact (spec, Design system section).
const REPO_ONLY = new Set(["scripts/gen-tokens-css.mjs", "scripts/gen-tokens-css.test.mjs", "tokens.css"]);

const args = process.argv.slice(2);
const opt = (flag, fallback) => {
  const i = args.indexOf(flag);
  return i === -1 ? fallback : args[i + 1];
};
const designDir = resolve(opt("--design", join(import.meta.dirname ?? ".", "..", "design")));
const projectArg = args.find((a) => !a.startsWith("--") && a !== opt("--design", null));

if (!projectArg) {
  process.stderr.write("ds-artifact-check: missing <project-dir>\n");
  process.exit(2);
}
const projectDir = resolve(projectArg);

const walk = (dir) => {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...walk(full));
    else out.push(relative(designDir, full).split(sep).join("/"));
  }
  return out.sort();
};

const problems = [];
for (const rel of walk(designDir)) {
  if (REPO_ONLY.has(rel)) continue;
  let mine;
  try {
    mine = readFileSync(join(designDir, rel));
  } catch {
    continue;
  }
  let theirs = null;
  try {
    theirs = readFileSync(join(projectDir, rel));
  } catch {
    problems.push(`missing: ${rel}`);
    continue;
  }
  if (!mine.equals(theirs)) problems.push(`differing: ${rel}`);
}

if (problems.length > 0) {
  for (const p of problems) process.stdout.write(`${p}\n`);
  process.exit(1);
}
process.stdout.write(`design/ matches ${projectDir}\n`);
