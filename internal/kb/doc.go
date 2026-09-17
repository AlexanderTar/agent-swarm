// Package kb indexes ~/.swarm/kb markdown with FTS5 and Ollama embeddings (§15).
package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	MaxChunk = 2000
	Overlap  = 200
)

type Doc struct {
	Slug         string
	Title        string
	Path         string
	Body         string
	Hash         string
	SupersededBy string
	Frontmatter  map[string]any
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ParseDoc splits YAML frontmatter from the body and picks a title.
func ParseDoc(slug, file string, raw []byte) (Doc, error) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\uFEFF")
	d := Doc{Slug: slug, Path: file, Body: text, Hash: sum(string(raw)), Frontmatter: map[string]any{}}
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if front, body, ok := splitFrontmatter(rest); ok {
			if err := yaml.Unmarshal([]byte(front), &d.Frontmatter); err != nil {
				return Doc{}, fmt.Errorf("%s: frontmatter: %w", slug, err)
			}
			if d.Frontmatter == nil {
				d.Frontmatter = map[string]any{}
			}
			d.Body = body
		}
	}
	d.Title, _ = d.Frontmatter["title"].(string)
	d.SupersededBy, _ = d.Frontmatter["superseded_by"].(string)
	if d.Title == "" {
		scanMarkdown(d.Body, func(_ string, level int, text string) {
			if d.Title == "" && level == 1 {
				d.Title = text
			}
		})
	}
	if d.Title == "" {
		d.Title = path.Base(slug)
	}
	return d, nil
}

// splitFrontmatter finds the first line of rest that is "---" plus optional trailing blanks.
func splitFrontmatter(rest string) (front, body string, ok bool) {
	for pos := 0; pos <= len(rest); {
		line, next, found := strings.Cut(rest[pos:], "\n")
		if strings.TrimRight(line, " \t") == "---" {
			if found {
				body = next
			}
			return rest[:max(pos-1, 0)], body, true
		}
		if !found {
			break
		}
		pos += len(line) + 1
	}
	return "", "", false
}

type Chunk struct {
	Index   int
	Heading string
	Body    string
	Hash    string
}

var heading = regexp.MustCompile(`^ {0,3}(#{1,3})[ \t]+(.+?)(?:[ \t]+#+)?[ \t]*$`)

// scanMarkdown calls fn for every line; level is 1–3 with the heading text for an
// ATX H1–H3 outside fenced code (CommonMark fence matching), else 0.
func scanMarkdown(body string, fn func(line string, level int, text string)) {
	var fenceChar byte
	fenceLen := 0
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimLeft(line, " ")
		if len(line)-len(t) > 3 {
			fn(line, 0, "")
			continue
		}
		c, n := fenceRun(t)
		switch {
		case fenceLen > 0:
			if c == fenceChar && n >= fenceLen && strings.TrimSpace(t[n:]) == "" {
				fenceLen = 0
			}
		case n >= 3 && !(c == '`' && strings.Contains(t[n:], "`")):
			fenceChar, fenceLen = c, n
		default:
			if m := heading.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[2]) != "" {
				fn(line, len(m[1]), strings.TrimSpace(m[2]))
				continue
			}
		}
		fn(line, 0, "")
	}
}

// fenceRun returns the fence character at the start of t and how many times it repeats.
func fenceRun(t string) (byte, int) {
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0
	}
	n := 1
	for n < len(t) && t[n] == t[0] {
		n++
	}
	return t[0], n
}

// ChunkMarkdown splits on H1–H3 (outside fences) into ≤2000-rune windows with 200 runes of overlap.
func ChunkMarkdown(body string) []Chunk {
	type section struct {
		heading string
		lines   []string
	}
	secs := []section{{}}
	scanMarkdown(body, func(line string, level int, text string) {
		if level > 0 {
			secs = append(secs, section{heading: text})
			return
		}
		secs[len(secs)-1].lines = append(secs[len(secs)-1].lines, line)
	})
	var out []Chunk
	for _, s := range secs {
		text := []rune(strings.TrimSpace(strings.Join(s.lines, "\n")))
		for start := 0; start < len(text); start += MaxChunk - Overlap {
			end := min(start+MaxChunk, len(text))
			piece := string(text[start:end])
			out = append(out, Chunk{Index: len(out), Heading: s.heading, Body: piece, Hash: sum(s.heading + "\n" + piece)})
			if end == len(text) {
				break
			}
		}
	}
	return out
}
