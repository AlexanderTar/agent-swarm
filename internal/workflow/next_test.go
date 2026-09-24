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
			name:  "failed run with no auto-retries left escalates",
			spec:  tddReviewed,
			runs:  []Run{failedBuild(1)},
			round: 1,
			want:  Action{Kind: ActionEscalate, Reason: "coder failed after 1 auto-retries"},
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
