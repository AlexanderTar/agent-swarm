// Package migrate imports an Agent Swarm 1.x database into the new schema and
// rewrites the v1 integrations (§20). Every destructive step is journaled before it
// runs, so --resume and --rollback always have something to work from (S-7).
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// JournalVersion is the on-disk format. A journal with a higher number is refused
// rather than half-understood.
const JournalVersion = 1

type Journal struct {
	Version   int    `json:"version"`
	StartedAt int64  `json:"started_at"`
	Complete  bool   `json:"complete"`
	Steps     []Step `json:"steps"`
}

// Step is one numbered §20 step. DoneAt stays zero while it is running, so a crash
// is visible in the file.
type Step struct {
	N         int      `json:"n"`
	Name      string   `json:"name"`
	StartedAt int64    `json:"started_at"`
	DoneAt    int64    `json:"done_at"`
	Undo      []Action `json:"undo"`
}

// Action is one reversible thing a step did.
type Action struct {
	Kind string   `json:"kind"` // rename | restore | remove | launchctl
	From string   `json:"from"`
	To   string   `json:"to"`
	Args []string `json:"args,omitempty"`
}

func JournalPath(home string) string { return filepath.Join(home, "migrate", "journal.json") }

// LoadJournal returns (nil, nil) when there is no journal.
func LoadJournal(home string) (*Journal, error) {
	body, err := os.ReadFile(JournalPath(home))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("%s: %w", JournalPath(home), err)
	}
	if j.Version > JournalVersion {
		return nil, fmt.Errorf("%s was written by a newer build of swarm (version %d)", JournalPath(home), j.Version)
	}
	return &j, nil
}

// Save writes the journal atomically, private to the user.
func (j *Journal) Save(home string) error {
	if err := os.MkdirAll(filepath.Dir(JournalPath(home)), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp := JournalPath(home) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, JournalPath(home))
}

// Begin appends a step and returns a pointer to it, so the caller can Add undo
// actions as it goes and set DoneAt when it finishes.
func (j *Journal) Begin(n int, name string) *Step {
	j.Steps = append(j.Steps, Step{N: n, Name: name})
	return &j.Steps[len(j.Steps)-1]
}

func (s *Step) Add(a Action) { s.Undo = append(s.Undo, a) }

// LastDone is the highest step number that finished. --resume continues after it.
func (j *Journal) LastDone() int {
	last := 0
	for _, s := range j.Steps {
		if s.DoneAt != 0 && s.N > last {
			last = s.N
		}
	}
	return last
}

// Undo is every action, newest step first and newest action first within a step
// (§20: "undoes the journaled steps in reverse").
func (j *Journal) Undo() []Action {
	var out []Action
	for i := len(j.Steps) - 1; i >= 0; i-- {
		s := j.Steps[i]
		for k := len(s.Undo) - 1; k >= 0; k-- {
			out = append(out, s.Undo[k])
		}
	}
	return out
}
