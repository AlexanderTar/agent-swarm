import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SqliteVectorIndex, SwarmDatabase } from "./db.js";
import { KbIndexer } from "./indexer.js";
import { KbStore } from "./kb.js";
import type { OllamaClient } from "./ollama.js";
import { SessionService } from "./sessions.js";
import { TaskService } from "./tasks.js";
import { getSwarmPaths } from "./types.js";

const stubOllama = {
  embed: async (xs: string[]) => xs.map(() => new Float32Array(256)),
  embedOne: async () => new Float32Array(256),
} as unknown as OllamaClient;

function harness(ollama: OllamaClient = stubOllama) {
  const home = mkdtempSync(join(tmpdir(), "swarm-indexer-"));
  const paths = getSwarmPaths(home);
  const swarm = new SwarmDatabase(paths.db);
  const kb = new KbStore(swarm.db, new SqliteVectorIndex(swarm.db), ollama, paths);
  const sessions = new SessionService(swarm.db);
  const tasks = new TaskService(swarm.db, sessions);
  return { home, paths, swarm, kb, tasks, sessions, indexer: new KbIndexer(kb, tasks, sessions) };
}

describe("KbIndexer", () => {
  let h: ReturnType<typeof harness>;

  beforeEach(() => {
    h = harness();
  });

  it("writes kb/tasks/<KEY>.md with frontmatter and the agent's summary", async () => {
    const task = h.tasks.create({
      title: "Fix board titles",
      summary: "Titles come from the agent now.",
      originAgent: "claude",
      tags: ["board", "mcp"],
    });
    h.sessions.upsert({ id: "s1", agent: "codex" });
    h.sessions.bind("s1", task.id);

    const path = await h.indexer.indexTask(task.id);
    expect(path).toBe(join(h.paths.kb, "tasks", "SW-1.md"));

    const doc = readFileSync(path!, "utf8");
    expect(doc).toContain("title: Fix board titles");
    expect(doc).toContain("task: SW-1");
    expect(doc).toContain("status: in_progress");
    expect(doc).toContain("- board");
    expect(doc).toContain("- codex");
    expect(doc).toContain("type: task");
    expect(doc).toContain("Titles come from the agent now.");

    expect(await h.kb.search("board titles", 5, { subdir: "tasks" })).not.toHaveLength(0);
    expect(await h.kb.search("board titles", 5, { subdir: "handoffs" })).toHaveLength(0);
    expect(await h.indexer.indexTask(999)).toBeNull();
  });

  it("indexes only the markdown artifacts that exist", async () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-artifacts-"));
    const other = mkdtempSync(join(tmpdir(), "swarm-artifacts-"));
    const present = join(dir, "notes.md");
    const sameName = join(other, "notes.md");
    writeFileSync(present, "# Notes\n\nMigration landed.\n");
    writeFileSync(sameName, "# Notes\n\nA different repo.\n");
    const task = h.tasks.create({ title: "Artifacts", originAgent: "claude" });
    h.tasks.addArtifact(task.id, "files", present);
    h.tasks.addArtifact(task.id, "files", sameName);
    h.tasks.addArtifact(task.id, "files", join(dir, "missing.md"));
    h.tasks.addArtifact(task.id, "files", join(dir, "notes.txt"));

    expect(await h.indexer.indexArtifacts(task.id)).toEqual([present, sameName]);
    // Same basename, different repos: slugs come from the absolute path so both land.
    expect(h.swarm.db.prepare("SELECT COUNT(*) AS c FROM kb_docs").get()).toEqual({ c: 2 });
    expect(await h.indexer.indexArtifacts(999)).toEqual([]);
  });

  it("returns null when no transcript resolves", async () => {
    const task = h.tasks.create({ title: "No transcript", originAgent: "claude" });
    h.sessions.upsert({ id: "s1", agent: "claude", cwd: join(h.home, "nowhere") });
    h.sessions.bind("s1", task.id);
    expect(await h.indexer.indexTranscript(task.id, "s1")).toBeNull();
    expect(await h.indexer.indexTranscript(task.id, "ghost")).toBeNull();
  });

  it("renders a recorded transcript and remembers where it came from", async () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-transcript-"));
    mkdirSync(dir, { recursive: true });
    const transcript = join(dir, "s1.jsonl");
    writeFileSync(
      transcript,
      `${JSON.stringify({ type: "user", message: { content: "Fix the migration" } })}\n`,
    );
    const task = h.tasks.create({ title: "Migration", originAgent: "claude" });
    h.sessions.upsert({ id: "s1", agent: "claude", transcriptPath: transcript });
    h.sessions.bind("s1", task.id);

    const path = await h.indexer.indexTranscript(task.id, "s1");
    expect(path).toBe(join(h.paths.kb, "transcripts", "SW-1-s1.md"));
    expect(readFileSync(path!, "utf8")).toContain("Fix the migration");
    expect(h.sessions.get("s1")?.transcriptPath).toBe(transcript);
  });

  it("swallows indexing errors and still reports what it wrote", async () => {
    const dead = {
      embed: async () => {
        throw new Error("Ollama unreachable");
      },
      embedOne: async () => {
        throw new Error("Ollama unreachable");
      },
    } as unknown as OllamaClient;
    const broken = harness(dead);
    const task = broken.tasks.create({
      title: "Offline",
      summary: "Ollama is down",
      originAgent: "claude",
    });
    broken.sessions.upsert({ id: "s1", agent: "claude" });
    broken.sessions.bind("s1", task.id);

    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(broken.indexer.ingestSession(task.id, "s1")).resolves.toEqual([]);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });

  it("ingests task, artifacts and transcript in one call", async () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-ingest-"));
    const artifact = join(dir, "plan.md");
    writeFileSync(artifact, "# Plan\n\nShip it.\n");
    const transcript = join(dir, "s1.jsonl");
    writeFileSync(
      transcript,
      `${JSON.stringify({ type: "user", message: { content: "Ship it" } })}\n`,
    );

    const task = h.tasks.create({
      title: "Everything",
      summary: "All three",
      originAgent: "claude",
    });
    h.tasks.addArtifact(task.id, "files", artifact);
    h.sessions.upsert({ id: "s1", agent: "claude", transcriptPath: transcript });
    h.sessions.bind("s1", task.id);

    expect(await h.indexer.ingestSession(task.id, "s1")).toEqual([
      join(h.paths.kb, "tasks", "SW-1.md"),
      artifact,
      join(h.paths.kb, "transcripts", "SW-1-s1.md"),
    ]);
  });
});
