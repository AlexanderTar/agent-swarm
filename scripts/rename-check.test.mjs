import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import assert from "node:assert/strict";

const SH = new URL("./rename-check.sh", import.meta.url).pathname;

function run(root, ...paths) {
  try {
    const out = execFileSync("sh", [SH, "--root", root, ...paths], { encoding: "utf8" });
    return { code: 0, out };
  } catch (err) {
    return { code: err.status ?? 1, out: (err.stdout ?? "") + (err.stderr ?? "") };
  }
}

function plant(root, rel, body) {
  const p = join(root, rel);
  mkdirSync(join(p, ".."), { recursive: true });
  writeFileSync(p, body);
  return rel;
}

test("fails on a planted 'Agent Swarm' outside the allow list", () => {
  const root = mkdtempSync(join(tmpdir(), "rename-check-"));
  plant(root, "notes.md", "# Agent Swarm roadmap\n");
  const r = run(root, "notes.md");
  assert.notEqual(r.code, 0, "planted violation must fail");
  assert.match(r.out, /notes\.md:1:/);
});

test("fails on a planted 'AlexanderTar/agent-swarm' outside the allow list", () => {
  const root = mkdtempSync(join(tmpdir(), "rename-check-"));
  plant(root, "notes.md", "https://github.com/AlexanderTar/agent-swarm.git\n");
  const r = run(root, "notes.md");
  assert.notEqual(r.code, 0, "planted remote must fail");
  assert.match(r.out, /notes\.md:1:/);
});

test("passes on allow-listed paths", () => {
  const root = mkdtempSync(join(tmpdir(), "rename-check-"));
  const paths = [
    plant(root, "docs/plans/n.md", "Agent Swarm\n"),
    plant(root, "internal/install/testdata/x.md", "Agent Swarm\n"),
    plant(root, "internal/install/files_test.go", "Agent Swarm\n"),
    plant(root, "scripts/rename-check-test.md", "Agent Swarm\n"),
    plant(root, "scripts/site-check.sh", "Agent Swarm\n"),
    plant(root, "internal/hook/testdata/q.json", "AlexanderTar/agent-swarm\n"),
  ];
  const r = run(root, ...paths);
  assert.equal(r.code, 0, `allow-listed paths must pass: ${r.out}`);
});

test("ignores comments naming external spec files", () => {
  const root = mkdtempSync(join(tmpdir(), "rename-check-"));
  plant(root, "x.go", "// see ~/.superpowers/specs/2026-09-17-agent-swarm-contracts.md\n");
  const r = run(root, "x.go");
  assert.equal(r.code, 0, `spec-file comments must pass: ${r.out}`);
});

