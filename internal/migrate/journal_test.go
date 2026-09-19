package migrate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

func TestLoadJournalReportsAbsenceWithoutAnError(t *testing.T) {
	j, err := migrate.LoadJournal(t.TempDir())
	if err != nil || j != nil {
		t.Fatalf("= %v, %v; an absent journal is not an error", j, err)
	}
}

func TestSaveAndLoadRoundTripAndTheFileIsPrivate(t *testing.T) {
	home := t.TempDir()
	j := &migrate.Journal{Version: 1, StartedAt: 1700000000000}
	s := j.Begin(2, "stop v1")
	s.Add(migrate.Action{Kind: "launchctl", Args: []string{"bootstrap", "gui/501", "/fake/p.plist"}})
	if err := j.Save(home); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(migrate.JournalPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600; the journal names the user's paths", fi.Mode().Perm())
	}
	got, err := migrate.LoadJournal(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 1 || got.Steps[0].N != 2 || got.Steps[0].Name != "stop v1" {
		t.Fatalf("steps = %+v", got.Steps)
	}
	if len(got.Steps[0].Undo) != 1 || got.Steps[0].Undo[0].Kind != "launchctl" {
		t.Fatalf("undo = %+v", got.Steps[0].Undo)
	}
	if got.Steps[0].DoneAt != 0 {
		t.Error("Begin must leave DoneAt zero: the step has not finished yet (S-7)")
	}
}

// S-7: a crash leaves exactly one step unfinished, and that is where resume starts.
func TestLastDoneIgnoresAnUnfinishedStep(t *testing.T) {
	j := &migrate.Journal{Version: 1}
	s2 := j.Begin(2, "stop v1")
	s2.DoneAt = 1
	s3 := j.Begin(3, "backup")
	s3.DoneAt = 2
	j.Begin(4, "build") // crashed here
	if got := j.LastDone(); got != 3 {
		t.Fatalf("LastDone = %d, want 3", got)
	}
	s := j.Steps[len(j.Steps)-1]
	if s.N != 4 || s.DoneAt != 0 {
		t.Fatalf("last step = %+v", s)
	}
}

// §20: "undoes the journaled steps in reverse".
func TestUndoReturnsActionsNewestFirst(t *testing.T) {
	j := &migrate.Journal{Version: 1}
	s2 := j.Begin(2, "stop v1")
	s2.Add(migrate.Action{Kind: "launchctl", Args: []string{"bootstrap"}})
	s2.DoneAt = 1
	s6 := j.Begin(6, "switch")
	s6.Add(migrate.Action{Kind: "rename", From: "/a/swarm.db", To: "/a/swarm-v1.db"})
	s6.Add(migrate.Action{Kind: "rename", From: "/a/swarm-v2.tmp.db", To: "/a/swarm.db"})
	s6.DoneAt = 2

	got := j.Undo()
	want := []string{"rename:/a/swarm-v2.tmp.db", "rename:/a/swarm.db", "launchctl:"}
	if len(got) != len(want) {
		t.Fatalf("undo = %+v", got)
	}
	for i, a := range got {
		if a.Kind+":"+a.From != want[i] {
			t.Errorf("undo[%d] = %s:%s, want %s", i, a.Kind, a.From, want[i])
		}
	}
}

// A Save must never leave a half-written journal: it is the only record of what to
// undo, so it is written to a temp file and renamed.
func TestSaveIsAtomic(t *testing.T) {
	home := t.TempDir()
	j := &migrate.Journal{Version: 1}
	j.Begin(2, "stop v1")
	if err := j.Save(home); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(home, "migrate"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "journal.json" {
		t.Fatalf("dir = %v", ents)
	}
	body, _ := os.ReadFile(migrate.JournalPath(home))
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
}

func TestLoadJournalRejectsAFutureVersion(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "migrate"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrate.JournalPath(home), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.LoadJournal(home); err == nil {
		t.Fatal("want an error: a newer journal must not be interpreted by an older build")
	}
}
