package kb

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/fsnotify/fsnotify"
)

const batchSize = 32

type Hit struct {
	Slug    string  `json:"slug"`
	Title   string  `json:"title"`
	Heading string  `json:"heading"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

type DocView struct {
	Slug        string         `json:"slug"`
	Title       string         `json:"title"`
	Markdown    string         `json:"markdown"`
	Frontmatter map[string]any `json:"frontmatter"`
}

type Status struct {
	Docs      int    `json:"docs"`
	Chunks    int    `json:"chunks"`
	Embedded  int    `json:"embedded"`
	Pending   int    `json:"pending"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type NotFoundError struct{ Slug string }

func (e *NotFoundError) Error() string { return "No document " + e.Slug + "." }

type Index struct {
	DB  *db.DB
	Dir string
	Emb Embedder
	Now func() time.Time

	syncMu    sync.Mutex // one Sync at a time
	stale     bool       // guarded by syncMu: the DB may differ from vecs; cleared by a successful reload
	mu        sync.RWMutex
	vecs      map[int64][]float32
	available bool
	lastErr   string
	syncs     atomic.Int64

	watchReady func() // test hook: called once every folder is watched
}

type scored struct {
	id    int64
	score float64
}

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	inv := 1 / math.Sqrt(n)
	for i, x := range v {
		out[i] = float32(float64(x) * inv)
	}
	return out
}

func encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// setVectors replaces the in-memory index, normalising each vector.
// ponytail: map of slices; switch to one flat []float32 if memory or speed matter.
func (x *Index) setVectors(raw map[int64][]float32) {
	m := make(map[int64][]float32, len(raw))
	for id, v := range raw {
		m[id] = normalize(v)
	}
	x.mu.Lock()
	x.vecs = m
	x.mu.Unlock()
}

func (x *Index) checkAvailable(ctx context.Context) bool {
	err := x.Emb.Check(ctx)
	x.mu.Lock()
	defer x.mu.Unlock()
	x.available = err == nil
	x.lastErr = ""
	if err != nil {
		x.lastErr = ErrUnavailable.Error()
	}
	return x.available
}

// Load checks Ollama and reads stored vectors into memory.
func (x *Index) Load(ctx context.Context) error {
	x.checkAvailable(ctx)
	x.syncMu.Lock()
	defer x.syncMu.Unlock()
	err := x.reload(ctx)
	x.stale = err != nil
	return err
}

func (x *Index) reload(ctx context.Context) error {
	rows, err := x.DB.QueryContext(ctx, `SELECT chunk_id, vec FROM kb_vectors WHERE model = ?`, x.Emb.Model())
	if err != nil {
		return err
	}
	defer rows.Close()
	raw := map[int64][]float32{}
	for rows.Next() {
		var id int64
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return err
		}
		raw[id] = decode(b)
	}
	x.setVectors(raw)
	return rows.Err()
}

// Sync brings the database in line with the folder, then embeds pending chunks.
func (x *Index) Sync(ctx context.Context) error {
	x.syncMu.Lock()
	defer x.syncMu.Unlock()
	x.syncs.Add(1)
	if err := os.MkdirAll(x.Dir, 0o755); err != nil {
		return err
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(x.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != x.Dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(x.Dir, p)
		slug := filepath.ToSlash(strings.TrimSuffix(rel, ".md"))
		seen[slug] = true
		c, err := x.syncFile(ctx, slug, p)
		x.stale = x.stale || c
		return err
	})
	if err != nil {
		return err
	}
	rows, err := x.DB.QueryContext(ctx, `SELECT slug FROM kb_docs`)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			rows.Close()
			return err
		}
		if !seen[slug] {
			gone = append(gone, slug)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	x.stale = x.stale || len(gone) > 0
	for _, slug := range gone {
		// delete chunks directly so the kb_fts delete trigger fires; vectors cascade
		if _, err := x.DB.ExecContext(ctx, `DELETE FROM kb_chunks WHERE doc_id = (SELECT id FROM kb_docs WHERE slug = ?)`, slug); err != nil {
			return err
		}
		if _, err := x.DB.ExecContext(ctx, `DELETE FROM kb_docs WHERE slug = ?`, slug); err != nil {
			return err
		}
	}
	embedded, err := x.embedPending(ctx)
	x.stale = x.stale || embedded
	if err != nil {
		return err
	}
	if !x.stale {
		return nil // nothing moved: keep the in-memory vectors
	}
	if err := x.reload(ctx); err != nil {
		return err
	}
	x.stale = false
	return nil
}

