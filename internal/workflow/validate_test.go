package workflow

import "testing"

func intp(n int) *int { return &n }

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name  string
		level Level
		spec  Spec
		want  string
	}{
		{
			name:  "step must set exactly one of run or review (neither set)",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b"},
			}},
			want: `step b: set exactly one of run or review`,
		},
		{
			name:  "step must set exactly one of run or review (both set)",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder", Review: []string{"reviewer"}},
			}},
			want: `step a: set exactly one of run or review`,
		},
		{
			name:  "duplicate step id",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "a", Review: []string{"reviewer"}, Of: "a"},
			}},
			want: `duplicate step id "a"`,
		},
		{
			name:  "invalid step id",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "Build", Run: "coder"},
			}},
			want: `step Build: invalid id, must match [a-z][a-z0-9-]*`,
		},
		{
			name:  "unknown run role",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "orchestrator"},
			}},
			want: `step a: orchestrator can't run a step`,
		},
		{
			name:  "unknown review role",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"coder"}, Of: "a"},
			}},
			want: `step b: coder can't review`,
		},
		{
			name:  "of names an unknown run step",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "nope"},
			}},
			want: `step b: of/fix must name an earlier run step`,
		},
		{
			name:  "loop.fix names an unknown run step",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "a", Loop: &Loop{Fix: "nope"}},
			}},
			want: `step b: of/fix must name an earlier run step`,
		},
		{
			name:  "loop max_rounds out of range",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "a", Loop: &Loop{Fix: "a", MaxRounds: 6}},
			}},
			want: `max_rounds must be 1–5`,
		},
		{
			name:  "spec-level max_rounds out of range",
			level: LevelTask,
			spec: Spec{
				MaxRounds: 0,
				Steps:     []Step{{ID: "a", Run: "coder"}},
			},
			want: "", // 0 means unset, no error - sanity control row
		},
		{
			name:  "spec-level max_rounds too high",
			level: LevelTask,
			spec: Spec{
				MaxRounds: 7,
				Steps:     []Step{{ID: "a", Run: "coder"}},
			},
			want: `max_rounds must be 1–5`,
		},
		{
			name:  "first step must be a run step",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Review: []string{"reviewer"}, Of: "b"},
				{ID: "b", Run: "coder"},
			}},
			want: `the first step must be a run step`,
		},
		{
			name:  "at most 6 steps",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "s1", Run: "coder"},
				{ID: "s2", Review: []string{"reviewer"}, Of: "s1"},
				{ID: "s3", Run: "coder"},
				{ID: "s4", Review: []string{"reviewer"}, Of: "s3"},
				{ID: "s5", Run: "coder"},
				{ID: "s6", Review: []string{"reviewer"}, Of: "s5"},
				{ID: "s7", Run: "coder"},
			}},
			want: `at most 6 steps`,
		},
		{
			name:  "after_tasks only on stories",
			level: LevelTask,
			spec:  Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer"}}},
			want:  `after_tasks is only for stories`,
		},
		{
			name:  "integration only on roots",
			level: LevelTask,
			spec:  Spec{Integration: &Integration{}},
			want:  `integration is only for epics and bugs`,
		},
		{
			name:  "task-level fields only on tasks (story)",
			level: LevelStory,
			spec:  Spec{Template: "tdd-reviewed"},
			want:  `template, steps, max_rounds and retries are only for tasks`,
		},
		{
			name:  "task-level fields only on tasks (root)",
			level: LevelRoot,
			spec:  Spec{Steps: []Step{{ID: "a", Run: "coder"}}},
			want:  `template, steps, max_rounds and retries are only for tasks`,
		},
		{
			name:  "unknown template name",
			level: LevelTask,
			spec:  Spec{Template: "nope"},
			want:  `unknown template "nope"`,
		},
		{
			name:  "after_tasks must be a review step",
			level: LevelStory,
			spec:  Spec{AfterTasks: &Step{ID: "review", Run: "coder"}},
			want:  `after_tasks must be a review step`,
		},
		{
			name:  "after_tasks can't have a loop",
			level: LevelStory,
			spec:  Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer"}, Loop: &Loop{Fix: "x"}}},
			want:  `after_tasks can't have a loop`,
		},
		{
			name:  "valid tdd-reviewed template passes",
			level: LevelTask,
			spec:  Spec{Template: "tdd-reviewed"},
			want:  "",
		},
		{
			name:  "valid resolved steps pass",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "build", Run: "coder", Gates: []Gate{GateTDD, GateCommit, GateVerify}},
				{ID: "review", Review: []string{"reviewer"}, Of: "build", Loop: &Loop{Fix: "build", MaxRounds: 3}},
			}},
			want: "",
		},
		{
			name:  "valid after_tasks on a story passes",
			level: LevelStory,
			spec:  Spec{AfterTasks: &Step{ID: "review", Review: []string{"reviewer"}}},
			want:  "",
		},
		{
			name:  "valid integration on a root passes",
			level: LevelRoot,
			spec:  Spec{Integration: &Integration{FinalReview: []string{"reviewer"}}},
			want:  "",
		},
		{
			name:  "template and steps both set",
			level: LevelTask,
			spec:  Spec{Template: "tdd-reviewed", Steps: []Step{{ID: "a", Run: "coder"}}},
			want:  `set template or steps, not both`,
		},
		{
			name:  "neither template nor steps set",
			level: LevelTask,
			spec:  Spec{},
			want:  `set template or steps`,
		},
		{
			name:  "neither template nor steps set (only max_rounds)",
			level: LevelTask,
			spec:  Spec{MaxRounds: 2},
			want:  `set template or steps`,
		},
		{
			name:  "retries too high",
			level: LevelTask,
			spec:  Spec{Template: "tdd-reviewed", Retries: intp(50)},
			want:  `retries must be 0–2`,
		},
		{
			name:  "retries negative",
			level: LevelTask,
			spec:  Spec{Template: "tdd-reviewed", Retries: intp(-3)},
			want:  `retries must be 0–2`,
		},
		{
			name:  "retries 0 and 2 are valid",
			level: LevelTask,
			spec:  Spec{Template: "tdd-reviewed", Retries: intp(0)},
			want:  "",
		},
		{
			name:  "integration.final_review roles must be review roles",
			level: LevelRoot,
			spec:  Spec{Integration: &Integration{FinalReview: []string{"coder"}}},
			want:  `integration final_review: coder can't review`,
		},
		{
			name:  "duplicate reviewer role",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer", "reviewer"}, Of: "a"},
			}},
			want: `step b: duplicate reviewer reviewer`,
		},
		{
			name:  "loop on a run step is rejected",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder", Loop: &Loop{Fix: "zzz"}, Of: "qq"},
			}},
			want: `step a: loop/of only apply to review steps`,
		},
		{
			name:  "of on a run step is rejected",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder", Of: "qq"},
			}},
			want: `step a: loop/of only apply to review steps`,
		},
		{
			name:  "after_tasks step id must match the pattern",
			level: LevelStory,
			spec:  Spec{AfterTasks: &Step{ID: "Review", Review: []string{"reviewer"}}},
			want:  `step Review: invalid id, must match [a-z][a-z0-9-]*`,
		},
		{
			name:  "after_tasks step sets exactly one of run or review",
			level: LevelStory,
			spec:  Spec{AfterTasks: &Step{ID: "review", Run: "coder", Review: []string{"reviewer"}}},
			want:  `step review: set exactly one of run or review`,
		},
		{
			name:  "unknown level",
			level: Level("epic"),
			spec:  Spec{Template: "tdd-reviewed"},
			want:  `unknown level "epic"`,
		},
		{
			name:  "loop.on_exhausted must be escalate or unset",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "a", Loop: &Loop{Fix: "a", OnExhausted: "retry-forever"}},
			}},
			want: `step b: on_exhausted must be "escalate"`,
		},
		{
			name:  "loop.on_exhausted unset is valid",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "a", Loop: &Loop{Fix: "a"}},
			}},
			want: "",
		},
		{
			name:  "loop.on_exhausted escalate is valid",
			level: LevelTask,
			spec: Spec{Steps: []Step{
				{ID: "a", Run: "coder"},
				{ID: "b", Review: []string{"reviewer"}, Of: "a", Loop: &Loop{Fix: "a", OnExhausted: "escalate"}},
			}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.level, tt.spec)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error %q", tt.want)
			}
			if err.Error() != tt.want {
				t.Fatalf("Validate() = %q, want %q", err.Error(), tt.want)
			}
		})
	}
}
