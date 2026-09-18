package migrate_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/migrate"
)

// §20's table, row by row. This test is the review artefact: if it disagrees with
// the spec, the spec wins and the table changes.
//
// The live spec (~/.superpowers/specs/2026-09-17-agent-swarm-go-orchestrator.md,
// §20) carries 10 rows as of 2026-09-18, not the 9 in the batch brief's own pasted
// snippet: a new row 2, "Full Go API migration with in-process ADK agent,
// alternative to SW-673 (endurio-chat)" (from SW-710), TASK, ready, was added to the
// live table the same day. It is decision-gated (37 child tasks are created only if
// this option is chosen over SW-673) and has no children yet, so it is the simplest
// row shape here — comparable to the no-children RAG/Customer Center rows.
func TestTableMatchesTheSpecTree(t *testing.T) {
	if len(migrate.Table) != 10 {
		t.Fatalf("roots = %d, want 10", len(migrate.Table))
	}
	type want struct {
		title    string
		typ      string
		status   string
		from     []string
		stories  []string
		taskFrom []string // bug roots only
	}
	wants := []want{
		{"Migrate the coach agent from Eve to ADK Go (endurio-chat)", "epic", "ready", []string{"SW-673"},
			[]string{"PR A: Qdrant vector store",
				"PR B: ai-SDK completion and the unified tool registry",
				"PR C: Go ADK service, MCP bridge and Eve removal"}, nil},
		{"Full Go API migration with in-process ADK agent, alternative to SW-673 (endurio-chat)", "task", "ready",
			[]string{"SW-710"}, nil, nil},
		{"Support, chat attachments and privacy across Endurio", "epic", "ready", []string{"SW-661"},
			[]string{"endurio-chat backend", "endurio-app client", "Cross-repo verification"}, nil},
		{"Restore end-to-end OpenObserve telemetry in endurio-chat", "epic", "ready", []string{"SW-652"},
			[]string{"Telemetry pipeline"}, nil},
		{"Eve launcher memory on Railway (endurio-chat)", "bug", "ready", []string{"SW-648"},
			nil, []string{"SW-649", "SW-650", "SW-651"}},
		{"Indoor trainer workout export", "epic", "ready", nil,
			[]string{"Chat backend endpoint (.zwo/.erg)", "App side (endurio-app)"}, nil},
		{"Workout provider adapters", "epic", "ready", nil,
			[]string{"Wahoo Cloud API adapter", "Polar AccessLink v3 adapter",
				"Zwift ride attribution via FIT manufacturer + dedup", "COROS provider adapter",
				"Swim lap-schema widening", "Vendor API procurement: Wahoo, Polar, COROS"}, nil},
		{"Health platform workout ingestion", "epic", "ready", nil,
			[]string{"Compile the health-sync native code", "Apple Health workout ingestion",
				"Health Connect workout ingestion (Android)", "Apple Developer Program gated work"}, nil},
		{"RAG corpus expansion", "epic", "blocked", []string{"SW-359"}, nil, nil},
		{"Customer Center appearance colors on RC dashboard", "epic", "blocked", []string{"SW-256"}, nil, nil},
	}
	for i, w := range wants {
		r := migrate.Table[i]
		if r.Title != w.title || r.Type != w.typ || r.Status != w.status {
			t.Errorf("root %d = %q/%s/%s, want %q/%s/%s", i, r.Title, r.Type, r.Status, w.title, w.typ, w.status)
		}
		if strings.Join(r.From, ",") != strings.Join(w.from, ",") {
			t.Errorf("root %d From = %v, want %v", i, r.From, w.from)
		}
		var titles []string
		for _, s := range r.Stories {
			titles = append(titles, s.Title)
		}
		if strings.Join(titles, "|") != strings.Join(w.stories, "|") {
			t.Errorf("root %d stories = %v, want %v", i, titles, w.stories)
		}
		var taskFrom []string
		for _, c := range r.Tasks {
			taskFrom = append(taskFrom, c.From)
		}
		if strings.Join(taskFrom, ",") != strings.Join(w.taskFrom, ",") {
			t.Errorf("root %d tasks = %v, want %v", i, taskFrom, w.taskFrom)
		}
	}
}

// SW-673's three stories, exactly as §20 splits them, with the story-level chain.
func TestSW673SplitsThirtySixTasksAcrossThreeBlockedStories(t *testing.T) {
	r := migrate.Table[0]
	ranges := [][2]int{{674, 679}, {680, 692}, {693, 709}}
	total := 0
	for i, s := range r.Stories {
		var got []string
		for _, c := range s.Tasks {
			got = append(got, c.From)
		}
		var want []string
		for n := ranges[i][0]; n <= ranges[i][1]; n++ {
			want = append(want, fmt.Sprintf("SW-%d", n))
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("story %d tasks = %v, want %v", i, got, want)
		}
		total += len(s.Tasks)
		// §20: PR B is blocked by PR A, PR C by PR B.
		if i == 0 && s.BlockedBy != "" {
			t.Errorf("PR A must not be blocked: %q", s.BlockedBy)
		}
		if i > 0 && s.BlockedBy != r.Stories[i-1].Title {
			t.Errorf("story %d BlockedBy = %q, want %q", i, s.BlockedBy, r.Stories[i-1].Title)
		}
	}
	if total != 36 {
		t.Fatalf("SW-673 has %d tasks, want 36 (SW-674…SW-709, all present on 2026-09-18)", total)
	}
}

