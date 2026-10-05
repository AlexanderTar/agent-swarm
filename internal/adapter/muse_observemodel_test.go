package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// museLogFixture writes a session.jsonl under UserHome/.local/share/muse/
// sessions/2026/09/23/<id>/ and returns the adapter plus the file path.
func museLogFixture(t *testing.T, id string, lines ...string) (*Muse, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "muse", "sessions", "2026", "09", "23", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return newMuse(Deps{UserHome: home}), p
}

const (
	// modelled on the real lines 20 and 26 of a muse 1.4.2 session.jsonl
	museModelDone = `{"sequence":21,"payload_type":"runtime.model_reconfigure.completed","payload":{"kind":"model_reconfigure","record":{"apply_outcome":"completed","previous":{"model_id":""},"effective":{"provider_id":"meta","model_id":"muse-spark-1.3-contributor","source":"model_reconfigure"}}}}`
	museRunModel  = `{"sequence":27,"payload_type":"run.model.configured","payload":{"kind":"run_model","record":{"model_id":"muse-spark-2","source":"model_reconfigure"}}}`
	museStartup   = `{"sequence":3,"payload_type":"run.model.configured","payload":{"kind":"run_model","record":{"model_id":"muse-spark-0","source":"startup"}}}`
	museNoise     = `{"sequence":9,"payload_type":"run.tool.completed","payload":{"x":1}}`
	// ponytail: effort shape unconfirmed (no local sample of
	// reasoning_effort_reconfigure_completed); this fixture is the guess.
	museEffortDone = `{"sequence":30,"payload_type":"runtime.reasoning_effort_reconfigure.completed","payload":{"record":{"effective":{"reasoning_effort":"low"}}}}`
)

func TestMuseObserveModel(t *testing.T) {
	cases := []struct {
		name          string
		lines         []string
		model, effort string
		ok            bool
	}{
		{"reconfigure completed", []string{museStartup, museNoise, museModelDone, museNoise}, "muse-spark-1.3-contributor", "", true},
		{"run.model.configured after reconfigure wins", []string{museModelDone, museRunModel}, "muse-spark-2", "", true},
		{"startup source ignored", []string{museStartup}, "", "", false},
		{"effort parsed fail-soft", []string{museModelDone, museEffortDone}, "muse-spark-1.3-contributor", "low", true},
		{"effort alone is not an observation", []string{museEffortDone}, "", "", false},
		{"malformed tolerated", []string{museModelDone, `{broken`}, "muse-spark-1.3-contributor", "", true},
	}
	for _, c := range cases {
		m, _ := museLogFixture(t, "sess-1", c.lines...)
		model, effort, ok := m.ObserveModel("", "sess-1")
		if model != c.model || effort != c.effort || ok != c.ok {
			t.Errorf("%s: got %q/%q/%v, want %q/%q/%v", c.name, model, effort, ok, c.model, c.effort, c.ok)
		}
	}
	m, _ := museLogFixture(t, "sess-1", museModelDone)
	if _, _, ok := m.ObserveModel("", ""); ok {
		t.Error("empty provider session id must not be ok")
	}
	if _, _, ok := m.ObserveModel("", "other"); ok {
		t.Error("unknown session must not be ok")
	}
}

func TestMuseObserveModelSeesAppendedChange(t *testing.T) {
	m, p := museLogFixture(t, "sess-1", museModelDone)
	if model, _, _ := m.ObserveModel("", "sess-1"); model != "muse-spark-1.3-contributor" {
		t.Fatalf("model = %q", model)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(museRunModel + "\n"); err != nil {
		t.Fatal(err)
	}
	if model, _, _ := m.ObserveModel("", "sess-1"); model != "muse-spark-2" {
		t.Fatalf("after append model = %q, want muse-spark-2 (a cached read must notice the new size)", model)
	}
}
