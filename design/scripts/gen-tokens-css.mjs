#!/usr/bin/env node
// Generates design/tokens.css from design/tokens.json (no dependencies).
// Usage: node design/scripts/gen-tokens-css.mjs [--check] [--tokens PATH] [--out PATH]
// --check exits 1 when the committed CSS is stale instead of rewriting it.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const opt = (flag, fallback) => {
  const i = args.indexOf(flag);
  return i === -1 ? fallback : args[i + 1];
};
const tokensPath = resolve(opt("--tokens", join(here, "..", "tokens.json")));
const outPath = resolve(opt("--out", join(here, "..", "tokens.css")));
const check = args.includes("--check");

const fail = (msg) => {
  process.stderr.write(`gen-tokens-css: invalid tokens.json: ${msg}\n`);
  process.exit(2);
};

let doc;
try {
  doc = JSON.parse(readFileSync(tokensPath, "utf8"));
} catch (err) {
  fail(`${tokensPath}: ${err.message}`);
}
if (!doc || !Array.isArray(doc.groups) || doc.groups.length === 0) {
  fail("top-level \"groups\" must be a non-empty array");
}
const NAME = /^--[a-z0-9-]+$/;
for (const [gi, group] of doc.groups.entries()) {
  if (!group || typeof group.name !== "string" || group.name.length === 0) {
    fail(`groups[${gi}].name must be a non-empty string`);
  }
  if (!group.tokens || typeof group.tokens !== "object" || Array.isArray(group.tokens)) {
    fail(`groups[${gi}].tokens must be an object`);
  }
  for (const [name, token] of Object.entries(group.tokens)) {
    if (!NAME.test(name)) fail(`groups[${gi}]: ${JSON.stringify(name)} is not a --custom-property name`);
    if (!token || typeof token.value !== "string" || token.value.length === 0) {
      fail(`groups[${gi}]: ${name} needs a non-empty string "value"`);
    }
    if (token.comment !== undefined && typeof token.comment !== "string") {
      fail(`groups[${gi}]: ${name} "comment" must be a string`);
    }
  }
}

const lines = [
  "/* Swarm design tokens (generated from design/tokens.json; do not edit by hand).",
  "   Dark only. Semantic names match the board's variables in web/src/index.css. */",
  "",
  ":root {",
  "  color-scheme: dark;",
  "",
];
for (const group of doc.groups) {
  lines.push(`  /* ${group.name} */`);
  for (const [name, token] of Object.entries(group.tokens)) {
    const parts = token.value.split("\n");
    const note = token.comment ? ` /* ${token.comment} */` : "";
    parts.forEach((part, i) => {
      const first = i === 0 ? `  ${name}: ` : "    ";
      const last = i === parts.length - 1 ? `;${note}` : ",";
      lines.push(`${first}${part.trimEnd()}${last}`);
    });
  }
  lines.push("");
}
lines[lines.length - 1] = "}";
const css = `${lines.join("\n")}\n`;

if (check) {
  let current = null;
  try {
    current = readFileSync(outPath, "utf8");
  } catch {
    // Missing output counts as stale.
  }
  if (current !== css) {
    process.stderr.write(`gen-tokens-css: ${outPath} is stale; run node design/scripts/gen-tokens-css.mjs\n`);
    process.exit(1);
  }
} else {
  writeFileSync(outPath, css);
}
