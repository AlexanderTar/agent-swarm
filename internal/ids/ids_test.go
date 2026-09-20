package ids

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestKebab(t *testing.T) {
	long := "alpha-bravo-charlie-delta-echo-foxtrot-golf-hotel-india"
	cases := []struct{ in, want string }{
		{"Investigate login crash", "investigate-login-crash"},
		{"Café Crash", "cafe-crash"},
		{"  hello ,,  world!! ", "hello-world"},
		{"--Hi--", "hi"},
		{"Auth_Epic/V2", "auth-epic-v2"},
		{long, "alpha-bravo-charlie-delta-echo-foxtrot-golf"},
		{strings.Repeat("a", 60), strings.Repeat("a", 48)},
	}
	for _, c := range cases {
		got, err := Kebab(c.in)
		if err != nil || got != c.want {
			t.Errorf("Kebab(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
		if len(got) > MaxName {
			t.Errorf("Kebab(%q) longer than %d", c.in, MaxName)
		}
	}
	for _, in := range []string{"🔥🔥", "", "---", "日本語"} {
		if _, err := Kebab(in); !errors.Is(err, ErrEmptyName) {
			t.Errorf("Kebab(%q) err = %v, want ErrEmptyName", in, err)
		}
	}
	if ErrEmptyName.Error() != "Enter a name containing a letter or number." {
		t.Errorf("copy = %q", ErrEmptyName.Error())
	}
}

func TestKebabMaxCutsAtBoundary(t *testing.T) {
	got, _ := KebabMax("Authentication epic for login", 24)
	if got != "authentication-epic-for" {
		t.Fatalf("got %q", got)
	}
	got, _ = KebabMax("abc-defgh", 4) // s[4] is 'd'; last '-' at 3 → "abc"
	if got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestUnique(t *testing.T) {
	taken := map[string]bool{"coder": true, "coder-2": true}
	if got := Unique("coder", func(s string) bool { return taken[s] }); got != "coder-3" {
		t.Fatalf("got %q", got)
	}
	if got := Unique("fresh", func(string) bool { return false }); got != "fresh" {
		t.Fatalf("got %q", got)
	}
	base := strings.Repeat("a", 44) + "-bcd" // 48 chars
	got := Unique(base, func(s string) bool { return s == base })
	if got != strings.Repeat("a", 44)+"-b-2" || len(got) > MaxName {
		t.Fatalf("got %q (%d)", got, len(got))
	}
	// truncation that lands on a '-' is trimmed before the suffix
	base = strings.Repeat("a", 45) + "-bc" // cut to 46 → "aaa…a-" → trimmed
	got = Unique(base, func(s string) bool { return s == base })
	if got != strings.Repeat("a", 45)+"-2" {
		t.Fatalf("got %q", got)
	}
}

func TestNewPrefixesAndOrder(t *testing.T) {
	for _, p := range []string{"itm", "agt", "ses", "msg", "ckp", "wt", "req", "art", "ntf", "repo", "adv"} {
		id := New(p)
		if !strings.HasPrefix(id, p+"_") || len(id) != len(p)+1+26 {
			t.Errorf("New(%q) = %q", p, id)
		}
	}
	var got []string
	for range 1000 {
		got = append(got, New("itm"))
	}
	if !slices.IsSorted(got) {
		t.Fatal("ids are not in creation order")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown prefix must panic")
		}
	}()
	New("bogus")
}

func openCounters(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "k.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE key_counters (type TEXT PRIMARY KEY, next INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return d
}

func nextKey(t *testing.T, d *sql.DB, typ string) string {
	tx, err := d.Begin()
	if err != nil {
		t.Error(err)
		return ""
	}
	k, err := NextKey(context.Background(), tx, typ)
	if err != nil {
		tx.Rollback()
		t.Error(err)
		return ""
	}
	if err := tx.Commit(); err != nil {
		t.Error(err)
	}
	return k
}

func TestNextKeySeparateCounters(t *testing.T) {
	d := openCounters(t)
	got := []string{nextKey(t, d, "task"), nextKey(t, d, "task"), nextKey(t, d, "epic"), nextKey(t, d, "spike"), nextKey(t, d, "chore")}
	want := []string{"TASK-1", "TASK-2", "EPIC-1", "SPIKE-1", "CHORE-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	tx, _ := d.Begin()
	defer tx.Rollback()
	if _, err := NextKey(context.Background(), tx, "widget"); err == nil {
		t.Fatal("unknown type must fail")
	}
}

func TestNextKeyNoGapsUnderConcurrency(t *testing.T) {
	d := openCounters(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := nextKey(t, d, "task")
			mu.Lock()
			seen[k] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i := 1; i <= 50; i++ {
		if !seen[fmt.Sprintf("TASK-%d", i)] {
			t.Fatalf("missing TASK-%d in %v", i, seen)
		}
	}
}
