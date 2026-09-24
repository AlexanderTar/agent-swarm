package workflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFindingJSONTags(t *testing.T) {
	full := Finding{Severity: "major", File: "a.go", Line: 1, Unit: 2, Summary: "s", Reviewer: "reviewer"}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"severity":"major","file":"a.go","line":1,"unit":2,"summary":"s","reviewer":"reviewer"}`
	if string(b) != want {
		t.Fatalf("Marshal() = %s, want %s", b, want)
	}

	// Line/Unit/Reviewer are omitempty.
	minimal := Finding{Severity: "minor", File: "b.go", Summary: "n"}
	b2, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want2 := `{"severity":"minor","file":"b.go","summary":"n"}`
	if string(b2) != want2 {
		t.Fatalf("Marshal() = %s, want %s", b2, want2)
	}
}

// r builds a Run for table tests. Use struct literals directly (or mutate
// the result) when a case needs SHA, Findings or AutoRetries.
func r(step string, round int, role string, state RunState, verdict Verdict) Run {
	return Run{StepID: step, Round: round, Role: role, State: state, Verdict: verdict}
}

// cr2 builds a completed run step with a sha (a run step that finished
// cleanly), as used throughout the probe2 tables.
func cr2(step string, round int, role, sha string) Run {
	run := r(step, round, role, RunStateCompleted, VerdictNone)
	run.SHA = sha
	return run
}

func resolveT(t *testing.T, template string) Spec {
	t.Helper()
	s, err := Resolve(Spec{Template: template}, false)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", template, err)
	}
	return s
}

func TestNext(t *testing.T) {
	tddReviewed := resolveT(t, "tdd-reviewed")
	uiTddReviewed := resolveT(t, "ui-tdd-reviewed")
	mechanical := resolveT(t, "mechanical")
	designReviewed := resolveT(t, "design-reviewed")

	// notesBuildReview: a step before the fix step ("notes") that is never
	// itself retried - only "build" is the review's fix target.
	notesBuildReview, err := Resolve(Spec{Steps: []Step{
		{ID: "notes", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "build"}},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// buildNotesReview: Of names "build" explicitly even though "notes" (a
	// run step with no gates relevant to sha) is the nearer preceding step.
	buildNotesReview, err := Resolve(Spec{Steps: []Step{
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "notes", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build"}},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// loopLessReview simulates a story's after_tasks-shaped review: a
	// review step with no Loop at all (Next must handle this directly,
	// since Resolve only synthesizes a Loop for task-level Steps).
	loopLessReview := Spec{Steps: []Step{
		{ID: "merge", Run: "coder"},
		{ID: "review", Review: []string{"reviewer"}, Of: "merge"},
	}}

	// twoLoop (probe2 table A): two independent run/review pairs. review2's
	// fix loop must never respawn build/review1, which already passed.
	twoLoop, err := Resolve(Spec{Steps: []Step{
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "review1", Review: []string{"reviewer"}},
		{ID: "docs", Run: "mechanical", Gates: []Gate{GateCommit}},
		{ID: "review2", Review: []string{"reviewer"}},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// buildLintReview (probe2 table B): the fix step ("build") isn't
	// adjacent to its review - "lint" sits between them and must be
	// re-run too once round advances, since it's within the pinned range
	// (indexOf(fix)..end), not before it.
	buildLintReview, err := Resolve(Spec{Steps: []Step{
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "lint", Run: "mechanical", Gates: []Gate{GateCommit}},
		{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "build"}},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// researchThenBuild (probe2 table C): an earlier, already-passed
	// run/review pair ("research"/"rreview") must stay carried forward
	// when a later pair's review requests changes.
	researchThenBuild, err := Resolve(Spec{Steps: []Step{
		{ID: "research", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		{ID: "rreview", Review: []string{"reviewer"}},
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "review", Review: []string{"reviewer"}},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	// twoReviewsOfBuild (probe2 table D): two review steps of the same
	// build step. When one requests changes, the pinned range covers the
	// build step's index onward, so the other reviewer is re-run too.
	twoReviewsOfBuild, err := Resolve(Spec{Steps: []Step{
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "r-a", Review: []string{"reviewer"}},
		{ID: "r-b", Review: []string{"ui_reviewer"}, Of: "build"},
	}}, false)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	buildCompleted := func(sha string) Run {
		run := r("build", 1, "coder", RunStateCompleted, VerdictNone)
		run.SHA = sha
		return run
	}

	buildCompletedAt := func(round int, sha string) Run {
		run := r("build", round, "coder", RunStateCompleted, VerdictNone)
		run.SHA = sha
		return run
	}

	failedBuild := func(autoRetries int) Run {
		run := r("build", 1, "coder", RunStateFailed, VerdictNone)
		run.AutoRetries = autoRetries
		return run
	}

	tests := []struct {
		name        string
		spec        Spec
		runs        []Run
		round       int
		extraRounds int
		want        Action
	}{
		{
			name:  "first spawn",
			spec:  tddReviewed,
			runs:  nil,
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 1},
		},
		{
			name:  "wait while build active",
			spec:  tddReviewed,
			runs:  []Run{r("build", 1, "coder", RunStateActive, VerdictNone)},
			round: 1,
			want:  Action{Kind: ActionWait, StepID: "build", Round: 1},
		},
		{
			name:  "wait while build waiting (no budget slot)",
			spec:  tddReviewed,
			runs:  []Run{r("build", 1, "coder", RunStateWaiting, VerdictNone)},
			round: 1,
			want:  Action{Kind: ActionWait, StepID: "build", Round: 1},
		},
		{
			name:  "review spawn carries the builder's sha",
			spec:  tddReviewed,
			runs:  []Run{buildCompleted("abc1234")},
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer"}, Round: 1, SHA: "abc1234"},
		},
		{
			name:  "parallel reviewers spawn together",
			spec:  uiTddReviewed,
			runs:  []Run{buildCompleted("abc1234")},
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer", "ui_reviewer"}, Round: 1, SHA: "abc1234"},
		},
		{
			name: "parallel reviewers: wait while one is still active",
			spec: uiTddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictPass),
				r("review", 1, "ui_reviewer", RunStateActive, VerdictNone),
			},
			round: 1,
			want:  Action{Kind: ActionWait, StepID: "review", Round: 1},
		},
		{
			name: "all pass succeeds with the builder's sha",
			spec: uiTddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictPass),
				r("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass),
			},
			round: 1,
			want:  Action{Kind: ActionSucceed, SHA: "abc1234"},
		},
		{
			name: "mixed verdicts retry_fix with merged findings in stable order",
			spec: uiTddReviewed,
			runs: func() []Run {
				build := buildCompleted("abc1234")
				reviewer := r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested)
				reviewer.Findings = []Finding{
					{Severity: "major", File: "b.go", Line: 5, Summary: "x"},
					{Severity: "minor", File: "a.go", Line: 1, Summary: "y"},
				}
				ui := r("review", 1, "ui_reviewer", RunStateCompleted, VerdictChangesRequested)
				ui.Findings = []Finding{
					{Severity: "major", File: "a.go", Line: 2, Summary: "z"},
				}
				return []Run{build, reviewer, ui}
			}(),
			round: 1,
			want: Action{
				Kind:   ActionRetryFix,
				StepID: "build",
				Round:  2,
				Findings: []Finding{
					{Severity: "minor", File: "a.go", Line: 1, Summary: "y", Reviewer: "reviewer"},
					{Severity: "major", File: "b.go", Line: 5, Summary: "x", Reviewer: "reviewer"},
					{Severity: "major", File: "a.go", Line: 2, Summary: "z", Reviewer: "ui_reviewer"},
				},
			},
		},
		{
			// Decision C: retry_fix findings include every reviewer of the
			// round, tagged with its role, even one who passed but left a
			// minor/nit finding - not just the reviewers who blocked.
			name: "a pass reviewer's minor findings are still merged in",
			spec: uiTddReviewed,
			runs: func() []Run {
				build := buildCompleted("abc1234")
				reviewer := r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested)
				reviewer.Findings = []Finding{{Severity: "major", File: "a.go", Summary: "m"}}
				ui := r("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass)
				ui.Findings = []Finding{{Severity: "minor", File: "b.go", Summary: "n"}}
				return []Run{build, reviewer, ui}
			}(),
			round: 1,
			want: Action{
				Kind:   ActionRetryFix,
				StepID: "build",
				Round:  2,
				Findings: []Finding{
					{Severity: "major", File: "a.go", Summary: "m", Reviewer: "reviewer"},
					{Severity: "minor", File: "b.go", Summary: "n", Reviewer: "ui_reviewer"},
				},
			},
		},
		{
			name: "review rounds exhausted escalates",
			spec: tddReviewed,
			runs: func() []Run {
				build := buildCompletedAt(3, "abc1234")
				reviewer := r("review", 3, "reviewer", RunStateCompleted, VerdictChangesRequested)
				reviewer.Findings = []Finding{{Severity: "major", File: "a.go", Line: 1, Summary: "still broken"}}
				return []Run{build, reviewer}
			}(),
			round: 3,
			want:  Action{Kind: ActionEscalate, Reason: `review rounds exhausted (3/3)`},
		},
		{
			name: "extra_rounds grants one more round instead of exhausting",
			spec: tddReviewed,
			runs: func() []Run {
				build := buildCompletedAt(3, "abc1234")
				reviewer := r("review", 3, "reviewer", RunStateCompleted, VerdictChangesRequested)
				reviewer.Findings = []Finding{{Severity: "major", File: "a.go", Line: 1, Summary: "still broken"}}
				return []Run{build, reviewer}
			}(),
			round:       3,
			extraRounds: 1,
			want: Action{
				Kind:     ActionRetryFix,
				StepID:   "build",
				Round:    4,
				Findings: []Finding{{Severity: "major", File: "a.go", Line: 1, Summary: "still broken", Reviewer: "reviewer"}},
			},
		},
		{
			name: "blocked verdict escalates",
			spec: tddReviewed,
			runs: func() []Run {
				build := buildCompleted("abc1234")
				reviewer := r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked)
				reviewer.Findings = []Finding{{Severity: "critical", File: "a.go", Line: 1, Summary: "can't proceed without design sign-off"}}
				return []Run{build, reviewer}
			}(),
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: `reviewer blocked: can't proceed without design sign-off`},
		},
		{
			name:  "failed run with auto-retries left",
			spec:  tddReviewed,
			runs:  []Run{failedBuild(0)},
			round: 1,
			want:  Action{Kind: ActionAutoRetry, StepID: "build", Round: 1, Run: func() *Run { fb := failedBuild(0); return &fb }()},
		},
		{
			name:  "failed run with no auto-retries left escalates (singular)",
			spec:  tddReviewed,
			runs:  []Run{failedBuild(1)},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "coder failed after 1 auto-retry"},
		},
		{
			name: "failed run with no auto-retries left escalates (plural)",
			spec: func() Spec {
				s := tddReviewed
				two := 2
				s.Retries = &two
				return s
			}(),
			runs:  []Run{failedBuild(2)},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "coder failed after 2 auto-retries"},
		},
		{
			name: "failed run with retries=0 escalates without mentioning auto-retries",
			spec: func() Spec {
				s := tddReviewed
				zero := 0
				s.Retries = &zero
				return s
			}(),
			runs:  []Run{failedBuild(0)},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "coder failed"},
		},
		{
			name: "single-step template succeeds once its run completes",
			spec: mechanical,
			runs: []Run{func() Run {
				run := r("change", 1, "mechanical", RunStateCompleted, VerdictNone)
				run.SHA = "cafe123"
				return run
			}()},
			round: 1,
			want:  Action{Kind: ActionSucceed, SHA: "cafe123"},
		},
		{
			name:  "build completed without a sha escalates (commit gate)",
			spec:  tddReviewed,
			runs:  []Run{r("build", 1, "coder", RunStateCompleted, VerdictNone)},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "build completed without a sha"},
		},
		{
			name: "blocked verdict with no findings omits the colon",
			spec: tddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked),
			},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "reviewer blocked"},
		},
		{
			name: "blocked escalates even with extraRounds available",
			spec: tddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked),
			},
			round:       1,
			extraRounds: 1,
			want:        Action{Kind: ActionEscalate, Reason: "reviewer blocked"},
		},

		// Finding 2: a step that isn't the loop's fix step is satisfied by
		// its latest earlier-round run, with its sha carried forward - it is
		// never re-spawned just because the round advanced.
		{
			name: "an earlier, non-retried step stays satisfied across rounds",
			spec: notesBuildReview,
			runs: []Run{
				r("notes", 1, "researcher", RunStateCompleted, VerdictNone),
				func() Run { run := r("build", 1, "coder", RunStateCompleted, VerdictNone); run.SHA = "a1"; return run }(),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				r("build", 2, "coder", RunStateActive, VerdictNone),
			},
			round: 2,
			want:  Action{Kind: ActionWait, StepID: "build", Round: 2},
		},

		// Finding 3: Of can name any earlier run step, not just the nearest
		// one; the sha used for the review is that named step's sha.
		{
			name: "Of names a non-adjacent run step; its sha is used",
			spec: buildNotesReview,
			runs: []Run{
				func() Run {
					run := r("build", 1, "coder", RunStateCompleted, VerdictNone)
					run.SHA = "buildsha"
					return run
				}(),
				r("notes", 1, "researcher", RunStateCompleted, VerdictNone),
			},
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer"}, Round: 1, SHA: "buildsha"},
		},

		// Finding 4: a review step some of whose roles have no row yet this
		// round spawns exactly the missing roles; it never succeeds early.
		{
			name: "a missing reviewer role is spawned, not skipped",
			spec: uiTddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictPass),
			},
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"ui_reviewer"}, Round: 1, SHA: "abc1234"},
		},

		// Finding 5: a failed/cancelled run from an earlier, superseded
		// round must not trigger auto-retry/escalate; only the current
		// round's runs are examined for failures.
		{
			name: "a stale cancelled run from an earlier round is ignored",
			spec: tddReviewed,
			runs: []Run{
				buildCompleted("x"),
				r("review", 1, "reviewer", RunStateCancelled, VerdictNone),
				r("build", 2, "coder", RunStateActive, VerdictNone),
			},
			round:       2,
			extraRounds: 1,
			want:        Action{Kind: ActionWait, StepID: "build", Round: 2},
		},
		{
			name: "a stale exhausted-failure run from an earlier round is ignored",
			spec: tddReviewed,
			runs: []Run{
				buildCompleted("x"),
				func() Run {
					run := r("review", 1, "reviewer", RunStateFailed, VerdictNone)
					run.AutoRetries = 1
					return run
				}(),
				r("build", 2, "coder", RunStateActive, VerdictNone),
			},
			round:       2,
			extraRounds: 1,
			want:        Action{Kind: ActionWait, StepID: "build", Round: 2},
		},

		// Decision A: a loop-less review step (story after_tasks shape)
		// escalates instead of trying to retry a fix step that doesn't
		// exist for it.
		{
			name: "a loop-less review step escalates on changes_requested",
			spec: loopLessReview,
			runs: []Run{
				r("merge", 1, "coder", RunStateCompleted, VerdictNone),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "review changes requested"},
		},

		{
			name:  "design-reviewed review spawn carries an empty sha (no commit gate on design)",
			spec:  designReviewed,
			runs:  []Run{r("design", 1, "designer", RunStateCompleted, VerdictNone)},
			round: 1,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"ui_reviewer"}, Round: 1, SHA: ""},
		},

		{
			name: "a crash between RetryFix and inserting the new round's run just re-spawns it",
			spec: tddReviewed,
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 2,
			want:  Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 2},
		},

		{
			// Next only ever reads each step's own (already-resolved)
			// Loop.MaxRounds; it never looks at Spec.MaxRounds itself. A
			// caller that hands Next an unresolved spec (Template set, an
			// override that was never applied to the Loop) gets the loop's
			// own max_rounds, not the override.
			name: "Next uses each step's resolved Loop.MaxRounds, not an unapplied Spec.MaxRounds",
			spec: Spec{Template: "tdd-reviewed", MaxRounds: 1, Steps: Templates["tdd-reviewed"]},
			runs: []Run{
				buildCompleted("abc1234"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 1,
			want:  Action{Kind: ActionRetryFix, StepID: "build", Round: 2},
		},

		// Finding 6: an unresolved loop with no Fix falls back to the
		// step's Of, not the review step's own id.
		{
			name: "loop with no fix falls back to Of, not the review step's own id",
			spec: Spec{Steps: []Step{
				{ID: "build", Run: "coder"},
				{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{}},
			}},
			runs: []Run{
				r("build", 1, "coder", RunStateCompleted, VerdictNone),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 1,
			want:  Action{Kind: ActionRetryFix, StepID: "build", Round: 2},
		},

		// Finding 9: a round below 1 is clamped to 1.
		{
			name:  "round below 1 is clamped to 1",
			spec:  tddReviewed,
			runs:  nil,
			round: 0,
			want:  Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 1},
		},

		// probe2 table A: two independent loops. review2 (fix: docs)
		// requesting changes must never touch build/review1.
		{
			name: "table A: review2 requests changes; only docs is retried, not build",
			spec: twoLoop,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				r("review1", 1, "reviewer", RunStateCompleted, VerdictPass),
				cr2("docs", 1, "mechanical", "d1"),
				r("review2", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 1,
			want:  Action{Kind: ActionRetryFix, StepID: "docs", Round: 2},
		},
		{
			name: "table A: round 2, docs fixing; build/review1 stay carried forward",
			spec: twoLoop,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				r("review1", 1, "reviewer", RunStateCompleted, VerdictPass),
				cr2("docs", 1, "mechanical", "d1"),
				r("review2", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				r("docs", 2, "mechanical", RunStateActive, VerdictNone),
			},
			round: 2,
			want:  Action{Kind: ActionWait, StepID: "docs", Round: 2},
		},

		// probe2 table B (finding 1, critical): "lint" sits between the fix
		// step ("build") and its review. Once build is fixed, lint is
		// within the pinned range too and must be re-run before review, so
		// the eventual succeed carries lint's fresh sha, not the stale one.
		{
			name: "table B: build fixed at round 2; lint (between build and review) must be re-run too",
			spec: buildLintReview,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				cr2("lint", 1, "mechanical", "l1"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				cr2("build", 2, "coder", "b2"),
			},
			round: 2,
			want:  Action{Kind: ActionSpawn, StepID: "lint", Roles: []string{"mechanical"}, Round: 2},
		},
		{
			name: "table B: build and lint both re-run at round 2; review spawns with lint's fresh sha",
			spec: buildLintReview,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				cr2("lint", 1, "mechanical", "l1"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				cr2("build", 2, "coder", "b2"),
				cr2("lint", 2, "mechanical", "l2"),
			},
			round: 2,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer"}, Round: 2, SHA: "l2"},
		},
		{
			name: "table B: round 2 review passes; succeeds with lint's fresh sha, not the stale round-1 one",
			spec: buildLintReview,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				cr2("lint", 1, "mechanical", "l1"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				cr2("build", 2, "coder", "b2"),
				cr2("lint", 2, "mechanical", "l2"),
				r("review", 2, "reviewer", RunStateCompleted, VerdictPass),
			},
			round: 2,
			want:  Action{Kind: ActionSucceed, SHA: "l2"},
		},

		// probe2 table C: an earlier, already-passed run/review pair
		// ("research"/"rreview") must stay carried forward when a later
		// pair's review requests changes - it must never be re-spawned.
		{
			name: "table C: build's review requests changes; research/rreview carry forward, not re-spawned",
			spec: researchThenBuild,
			runs: []Run{
				cr2("research", 1, "researcher", ""),
				r("rreview", 1, "reviewer", RunStateCompleted, VerdictPass),
				cr2("build", 1, "coder", "b1"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				r("build", 2, "coder", RunStateActive, VerdictNone),
			},
			round: 2,
			want:  Action{Kind: ActionWait, StepID: "build", Round: 2},
		},

		// probe2 table D: two review steps of the same build step. r-b
		// requesting changes pins from build's index onward, so r-a (an
		// unrelated reviewer of the same build) is re-run too.
		{
			name: "table D: r-b requests changes; r-a (same build, different reviewer) is re-spawned too",
			spec: twoReviewsOfBuild,
			runs: []Run{
				cr2("build", 1, "coder", "b1"),
				r("r-a", 1, "reviewer", RunStateCompleted, VerdictPass),
				r("r-b", 1, "ui_reviewer", RunStateCompleted, VerdictChangesRequested),
				cr2("build", 2, "coder", "b2"),
			},
			round: 2,
			want:  Action{Kind: ActionSpawn, StepID: "r-a", Roles: []string{"reviewer"}, Round: 2, SHA: "b2"},
		},

		// probe2 table M / finding 7: a blocked verdict escalates even when
		// another reviewer role hasn't run yet - it never waits for or
		// spawns the missing role first.
		{
			name: "table M: one reviewer blocked, the other missing entirely - escalates, doesn't spawn the missing one",
			spec: uiTddReviewed,
			runs: []Run{
				buildCompleted("x"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked),
			},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "reviewer blocked"},
		},
		{
			name: "table M: round 2, one reviewer's round-2 row still missing while the other passed",
			spec: uiTddReviewed,
			runs: []Run{
				buildCompleted("x"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
				r("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass),
				cr2("build", 2, "coder", "y"),
				r("review", 2, "ui_reviewer", RunStateCompleted, VerdictPass),
			},
			round: 2,
			want:  Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer"}, Round: 2, SHA: "y"},
		},

		// Finding 3 (major): a carried-forward step whose latest run is
		// failed/cancelled must be auto-retried/escalated, never waited on
		// forever. Anchored by a real CR review (fix: build), so "notes"
		// unambiguously stays carried forward.
		{
			name: "finding 3: a carried-forward step's stale failure escalates instead of waiting forever",
			spec: notesBuildReview,
			runs: []Run{
				func() Run {
					run := r("notes", 1, "researcher", RunStateFailed, VerdictNone)
					run.AutoRetries = 1
					return run
				}(),
				cr2("build", 1, "coder", "a1"),
				r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested),
			},
			round: 2,
			want:  Action{Kind: ActionEscalate, Reason: "researcher failed after 1 auto-retry"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Next(tt.spec, tt.runs, tt.round, tt.extraRounds)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Next() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestNextRoundEdgeCases covers probe2 table E: a future-round run row is
// invisible at an earlier round, an exhausted-failure escalation pluralizes
// correctly, and - when round advanced with no round-1 review evidence at
// all to explain it (no CR review found) - the decided fallback pins from
// the failed step's own index, so that step is freshly spawned rather than
// re-examined via its stale earlier-round failure.
func TestNextRoundEdgeCases(t *testing.T) {
	td := resolveT(t, "tdd-reviewed")

	t.Run("a future-round run is invisible at an earlier round", func(t *testing.T) {
		got := Next(td, []Run{cr2("build", 2, "coder", "x")}, 1, 0)
		want := Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 1}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("exhausted failure with retries=2 pluralizes auto-retries", func(t *testing.T) {
		s := Spec{Steps: td.Steps, Retries: intp(2)}
		got := Next(s, []Run{{StepID: "build", Round: 1, Role: "coder", State: RunStateFailed, AutoRetries: 2}}, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: "coder failed after 2 auto-retries"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	// probe2's literal "E failed r1 earlier in a carried step" input: round
	// advanced to 2 but no round-1 review ever requested changes (there is
	// no review run at all), so there's no CR-driven pin target. The
	// decided fallback then pins from the failed step's own index ("notes",
	// index 0) to the end. Notes therefore requires an exact round-2 match;
	// finding none, it is freshly spawned. This does NOT re-examine the
	// stale round-1 failure (that data is superseded once the step is
	// pinned) - it's a distinct scenario from finding 3, which is about a
	// step that stays *carried forward* (see TestNext's "finding 3" case,
	// anchored by an actual CR review).
	t.Run("round bumped with no round-1 review evidence: pins from the failed step's own index", func(t *testing.T) {
		s2, err := Resolve(Spec{Steps: []Step{
			{ID: "notes", Run: "researcher"},
			{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
			{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "build"}},
		}}, false)
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		runs := []Run{
			func() Run {
				run := r("notes", 1, "researcher", RunStateFailed, VerdictNone)
				run.AutoRetries = 1
				return run
			}(),
			cr2("build", 2, "coder", "z"),
		}
		got := Next(s2, runs, 2, 0)
		want := Action{Kind: ActionSpawn, StepID: "notes", Roles: []string{"researcher"}, Round: 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})
}

// TestNextIgnoresInputOrder checks that Next sorts its own copy of runs
// before evaluating them, so the caller's ordering never changes the
// result - not even the "first" failed/cancelled run picked in the
// failure-handling pass.
func TestNextIgnoresInputOrder(t *testing.T) {
	s := resolveT(t, "ui-tdd-reviewed")
	build := r("build", 1, "coder", RunStateCompleted, VerdictNone)
	build.SHA = "abc1234"
	reviewer := r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested)
	reviewer.Findings = []Finding{{File: "b.go", Line: 5, Summary: "x"}}
	ui := r("review", 1, "ui_reviewer", RunStateCompleted, VerdictChangesRequested)
	ui.Findings = []Finding{{File: "a.go", Line: 2, Summary: "z"}}

	orderings := [][]Run{
		{build, reviewer, ui},
		{ui, reviewer, build},
		{reviewer, build, ui},
		{ui, build, reviewer},
	}

	var first Action
	for i, runs := range orderings {
		got := Next(s, runs, 1, 0)
		if i == 0 {
			first = got
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("Next() with runs in order %d = %+v, want %+v (order must not matter)", i, got, first)
		}
	}

	// Also verify the caller's slice itself was left untouched.
	want := []Run{build, reviewer, ui}
	if !reflect.DeepEqual(orderings[0], want) {
		t.Fatalf("Next() mutated its runs argument: got %+v, want %+v", orderings[0], want)
	}
}
