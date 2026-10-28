import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const GEN = new URL("./gen-tokens-css.mjs", import.meta.url).pathname;
const DESIGN_DIR = dirname(dirname(fileURLToPath(import.meta.url)));
const TOKENS_JSON = join(DESIGN_DIR, "tokens.json");
const TOKENS_CSS = join(DESIGN_DIR, "tokens.css");

const VALID = {
  name: "Swarm",
  version: 1,
  color: {
    themes: [{ id: "dark", name: "Dark" }],
    tokens: [
      { name: "violet-500", value: { dark: "#6e5ce6" }, usage: "THE brand colour" },
      { name: "violet-400", value: { dark: "#7b5cf0" } },
      { name: "primary", value: { dark: "{violet-500}" } },
    ],
  },
};

function setup(tokensDoc) {
  const dir = mkdtempSync(join(tmpdir(), "gen-tokens-"));
  const tokens = join(dir, "tokens.json");
  const out = join(dir, "tokens.css");
  writeFileSync(tokens, JSON.stringify(tokensDoc));
  return { dir, tokens, out };
}

function run(args) {
  try {
    const out = execFileSync("node", [GEN, ...args], { encoding: "utf8" });
    return { code: 0, out };
  } catch (err) {
    return { code: err.status ?? 1, out: (err.stdout ?? "") + (err.stderr ?? "") };
  }
}

test("writes tokens.css from tokens.json", () => {
  const { tokens, out } = setup(VALID);
  const r = run(["--tokens", tokens, "--out", out]);
  assert.equal(r.code, 0, r.out);
  const css = readFileSync(out, "utf8");
  assert.match(css, /--violet-500: #6e5ce6;/);
  assert.match(css, /--primary: var\(--violet-500\);/);
});

test("rejects a token without a value", () => {
  const bad = {
    color: { themes: [{ id: "dark", name: "Dark" }], tokens: [{ name: "oops", value: {} }] },
  };
  const { tokens, out } = setup(bad);
  const r = run(["--tokens", tokens, "--out", out]);
  assert.notEqual(r.code, 0, "invalid tokens must fail");
  assert.match(r.out, /invalid/i);
});

const LIST_VALID = {
  name: "Swarm",
  version: 1,
  color: {
    themes: [{ id: "dark", name: "Dark" }],
    tokens: [
      { name: "violet-500", value: { dark: "#6e5ce6" }, usage: "THE brand colour" },
      { name: "primary", value: { dark: "{violet-500}" }, usage: "semantic colour" },
    ],
  },
  type: {
    fonts: [{ family: "Onest", file: "fonts/onest-latin.woff2", weight: "100 900" }],
    families: { sans: "\"Onest\", sans-serif" },
    groups: [],
  },
  spacing: {
    tokens: [{ name: "space-1", value: "0.25rem", usage: "4" }],
  },
  shadow: {
    tokens: [
      { name: "shadow-frame", value: { dark: "0 0 0 1px {border}\n0 1px 2px black" }, usage: "elevation" },
    ],
  },
};

test("writes tokens.css from the artifact list format ({alias} becomes var())", () => {
  const { tokens, out } = setup(LIST_VALID);
  const r = run(["--tokens", tokens, "--out", out]);
  assert.equal(r.code, 0, r.out);
  const css = readFileSync(out, "utf8");
  assert.match(css, /--violet-500: #6e5ce6;/);
  assert.match(css, /--primary: var\(--violet-500\);/);
  assert.match(css, /--space-1: 0\.25rem;/);
  assert.match(css, /--shadow-frame: 0 0 0 1px var\(--border\),\n    0 1px 2px black;/);
});

test("rejects a list-format token without a dark value", () => {
  const bad = {
    name: "Swarm",
    color: { themes: [{ id: "dark", name: "Dark" }], tokens: [{ name: "oops", value: {} }] },
  };
  const { tokens, out } = setup(bad);
  const r = run(["--tokens", tokens, "--out", out]);
  assert.notEqual(r.code, 0, "invalid tokens must fail");
  assert.match(r.out, /invalid/i);
});

// Differences the repo keeps on purpose, documented in design/README.md.
// Empty today: the generated CSS is identical to the committed file.
const ALLOW_LIST = new Map([
  // e.g. ["--renamed-token", "renamed on <date>: old value -> new value"],
]);

function parseVars(css) {
  const noComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const vars = new Map();
  for (const m of noComments.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
    vars.set(m[1], m[2].replace(/\s+/g, " ").trim());
  }
  return vars;
}

test("generated CSS keeps every committed variable and value (allow-listed diffs only)", () => {
  const doc = JSON.parse(readFileSync(TOKENS_JSON, "utf8"));
  assert.ok(doc.color && !doc.groups, "tokens.json must be in the artifact list format");
  const dir = mkdtempSync(join(tmpdir(), "gen-tokens-parity-"));
  const out = join(dir, "tokens.css");
  const r = run(["--tokens", TOKENS_JSON, "--out", out]);
  assert.equal(r.code, 0, r.out);
  const fresh = parseVars(readFileSync(out, "utf8"));
  const committed = parseVars(readFileSync(TOKENS_CSS, "utf8"));
  for (const [name, value] of committed) {
    assert.ok(fresh.has(name), `generated CSS drops committed ${name}`);
    if (ALLOW_LIST.has(name)) continue;
    assert.equal(fresh.get(name), value, `${name} changed without an allow-list entry`);
  }
  for (const name of fresh.keys()) {
    assert.ok(committed.has(name) || ALLOW_LIST.has(name), `generated CSS adds ${name} without an allow-list entry`);
  }
});

test("tokens.json stays in the page-readable artifact grammar (no raw var())", () => {
  const raw = readFileSync(TOKENS_JSON, "utf8");
  assert.ok(!raw.includes("var(--"), "aliases must use the {name} form so the artifact page can read the file");
  const doc = JSON.parse(raw);
  assert.ok(doc.color.themes.some((t) => t.id === "dark"), "color needs a dark theme");
  for (const family of ["color", "type", "spacing", "radius", "shadow", "text"]) {
    assert.ok(doc[family], `missing ${family} family`);
  }
  // Repo-only extras the artifact page drops or clamps (see design/README.md).
  const shadow = Object.fromEntries(doc.shadow.tokens.map((t) => [t.name, t.value.dark ?? t.value]));
  assert.ok(shadow["shadow-icon"]?.startsWith("drop-shadow("), "repo keeps the icon drop shadow");
  assert.ok(shadow["shadow-frame"]?.includes("{border}"), "repo keeps the var()-bearing frame shadow as an alias");
  const gradient = Object.fromEntries(doc.gradient.tokens.map((t) => [t.name, t.value]));
  assert.ok(gradient["gradient-hero"]?.includes("\n"), "repo keeps the multi-line hero glow");
});

test("--check passes on fresh output and exits 1 when stale", () => {
  const { tokens, out } = setup(VALID);
  assert.equal(run(["--tokens", tokens, "--out", out]).code, 0);
  assert.equal(run(["--tokens", tokens, "--out", out, "--check"]).code, 0);
  writeFileSync(out, "/* stale */\n");
  const r = run(["--tokens", tokens, "--out", out, "--check"]);
  assert.equal(r.code, 1, "stale output must exit 1");
});
