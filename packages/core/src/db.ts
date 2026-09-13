import Database from "better-sqlite3";
import * as sqliteVec from "sqlite-vec";
import { mkdirSync } from "node:fs";
import { dirname } from "node:path";
import type { SwarmConfig } from "./types.js";
const SCHEMA_VERSION = 4;
export const EMBED_DIM = 256;

export const TASK_SESSIONS_SCHEMA = `
  CREATE TABLE IF NOT EXISTS task_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    agent_kind TEXT,
    cwd TEXT,
    model TEXT,
    pid INTEGER,
    transcript_path TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
  );
  CREATE UNIQUE INDEX IF NOT EXISTS idx_task_sessions_task_session ON task_sessions(task_id, session_id);
  CREATE INDEX IF NOT EXISTS idx_task_sessions_task ON task_sessions(task_id);
`;

export class SwarmDatabase {
  readonly db: Database.Database;

  constructor(dbPath: string, embedDimensions = EMBED_DIM) {
    mkdirSync(dirname(dbPath), { recursive: true });
    this.db = new Database(dbPath);
    this.db.pragma("journal_mode = WAL");
    this.db.pragma("foreign_keys = ON");
    sqliteVec.load(this.db);
    this.migrate(embedDimensions);
  }

  private migrate(embedDimensions: number): void {
    const hasMeta = this.db
      .prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='schema_meta'")
      .get() as { name: string } | undefined;

    if (!hasMeta) {
      this.db.exec(`
        CREATE TABLE schema_meta (
          version INTEGER NOT NULL,
          embed_model TEXT NOT NULL,
          embed_dimensions INTEGER NOT NULL,
          migrated_at TEXT NOT NULL DEFAULT (datetime('now'))
        );

        CREATE TABLE tasks (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          key TEXT NOT NULL UNIQUE,
          title TEXT NOT NULL DEFAULT 'Untitled',
          status TEXT NOT NULL DEFAULT 'ready',
          priority TEXT NOT NULL DEFAULT 'medium',
          repo_path TEXT,
          repo_remote TEXT,
          branch TEXT,
          worktree TEXT,
          origin_agent TEXT NOT NULL DEFAULT 'unknown',
          origin_session_id TEXT,
          origin_model TEXT,
          origin_cwd TEXT,
          origin_pid INTEGER,
          parent_task_id INTEGER REFERENCES tasks(id),
          required INTEGER NOT NULL DEFAULT 1,
          coordinator_session_id TEXT,
          claimed_by TEXT,
          claimed_agent TEXT,
          claimed_session_id TEXT,
          claim_token TEXT,
          claimed_at INTEGER,
          claim_expires_at INTEGER,
          heartbeat_at INTEGER,
          initial_context TEXT,
          handoff_note TEXT,
          artifacts_json TEXT NOT NULL DEFAULT '{}',
          kb_links_json TEXT NOT NULL DEFAULT '[]',
          tags_json TEXT NOT NULL DEFAULT '[]',
          turn_count INTEGER NOT NULL DEFAULT 0,
          last_activity_at TEXT,
          created_at TEXT NOT NULL DEFAULT (datetime('now')),
          updated_at TEXT NOT NULL DEFAULT (datetime('now'))
        );

        CREATE INDEX idx_tasks_status ON tasks(status);
        CREATE INDEX idx_tasks_session ON tasks(origin_session_id);
        CREATE INDEX idx_tasks_parent ON tasks(parent_task_id);
        CREATE INDEX idx_tasks_claim ON tasks(claimed_by, claim_expires_at);

        CREATE TABLE task_events (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
          event_type TEXT NOT NULL,
          payload_json TEXT NOT NULL DEFAULT '{}',
          created_at TEXT NOT NULL DEFAULT (datetime('now'))
        );

        CREATE INDEX idx_task_events_task ON task_events(task_id);

        CREATE TABLE subtasks (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
          subject TEXT NOT NULL,
          description TEXT,
          completed INTEGER NOT NULL DEFAULT 0,
          created_at TEXT NOT NULL DEFAULT (datetime('now'))
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
          title TEXT NOT NULL,
          path TEXT NOT NULL UNIQUE,
          frontmatter_json TEXT NOT NULL DEFAULT '{}',
          content_hash TEXT NOT NULL,
          superseded_by TEXT,
          created_at TEXT NOT NULL DEFAULT (datetime('now')),
          updated_at TEXT NOT NULL DEFAULT (datetime('now'))
        );

        CREATE TABLE kb_chunks (
          id INTEGER PRIMARY KEY AUTOINCREMENT,
          doc_id INTEGER NOT NULL REFERENCES kb_docs(id) ON DELETE CASCADE,
          chunk_index INTEGER NOT NULL,
          heading TEXT,
          body TEXT NOT NULL,
          content_hash TEXT NOT NULL,
          UNIQUE(doc_id, chunk_index)
        );

        CREATE VIRTUAL TABLE kb_fts USING fts5(
          body,
          heading,
          content='kb_chunks',
          content_rowid='id'
        );

        CREATE VIRTUAL TABLE vec_chunks USING vec0(
          embedding float[${embedDimensions}],
          doc_id integer partition key
        );

        INSERT INTO schema_meta (version, embed_model, embed_dimensions)
        VALUES (${SCHEMA_VERSION}, 'nomic-embed-text', ${embedDimensions});
      `);
      this.db.exec(TASK_SESSIONS_SCHEMA);
      this.migrateToV4();
      return;
    }

    const row = this.db.prepare("SELECT version FROM schema_meta LIMIT 1").get() as { version: number } | undefined;
    let version = row?.version ?? 0;
    if (version < 2) {
      // Session identity is provenance, not task identity. Never merge tasks by session.
      this.db.prepare("UPDATE schema_meta SET version = 2").run();
      version = 2;
    }
    if (version < 3) {
      this.migrateToV3();
      this.db.prepare("UPDATE schema_meta SET version = 3").run();
      version = 3;
    }
    if (version < 4) {
      this.migrateToV4();
      this.db.prepare("UPDATE schema_meta SET version = 4").run();
    }
  }

