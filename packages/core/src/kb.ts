import { createHash } from "node:crypto";
import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { join, relative, basename } from "node:path";
import matter from "gray-matter";
import type { FSWatcher } from "chokidar";
import chokidar from "chokidar";
import type Database from "better-sqlite3";
import type { OllamaClient } from "./ollama.js";
import type { SqliteVectorIndex } from "./db.js";
import type { SwarmPaths } from "./types.js";

export interface KbSearchResult {
  slug: string;
  title: string;
  heading: string | null;
  body: string;
  score: number;
  path: string;
}

function hashContent(s: string): string {
  return createHash("sha256").update(s).digest("hex").slice(0, 16);
}

function slugify(name: string): string {
  return name.replace(/\.md$/i, "").replace(/[^a-z0-9]+/gi, "-").replace(/^-|-$/g, "").toLowerCase();
}

export function chunkMarkdown(content: string, maxChars = 2000, overlap = 200): Array<{ heading: string | null; body: string }> {
  const lines = content.split("\n");
  const chunks: Array<{ heading: string | null; body: string }> = [];
  let currentHeading: string | null = null;
  let buffer: string[] = [];

  const flush = () => {
    const body = buffer.join("\n").trim();
    if (body.length > 0) chunks.push({ heading: currentHeading, body });
    buffer = body.length > overlap ? [body.slice(-overlap)] : [];
  };

  for (const line of lines) {
    if (/^#{1,3}\s/.test(line)) {
      flush();
      currentHeading = line.replace(/^#+\s*/, "");
      continue;
    }
    buffer.push(line);
    if (buffer.join("\n").length >= maxChars) flush();
  }
  flush();
  return chunks;
}

export class KbStore {
  private watcher: FSWatcher | null = null;

  constructor(
    private db: Database.Database,
    private vector: SqliteVectorIndex,
    private ollama: OllamaClient,
    private paths: SwarmPaths,
  ) {
    for (const sub of ["notes", "handoffs", "decisions", "projects", "inbox", "tasks", "transcripts", "memory"]) {
      mkdirSync(join(paths.kb, sub), { recursive: true });
    }
  }

  startWatching(onChange?: () => void): void {
    this.watcher = chokidar.watch(join(this.paths.kb, "**/*.md"), {
      ignoreInitial: false,
      awaitWriteFinish: { stabilityThreshold: 300 },
    });
    this.watcher.on("add", (p: string) => void this.indexFile(p).then(() => onChange?.()));
    this.watcher.on("change", (p: string) => void this.indexFile(p).then(() => onChange?.()));
    this.watcher.on("unlink", (p: string) => {
      this.removeFile(p);
      onChange?.();
    });
  }

  stopWatching(): void {
    void this.watcher?.close();
  }

  writeDoc(subdir: string, filename: string, frontmatter: Record<string, unknown>, body: string): string {
    const dir = join(this.paths.kb, subdir);
    mkdirSync(dir, { recursive: true });
    const path = join(dir, filename.endsWith(".md") ? filename : `${filename}.md`);
    const fm = { ...frontmatter, updated: new Date().toISOString() };
    const content = matter.stringify(body, fm);
    writeFileSync(path, content, "utf8");
    return path;
  }

  getDoc(slug: string): { title: string; path: string; body: string; frontmatter: Record<string, unknown> } | null {
    const row = this.db.prepare("SELECT * FROM kb_docs WHERE slug = ?").get(slug) as
      | { title: string; path: string; frontmatter_json: string }
      | undefined;
    if (!row || !existsSync(row.path)) return null;
    const parsed = matter(readFileSync(row.path, "utf8"));
    return {
      title: row.title,
      path: row.path,
      body: parsed.content,
      frontmatter: parsed.data as Record<string, unknown>,
    };
  }

  async indexFile(absPath: string): Promise<void> {
    if (!existsSync(absPath)) return;
    // Artifacts live outside the kb, where two README.md would collide on the UNIQUE slug.
    const rel = relative(this.paths.kb, absPath);
    const slug = slugify(rel.startsWith("..") ? absPath : basename(absPath));
    const raw = readFileSync(absPath, "utf8");
    const parsed = matter(raw);
    const contentHash = hashContent(raw);
    const title = (parsed.data.title as string) ?? slug;

    const existing = this.db.prepare("SELECT id, content_hash FROM kb_docs WHERE path = ?").get(absPath) as
      | { id: number; content_hash: string }
      | undefined;

    if (existing?.content_hash === contentHash) return;

    let docId: number;
    if (existing) {
      docId = existing.id;
      this.db
        .prepare(
          "UPDATE kb_docs SET slug=?, title=?, frontmatter_json=?, content_hash=?, updated_at=datetime('now') WHERE id=?",
        )
        .run(slug, title, JSON.stringify(parsed.data), contentHash, docId);
      this.db.prepare("DELETE FROM kb_chunks WHERE doc_id = ?").run(docId);
      this.vector.deleteByDoc(docId);
    } else {
      const r = this.db
        .prepare(
          "INSERT INTO kb_docs (slug, title, path, frontmatter_json, content_hash) VALUES (?, ?, ?, ?, ?)",
        )
        .run(slug, title, absPath, JSON.stringify(parsed.data), contentHash);
      docId = Number(r.lastInsertRowid);
    }

    const chunks = chunkMarkdown(parsed.content);
    if (chunks.length === 0) return;

    const texts = chunks.map((c) => (c.heading ? `${c.heading}\n${c.body}` : c.body));
    const embeddings = await this.ollama.embed(texts);

    for (let i = 0; i < chunks.length; i++) {
      const chunk = chunks[i]!;
      const emb = embeddings[i]!;
      const chunkHash = hashContent(chunk.body);
      const ins = this.db
        .prepare("INSERT INTO kb_chunks (doc_id, chunk_index, heading, body, content_hash) VALUES (?, ?, ?, ?, ?)")
        .run(docId, i, chunk.heading, chunk.body, chunkHash);
      const chunkId = Number(ins.lastInsertRowid);
      this.vector.upsert(chunkId, docId, emb);
      this.db.prepare("INSERT INTO kb_fts(rowid, body, heading) VALUES (?, ?, ?)").run(chunkId, chunk.body, chunk.heading ?? "");
    }
  }

  removeFile(absPath: string): void {
    const row = this.db.prepare("SELECT id FROM kb_docs WHERE path = ?").get(absPath) as { id: number } | undefined;
    if (!row) return;
    this.vector.deleteByDoc(row.id);
    this.db.prepare("DELETE FROM kb_docs WHERE id = ?").run(row.id);
  }

  async search(query: string, limit = 10, opts?: { subdir?: string }): Promise<KbSearchResult[]> {
    const prefix = opts?.subdir ? `${join(this.paths.kb, opts.subdir)}/%` : null;
    // Both legs filter after ranking, so over-fetch when most docs live in other subdirs.
    const fetch = prefix ? limit * 10 : limit * 2;

    const queryVec = await this.ollama.embedOne(query);
    const vectorHits = this.vector.search(queryVec, fetch);

    const ftsHits = this.db
      .prepare(
        `SELECT c.id, c.body, c.heading, d.slug, d.title, d.path, bm25(kb_fts) as rank
         FROM kb_fts f JOIN kb_chunks c ON c.id = f.rowid JOIN kb_docs d ON d.id = c.doc_id
         WHERE kb_fts MATCH ?${prefix ? " AND d.path LIKE ?" : ""} ORDER BY rank LIMIT ?`,
      )
      .all(...[query.replace(/[^\w\s]/g, " "), ...(prefix ? [prefix] : []), fetch]) as Array<{
      id: number;
      body: string;
      heading: string | null;
      slug: string;
      title: string;
      path: string;
      rank: number;
    }>;

    const scores = new Map<number, { score: number; row: KbSearchResult }>();

    vectorHits.forEach((hit, rank) => {
      const row = this.db
        .prepare(
          `SELECT c.body, c.heading, d.slug, d.title, d.path FROM kb_chunks c JOIN kb_docs d ON d.id = c.doc_id
           WHERE c.id = ?${prefix ? " AND d.path LIKE ?" : ""}`,
        )
        .get(...[hit.chunkId, ...(prefix ? [prefix] : [])]) as
        | { body: string; heading: string | null; slug: string; title: string; path: string }
        | undefined;
      if (!row) return;
      const rrf = 1 / (60 + rank + 1);
      scores.set(hit.chunkId, {
        score: rrf,
        row: { slug: row.slug, title: row.title, heading: row.heading, body: row.body, score: rrf, path: row.path },
      });
    });

    ftsHits.forEach((hit, rank) => {
      const rrf = 1 / (60 + rank + 1);
      const existing = scores.get(hit.id);
      if (existing) {
        existing.score += rrf;
        existing.row.score = existing.score;
      } else {
        scores.set(hit.id, {
          score: rrf,
          row: {
            slug: hit.slug,
            title: hit.title,
            heading: hit.heading,
            body: hit.body,
            score: rrf,
            path: hit.path,
          },
        });
      }
    });

    return [...scores.values()]
      .sort((a, b) => b.score - a.score)
      .slice(0, limit)
      .map((v) => v.row);
  }

  async reindexAll(): Promise<number> {
    const { readdirSync, statSync } = await import("node:fs");
    let count = 0;
    const walk = (dir: string) => {
      for (const entry of readdirSync(dir)) {
        const p = join(dir, entry);
        if (statSync(p).isDirectory()) walk(p);
        else if (p.endsWith(".md")) {
          void this.indexFile(p);
          count++;
        }
      }
    };
    walk(this.paths.kb);
    return count;
  }
}
