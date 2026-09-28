// Package attachments stores the images a user attaches to a New orchestrator
// request (spec docs/specs/2026-09-28-request-images.md). The directory
// <SWARM_HOME>/attachments/<ITEM-KEY>/ is the only record; Sweep removes it once
// the root item is Done, Cancelled or gone.
package attachments

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/items"
)

const (
	MaxCount = 10
	MaxBytes = 10 << 20
)

type Input struct{ Name, Data string }

type File struct {
	Name string // original name, display only
	Ext  string
	Body []byte
}

type Saved struct{ Path, Name string }

var exts = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

func bad(format string, a ...any) error {
	return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(format, a...)}
}

// Decode validates every image before anything is created; it never touches disk.
func Decode(in []Input) ([]File, error) {
	if len(in) > MaxCount {
		return nil, bad("At most 10 images.")
	}
	out := make([]File, 0, len(in))
	for _, a := range in {
		if base64.StdEncoding.DecodedLen(len(a.Data)) > MaxBytes+3 {
			return nil, bad("Image %q is larger than 10 MB.", a.Name)
		}
		body, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return nil, bad("Image %q is not valid base64.", a.Name)
		}
		if len(body) > MaxBytes {
			return nil, bad("Image %q is larger than 10 MB.", a.Name)
		}
		ext, ok := exts[http.DetectContentType(body)]
		if !ok {
			return nil, bad("Image %q is not a PNG, JPEG, GIF or WebP.", a.Name)
		}
		out = append(out, File{Name: a.Name, Ext: ext, Body: body})
	}
	return out, nil
}

func Dir(home, key string) string { return filepath.Join(home, "attachments", key) }

var unsafeRun = regexp.MustCompile(`[^a-z0-9.-]+`)

func safeName(name string) string {
	s := strings.Trim(unsafeRun.ReplaceAllString(strings.ToLower(name), "-"), "-.")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	if s == "" {
		return "image"
	}
	return s
}

// Save writes NN-<safe-name><ext>; any failure removes the whole directory.
func Save(home, key string, files []File) (saved []Saved, err error) {
	dir := Dir(home, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	for i, f := range files {
		base := strings.TrimSuffix(safeName(f.Name), filepath.Ext(safeName(f.Name)))
		if base == "" {
			base = "image"
		}
		p := filepath.Join(dir, fmt.Sprintf("%02d-%s%s", i+1, base, f.Ext))
		if err := os.WriteFile(p, f.Body, 0o600); err != nil {
			return nil, err
		}
		saved = append(saved, Saved{Path: p, Name: f.Name})
	}
	return saved, nil
}

func BriefSection(saved []Saved) string {
	var b strings.Builder
	b.WriteString("## Attachments\n\nThe user attached these images to the request. Open each one with your file or image tool before you plan, and pass the paths that matter to children in their briefs.\n")
	for _, s := range saved {
		fmt.Fprintf(&b, "\n- %s (%s)", s.Path, s.Name)
	}
	return b.String()
}

func FailureSection(count int, err error) string {
	return fmt.Sprintf("## Attachments\n\nThe user attached %d images, but Swarm could not save them (%v). Ask the user to share them another way.", count, err)
}
