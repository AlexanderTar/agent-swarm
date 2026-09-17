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

var h1 = regexp.MustCompile(`(?m)^#\s+(.+?)\s*$`)

// ParseDoc splits YAML frontmatter from the body and picks a title.
func ParseDoc(slug, file string, raw []byte) (Doc, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	d := Doc{Slug: slug, Path: file, Body: text, Hash: sum(string(raw)), Frontmatter: map[string]any{}}
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if end := strings.Index(rest, "\n---\n"); end >= 0 {
			if err := yaml.Unmarshal([]byte(rest[:end]), &d.Frontmatter); err != nil {
				return Doc{}, fmt.Errorf("%s: frontmatter: %w", slug, err)
			}
			if d.Frontmatter == nil {
				d.Frontmatter = map[string]any{}
			}
			d.Body = rest[end+len("\n---\n"):]
		}
	}
	d.Title, _ = d.Frontmatter["title"].(string)
	d.SupersededBy, _ = d.Frontmatter["superseded_by"].(string)
	if d.Title == "" {
		if m := h1.FindStringSubmatch(d.Body); m != nil {
			d.Title = m[1]
		} else {
			d.Title = path.Base(slug)
		}
	}
	return d, nil
}

type Chunk struct {
	Index   int
	Heading string
	Body    string
	Hash    string
}

var heading = regexp.MustCompile(`^#{1,3}\s+(.+?)(\s+#+)?\s*$`)

// ChunkMarkdown splits on H1–H3 (outside fences) into ≤2000-rune windows with 200 runes of overlap.
func ChunkMarkdown(body string) []Chunk {
	type section struct {
		heading string
		lines   []string
	}
	secs := []section{{}}
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence {
			if m := heading.FindStringSubmatch(line); m != nil {
				secs = append(secs, section{heading: m[1]})
				continue
			}
		}
		secs[len(secs)-1].lines = append(secs[len(secs)-1].lines, line)
	}
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