// §20's new row 2: SW-710 is a plain, childless TASK root — decision-gated, so no
// stories or tasks are authored under it here.
func TestSW710HasNoChildrenYet(t *testing.T) {
	r := migrate.Table[1]
	if len(r.Stories) != 0 || len(r.Tasks) != 0 {
		t.Fatalf("SW-710 root = %+v, want no stories or tasks (decision-gated)", r)
	}
	if len(r.From) != 1 || r.From[0] != "SW-710" {
		t.Fatalf("SW-710 root From = %v, want [SW-710]", r.From)
	}
}

// §20: "in order; each task is blocked by the one before".
func TestTelemetryPipelineTasksAreChained(t *testing.T) {
	s := migrate.Table[3].Stories[0]
	if len(s.Tasks) != 8 {
		t.Fatalf("tasks = %d, want 8 (SW-653…SW-660)", len(s.Tasks))
	}
	for i, c := range s.Tasks {
		want := ""
		if i > 0 {
			want = s.Tasks[i-1].From
		}
		if c.BlockedBy != want {
			t.Errorf("%s BlockedBy = %q, want %q", c.From, c.BlockedBy, want)
		}
	}
}

// §20: "SW-651 (ready, blocked by SW-650)".
func TestBugTasksCarryTheSingleDependency(t *testing.T) {
	r := migrate.Table[4]
	if r.Tasks[0].BlockedBy != "" || r.Tasks[1].BlockedBy != "" {
		t.Errorf("SW-649/SW-650 must not be blocked: %+v", r.Tasks)
	}
	if r.Tasks[2].BlockedBy != "SW-650" {
		t.Errorf("SW-651 BlockedBy = %q, want SW-650", r.Tasks[2].BlockedBy)
	}
}

// §20: "App side (endurio-app) ← SW-362, blocked by the backend story".
func TestStoryLevelDependenciesNameAnotherStoryInTheSameRoot(t *testing.T) {
	for i, r := range migrate.Table {
		titles := map[string]bool{}
		for _, s := range r.Stories {
			titles[s.Title] = true
		}
		for _, s := range r.Stories {
			if s.BlockedBy == "" {
				continue
			}
			if !titles[s.BlockedBy] {
				t.Errorf("root %d story %q is blocked by %q, which is not a story of that root", i, s.Title, s.BlockedBy)
			}
			if s.BlockedBy == s.Title {
				t.Errorf("root %d story %q blocks itself", i, s.Title)
			}
		}
	}
}

func TestSourceKeysAreUniqueAndCoverEveryReference(t *testing.T) {
	keys := migrate.SourceKeys()
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("%s appears twice", k)
		}
		seen[k] = true
		if !strings.HasPrefix(k, "SW-") {
			t.Errorf("%q is not an SW key", k)
		}
	}
	// 7 roots with their own v1 row (SW-673, 710, 661, 652, 648, 359, 256)
	// + 12 story-sourced (2 + 6 + 4, the workout/health/trainer rows)
	// + 57 tasks (36 + 10 + 8 + 3) = 76.
	if len(keys) != 76 {
		t.Fatalf("SourceKeys = %d, want 76", len(keys))
	}
	if !sort.StringsAreSorted(keys) {
		t.Error("SourceKeys must be sorted, so an error message about missing keys is stable")
	}
	// Every key referenced anywhere in the table is in the list.
	for _, r := range migrate.Table {
		for _, k := range r.From {
			if !seen[k] {
				t.Errorf("root source %s is missing from SourceKeys", k)
			}
		}
		for _, s := range r.Stories {
			if s.From != "" && !seen[s.From] {
				t.Errorf("story source %s is missing", s.From)
			}
			for _, c := range s.Tasks {
				if !seen[c.From] {
					t.Errorf("task source %s is missing", c.From)
				}
			}
		}
		for _, c := range r.Tasks {
			if !seen[c.From] {
				t.Errorf("task source %s is missing", c.From)
			}
		}
	}
}

// §20's "Everything else stays only in swarm-v1.db" list.
func TestNotImportedListsTheThirteenTopLevelTasks(t *testing.T) {
	want := []string{"SW-374", "SW-494", "SW-498", "SW-526", "SW-552", "SW-559", "SW-562",
		"SW-600", "SW-607", "SW-612", "SW-616", "SW-629", "SW-638"}
	got := append([]string(nil), migrate.NotImported...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NotImported = %v, want %v", got, want)
	}
	imported := map[string]bool{}
	for _, k := range migrate.SourceKeys() {
		imported[k] = true
	}
	for _, k := range got {
		if imported[k] {
			t.Errorf("%s is both imported and listed as not imported", k)
		}
	}
}

// §10.1's derived story status, which the six spec-authored stories use.
func TestDeriveStoryStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children []string
		want     string
	}{
		{"all ready", []string{"ready", "ready"}, "ready"},
		{"one moved on", []string{"ready", "in_progress"}, "in_progress"},
		{"all in review", []string{"in_review", "done"}, "in_review"},
		{"all finished", []string{"done", "cancelled"}, "done"},
		{"only cancelled", []string{"cancelled"}, "cancelled"},
		{"no children", nil, "ready"},
		{"a draft child", []string{"draft", "ready"}, "ready"},
		{"a blocked child", []string{"blocked", "ready"}, "in_progress"},
	} {
		if got := migrate.DeriveStoryStatus(tc.children); got != tc.want {
			t.Errorf("%s: DeriveStoryStatus(%v) = %q, want %q", tc.name, tc.children, got, tc.want)
		}
	}
}