// syncFile upserts one document and reports whether the database changed.
func (x *Index) syncFile(ctx context.Context, slug, file string) (bool, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return false, nil // unreadable files are skipped
	}
	var docID int64
	var oldHash string
	err = x.DB.QueryRowContext(ctx, `SELECT id, content_hash FROM kb_docs WHERE slug = ?`, slug).Scan(&docID, &oldHash)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if oldHash == sum(string(raw)) {
		return false, nil
	}
	doc, perr := ParseDoc(slug, file, raw)
	if perr != nil {
		return false, nil // bad frontmatter: leave the previous version indexed
	}
	fm, _ := json.Marshal(doc.Frontmatter)
	now := db.Millis(x.Now())
	return true, x.DB.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `INSERT INTO kb_docs (slug, title, path, frontmatter_json, content_hash, superseded_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
			ON CONFLICT(slug) DO UPDATE SET title = excluded.title, path = excluded.path,
			frontmatter_json = excluded.frontmatter_json, content_hash = excluded.content_hash,
			superseded_by = excluded.superseded_by, updated_at = excluded.updated_at
			RETURNING id`, slug, doc.Title, file, string(fm), doc.Hash, doc.SupersededBy, now, now).Scan(&docID)
		if err != nil {
			return err
		}
		existing := map[int]struct {
			id   int64
			hash string
		}{}
		rows, err := tx.QueryContext(ctx, `SELECT id, chunk_index, content_hash FROM kb_chunks WHERE doc_id = ?`, docID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var idx int
			var h string
			if err := rows.Scan(&id, &idx, &h); err != nil {
				rows.Close()
				return err
			}
			existing[idx] = struct {
				id   int64
				hash string
			}{id, h}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		chunks := ChunkMarkdown(doc.Body)
		for _, c := range chunks {
			old, ok := existing[c.Index]
			switch {
			case ok && old.hash == c.Hash:
			case ok:
				if _, err := tx.ExecContext(ctx, `UPDATE kb_chunks SET heading = ?, body = ?, content_hash = ? WHERE id = ?`,
					c.Heading, c.Body, c.Hash, old.id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM kb_vectors WHERE chunk_id = ?`, old.id); err != nil {
					return err
				}
			default:
				if _, err := tx.ExecContext(ctx, `INSERT INTO kb_chunks (doc_id, chunk_index, heading, body, content_hash)
					VALUES (?, ?, ?, ?, ?)`, docID, c.Index, c.Heading, c.Body, c.Hash); err != nil {
					return err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM kb_chunks WHERE doc_id = ? AND chunk_index >= ?`, docID, len(chunks))
		return err
	})
}

// embedPending embeds chunks without a vector and reports whether it stored any.
// An unavailable embedder is a state (see Status), not an error; DB failures are errors.
func (x *Index) embedPending(ctx context.Context) (bool, error) {
	if !x.checkAvailable(ctx) {
		return false, nil
	}
	stored := false
	for {
		ids, texts, err := x.pendingBatch(ctx)
		if err != nil || len(ids) == 0 {
			return stored, err
		}
		vecs, err := x.Emb.Embed(ctx, texts)
		if err == nil && len(vecs) != len(ids) {
			err = errors.New("embedder returned the wrong number of vectors")
		}
		if err != nil {
			x.markUnavailable()
			return stored, nil
		}
		for i, id := range ids {
			v := normalize(vecs[i])
			if _, err := x.DB.ExecContext(ctx, `INSERT OR REPLACE INTO kb_vectors (chunk_id, model, dim, vec) VALUES (?, ?, ?, ?)`,
				id, x.Emb.Model(), len(v), encode(v)); err != nil {
				return stored, err
			}
			stored = true
		}
	}
}

func (x *Index) pendingBatch(ctx context.Context) ([]int64, []string, error) {
	rows, err := x.DB.QueryContext(ctx, `SELECT c.id, COALESCE(c.heading, ''), c.body FROM kb_chunks c
		LEFT JOIN kb_vectors v ON v.chunk_id = c.id AND v.model = ?
		WHERE v.chunk_id IS NULL ORDER BY c.id LIMIT ?`, x.Emb.Model(), batchSize)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var texts []string
	for rows.Next() {
		var id int64
		var h, b string
		if err := rows.Scan(&id, &h, &b); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		texts = append(texts, strings.TrimSpace(h+"\n"+b))
	}
	return ids, texts, rows.Err()
}

func (x *Index) markUnavailable() {
	x.mu.Lock()
	x.available, x.lastErr = false, ErrUnavailable.Error()
	x.mu.Unlock()
}

// topK returns the k chunk ids with the highest dot product (vectors are unit length).
func (x *Index) topK(q []float32, k int) []int64 {
	x.mu.RLock()
	all := make([]scored, 0, len(x.vecs))
	for id, v := range x.vecs {
		var dot float64
		for i := range min(len(q), len(v)) {
			dot += float64(q[i] * v[i])
		}
		all = append(all, scored{id, dot})
	}
	x.mu.RUnlock()
	sortScored(all)
	var out []int64
	for _, s := range all[:min(k, len(all))] {
		out = append(out, s.id)
	}
	return out
}

