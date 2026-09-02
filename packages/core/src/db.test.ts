import { mkdtempSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import Database from "better-sqlite3";
import * as sqliteVec from "sqlite-vec";
import { describe, expect, it } from "vitest";
import { SwarmDatabase } from "./db.js";

/** Minimal v2 database: the columns the migration touches, plus the index it drops. */
function writeV2Db(dbPath: string): void {
  const db = new Database(dbPath);
  sqliteVec.load(db);
  db.exec(`
    CREATE TABLE schema_meta (version INTEGER NOT NULL, embed_model TEXT NOT NULL, embed_dimensions INTEGER NOT NULL);
    CREATE TABLE tasks (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      key TEXT NOT NULL UNIQUE,
      title TEXT NOT NULL DEFAULT 'Untitled',
      status TEXT NOT NULL DEFAULT 'in_progress',
      origin_session_id TEXT,
      initial_context TEXT,
      updated_at TEXT NOT NULL DEFAULT (datetime('now'))
    );
    CREATE UNIQUE INDEX idx_tasks_session_unique ON tasks(origin_session_id) WHERE origin_session_id IS NOT NULL;
    CREATE TABLE task_events (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
      event_type TEXT NOT NULL
    );
    CREATE TABLE sessions (
      id TEXT PRIMARY KEY,
      agent_kind TEXT NOT NULL,
      cwd TEXT,
      model TEXT,
      pid INTEGER,
      task_id INTEGER REFERENCES tasks(id),
      started_at TEXT NOT NULL DEFAULT (datetime('now')),
      ended_at TEXT
    );

    CREATE TABLE kb_docs (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      slug TEXT NOT NULL UNIQUE,
      path TEXT NOT NULL UNIQUE,
      content_hash TEXT NOT NULL
    );
    CREATE TABLE kb_chunks (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      doc_id INTEGER NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
      chunk_index INTEGER NOT NULL,
      heading TEXT,
      body TEXT NOT NULL,
      content_hash TEXT NOT NULL
    );
    CREATE VIRTUAL TABLE kb_fts USING fts5(body, heading, content='kb_chunks', content_rowid='id');
    CREATE VIRTUAL TABLE vec_chunks USING vec0(embedding float[256], doc_id integer partition key);

    INSERT INTO schema_meta (version, embed_model, embed_dimensions) VALUES (2, 'nomic-embed-text', 256);
    INSERT INTO tasks (key, title, status, initial_context) VALUES ('SW-1', 'claude · repo', 'in_progress', '## Session

- Agent: claude');
    INSERT INTO tasks (key, title, status, initial_context) VALUES ('SW-2', 'Real work', 'handoff', 'Written by a human');
    INSERT INTO tasks (key, title, status, initial_context) VALUES ('SW-3', 'Untitled', 'in_progress', NULL);
    INSERT INTO task_events (task_id, event_type) VALUES (1, 'tool');
    INSERT INTO task_events (task_id, event_type) VALUES (2, 'tool');
    INSERT INTO task_events (task_id, event_type) VALUES (3, 'tool');

    INSERT INTO kb_docs (slug, path, content_hash) VALUES ('note', '/kb/note.md', 'abc123');
    INSERT INTO kb_chunks (doc_id, chunk_index, body, content_hash) VALUES (1, 0, 'hello world', 'h1');
    INSERT INTO kb_fts(rowid, body, heading) VALUES (1, 'hello world', '');
  `);
  db.prepare("INSERT INTO vec_chunks(rowid, embedding, doc_id) VALUES (?, ?, ?)").run(
    1n,
    Buffer.from(new Float32Array(256).buffer),
    1n,
  );
  db.close();
}

describe("schema v3 migration", () => {
  it("archives hook tiles, purges their events, and snapshots first", () => {
    const dir = mkdtempSync(join(tmpdir(), "swarm-migrate-"));
    const dbPath = join(dir, "swarm.db");
    writeV2Db(dbPath);

    const swarm = new SwarmDatabase(dbPath);
    const version = swarm.db.prepare("SELECT version FROM schema_meta").get() as {
      version: number;
    };
    expect(version.version).toBe(3);

    const index = swarm.db
      .prepare(
        "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_tasks_session_unique'",
      )
      .get();
    expect(index).toBeUndefined();

    const cols = (
      swarm.db.prepare("PRAGMA table_info(sessions)").all() as Array<{ name: string }>
    ).map((c) => c.name);
    expect(cols).toEqual(
      expect.arrayContaining(["parent_session_id", "transcript_path", "last_seen_at"]),
    );

    expect(swarm.db.prepare("SELECT status FROM tasks WHERE key = 'SW-1'").get()).toEqual({
      status: "archived",
    });
    expect(swarm.db.prepare("SELECT status FROM tasks WHERE key = 'SW-2'").get()).toEqual({
      status: "ready",
    });
    // Registered-but-never-named tiles are hook debris too.
    expect(swarm.db.prepare("SELECT status FROM tasks WHERE key = 'SW-3'").get()).toEqual({
      status: "archived",
    });
    expect(swarm.db.prepare("SELECT task_id FROM task_events").all()).toEqual([{ task_id: 2 }]);

    // The kb is dropped and queued for re-embedding without its partitioned vector table.
    expect(swarm.db.prepare("SELECT COUNT(*) AS c FROM kb_chunks").get()).toEqual({ c: 0 });
    expect(swarm.db.prepare("SELECT COUNT(*) AS c FROM vec_chunks").get()).toEqual({ c: 0 });
    expect(swarm.db.prepare("SELECT content_hash FROM kb_docs").get()).toEqual({ content_hash: "" });
    expect(
      swarm.db.prepare("SELECT sql FROM sqlite_master WHERE name = 'vec_chunks'").get(),
    ).toMatchObject({ sql: expect.not.stringContaining("partition key") });

    expect(readdirSync(join(dir, "backups")).some((f) => f.startsWith("pre-v3-"))).toBe(true);
    swarm.close();

    // Re-opening is a no-op.
    const again = new SwarmDatabase(dbPath);
    expect(again.db.prepare("SELECT version FROM schema_meta").get()).toEqual({ version: 3 });
    again.close();
  });

  it("creates fresh databases at v3 without the unique session index", () => {
    const swarm = new SwarmDatabase(join(mkdtempSync(join(tmpdir(), "swarm-fresh-")), "swarm.db"));
    expect(swarm.db.prepare("SELECT version FROM schema_meta").get()).toEqual({ version: 3 });
    expect(
      swarm.db
        .prepare(
          "SELECT name FROM sqlite_master WHERE type='index' AND name='idx_tasks_session_unique'",
        )
        .get(),
    ).toBeUndefined();
    expect(
      swarm.db
        .prepare("SELECT name FROM sqlite_master WHERE type='index' AND name='idx_sessions_cwd'")
        .get(),
    ).toEqual({ name: "idx_sessions_cwd" });
    expect(
      swarm.db.prepare("SELECT sql FROM sqlite_master WHERE name = 'vec_chunks'").get(),
    ).toMatchObject({ sql: expect.not.stringContaining("partition key") });
    swarm.close();
  });
});
