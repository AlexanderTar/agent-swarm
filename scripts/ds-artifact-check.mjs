#!/usr/bin/env node
// Diffs design/ against a downloaded Design System artifact project/ folder.
// Usage: node scripts/ds-artifact-check.mjs [--design DIR] <project-dir>
// design/<path> maps to project/<path>; repo-only files are skipped, and
// artifact-only extras (archived/, uploads/) are ignored. Exits 1 on mismatch.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";

// Repo-only: never published to the artifact (spec, Design system section).
// Everything under design/scripts/ (generator, tests) plus the generated
// tokens.css stays in the repo.
const isRepoOnly = (rel) => rel === "tokens.css" || rel === "scripts" || rel.startsWith("scripts/");

// Page-owned index keys: the artifact page controls them, so they are masked
// out when design/design-system.json is compared.
const PAGE_OWNED_INDEX_KEYS = new Set(["sections", "blobs", "docs", "lastChange"]);

const sorted = (value) => {
  if (Array.isArray(value)) return value.map(sorted);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((k) => [k, sorted(value[k])]));
  }
  return value;
};

const maskedIndex = (rel, raw) => {
  let doc;
  try {
    doc = JSON.parse(raw.toString("utf8"));
  } catch {
    return { problem: `differing: ${rel} (not valid JSON)` };
  }
  for (const key of PAGE_OWNED_INDEX_KEYS) delete doc[key];
  return { canonical: JSON.stringify(sorted(doc)) };
};

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
  if (isRepoOnly(rel)) continue;
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
  if (rel === "design-system.json") {
    const mineMasked = maskedIndex(rel, mine);
    const theirsMasked = maskedIndex(rel, theirs);
    if (mineMasked.problem) problems.push(mineMasked.problem);
    else if (theirsMasked.problem) problems.push(theirsMasked.problem);
    else if (mineMasked.canonical !== theirsMasked.canonical) problems.push(`differing: ${rel}`);
    continue;
  }
  if (!mine.equals(theirs)) problems.push(`differing: ${rel}`);
}

if (problems.length > 0) {
  for (const p of problems) process.stdout.write(`${p}\n`);
  process.exit(1);
}
process.stdout.write(`design/ matches ${projectDir}\n`);