  /** v3 compatibility: create the participant table without deriving membership from sessions. */
  private migrateToV3(): void {
    this.db.exec(TASK_SESSIONS_SCHEMA);
  }

  /** v4: explicit task hierarchy and participant membership; leases use epoch milliseconds + opaque tokens. */
  private migrateToV4(): void {
    const addColumn = (definition: string) => {
      try {
        this.db.exec(`ALTER TABLE tasks ADD COLUMN ${definition}`);
      } catch {
        // Existing databases may already have the column.
      }
    };
    addColumn("parent_task_id INTEGER REFERENCES tasks(id)");
    addColumn("required INTEGER NOT NULL DEFAULT 1");
    addColumn("coordinator_session_id TEXT");
    addColumn("claim_token TEXT");

    // SQLite permits values with a different storage class in legacy columns.
    // Convert ISO-8601 timestamps before all lease comparisons become numeric.
    for (const column of ["claimed_at", "claim_expires_at", "heartbeat_at"]) {
      try {
        this.db.exec(`
          UPDATE tasks
          SET ${column} = CAST(strftime('%s', ${column}) AS INTEGER) * 1000
          WHERE typeof(${column}) = 'text' AND ${column} IS NOT NULL
        `);
      } catch {
        // A partially-created legacy database may be missing the column.
      }
    }
    this.db.exec(`
      DROP INDEX IF EXISTS idx_tasks_session_unique;
      DROP INDEX IF EXISTS idx_task_sessions_session;
      CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
      CREATE INDEX IF NOT EXISTS idx_tasks_claim ON tasks(claimed_by, claim_expires_at);
    `);
    this.db.exec(TASK_SESSIONS_SCHEMA);
    this.db.exec(`
      CREATE TRIGGER IF NOT EXISTS tasks_prevent_parent_cycle_insert
      BEFORE INSERT ON tasks WHEN NEW.parent_task_id IS NOT NULL
      BEGIN
        SELECT CASE WHEN NEW.parent_task_id = NEW.id THEN RAISE(ABORT, 'task cannot parent itself') END;
      END;
      CREATE TRIGGER IF NOT EXISTS tasks_prevent_parent_cycle_update
      BEFORE UPDATE OF parent_task_id ON tasks WHEN NEW.parent_task_id IS NOT NULL
      BEGIN
        WITH RECURSIVE ancestors(id) AS (
          SELECT NEW.parent_task_id
          UNION ALL
          SELECT parent_task_id FROM tasks JOIN ancestors ON tasks.id = ancestors.id WHERE parent_task_id IS NOT NULL
        )
        SELECT CASE WHEN EXISTS (SELECT 1 FROM ancestors WHERE id = NEW.id)
          THEN RAISE(ABORT, 'task hierarchy cycle') END;
      END;
    `);
  }

