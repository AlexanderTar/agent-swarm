#!/usr/bin/env node
// Generates design/tokens.css from design/tokens.json (no dependencies).
// tokens.json uses the Design System artifact list format: color tokens carry
// per-theme values ({dark: ...}), every other family carries plain strings,
// and {name} aliases resolve to var(--name) in the CSS output.
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

const NAME = /^[a-z0-9-]+$/;
const ALIAS = /\{([A-Za-z0-9_-]+)\}/g;

// Resolve one token value to its CSS text: {dark: ...} takes the dark theme,
// {name} aliases become var(--name).
const resolveValue = (where, value) => {
  const raw = typeof value === "string" ? value : value?.dark;
  if (typeof raw !== "string" || raw.length === 0) fail(`${where} needs a string value or a {"dark": ...} value`);
  return raw.replace(ALIAS, "var(--$1)");
};

const checkToken = (where, token) => {
  if (!token || typeof token.name !== "string" || !NAME.test(token.name)) {
    fail(`${where}: token name ${JSON.stringify(token?.name)} must match [a-z0-9-]+ (no -- prefix)`);
  }
  if (token.usage !== undefined && typeof token.usage !== "string") {
    fail(`${where}: ${token.name} "usage" must be a string`);
  }
};

if (!doc.color || !Array.isArray(doc.color.themes) || !doc.color.themes.some((t) => t.id === "dark")) {
  fail('color.themes must list a "dark" theme');
}
if (!Array.isArray(doc.color.tokens) || doc.color.tokens.length === 0) {
  fail("color.tokens must be a non-empty array");
}
for (const token of doc.color.tokens) {
  checkToken("color", token);
  if (typeof token.value !== "object" || token.value === null) fail(`color: ${token.name} value must be a {"dark": ...} object`);
  resolveValue(`color: ${token.name}`, token.value);
}

// [family key in tokens.json, css group comment, emit --name for each token]
const FAMILIES = [
  ["text", "text"],
  ["leading", "leading"],
  ["weight", "weight"],
  ["tracking", "tracking"],
  ["spacing", "space"],
  ["layout", "layout"],
  ["radius", "radius"],
  ["shadow", "shadow"],
  ["gradient", "gradient"],
  ["timing", "motion"],
];
for (const [key] of FAMILIES) {
  const family = doc[key];
  if (family === undefined) continue;
  if (!Array.isArray(family.tokens)) fail(`${key}.tokens must be an array`);
  for (const token of family.tokens) {
    checkToken(key, token);
    resolveValue(`${key}: ${token.name}`, token.value);
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

// Split a resolved value into layers at top-level commas (commas inside
// functions like linear-gradient(...) stay put). Data keeps real comma
// separators; the generator only wraps them one layer per line.
const splitLayers = (css) => {
  const layers = [];
  let depth = 0;
  let cur = "";
  for (const ch of css) {
    if (ch === "(") depth += 1;
    else if (ch === ")") depth = Math.max(0, depth - 1);
    if (ch === "," && depth === 0) {
      layers.push(cur);
      cur = "";
      continue;
    }
    cur += ch;
  }
  layers.push(cur);
  return layers.map((s) => s.trim()).filter((s) => s.length > 0);
};

// Usage strings stay in tokens.json (the artifact page shows them); the
// generated CSS omits them so multi-line values stay readable.
const emit = (group, name, value) => {
  const layers = splitLayers(resolveValue(group, value));
  layers.forEach((layer, i) => {
    const first = i === 0 ? `  --${name}: ` : "    ";
    const last = i === layers.length - 1 ? ";" : ",";
    lines.push(`${first}${layer}${last}`);
  });
};

lines.push("  /* color */");
for (const token of doc.color.tokens) {
  emit("color", token.name, token.value);
}
lines.push("");

if (doc.type?.families) {
  lines.push("  /* font */");
  for (const [key, cssName] of [["sans", "font-sans"], ["mono", "font-mono"]]) {
    if (doc.type.families[key] !== undefined) {
      emit("type", cssName, doc.type.families[key]);
    }
  }
  lines.push("");
}
for (const [key, group] of FAMILIES) {
  const family = doc[key];
  if (family === undefined) continue;
  lines.push(`  /* ${group} */`);
  for (const token of family.tokens) {
    emit(key, token.name, token.value);
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
