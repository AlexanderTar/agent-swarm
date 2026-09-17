package kb

import (
	"strings"
	"testing"
)

const fence = "\x60\x60\x60"

func TestChunkMarkdownSplitsOnHeadings(t *testing.T) {
	md := "Intro line.\n\n# Title\nBody one.\n\n## Section A\nA text.\n" + fence + "go\n# not a heading\n" + fence +
		"\n### Deep\nDeep text.\n#### Too deep\nstays in Deep.\n~~~\n## also not a heading\n~~~\n## Empty\n\n## Last ##\nEnd."
	got := ChunkMarkdown(md)
	want := []struct{ heading, body string }{
		{"", "Intro line."},
		{"Title", "Body one."},
		{"Section A", "A text.\n" + fence + "go\n# not a heading\n" + fence},
		{"Deep", "Deep text.\n#### Too deep\nstays in Deep.\n~~~\n## also not a heading\n~~~"},
		{"Last", "End."},
	}
	if len(got) != len(want) {
		t.Fatalf("chunks = %+v", got)
	}
	for i, w := range want {
		if got[i].Index != i || got[i].Heading != w.heading || got[i].Body != w.body || len(got[i].Hash) != 64 {
			t.Errorf("chunk %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestChunkMarkdownWindows(t *testing.T) {
	body := strings.Repeat("é", 4500) // runes, not bytes
	got := ChunkMarkdown("## Long\n" + body)
	if len(got) != 3 {
		t.Fatalf("chunks = %d", len(got))
	}
	lens := []int{2000, 2000, 900}
	for i, c := range got {
		if n := len([]rune(c.Body)); n != lens[i] || c.Heading != "Long" {
			t.Errorf("chunk %d: %d runes, heading %q", i, n, c.Heading)
		}
	}
	mixed := strings.Repeat("a", 1990) + strings.Repeat("b", 500)
	got = ChunkMarkdown(mixed)
	first, second := []rune(got[0].Body), []rune(got[1].Body)
	if string(first[len(first)-Overlap:]) != string(second[:Overlap]) {
		t.Error("consecutive chunks must overlap by 200 runes")
	}
	if got := ChunkMarkdown(strings.Repeat("x", 2000)); len(got) != 1 {
		t.Errorf("exactly 2000 runes = %d chunks", len(got))
	}
	if got := ChunkMarkdown("\n\n  \n"); len(got) != 0 {
		t.Errorf("blank doc = %d chunks", len(got))
	}
}

func TestChunkHashes(t *testing.T) {
	a := ChunkMarkdown("## H\nbody")[0].Hash
	if ChunkMarkdown("## H\nbody")[0].Hash != a {
		t.Error("hash must be stable")
	}
	if ChunkMarkdown("## H\nbody!")[0].Hash == a || ChunkMarkdown("## G\nbody")[0].Hash == a {
		t.Error("hash must cover heading and body")
	}
}

func TestParseDoc(t *testing.T) {
	raw := "---\r\ntitle: Board decisions\r\ntags: [board, ui]\r\nsuperseded_by: decisions/board-v2\r\n---\r\n# Ignored H1\r\nText.\r\n"
	d, err := ParseDoc("decisions/board", "/kb/decisions/board.md", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Board decisions" || d.SupersededBy != "decisions/board-v2" || d.Slug != "decisions/board" ||
		d.Path != "/kb/decisions/board.md" || d.Body != "# Ignored H1\nText.\n" || len(d.Hash) != 64 {
		t.Fatalf("doc = %+v", d)
	}
	if tags, _ := d.Frontmatter["tags"].([]any); len(tags) != 2 {
		t.Fatalf("frontmatter = %+v", d.Frontmatter)
	}
	d, _ = ParseDoc("notes/x", "/kb/notes/x.md", []byte("Intro\n# First heading\nbody"))
	if d.Title != "First heading" || len(d.Frontmatter) != 0 || d.Frontmatter == nil || d.SupersededBy != "" {
		t.Fatalf("no frontmatter = %+v", d)
	}
	d, _ = ParseDoc("notes/plain-file", "/kb/notes/plain-file.md", []byte("no headings at all"))
	if d.Title != "plain-file" {
		t.Fatalf("slug fallback = %q", d.Title)
	}
	if _, err := ParseDoc("bad", "/kb/bad.md", []byte("---\ntitle: [unclosed\n---\nx")); err == nil {
		t.Fatal("bad YAML must fail")
	}
	d, _ = ParseDoc("dash", "/kb/dash.md", []byte("---\nnot closed\n"))
	if d.Body != "---\nnot closed\n" {
		t.Fatalf("unterminated frontmatter is body: %q", d.Body)
	}
}