  checkEmbeddingConfig(config: SwarmConfig): { ok: boolean; reason?: string } {
    const meta = this.db.prepare("SELECT embed_model, embed_dimensions FROM schema_meta LIMIT 1").get() as
      | { embed_model: string; embed_dimensions: number }
      | undefined;
    if (!meta) return { ok: true };
    if (meta.embed_model !== config.embedModel) {
      return {
        ok: false,
        reason: `Embedding model mismatch: db=${meta.embed_model} config=${config.embedModel}. Reindex required.`,
      };
    }
    if (meta.embed_dimensions !== config.embedDimensions) {
      return {
        ok: false,
        reason: `Embedding dimensions mismatch: db=${meta.embed_dimensions} config=${config.embedDimensions}. Reindex required.`,
      };
    }
    return { ok: true };
  }

  close(): void {
    this.db.close();
  }
}

export interface VectorIndex {
  upsert(chunkId: number, docId: number, embedding: Float32Array): void;
  search(query: Float32Array, limit: number, docId?: number): Array<{ chunkId: number; distance: number }>;
  deleteByDoc(docId: number): void;
}

export class SqliteVectorIndex implements VectorIndex {
  constructor(private db: Database.Database) {}

  upsert(chunkId: number, docId: number, embedding: Float32Array): void {
    // sqlite-vec requires INTEGER partition keys; JS numbers bind as FLOAT unless BigInt.
    const rowid = BigInt(Math.trunc(Number(chunkId)));
    const partition = BigInt(Math.trunc(Number(docId)));
    this.db.prepare("DELETE FROM vec_chunks WHERE rowid = ?").run(rowid);
    this.db
      .prepare("INSERT INTO vec_chunks(rowid, embedding, doc_id) VALUES (?, ?, ?)")
      .run(rowid, Buffer.from(embedding.buffer), partition);
  }

  search(query: Float32Array, limit: number, docId?: number): Array<{ chunkId: number; distance: number }> {
    if (docId !== undefined) {
      const rows = this.db
        .prepare(
          `SELECT rowid, distance FROM vec_chunks
           WHERE doc_id = ?
           AND embedding MATCH ?
           ORDER BY distance LIMIT ?`,
        )
        .all(BigInt(Math.trunc(docId)), Buffer.from(query.buffer), limit) as Array<{ rowid: number | bigint; distance: number }>;
      return rows.map((r) => ({ chunkId: Number(r.rowid), distance: r.distance }));
    }
    const rows = this.db
      .prepare(
        `SELECT rowid, distance FROM vec_chunks
         WHERE embedding MATCH ?
         ORDER BY distance LIMIT ?`,
      )
      .all(Buffer.from(query.buffer), limit) as Array<{ rowid: number | bigint; distance: number }>;
    return rows.map((r) => ({ chunkId: Number(r.rowid), distance: r.distance }));
  }

  deleteByDoc(docId: number): void {
    this.db.prepare("DELETE FROM vec_chunks WHERE doc_id = ?").run(BigInt(Math.trunc(docId)));
  }
}
