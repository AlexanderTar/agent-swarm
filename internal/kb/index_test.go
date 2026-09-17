package kb

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db/dbtest"
)

var bg = context.Background()

// fakeEmb maps keyword counts to a 4-d vector and records what it embedded.
type fakeEmb struct {
	mu       sync.Mutex
	down     bool
	embedded []string
}

func (f *fakeEmb) Model() string { return "fake" }
func (f *fakeEmb) Check(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return ErrUnavailable
	}
	return nil
}
func (f *fakeEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	var out [][]float32
	for _, t := range texts {
		f.embedded = append(f.embedded, t)
		l := strings.ToLower(t)
		out = append(out, []float32{float32(strings.Count(l, "alpha")), float32(strings.Count(l, "beta")),
			float32(strings.Count(l, "gamma")), 0.01})
	}
	return out, nil
}
func (f *fakeEmb) count() int     { f.mu.Lock(); defer f.mu.Unlock(); return len(f.embedded) }
func (f *fakeEmb) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newIndex(t *testing.T, emb *fakeEmb) *Index {
	dir := t.TempDir()
	write(t, dir, "specs/alpha.md", "---\ntitle: Alpha spec\n---\n# Alpha\nalpha alpha design.\n## Details\nalpha details here.\n")
	write(t, dir, "decisions/beta.md", "# Beta decision\nbeta beta beta.\n")
	write(t, dir, "notes/gamma.md", "# Gamma\ngamma notes, also zeta keyword.\n")
	write(t, dir, "notes/old-alpha.md", "---\nsuperseded_by: specs/alpha\n---\n# Old alpha\nalpha alpha alpha alpha.\n")
	write(t, dir, ".hidden/skip.md", "# Hidden\nalpha\n")
	write(t, dir, "notes/readme.txt", "alpha")
	x := &Index{DB: dbtest.Open(t), Dir: dir, Emb: emb, Now: func() time.Time { return time.Unix(1_790_000_000, 0) }}
	if err := x.Load(bg); err != nil {
		t.Fatal(err)
	}
	if err := x.Sync(bg); err != nil {
		t.Fatal(err)
	}
	return x
}

func slugs(hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, h.Slug)
	}
	return out
}

func TestSyncIndexesAndSkipsUnchanged(t *testing.T) {
	emb := &fakeEmb{}
	x := newIndex(t, emb)
	st, err := x.Status(bg)
	if err != nil || st.Docs != 4 || st.Chunks != 5 || st.Embedded != 5 || st.Pending != 0 || !st.Available || st.Error != "" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if emb.count() != 5 {
		t.Fatalf("embedded %d texts", emb.count())
	}
	x.Sync(bg)
	if emb.count() != 5 {
		t.Fatal("unchanged docs must not be re-embedded")
	}
	write(t, x.Dir, "specs/alpha.md", "---\ntitle: Alpha spec\n---\n# Alpha\nalpha alpha design.\n## Details\nalpha details changed.\n")
	x.Sync(bg)
	if emb.count() != 6 || !strings.Contains(emb.embedded[5], "details changed") {
		t.Fatalf("only the changed chunk is re-embedded: %v", emb.embedded[5:])
	}
	os.Remove(filepath.Join(x.Dir, "decisions/beta.md"))
	x.Sync(bg)
	st, _ = x.Status(bg)
	var vectors int
	x.DB.QueryRow(`SELECT count(*) FROM kb_vectors`).Scan(&vectors)
	if st.Docs != 3 || st.Chunks != 4 || vectors != 4 {
		t.Fatalf("after delete: %+v, vectors %d", st, vectors)
	}
	hits, _ := x.Search(bg, "beta", 5)
	if slices.Contains(slugs(hits), "decisions/beta") {
		t.Fatal("deleted doc still searchable (memory index out of step)")
	}
}

