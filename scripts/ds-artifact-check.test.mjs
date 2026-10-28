import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import assert from "node:assert/strict";

const CHECK = new URL("./ds-artifact-check.mjs", import.meta.url).pathname;

function plant(root, rel, body) {
  const p = join(root, rel);
  mkdirSync(join(p, ".."), { recursive: true });
  writeFileSync(p, body);
  return p;
}

function run(design, project) {
  try {
    const out = execFileSync("node", [CHECK, "--design", design, project], { encoding: "utf8" });
    return { code: 0, out };
  } catch (err) {
    return { code: err.status ?? 1, out: (err.stdout ?? "") + (err.stderr ?? "") };
  }
}

function setup() {
  const design = mkdtempSync(join(tmpdir(), "ds-design-"));
  const project = mkdtempSync(join(tmpdir(), "ds-project-"));
  plant(design, "README.md", "# Swarm design system\n");
  plant(design, "tokens.json", "{}\n");
  plant(project, "README.md", "# Swarm design system\n");
  plant(project, "tokens.json", "{}\n");
  return { design, project };
}

test("reports a differing file", () => {
  const { design, project } = setup();
  plant(project, "tokens.json", "{\"changed\":true}\n");
  const r = run(design, project);
  assert.notEqual(r.code, 0, "differing file must fail");
  assert.match(r.out, /tokens\.json/);
});

test("reports a missing file", () => {
  const { design, project } = setup();
  plant(design, "components/Colors/preview.html", "<!-- new -->\n");
  const r = run(design, project);
  assert.notEqual(r.code, 0, "missing file must fail");
  assert.match(r.out, /components\/Colors\/preview\.html/);
});

test("ignores repo-only files", () => {
  const { design, project } = setup();
  plant(design, "scripts/gen-tokens-css.mjs", "// repo only\n");
  plant(design, "scripts/gen-tokens-css.test.mjs", "// repo only\n");
  plant(design, "tokens.css", ":root {}\n");
  const r = run(design, project);
  assert.equal(r.code, 0, `repo-only files must be ignored: ${r.out}`);
});

test("exits 2 when the project dir is missing", () => {
  const { design } = setup();
  let code = 0;
  let out = "";
  try {
    out = execFileSync("node", [CHECK, "--design", design], { encoding: "utf8" });
  } catch (err) {
    code = err.status ?? 1;
    out = (err.stdout ?? "") + (err.stderr ?? "");
  }
  assert.equal(code, 2, `missing project dir must exit 2: ${out}`);
  assert.match(out, /missing <project-dir>/);
});

test("passes when the project copy matches", () => {
  const { design, project } = setup();
  const r = run(design, project);
  assert.equal(r.code, 0, r.out);
});
