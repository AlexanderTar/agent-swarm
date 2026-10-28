import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function vercel() {
  return JSON.parse(readFileSync(join(ROOT, "vercel.json"), "utf8"));
}

test("vercel builds site/dist and skips builds unless site, design or vercel.json changed", () => {
  const v = vercel();
  assert.equal(v.buildCommand, "node design/scripts/gen-tokens-css.mjs --check && sh site/build.sh");
  assert.equal(v.outputDirectory, "site/dist");
  assert.equal(v.ignoreCommand, "[ -n \"${VERCEL_GIT_PREVIOUS_SHA:-}\" ] && git cat-file -e \"$VERCEL_GIT_PREVIOUS_SHA\" 2>/dev/null && git diff --quiet \"$VERCEL_GIT_PREVIOUS_SHA\" HEAD -- site design vercel.json");
  assert.equal(v.framework, null);
  assert.equal(v.installCommand, "");
});
