package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// archivedKBFolders is §15's list: inbox (1,177 files on 2026-09-17) and handoffs
// (455) stop being indexed. Everything else — specs, plans, decisions and the rest —
// is untouched.
var archivedKBFolders = []string{"inbox", "handoffs"}

// NoteFile is one imported note's target path, complete markdown content and
// expected sha256, as step 4 computed and stored it in the (now live) database.
type NoteFile struct {
	Path    string
	Content string
	SHA256  string
}

// LoadNoteFiles reads back the note artifacts step 4 wrote into the database, so
// step 7 can flush them to disk once the switch (step 6) has confirmed the new
// database is live. Reading from the database rather than threading an in-memory
// value from step 4 to step 7 is what makes this correct across --resume from a
// fresh process: a crash between steps 4 and 7 means step 4 never re-runs, so the
// content must survive somewhere durable, and it already does (artifact_revisions).
func LoadNoteFiles(ctx context.Context, d *db.DB) ([]NoteFile, error) {
	rows, err := d.QueryContext(ctx, `SELECT a.path, r.content, r.sha256 FROM artifacts a
		JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.head_revision
		WHERE a.kind = 'note'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteFile
	for rows.Next() {
		var n NoteFile
		if err := rows.Scan(&n.Path, &n.Content, &n.SHA256); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// noteMatchesOnDisk reports whether path already holds exactly wantSHA256's bytes.
// A missing, unreadable, or truncated/corrupted file (e.g. a crash mid-WriteFile)
// all report false, so the caller rewrites it.
func noteMatchesOnDisk(path, wantSHA256 string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]) == wantSHA256
}

// knowledgeBase is §20 step 7: archive kb/inbox and kb/handoffs, then flush the note
// bodies step 4 computed (in the temp DB, now switched to be the live swarm.db) to
// disk. Each move and each file write is journaled — and saved — immediately before
// it runs, so a hard crash (including Ctrl-C) partway through this step still leaves
// an accurate undo record (C1). A resume compares the on-disk file's actual hash
// against the sha256 the database already recorded, not just whether a file exists
// at that path, so a truncated write (a crash mid-WriteFile) gets repaired rather
// than silently accepted (Important 2). Only a file this run actually (re)writes is
// journaled — a file that was already correct is left alone, so undo stays precise.
func (r *Runner) knowledgeBase(ctx context.Context, j *Journal, s *Step) error {
	archive := filepath.Join(r.home(), "kb-archive")
	for _, name := range archivedKBFolders {
		from := filepath.Join(r.kbPath(), name)
		if _, err := os.Stat(from); err != nil {
			continue // already moved (a resume), or never there
		}
		if err := os.MkdirAll(archive, 0o755); err != nil {
			return err
		}
		to := uniquePath(filepath.Join(archive, name))
		s.Add(Action{Kind: "rename", From: from, To: to})
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		r.logf("    archived kb/%s", name)
	}

	d, err := db.Open(ctx, r.v1Path())
	if err != nil {
		return err
	}
	defer d.Close()
	notes, err := LoadNoteFiles(ctx, d)
	if err != nil {
		return err
	}
	written := 0
	for _, n := range notes {
		if noteMatchesOnDisk(n.Path, n.SHA256) {
			continue // already correctly written (a clean resume)
		}
		s.Add(Action{Kind: "remove", To: n.Path})
		if err := j.Save(r.home()); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(n.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(n.Path, []byte(n.Content), 0o644); err != nil {
			return err
		}
		written++
	}
	if written > 0 {
		r.logf("    wrote %d imported note(s)", written)
	}
	return nil
}
