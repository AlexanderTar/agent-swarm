import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function ignoreCommand() {
  return JSON.parse(readFileSync(join(ROOT, "vercel.json"), "utf8")).ignoreCommand;
}

// Exit 0 from the ignoreCommand means Vercel skips the build; exit 1 (or
// more) means it builds. See the Vercel "Ignored Build Step" guide.
function runIgnore(dir, previousSha) {
  const env = { ...process.env };
  if (previousSha === undefined) delete env.VERCEL_GIT_PREVIOUS_SHA;
  else env.VERCEL_GIT_PREVIOUS_SHA = previousSha;
  const r = spawnSync("sh", ["-c", ignoreCommand()], { cwd: dir, env });
  assert.equal(r.error, undefined, `ignoreCommand failed to run: ${r.error}`);
  return r.status;
}

function git(dir, ...args) {
  const r = spawnSync("git", ["-c", "commit.gpgsign=false", ...args], { cwd: dir, encoding: "utf8" });
  assert.equal(r.status, 0, `git ${args.join(" ")} failed: ${r.stderr}`);
  return r.stdout.trim();
}

// Fixture history (oldest first):
//   c0: internal/a.txt (unrelated)
//   c1: site/index.html (relevant)
//   c2 (HEAD): internal/b.txt (unrelated)
function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "vercel-ignore-"));
  try {
    git(dir, "init", "-q");
    git(dir, "config", "user.email", "test@example.com");
    git(dir, "config", "user.name", "test");
    git(dir, "config", "commit.gpgsign", "false");
    mkdirSync(join(dir, "internal"), { recursive: true });
    mkdirSync(join(dir, "site"), { recursive: true });
    writeFileSync(join(dir, "internal", "a.txt"), "a\n");
    git(dir, "add", ".");
    git(dir, "commit", "-qm", "unrelated");
    const c0 = git(dir, "rev-parse", "HEAD");
    writeFileSync(join(dir, "site", "index.html"), "<h1>hi</h1>\n");
    git(dir, "add", ".");
    git(dir, "commit", "-qm", "site change");
    const c1 = git(dir, "rev-parse", "HEAD");
    writeFileSync(join(dir, "internal", "b.txt"), "b\n");
    git(dir, "add", ".");
    git(dir, "commit", "-qm", "unrelated again");
    const c2 = git(dir, "rev-parse", "HEAD");
    return { dir, c0, c1, c2 };
  } catch (e) {
    rmSync(dir, { recursive: true, force: true });
    throw e;
  }
}

test("builds when an earlier commit since the last deploy touched site/", (t) => {
  const { dir, c0 } = fixture();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  // HEAD itself is unrelated, but site/ changed since the last deploy.
  assert.equal(runIgnore(dir, c0), 1, "must build: site/ changed since the last deploy");
});

test("skips when nothing relevant changed since the last deploy", (t) => {
  const { dir, c1 } = fixture();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  // Last deploy already includes the site/ change; only unrelated files moved.
  assert.equal(runIgnore(dir, c1), 0, "must skip: no site/, design/ or vercel.json change");
});

test("builds when the previous sha is unset (first deploy)", (t) => {
  const { dir } = fixture();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  assert.equal(runIgnore(dir, undefined), 1, "must build: no previous deploy to compare against");
});

test("builds when the previous sha is not in the clone", (t) => {
  const { dir } = fixture();
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  // Vercel shallow-clones (--depth=10), so an older deploy's sha may be missing.
  assert.equal(runIgnore(dir, "0000000000000000000000000000000000000000"), 1, "must build: unknown base");
});