func sortScored(s []scored) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].score != s[j].score {
			return s[i].score > s[j].score
		}
		return s[i].id < s[j].id
	})
}

// rrf fuses ranked id lists with reciprocal rank fusion, k = 60.
func rrf(lists ...[]int64) []scored {
	scores := map[int64]float64{}
	for _, list := range lists {
		for rank, id := range list {
			scores[id] += 1.0 / float64(60+rank+1)
		}
	}
	out := make([]scored, 0, len(scores))
	for id, s := range scores {
		out = append(out, scored{id, s})
	}
	sortScored(out)
	return out
}

func kbQuery(q string) string {
	var parts []string
	for _, tok := range strings.Fields(q) {
		tok = strings.ReplaceAll(tok, `"`, "")
		if strings.IndexFunc(tok, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			parts = append(parts, `"`+tok+`"*`)
		}
	}
	return strings.Join(parts, " OR ")
}

func (x *Index) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = 10
	}
	fq := kbQuery(q)
	if fq == "" {
		return []Hit{}, nil
	}
	x.mu.RLock()
	ok := x.available
	x.mu.RUnlock()
	if !ok && !x.checkAvailable(ctx) {
		return nil, ErrUnavailable
	}
	qv, err := x.Emb.Embed(ctx, []string{q})
	if err != nil || len(qv) != 1 {
		x.markUnavailable()
		return nil, ErrUnavailable
	}
	vectorIDs := x.topK(normalize(qv[0]), 2*limit)
	var textIDs []int64
	rows, err := x.DB.QueryContext(ctx, `SELECT rowid FROM kb_fts WHERE kb_fts MATCH ? ORDER BY bm25(kb_fts) LIMIT ?`, fq, 2*limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		textIDs = append(textIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hits := []Hit{}
	for _, s := range rrf(vectorIDs, textIDs) {
		if len(hits) == limit {
			break
		}
		var h Hit
		var superseded sql.NullString
		err := x.DB.QueryRowContext(ctx, `SELECT d.slug, d.title, COALESCE(c.heading, ''), c.body, d.superseded_by
			FROM kb_chunks c JOIN kb_docs d ON d.id = c.doc_id WHERE c.id = ?`, s.id).
			Scan(&h.Slug, &h.Title, &h.Heading, &h.Snippet, &superseded)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err != nil || superseded.Valid {
			continue // chunk deleted since ranking, or doc superseded
		}
		snippet := []rune(strings.Join(strings.Fields(h.Snippet), " "))
		h.Snippet = string(snippet[:min(200, len(snippet))])
		h.Score = s.score
		hits = append(hits, h)
	}
	return hits, nil
}

func (x *Index) Get(ctx context.Context, slug string) (DocView, error) {
	var file string
	err := x.DB.QueryRowContext(ctx, `SELECT path FROM kb_docs WHERE slug = ?`, slug).Scan(&file)
	if errors.Is(err, sql.ErrNoRows) {
		return DocView{}, &NotFoundError{Slug: slug}
	}
	if err != nil {
		return DocView{}, err
	}
	raw, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return DocView{}, &NotFoundError{Slug: slug}
	}
	if err != nil {
		return DocView{}, err
	}
	d, err := ParseDoc(slug, file, raw)
	if err != nil {
		return DocView{}, err
	}
	return DocView{Slug: slug, Title: d.Title, Markdown: d.Body, Frontmatter: d.Frontmatter}, nil
}

func (x *Index) Status(ctx context.Context) (Status, error) {
	var st Status
	err := x.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM kb_docs), (SELECT count(*) FROM kb_chunks),
		(SELECT count(*) FROM kb_vectors WHERE model = ?)`, x.Emb.Model()).Scan(&st.Docs, &st.Chunks, &st.Embedded)
	st.Pending = st.Chunks - st.Embedded
	x.mu.RLock()
	st.Available, st.Error = x.available, x.lastErr
	x.mu.RUnlock()
	return st, err
}

// Watch re-syncs after changes settle for `debounce` (§15: 500 ms). It blocks until ctx ends.
func (x *Index) Watch(ctx context.Context, debounce time.Duration) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	addTree := func(root string) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if p != x.Dir && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			w.Add(p)
			return nil
		})
	}
	os.MkdirAll(x.Dir, 0o755)
	addTree(x.Dir)
	if x.watchReady != nil {
		x.watchReady()
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					addTree(ev.Name)
				}
			}
			timer.Reset(debounce)
		case <-w.Errors:
		case <-timer.C:
			x.Sync(ctx)
		}
	}
}
