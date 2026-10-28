import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import assert from "node:assert/strict";

const GEN = new URL("./gen-tokens-css.mjs", import.meta.url).pathname;

const VALID = {
  groups: [
    {
      name: "Primitives: brand violet",
      tokens: {
        "--violet-500": { value: "#6e5ce6", comment: "THE brand colour" },
        "--violet-400": { value: "#7b5cf0" },
      },
    },
    {
      name: "Semantic",
      tokens: {
        "--primary": { value: "var(--violet-500)" },
      },
    },
  ],
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
  const bad = { groups: [{ name: "x", tokens: { "--oops": {} } }] };
  const { tokens, out } = setup(bad);
  const r = run(["--tokens", tokens, "--out", out]);
  assert.notEqual(r.code, 0, "invalid tokens must fail");
  assert.match(r.out, /invalid/i);
});

test("--check passes on fresh output and exits 1 when stale", () => {
  const { tokens, out } = setup(VALID);
  assert.equal(run(["--tokens", tokens, "--out", out]).code, 0);
  assert.equal(run(["--tokens", tokens, "--out", out, "--check"]).code, 0);
  writeFileSync(out, "/* stale */\n");
  const r = run(["--tokens", tokens, "--out", out, "--check"]);
  assert.equal(r.code, 1, "stale output must exit 1");
});
