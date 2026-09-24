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

// failed3 builds a failed run with the given AutoRetries count.
func failed3(step string, round int, role string, autoRetries int) Run {
	run := r(step, round, role, RunStateFailed, VerdictNone)
	run.AutoRetries = autoRetries
	return run
}

// active3 builds an active (in-progress) run.
func active3(step string, round int, role string) Run {
	return r(step, round, role, RunStateActive, VerdictNone)
}

func cat3(rs ...[]Run) []Run {
	var out []Run
	for _, r := range rs {
		out = append(out, r...)
	}
	return out
}

// threeLoopsSpec (probe3 "three"): a-ra, b-rb, c-rc, three independent
// run/review pairs chained one after another.
func threeLoopsSpec(t *testing.T) Spec {
	t.Helper()
	s, err := Resolve(Spec{Steps: []Step{
		{ID: "a", Run: "coder", Gates: []Gate{GateCommit}}, {ID: "ra", Review: []string{"reviewer"}},
		{ID: "b", Run: "mechanical", Gates: []Gate{GateCommit}}, {ID: "rb", Review: []string{"reviewer"}},
		{ID: "c", Run: "mechanical", Gates: []Gate{GateCommit}}, {ID: "rc", Review: []string{"ui_reviewer"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(LevelTask, s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNextThreeLoops locks in probe3 table 3L: index pinning across three
// independent run/review pairs, including a mid-chain retry (rb) followed
// by a later one (rc) that must only ever pin from its own fix step (c)
// onward, never re-touching a/ra or the already-passed b/rb.
func TestNextThreeLoops(t *testing.T) {
	three := threeLoopsSpec(t)

	r1 := []Run{
		cr2("a", 1, "coder", "a1"), r("ra", 1, "reviewer", RunStateCompleted, VerdictPass),
		cr2("b", 1, "mechanical", "b1"), r("rb", 1, "reviewer", RunStateCompleted, VerdictPass),
		cr2("c", 1, "mechanical", "c1"), r("rc", 1, "ui_reviewer", RunStateCompleted, VerdictChangesRequested),
	}

	tests := []struct {
		name  string
		runs  []Run
		round int
		want  Action
	}{
		{"rc CR at r1 retries only c", r1, 1, Action{Kind: ActionRetryFix, StepID: "c", Round: 2}},
		{"r2 c active waits on c alone", cat3(r1, []Run{active3("c", 2, "mechanical")}), 2, Action{Kind: ActionWait, StepID: "c", Round: 2}},
		{"r2 c done spawns rc with c's fresh sha", cat3(r1, []Run{cr2("c", 2, "mechanical", "c2")}), 2, Action{Kind: ActionSpawn, StepID: "rc", Roles: []string{"ui_reviewer"}, Round: 2, SHA: "c2"}},
		{"r2 rc passes, succeeds with c2", cat3(r1, []Run{cr2("c", 2, "mechanical", "c2"), r("rc", 2, "ui_reviewer", RunStateCompleted, VerdictPass)}), 2, Action{Kind: ActionSucceed, SHA: "c2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Next(three, tt.runs, tt.round, 0); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Next() = %+v, want %+v", got, tt.want)
			}
		})
	}

	// A mid-chain retry (rb CR at r1) re-runs b onward at r2; c/rc, never
	// having run, are then picked up fresh once b/rb pass, and a later rc
	// CR at r2 pins only from c onward for r3 - b/rb (already passed at
	// r2) and a/ra (r1) both stay carried forward.
	s1 := []Run{cr2("a", 1, "coder", "a1"), r("ra", 1, "reviewer", RunStateCompleted, VerdictPass), cr2("b", 1, "mechanical", "b1"), r("rb", 1, "reviewer", RunStateCompleted, VerdictChangesRequested)}
	if got, want := Next(three, s1, 1, 0), (Action{Kind: ActionRetryFix, StepID: "b", Round: 2}); !reflect.DeepEqual(got, want) {
		t.Fatalf("rb CR at r1: Next() = %+v, want %+v", got, want)
	}
	s2 := cat3(s1, []Run{cr2("b", 2, "mechanical", "b2"), r("rb", 2, "reviewer", RunStateCompleted, VerdictPass)})
	if got, want := Next(three, s2, 2, 0), (Action{Kind: ActionSpawn, StepID: "c", Roles: []string{"mechanical"}, Round: 2}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 rb passed, c never ran: Next() = %+v, want %+v", got, want)
	}
	s2c := cat3(s2, []Run{cr2("c", 2, "mechanical", "c2"), r("rc", 2, "ui_reviewer", RunStateCompleted, VerdictChangesRequested)})
	if got, want := Next(three, s2c, 2, 0), (Action{Kind: ActionRetryFix, StepID: "c", Round: 3}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 rc CR: Next() = %+v, want %+v", got, want)
	}
	s3 := cat3(s2c, []Run{cr2("c", 3, "mechanical", "c3")})
	if got, want := Next(three, s3, 3, 0), (Action{Kind: ActionSpawn, StepID: "rc", Roles: []string{"ui_reviewer"}, Round: 3, SHA: "c3"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r3 c done, b/rb carried from r2: Next() = %+v, want %+v", got, want)
	}
	// Rounds are shared across the whole workflow (one workflows.round):
	// rb already spent a round getting to r2, so rc's own loop (max 3)
	// only has one round left before exhausting at r3.
	exhausted := cat3(s3, []Run{r("rc", 3, "ui_reviewer", RunStateCompleted, VerdictChangesRequested)})
	if got, want := Next(three, exhausted, 3, 0), (Action{Kind: ActionEscalate, Reason: "rc rounds exhausted (3/3)"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r3 rc CR, budget shared: Next() = %+v, want %+v", got, want)
	}
	if got, want := Next(three, exhausted, 3, 1), (Action{Kind: ActionRetryFix, StepID: "c", Round: 4}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r3 rc CR with an extra round granted: Next() = %+v, want %+v", got, want)
	}
}

// fixIndexSpec (probe3 "fx"): notes -> build -> review{fix:build}. Exercises
// carry-forward alongside failures on both sides of the pinned range.
func fixIndexSpec(t *testing.T) Spec {
	t.Helper()
	s, err := Resolve(Spec{Steps: []Step{
		{ID: "notes", Run: "researcher", Gates: []Gate{GateArtifactNotes}},
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "build"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNextFixIndexCarryForward locks in probe3 table FX: failures on either
// side of a fix index > 0 are handled correctly, including an orchestrator
// crash-resume that bumps the round with no CR review to explain it.
func TestNextFixIndexCarryForward(t *testing.T) {
	fx := fixIndexSpec(t)

	f1 := []Run{cr2("notes", 1, "researcher", ""), cr2("build", 1, "coder", "b1"), r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested)}
	if got, want := Next(fx, f1, 2, 0), (Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 2}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 no build row yet: Next() = %+v, want %+v", got, want)
	}

	// notes (carried forward, before the fix index) failed at its latest
	// round with retries left: auto-retry, not an infinite wait.
	f1f := []Run{failed3("notes", 1, "researcher", 0), cr2("build", 1, "coder", "b1"), r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested), active3("build", 2, "coder")}
	wantRun := failed3("notes", 1, "researcher", 0)
	if got, want := Next(fx, f1f, 2, 0), (Action{Kind: ActionAutoRetry, StepID: "notes", Round: 1, Run: &wantRun}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 carried notes failed with retries left: Next() = %+v, want %+v", got, want)
	}

	// build (the fix step itself) crashes in round 2 with retries exhausted.
	f2 := cat3(f1, []Run{failed3("build", 2, "coder", 1)})
	if got, want := Next(fx, f2, 2, 0), (Action{Kind: ActionEscalate, Reason: "coder failed after 1 auto-retry"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 build failed exhausted: Next() = %+v, want %+v", got, want)
	}

	// Orchestrator resumes after the crash: round bumps to 3 with no CR
	// review to explain it, so pinnedFrom falls back to the failed step's
	// own index (build) - notes stays carried forward, build is pinned.
	if got, want := Next(fx, cat3(f2, []Run{active3("build", 3, "coder")}), 3, 1), (Action{Kind: ActionWait, StepID: "build", Round: 3}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r3 after crash resume, build r3 active: Next() = %+v, want %+v", got, want)
	}
	if got, want := Next(fx, f2, 3, 1), (Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 3}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r3 after crash resume, no r3 row: Next() = %+v, want %+v (must respawn build, not notes)", got, want)
	}

	// The reviewer itself crashed and exhausted its retries in round 1;
	// resuming bumps to round 2 with no CR review either (it never got to
	// verdict), pinning from review's own index. build/notes carry forward.
	f3 := []Run{cr2("notes", 1, "researcher", ""), cr2("build", 1, "coder", "b1"), failed3("review", 1, "reviewer", 1)}
	if got, want := Next(fx, f3, 2, 1), (Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer"}, Round: 2, SHA: "b1"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("r2 after reviewer crash resume: Next() = %+v, want %+v", got, want)
	}
}

// TestNextBlockedPinsLikeChangesRequested covers probe3 tables BL and UI
// (decision 1, major): a blocked verdict must pin pinnedFrom exactly like
// changes_requested - checked before the failed/cancelled fallback -
// otherwise a resume after a blocked escalation re-touches steps that
// already passed (BL) or re-reviews a stale build instead of fixing it
// (UI).
func TestNextBlockedPinsLikeChangesRequested(t *testing.T) {
	three := threeLoopsSpec(t)
	bl1 := []Run{
		cr2("a", 1, "coder", "a1"), r("ra", 1, "reviewer", RunStateCompleted, VerdictPass),
		cr2("b", 1, "mechanical", "b1"), r("rb", 1, "reviewer", RunStateCompleted, VerdictPass),
		cr2("c", 1, "mechanical", "c1"), r("rc", 1, "ui_reviewer", RunStateCompleted, VerdictBlocked),
	}

	blTests := []struct {
		name        string
		runs        []Run
		round       int
		extraRounds int
		want        Action
	}{
		{"r1 rc blocked escalates", bl1, 1, 0, Action{Kind: ActionEscalate, Reason: "ui_reviewer blocked"}},
		// Resume grants an extra round and bumps to r2. rc's blocked
		// verdict must pin from c (rc's fix), never re-touching a/ra or
		// b/rb.
		{"resume r2, engine inserted c r2 active waits on c", cat3(bl1, []Run{active3("c", 2, "mechanical")}), 2, 1, Action{Kind: ActionWait, StepID: "c", Round: 2}},
		{"resume r2, no rows yet spawns c", bl1, 2, 1, Action{Kind: ActionSpawn, StepID: "c", Roles: []string{"mechanical"}, Round: 2}},
	}
	for _, tt := range blTests {
		t.Run("BL "+tt.name, func(t *testing.T) {
			if got := Next(three, tt.runs, tt.round, tt.extraRounds); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Next() = %+v, want %+v", got, tt.want)
			}
		})
	}

	ui := resolveT(t, "ui-tdd-reviewed")
	u1 := []Run{cr2("build", 1, "coder", "x1"), r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked), active3("review", 1, "ui_reviewer")}
	u1c := []Run{cr2("build", 1, "coder", "x1"), r("review", 1, "reviewer", RunStateCompleted, VerdictBlocked), r("review", 1, "ui_reviewer", RunStateCancelled, VerdictNone)}

	uiTests := []struct {
		name        string
		runs        []Run
		round       int
		extraRounds int
		want        Action
	}{
		{"r1 blocked while ui_reviewer active escalates", u1, 1, 0, Action{Kind: ActionEscalate, Reason: "reviewer blocked"}},
		// The orchestrator's resume decision is to fix the build, not
		// re-review the same x1 commit: the blocked reviewer's pin target
		// is "build" (review's Loop.Fix), so build itself is what's pinned
		// for r2 - and with no r2 build row yet, it's spawned fresh.
		{"resume r2, no build r2 row spawns build (not a re-review of x1)", u1c, 2, 1, Action{Kind: ActionSpawn, StepID: "build", Roles: []string{"coder"}, Round: 2}},
		{"resume r2, build r2 active waits", cat3(u1c, []Run{active3("build", 2, "coder")}), 2, 1, Action{Kind: ActionWait, StepID: "build", Round: 2}},
	}
	for _, tt := range uiTests {
		t.Run("UI "+tt.name, func(t *testing.T) {
			if got := Next(ui, tt.runs, tt.round, tt.extraRounds); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Next() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestNextExtraRoundsSameRound covers probe3 table EX: a resume that grants
// an extra round without bumping workflows.round retries the same round
// number, one higher.
func TestNextExtraRoundsSameRound(t *testing.T) {
	td := resolveT(t, "tdd-reviewed")
	ex := []Run{cr2("build", 3, "coder", "b3"), r("review", 3, "reviewer", RunStateCompleted, VerdictChangesRequested)}
	got := Next(td, ex, 3, 1)
	want := Action{Kind: ActionRetryFix, StepID: "build", Round: 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Next() = %+v, want %+v", got, want)
	}
}

// TestNextStaleReviewSha covers probe3's table-B follow-up: a review run's
// sha is stamped with the sha it actually reviewed. Once that sha is
// superseded (the reviewed step re-ran) in the SAME round the stale row
// belongs to, the approval doesn't count - and since a fresh row can't be
// spawned at that same (step, round, role) key, Next escalates instead of
// trying to (round 4, decision 1; round 3's "just re-spawn it" turned out
// to be a no-op against the unique-run-key constraint and would have hung
// the workflow - see TestNextStaleReviewInCurrentRoundEscalates for the
// full set of same-round cases, and TestNextCarriedStaleReviewIsDroppedAndRespawned
// for the earlier-round case, which still drops and re-spawns).
func TestNextStaleReviewSha(t *testing.T) {
	b, err := Resolve(Spec{Steps: []Step{
		{ID: "build", Run: "coder", Gates: []Gate{GateCommit}},
		{ID: "lint", Run: "mechanical", Gates: []Gate{GateCommit}},
		{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "build"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	bb := []Run{cr2("build", 1, "coder", "b1"), cr2("lint", 1, "mechanical", "l1"), r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested), cr2("build", 2, "coder", "b2")}
	stale := Run{StepID: "review", Round: 2, Role: "reviewer", State: RunStateCompleted, Verdict: VerdictPass, SHA: "l1"}

	if got, want := Next(b, cat3(bb, []Run{stale}), 2, 0), (Action{Kind: ActionSpawn, StepID: "lint", Roles: []string{"mechanical"}, Round: 2}); !reflect.DeepEqual(got, want) {
		t.Fatalf("no lint r2 row yet: Next() = %+v, want %+v", got, want)
	}

	withFreshLint := cat3(bb, []Run{stale, cr2("lint", 2, "mechanical", "l2")})
	got := Next(b, withFreshLint, 2, 0)
	want := Action{Kind: ActionEscalate, Reason: "reviewer reviewed l1, but lint is now at l2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale review (sha l1) in round 2, once lint is fresh (l2) in round 2: Next() = %+v, want %+v (can't re-spawn the same (review, round 2, reviewer) key)", got, want)
	}
}

// TestNextAfterTasksShape covers probe3 table AT (decision 3, minor): when a
// resolved spec has no Steps but does have AfterTasks (a story spec, per
// Resolve), Next operates on []Step{*s.AfterTasks} instead of bailing out
// with "workflow has no steps".
func TestNextAfterTasksShape(t *testing.T) {
	storySpec, err := Resolve(Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer", "ui_reviewer"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(storySpec.Steps) != 0 {
		t.Fatalf("storySpec.Steps = %v, want none (AfterTasks isn't expanded into Steps)", storySpec.Steps)
	}

	tests := []struct {
		name  string
		runs  []Run
		round int
		want  Action
	}{
		{
			name: "no runs yet spawns both review roles",
			runs: nil,
			want: Action{Kind: ActionSpawn, StepID: "review", Roles: []string{"reviewer", "ui_reviewer"}, Round: 1},
		},
		{
			name: "changes requested escalates (no loop on an after_tasks review)",
			runs: []Run{r("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested), r("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass)},
			want: Action{Kind: ActionEscalate, Reason: "review changes requested"},
		},
		{
			name: "all pass succeeds",
			runs: []Run{r("review", 1, "reviewer", RunStateCompleted, VerdictPass), r("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass)},
			want: Action{Kind: ActionSucceed},
		},
		{
			name: "a crashed reviewer escalates once retries are exhausted",
			runs: []Run{failed3("review", 1, "reviewer", 1)},
			want: Action{Kind: ActionEscalate, Reason: "reviewer failed after 1 auto-retry"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			round := tt.round
			if round == 0 {
				round = 1
			}
			if got := Next(storySpec, tt.runs, round, 0); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Next() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// rsha builds a Run with an explicit sha, state and verdict - used for the
// stale-review-sha probes (probe4).
func rsha(step string, round int, role string, state RunState, verdict Verdict, sha string) Run {
	return Run{StepID: step, Round: round, Role: role, State: state, Verdict: verdict, SHA: sha}
}

// TestNextStaleReviewInCurrentRoundEscalates covers probe4 S1-S5 (decision
// 1, major, and decision 2, minor): a stale review row - one whose sha
// doesn't match the reviewed step's current sha - AT THE CURRENT ROUND
// can't simply be dropped and "respawned": the (step, round, role) key
// already has a row, so a Spawn for it would be a no-op against the
// UNIQUE(workflow_id, step_id, round, role) constraint and hang the
// workflow forever. Next must escalate instead, regardless of the stale
// run's own verdict or state (pass, changes_requested, blocked, still
// active, or even failed - the failure scan must not auto-retry a stale
// row at its old sha either).
func TestNextStaleReviewInCurrentRoundEscalates(t *testing.T) {
	ui := resolveT(t, "ui-tdd-reviewed")
	td := resolveT(t, "tdd-reviewed")
	wantReason := "reviewer reviewed b1old, but build is now at b1new"

	t.Run("S1 one of two reviewers stale pass, the other fresh pass", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1new"),
			rsha("review", 1, "reviewer", RunStateCompleted, VerdictPass, "b1old"),
			rsha("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass, "b1new"),
		}
		got := Next(ui, runs, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: wantReason}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S2 stale changes_requested + fresh pass", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1new"),
			rsha("review", 1, "reviewer", RunStateCompleted, VerdictChangesRequested, "b1old"),
			rsha("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass, "b1new"),
		}
		got := Next(ui, runs, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: wantReason}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S3 stale blocked", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1new"),
			rsha("review", 1, "reviewer", RunStateCompleted, VerdictBlocked, "b1old"),
		}
		got := Next(td, runs, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: wantReason}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S4 stale reviewer still active", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1new"),
			rsha("review", 1, "reviewer", RunStateActive, VerdictNone, "b1old"),
		}
		got := Next(td, runs, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: wantReason}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S5 stale failed reviewer with retries left does not auto-retry at the old sha", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1new"),
			rsha("review", 1, "reviewer", RunStateFailed, VerdictNone, "b1old"),
		}
		got := Next(td, runs, 1, 0)
		want := Action{Kind: ActionEscalate, Reason: wantReason}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})
}

// TestNextEmptyReviewSHAIsNeverStale covers probe4 S6-S8: a review run with
// no sha recorded at all - because it's reviewing a step with no commit
// gate, or just wasn't stamped - is never treated as stale, regardless of
// whether the reviewed step has a sha of its own.
func TestNextEmptyReviewSHAIsNeverStale(t *testing.T) {
	td := resolveT(t, "tdd-reviewed")

	t.Run("S6 empty-sha review pass succeeds with Of's sha", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1"),
			rsha("review", 1, "reviewer", RunStateCompleted, VerdictPass, ""),
		}
		got := Next(td, runs, 1, 0)
		want := Action{Kind: ActionSucceed, SHA: "b1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S7 empty-sha active reviewer just waits", func(t *testing.T) {
		runs := []Run{
			rsha("build", 1, "coder", RunStateCompleted, VerdictNone, "b1"),
			rsha("review", 1, "reviewer", RunStateActive, VerdictNone, ""),
		}
		got := Next(td, runs, 1, 0)
		want := Action{Kind: ActionWait, StepID: "review", Round: 1}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S8 a step with no commit gate has no sha to compare against", func(t *testing.T) {
		dr := resolveT(t, "design-reviewed")
		runs := []Run{
			rsha("design", 1, "designer", RunStateCompleted, VerdictNone, ""),
			rsha("review", 1, "ui_reviewer", RunStateCompleted, VerdictPass, "zzz"),
		}
		got := Next(dr, runs, 1, 0)
		want := Action{Kind: ActionSucceed, SHA: ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})
}

// twoPairsSpec (probe3/probe4 "two"): a-ra, b-rb, chained.
func twoPairsSpec(t *testing.T) Spec {
	t.Helper()
	s, err := Resolve(Spec{Steps: []Step{
		{ID: "a", Run: "coder", Gates: []Gate{GateCommit}}, {ID: "ra", Review: []string{"reviewer"}},
		{ID: "b", Run: "mechanical", Gates: []Gate{GateCommit}}, {ID: "rb", Review: []string{"reviewer"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestNextCarriedStaleReviewIsDroppedAndRespawned covers probe4 S9-S11: a
// stale review row carried forward from an EARLIER round (not the current
// one) is simply dropped - that role is treated as missing and re-spawned
// at the fresh sha in the current round, since that's a brand new
// (step, round, role) key, not a collision.
func TestNextCarriedStaleReviewIsDroppedAndRespawned(t *testing.T) {
	two := twoPairsSpec(t)
	base := []Run{
		rsha("a", 1, "coder", RunStateCompleted, VerdictNone, "a1"),
		rsha("ra", 1, "reviewer", RunStateCompleted, VerdictPass, "a1"),
		rsha("b", 1, "mechanical", RunStateCompleted, VerdictNone, "b1"),
		rsha("rb", 1, "reviewer", RunStateCompleted, VerdictChangesRequested, "b1"),
	}

	t.Run("S9 carried ra/a consistent; b's round-2 retry is just active", func(t *testing.T) {
		runs := append(append([]Run{}, base...), rsha("b", 2, "mechanical", RunStateActive, VerdictNone, ""))
		got := Next(two, runs, 2, 0)
		want := Action{Kind: ActionWait, StepID: "b", Round: 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S10 carried ra is stale against a re-completed carried a; ra is re-spawned at a's fresh sha", func(t *testing.T) {
		bad := append([]Run{}, base...)
		bad[0].SHA = "a1new" // "a" re-completed round 1 with a different sha than what ra reviewed
		runs := append(bad, rsha("b", 2, "mechanical", RunStateActive, VerdictNone, ""))
		got := Next(two, runs, 2, 0)
		want := Action{Kind: ActionSpawn, StepID: "ra", Roles: []string{"reviewer"}, Round: 2, SHA: "a1new"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("S11 fix index before Of: both re-run at round 2, review re-spawns fresh (no stale row to drop)", func(t *testing.T) {
		fo, err := Resolve(Spec{Steps: []Step{
			{ID: "a", Run: "coder", Gates: []Gate{GateCommit}},
			{ID: "b", Run: "mechanical", Gates: []Gate{GateCommit}},
			{ID: "rb", Review: []string{"reviewer"}, Loop: &Loop{Fix: "a"}},
		}}, false)
		if err != nil {
			t.Fatal(err)
		}
		runs := []Run{
			rsha("a", 1, "coder", RunStateCompleted, VerdictNone, "a1"),
			rsha("b", 1, "mechanical", RunStateCompleted, VerdictNone, "b1"),
			rsha("rb", 1, "reviewer", RunStateCompleted, VerdictChangesRequested, "b1"),
			rsha("a", 2, "coder", RunStateCompleted, VerdictNone, "a2"),
			rsha("b", 2, "mechanical", RunStateCompleted, VerdictNone, "b2"),
		}
		got := Next(fo, runs, 2, 0)
		want := Action{Kind: ActionSpawn, StepID: "rb", Roles: []string{"reviewer"}, Round: 2, SHA: "b2"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})
}

// TestNextPinnedFromStaleReviewStillPinsFixStep covers probe4 S12 and the
// resume-path fix to f97734e (P6 fix1, Important): a stale review row's own
// sha can't be trusted, but which step it belongs to can. The only way such
// a row survives to the previous round at all is an orchestrator resume
// after the same-round stale-review escalation (spec ~737: retry re-runs
// the fix step, then the review at the fresh sha) - so pinnedFrom must
// still pin from that row's fix step, whether the row carries a
// changes_requested/blocked verdict or (having itself been the escalated
// row) a pass.
func TestNextPinnedFromStaleReviewStillPinsFixStep(t *testing.T) {
	two := twoPairsSpec(t)

	t.Run("S12 literal: a stale ra CR at r1 doesn't change the (already-0) pin", func(t *testing.T) {
		// ra's own recorded sha ("a0") never matched what "a" actually
		// completed with ("a1") even back in round 1 - a corrupted/bogus
		// row. rb's own review passed (fresh, sha matches b). A stale CR
		// still pins (from "a", ra's fix step), which happens to be index
		// 0 - the same as the "pin everything" default - so this case
		// can't tell a filtered and an unfiltered implementation apart on
		// its own; see the next case for that.
		runs := []Run{
			rsha("a", 1, "coder", RunStateCompleted, VerdictNone, "a1"),
			rsha("ra", 1, "reviewer", RunStateCompleted, VerdictChangesRequested, "a0"),
			rsha("b", 1, "mechanical", RunStateCompleted, VerdictNone, "b1"),
			rsha("rb", 1, "reviewer", RunStateCompleted, VerdictPass, "b1"),
			rsha("b", 2, "mechanical", RunStateActive, VerdictNone, ""),
		}
		got := Next(two, runs, 2, 0)
		want := Action{Kind: ActionSpawn, StepID: "a", Roles: []string{"coder"}, Round: 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v", got, want)
		}
	})

	t.Run("a stale CR pins at its own fix step (b), not wherever it happens to coincide", func(t *testing.T) {
		// ra passed cleanly (fresh). rb's CR is stale: rb's own recorded
		// sha ("stale") never matches what "b" actually completed with
		// ("b1"). Its sha can't be trusted, but its step id can: pinnedFrom
		// pins from "b" (rb's fix step, index 2), leaving a/ra carried
		// forward - and with b's round-2 row still active, Next waits on
		// it, exactly as it did before f97734e.
		runs := []Run{
			rsha("a", 1, "coder", RunStateCompleted, VerdictNone, "a1"),
			rsha("ra", 1, "reviewer", RunStateCompleted, VerdictPass, "a1"),
			rsha("b", 1, "mechanical", RunStateCompleted, VerdictNone, "b1"),
			rsha("rb", 1, "reviewer", RunStateCompleted, VerdictChangesRequested, "stale"),
			rsha("b", 2, "mechanical", RunStateActive, VerdictNone, ""),
		}
		got := Next(two, runs, 2, 0)
		want := Action{Kind: ActionWait, StepID: "b", Round: 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v (a stale review must pin at its own fix step)", got, want)
		}
	})

	t.Run("a stale PASS row (stale-escalation, then resumed) also pins at its fix step", func(t *testing.T) {
		// ra passed fresh (sha matches a). "b" actually completed round 1
		// with "b1new" (after an in-round retry), but rb's row recorded
		// "b1" - the sha it reviewed before that retry landed. At round 1
		// this would itself have escalated
		// (TestNextStaleReviewInCurrentRoundEscalates/S1); the only way it
		// can be sitting at the previous round at all is a resume after
		// that escalation. It carries no changes_requested/blocked verdict
		// to trigger the ordinary pin, but its staleness alone still means
		// "b" is the fix loop that needs re-pinning - not "pin
		// everything", which would otherwise wrongly respawn "a".
		runs := []Run{
			rsha("a", 1, "coder", RunStateCompleted, VerdictNone, "a1"),
			rsha("ra", 1, "reviewer", RunStateCompleted, VerdictPass, "a1"),
			rsha("b", 1, "mechanical", RunStateCompleted, VerdictNone, "b1new"),
			rsha("rb", 1, "reviewer", RunStateCompleted, VerdictPass, "b1"),
			rsha("b", 2, "mechanical", RunStateActive, VerdictNone, ""),
		}
		got := Next(two, runs, 2, 1)
		want := Action{Kind: ActionWait, StepID: "b", Round: 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Next() = %+v, want %+v (a stale pass row must still pin, not fall through to \"pin everything\")", got, want)
		}
	})
}
