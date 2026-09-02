import Database from "better-sqlite3";
import * as sqliteVec from "sqlite-vec";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import type { SwarmConfig } from "./types.js";

const SCHEMA_VERSION = 3;
export const EMBED_DIM = 256;

export class SwarmDatabase {
  readonly db: Database.Database;

  constructor(
    private dbPath: string,
    embedDimensions = EMBED_DIM,
  ) {
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
          status TEXT NOT NULL DEFAULT 'in_progress',
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
          claimed_by TEXT,
          claimed_agent TEXT,
          claimed_session_id TEXT,
          claimed_at TEXT,
          claim_expires_at TEXT,
          heartbeat_at TEXT,
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
        CREATE INDEX idx_tasks_claim ON tasks(claimed_by, claim_expires_at);
        CREATE INDEX idx_tasks_updated ON tasks(updated_at);

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
          parent_session_id TEXT,
          transcript_path TEXT,
          started_at TEXT NOT NULL DEFAULT (datetime('now')),
          last_seen_at TEXT,
          ended_at TEXT
        );

        CREATE INDEX idx_sessions_task ON sessions(task_id);
        CREATE INDEX idx_sessions_cwd ON sessions(cwd, last_seen_at);

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
          doc_id integer
        );

        INSERT INTO schema_meta (version, embed_model, embed_dimensions)
        VALUES (${SCHEMA_VERSION}, 'nomic-embed-text', ${embedDimensions});
      `);
      return;
    }

    const row = this.db.prepare("SELECT version FROM schema_meta LIMIT 1").get() as { version: number } | undefined;
    if (!row || row.version >= SCHEMA_VERSION) return;

    this.snapshot();

    this.db.transaction(() => {
      // One task may now hold many sessions.
      this.db.exec("DROP INDEX IF EXISTS idx_tasks_session_unique");

      // Sessions become the session <-> task join.
      this.addColumn("sessions", "parent_session_id", "TEXT");
      this.addColumn("sessions", "transcript_path", "TEXT");
      this.addColumn("sessions", "last_seen_at", "TEXT");

      this.db.exec(`
        CREATE INDEX IF NOT EXISTS idx_sessions_task ON sessions(task_id);
        CREATE INDEX IF NOT EXISTS idx_sessions_cwd ON sessions(cwd, last_seen_at);
        CREATE INDEX IF NOT EXISTS idx_tasks_updated ON tasks(updated_at);

        DELETE FROM task_events
         WHERE task_id IN (
           SELECT id FROM tasks
            WHERE initial_context LIKE '## Session%'
               OR (initial_context IS NULL AND title = 'Untitled')
         );
        UPDATE tasks
           SET status = 'archived', updated_at = datetime('now')
         WHERE status != 'archived'
           AND (initial_context LIKE '## Session%' OR (initial_context IS NULL AND title = 'Untitled'));

        UPDATE tasks SET status = 'ready' WHERE status = 'handoff';
      `);

      // `doc_id integer partition key` preallocated a chunk block per doc: 369 MB for 977 chunks.
      // A plain metadata column serves both the doc_id filter and deleteByDoc.
      this.db.exec(`
        DROP TABLE IF EXISTS vec_chunks;
        CREATE VIRTUAL TABLE vec_chunks USING vec0(
          embedding float[${embedDimensions}],
          doc_id integer
        );
        DELETE FROM kb_chunks;
        INSERT INTO kb_fts(kb_fts) VALUES('delete-all');
      `);
      // indexFile early-returns on a matching hash, so blanking it is what re-embeds the kb.
      this.db.exec("UPDATE kb_docs SET content_hash = ''");

      this.db.prepare(`UPDATE schema_meta SET version = ${SCHEMA_VERSION}`).run();
    })();

    try {
      this.db.exec("VACUUM");
      // WAL mode defers the truncation, so the file stays huge until a checkpoint.
      this.db.pragma("wal_checkpoint(TRUNCATE)");
    } catch {
      /* a fat file is not data loss */
    }
  }

  private addColumn(table: string, column: string, type: string): void {
    const cols = this.db.prepare(`PRAGMA table_info(${table})`).all() as Array<{ name: string }>;
    if (!cols.some((c) => c.name === column)) {
      this.db.exec(`ALTER TABLE ${table} ADD COLUMN ${column} ${type}`);
    }
  }

  /** Best-effort pre-migration snapshot next to the db, per user decision 4. */
  private snapshot(): void {
    try {
      const dir = join(dirname(this.dbPath), "backups");
      mkdirSync(dir, { recursive: true });
      const stamp = new Date().toISOString().replace(/[:.]/g, "-");
      this.db.prepare("VACUUM INTO ?").run(join(dir, `pre-v3-${stamp}.db`));
    } catch {
      /* snapshot is best effort; the migration still runs */
    }
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