func TestSearchFusesVectorAndText(t *testing.T) {
	x := newIndex(t, &fakeEmb{})
	hits, err := x.Search(bg, "alpha", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Slug != "specs/alpha" || hits[0].Title != "Alpha spec" || hits[0].Score <= 0 {
		t.Fatalf("hits = %+v", hits)
	}
	if slices.Contains(slugs(hits), "notes/old-alpha") {
		t.Fatal("superseded docs are dropped")
	}
	zeta, _ := x.Search(bg, "zeta", 3)
	if len(zeta) == 0 || zeta[0].Slug != "notes/gamma" || !strings.Contains(zeta[0].Snippet, "zeta") {
		t.Fatalf("text-only match = %+v", zeta)
	}
	again, _ := x.Search(bg, "alpha", 3)
	if !slices.Equal(slugs(again), slugs(hits)) {
		t.Fatal("search must be deterministic")
	}
	if hits, _ := x.Search(bg, "   ", 3); len(hits) != 0 {
		t.Fatalf("blank query = %+v", hits)
	}
}

func TestRRF(t *testing.T) {
	got := rrf([]int64{1, 2, 3}, []int64{3, 1})
	want := []scored{{1, 1.0/61 + 1.0/62}, {3, 1.0/63 + 1.0/61}, {2, 1.0 / 62}}
	if len(got) != 3 {
		t.Fatalf("rrf = %+v", got)
	}
	for i := range want {
		if got[i].id != want[i].id || math.Abs(got[i].score-want[i].score) > 1e-12 {
			t.Errorf("rrf[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	tie := rrf([]int64{7}, []int64{5})
	if tie[0].id != 5 || tie[1].id != 7 {
		t.Errorf("ties break by id: %+v", tie)
	}
}

func TestTopKMatchesBruteForce(t *testing.T) {
	x := &Index{}
	rng := rand.New(rand.NewSource(7))
	raw := map[int64][]float32{}
	for id := int64(1); id <= 200; id++ {
		v := []float32{rng.Float32() - 0.5, rng.Float32() - 0.5, rng.Float32() - 0.5}
		raw[id] = v
	}
	x.setVectors(raw)
	q := []float32{0.3, -0.2, 0.9}
	got := x.topK(normalize(q), 10)
	cos := func(a, b []float32) float64 {
		var dot, na, nb float64
		for i := range a {
			dot += float64(a[i] * b[i])
			na += float64(a[i] * a[i])
			nb += float64(b[i] * b[i])
		}
		return dot / math.Sqrt(na*nb)
	}
	var ref []scored
	for id, v := range raw {
		ref = append(ref, scored{id, cos(q, v)})
	}
	slices.SortFunc(ref, func(a, b scored) int {
		if a.score != b.score {
			if a.score > b.score {
				return -1
			}
			return 1
		}
		return int(a.id - b.id)
	})
	for i := range 10 {
		if got[i] != ref[i].id {
			t.Fatalf("topK[%d] = %d, want %d", i, got[i], ref[i].id)
		}
	}
}

func TestOllamaDownKeepsIndexing(t *testing.T) {
	emb := &fakeEmb{down: true}
	x := newIndex(t, emb)
	st, _ := x.Status(bg)
	if st.Available || st.Pending != 5 || st.Embedded != 0 || st.Error != ErrUnavailable.Error() || st.Docs != 4 {
		t.Fatalf("status = %+v", st)
	}
	if _, err := x.Search(bg, "alpha", 3); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("search = %v", err)
	}
	emb.setDown(false)
	x.Sync(bg)
	st, _ = x.Status(bg)
	if !st.Available || st.Pending != 0 || st.Embedded != 5 {
		t.Fatalf("after recovery = %+v", st)
	}
	if hits, err := x.Search(bg, "alpha", 3); err != nil || len(hits) == 0 {
		t.Fatalf("search after recovery = %v %v", hits, err)
	}
}

func TestGet(t *testing.T) {
	x := newIndex(t, &fakeEmb{})
	d, err := x.Get(bg, "specs/alpha")
	if err != nil || d.Title != "Alpha spec" || !strings.HasPrefix(d.Markdown, "# Alpha") || d.Frontmatter["title"] != "Alpha spec" {
		t.Fatalf("Get = %+v, %v", d, err)
	}
	var nf *NotFoundError
	if _, err := x.Get(bg, "specs/missing"); !errors.As(err, &nf) || err.Error() != "No document specs/missing." {
		t.Fatalf("missing = %v", err)
	}
}

func TestWatchDebounces(t *testing.T) {
	x := newIndex(t, &fakeEmb{})
	ready := make(chan struct{})
	x.watchReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { x.Watch(ctx, 200*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }() // stop the watcher before the DB closes
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher never registered its folders")
	}
	before := x.syncs.Load()
	for i := range 5 {
		write(t, x.Dir, "notes/burst.md", strings.Repeat("alpha ", i+1))
	}
	write(t, x.Dir, "brand-new/deep.md", "# New folder\nbeta\n")
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := x.Status(bg)
		if st.Docs == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch did not index: %+v", st)
		}
		time.Sleep(20 * time.Millisecond) // poll interval, not a wait for the watcher
	}
	if n := x.syncs.Load() - before; n < 1 || n > 3 {
		t.Fatalf("burst caused %d syncs, want it debounced", n)
	}
}

func TestSearchEmbedFailureMarksUnavailable(t *testing.T) {
	emb := &fakeEmb{}
	x := newIndex(t, emb)
	emb.setDown(true)
	if _, err := x.Search(bg, "alpha", 3); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("search = %v", err)
	}
	if st, _ := x.Status(bg); st.Available || st.Error != ErrUnavailable.Error() {
		t.Fatalf("status after failed search = %+v", st)
	}
	emb.setDown(false)
	if _, err := x.Search(bg, "alpha", 3); err != nil {
		t.Fatal(err)
	}
	if st, _ := x.Status(bg); !st.Available || st.Error != "" {
		t.Fatalf("status after recovery = %+v", st)
	}
}

