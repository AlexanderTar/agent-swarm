import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const DESIGN_DIR = dirname(dirname(fileURLToPath(import.meta.url)));

test("design-system.json has the artifact index shape", () => {
  const index = JSON.parse(readFileSync(join(DESIGN_DIR, "design-system.json"), "utf8"));
  assert.equal(index.v, 3);
  assert.equal(index.layout, "files");
  assert.ok(index.createdOnFiles, "missing createdOnFiles");
  assert.equal(typeof index.title, "string");
  assert.equal(typeof index.namespace, "string");
  assert.ok(Array.isArray(index.libraries));
  assert.ok(Array.isArray(index.groups));
  assert.ok(index.groups.includes("Logos"), "groups must list Logos");
  const logos = index.assetGroups?.Logos;
  assert.ok(logos, "missing assetGroups.Logos");
  assert.deepEqual(logos.order, ["app-icon.svg", "glyph.svg"]);
  for (const file of logos.order) {
    const entry = logos.files?.[file];
    assert.ok(entry, `missing assetGroups.Logos.files[${file}]`);
    assert.equal(typeof entry.blob, "string");
    assert.equal(typeof entry.size, "number");
    assert.equal(typeof entry.type, "string");
  }
  assert.ok(index.lastChange?.by && index.lastChange?.at, "missing lastChange");
});

test("every preview starts with an @dsCard marker (Cover included)", () => {
  const dir = join(DESIGN_DIR, "components");
  const names = readdirSync(dir).filter((n) => statSync(join(dir, n)).isDirectory());
  assert.ok(names.includes("Cover"), "missing components/Cover");
  for (const name of names) {
    const html = readFileSync(join(dir, name, "preview.html"), "utf8");
    assert.match(html, /^<!-- @dsCard( group="[^"]+")?( height=\d+)? -->\n/, `${name}/preview.html must start with an @dsCard marker`);
  }
});

test("Logo preview inlines the icon (no path or /_blob reference)", () => {
  const html = readFileSync(join(DESIGN_DIR, "components", "Logo", "preview.html"), "utf8");
  assert.ok(!html.includes("src="), "Logo preview must not reference an external image");
  assert.ok(!html.includes("/_blob"), "Logo preview must not reference an artifact blob");
  assert.ok(html.includes("<svg"), "Logo preview must inline the app icon SVG");
});

test("logos live in assets/Logos with a README", () => {
  assert.ok(statSync(join(DESIGN_DIR, "assets", "Logos", "app-icon.svg")).isFile());
  assert.ok(statSync(join(DESIGN_DIR, "assets", "Logos", "glyph.svg")).isFile());
  assert.ok(statSync(join(DESIGN_DIR, "assets", "Logos", "README.md")).isFile());
});
