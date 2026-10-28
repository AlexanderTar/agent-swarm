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
  plant(design, "scripts/design-layout.test.mjs", "// repo only\n");
  plant(design, "tokens.css", ":root {}\n");
  const r = run(design, project);
  assert.equal(r.code, 0, `repo-only files must be ignored: ${r.out}`);
});

test("masks page-owned index keys when comparing design-system.json", () => {
  const { design, project } = setup();
  const index = {
    v: 3,
    layout: "files",
    title: "Swarm Design System",
    namespace: "SwarmDesignSystem",
    libraries: [],
    sections: { mine: true },
    groups: ["Logos"],
    assetGroups: {},
    blobs: { mine: true },
    docs: { sections: [] },
    lastChange: { by: "me", at: "today", via: "test", note: "repo" },
  };
  plant(design, "design-system.json", JSON.stringify(index));
  plant(
    project,
    "design-system.json",
    JSON.stringify({ ...index, sections: { page: true }, blobs: { page: true }, docs: { sections: ["a"] }, lastChange: { by: "page" } }),
  );
  const r = run(design, project);
  assert.equal(r.code, 0, `page-owned index keys must be masked: ${r.out}`);
});

test("still fails on a repo-owned index difference", () => {
  const { design, project } = setup();
  plant(design, "design-system.json", JSON.stringify({ v: 3, title: "Swarm Design System", sections: {}, blobs: {}, docs: {}, lastChange: {} }));
  plant(project, "design-system.json", JSON.stringify({ v: 3, title: "Renamed System", sections: {}, blobs: {}, docs: {}, lastChange: {} }));
  const r = run(design, project);
  assert.notEqual(r.code, 0, "repo-owned index difference must fail");
  assert.match(r.out, /design-system\.json/);
});

test("compares assets against the downloaded uploads", () => {
  const { design, project } = setup();
  plant(design, "assets/Logos/app-icon.svg", "<svg>mine</svg>\n");
  plant(project, "assets/Logos/app-icon.svg", "<svg>mine</svg>\n");
  assert.equal(run(design, project).code, 0);
  plant(project, "assets/Logos/app-icon.svg", "<svg>changed</svg>\n");
  const r = run(design, project);
  assert.notEqual(r.code, 0, "changed upload must fail");
  assert.match(r.out, /assets\/Logos\/app-icon\.svg/);
});

test("compares SVGs after canonical serialization (the upload store rewrites empty-element tags)", () => {
  const { design, project } = setup();
  // Same vector, once as self-closing tags and once as explicit close tags
  // with different inter-tag whitespace: the upload store rewrites one form
  // into the other on publish.
  plant(design, "assets/Logos/glyph.svg", '<svg><circle cx="12" cy="5" r="3"/><path d="M0 0h1"/></svg>\n');
  plant(project, "assets/Logos/glyph.svg", '<svg>\n  <circle cx="12" cy="5" r="3"></circle>\n  <path d="M0 0h1" ></path>\n</svg>\n');
  const r = run(design, project);
  assert.equal(r.code, 0, `re-serialized SVG must compare equal: ${r.out}`);
  plant(project, "assets/Logos/glyph.svg", '<svg><circle cx="12" cy="6" r="3"/><path d="M0 0h1"/></svg>\n');
  const changed = run(design, project);
  assert.notEqual(changed.code, 0, "a real SVG change must still fail");
  assert.match(changed.out, /assets\/Logos\/glyph\.svg/);
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