func TestSyncEmbedsPendingWithoutFileChanges(t *testing.T) {
	emb := &fakeEmb{down: true}
	x := newIndex(t, emb)
	if st, _ := x.Status(bg); st.Pending != 5 {
		t.Fatalf("status = %+v", st)
	}
	emb.setDown(false)
	if err := x.Sync(bg); err != nil { // no file changed since the first Sync
		t.Fatal(err)
	}
	if st, _ := x.Status(bg); st.Pending != 0 || st.Embedded != 5 || !st.Available {
		t.Fatalf("status = %+v", st)
	}
	// "alphaish" has no FTS match, so any hit comes from the vector index.
	if hits, err := x.Search(bg, "alphaish", 3); err != nil || len(hits) == 0 {
		t.Fatalf("vector-only search = %v, %v", hits, err)
	}
}

func TestSyncReturnsDBErrors(t *testing.T) {
	emb := &fakeEmb{down: true}
	x := newIndex(t, emb)
	if _, err := x.DB.Exec(`CREATE TRIGGER boom BEFORE INSERT ON kb_vectors BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	emb.setDown(false)
	if err := x.Sync(bg); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Sync = %v, want the vector insert error", err)
	}
	x.DB.Close()
	if err := x.Sync(bg); err == nil {
		t.Fatal("Sync on a closed DB must fail")
	}
}

func TestSyncWithoutChangesKeepsMemoryIndex(t *testing.T) {
	x := newIndex(t, &fakeEmb{})
	x.mu.RLock()
	before := reflect.ValueOf(x.vecs).Pointer()
	x.mu.RUnlock()
	if err := x.Sync(bg); err != nil {
		t.Fatal(err)
	}
	x.mu.RLock()
	after := reflect.ValueOf(x.vecs).Pointer()
	x.mu.RUnlock()
	if before != after {
		t.Fatal("a no-change Sync must not reload every vector")
	}
}

func TestSyncReloadsAfterPartialFailure(t *testing.T) {
	emb := &fakeEmb{down: true}
	x := newIndex(t, emb) // 5 chunks pending, nothing in memory
	if _, err := x.DB.Exec(`CREATE TRIGGER boom BEFORE INSERT ON kb_vectors
		WHEN new.chunk_id = (SELECT max(id) FROM kb_chunks) BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	emb.setDown(false)
	if err := x.Sync(bg); err == nil { // stores 4 vectors, then fails
		t.Fatal("want the injected failure")
	}
	x.DB.Exec(`DROP TRIGGER boom`)
	emb.setDown(true)
	if err := x.Sync(bg); err != nil { // no file change and nothing embedded
		t.Fatal(err)
	}
	emb.setDown(false)
	// "alphaish" has no FTS match, so hits must come from the 4 committed vectors.
	hits, err := x.Search(bg, "alphaish", 5)
	if err != nil || !slices.Contains(slugs(hits), "specs/alpha") {
		t.Fatalf("search after partial failure = %v, %v", hits, err)
	}
}
