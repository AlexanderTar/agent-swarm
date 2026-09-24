package workflow

import (
	"reflect"
	"testing"
)

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
					{Severity: "minor", File: "a.go", Line: 1, Summary: "y"},
					{Severity: "major", File: "b.go", Line: 5, Summary: "x"},
					{Severity: "major", File: "a.go", Line: 2, Summary: "z"},
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
				Findings: []Finding{{Severity: "major", File: "a.go", Line: 1, Summary: "still broken"}},
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
			want:  Action{Kind: ActionEscalate, Reason: "coder failed twice"},
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

// TestNextIsDeterministic checks that Next returns the identical action when
// called repeatedly with the same inputs.
func TestNextIsDeterministic(t *testing.T) {
	s := resolveT(t, "tdd-reviewed")
	runs := []Run{r("build", 1, "coder", RunStateActive, VerdictNone)}
	first := Next(s, runs, 1, 0)
	for i := 0; i < 5; i++ {
		if got := Next(s, runs, 1, 0); !reflect.DeepEqual(got, first) {
			t.Fatalf("Next() not deterministic: got %+v, want %+v", got, first)
		}
	}
}
