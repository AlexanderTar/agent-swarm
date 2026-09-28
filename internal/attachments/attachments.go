// Package attachments stores the images a user attaches to a New orchestrator
// request (spec docs/specs/2026-09-28-request-images.md). The directory
// <SWARM_HOME>/attachments/<ITEM-KEY>/ is the only record; Sweep removes it once
// the root item is Done, Cancelled or gone.
//
// # Which agent kinds can view an attached image
//
// Every kind gets the saved paths in its kickoff prompt (see the runtime's
// prompt assembly); only some can actually open the file. Probed live
// 2026-09-28 on an isolated `tmux -L imgprobe` socket, one session per kind,
// each pointed at a PNG reading "SWARM" and asked to open it by absolute
// path and report the word:
//
//	kind    CLI version              result
//	claude  2.1.283 (Claude Code)    sees image (Read tool; answered SWARM)
//	codex   codex-cli 0.158.0        not probed (real account's 5h usage
//	                                 window was exhausted by the other
//	                                 probes; codex ships a view_image tool
//	                                 for local paths, but that was not
//	                                 confirmed live this run)
//	agy     1.2.12                   sees image (Read tool; answered SWARM)
//	cursor  2026.09.26-dd393fe       sees image (Read tool; answered SWARM,
//	                                 on the account's Auto model -- named
//	                                 models were unavailable on this plan)
//	muse    1.4.0 (1.4.0-R4302.1)    sees image (Read tool; answered SWARM)
//
// A kind that can't view images (none observed here) still gets every
// attachment's absolute path, so it can still act on them (e.g. shell out
// to an image tool) even without native vision.
package attachments

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

// Sweep removes a key's attachments once its root item is Done, Cancelled or
// gone. ponytail: reopening an item after a sweep leaves it without images.
func Sweep(ctx context.Context, db *sql.DB, home string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(home, "attachments"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var status string
		err := db.QueryRowContext(ctx, `SELECT status FROM items WHERE key = ?`, e.Name()).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return removed, err
		}
		if err == nil && status != string(items.Done) && status != string(items.Cancelled) {
			continue
		}
		if err := os.RemoveAll(Dir(home, e.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

func SweepLoop(ctx context.Context, db *sql.DB, home string, every time.Duration, log func(string, ...any)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := Sweep(ctx, db, home); err != nil && ctx.Err() == nil {
			log("attachments sweep: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
