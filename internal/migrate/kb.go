package migrate

import (
	"context"
	"os"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/db"
)

// archivedKBFolders is §15's list: inbox (1,177 files on 2026-09-17) and handoffs
// (455) stop being indexed. Everything else — specs, plans, decisions and the rest —
// is untouched.
var archivedKBFolders = []string{"inbox", "handoffs"}

// NoteFile is one imported note's target path and complete markdown content, as
// step 4 computed and stored it in the (now live) database.
type NoteFile struct {
	Path    string
	Content string
}

// LoadNoteFiles reads back the note artifacts step 4 wrote into the database, so
// step 7 can flush them to disk once the switch (step 6) has confirmed the new
// database is live. Reading from the database rather than threading an in-memory
// value from step 4 to step 7 is what makes this correct across --resume from a
// fresh process: a crash between steps 4 and 7 means step 4 never re-runs, so the
// content must survive somewhere durable, and it already does (artifact_revisions).
func LoadNoteFiles(ctx context.Context, d *db.DB) ([]NoteFile, error) {
	rows, err := d.QueryContext(ctx, `SELECT a.path, r.content FROM artifacts a
		JOIN artifact_revisions r ON r.artifact_id = a.id AND r.revision = a.head_revision
		WHERE a.kind = 'note'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteFile
	for rows.Next() {
		var n NoteFile
		if err := rows.Scan(&n.Path, &n.Content); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// knowledgeBase is §20 step 7: archive kb/inbox and kb/handoffs, then flush the note
// bodies step 4 computed (in the temp DB, now switched to be the live swarm.db) to
// disk. Each move and each file write is journaled individually, so --rollback can
// undo exactly what this step created.
func (r *Runner) knowledgeBase(ctx context.Context, s *Step) error {
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
		if err := os.Rename(from, to); err != nil {
			return err
		}
		s.Add(Action{Kind: "rename", From: from, To: to})
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
	for _, n := range notes {
		if _, err := os.Stat(n.Path); err == nil {
			continue // already written (a resume)
		}
		if err := os.MkdirAll(filepath.Dir(n.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(n.Path, []byte(n.Content), 0o644); err != nil {
			return err
		}
		s.Add(Action{Kind: "remove", To: n.Path})
	}
	if len(notes) > 0 {
		r.logf("    wrote %d imported note(s)", len(notes))
	}
	return nil
}
