package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/AlexanderTar/agent-swarm/internal/workflow"
)

// capType uppercases a node type's first rune for error messages. strings.Title
// is deprecated and title-cases every word; types are single words and
// user-controlled JSON, so only the first rune is touched (and "" is safe).
func capType(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

var splitTDDTitle = regexp.MustCompile(`(?i)^(write|add) (a )?failing test|^(red|green)\b|make .* pass$|^fix review|^address review|^review\b`)
var reviewTaskTitle = regexp.MustCompile(`(?i)^(review\b|fix review\b|address review\b)`)

// lintTree reports all plan errors and advisory warnings in tree order.
func lintTree(tree Tree) (errs []error, warnings []string) {
	var visit func(TreeNode, workflow.Level)
	visit = func(n TreeNode, level workflow.Level) {
		if n.Workflow != nil {
			if err := workflow.Validate(level, *n.Workflow); err != nil {
				errs = append(errs, fmt.Errorf("%s %s workflow: %w.", capType(n.Type), n.Ref, err))
			}
		}
		if level != workflow.LevelTask && (len(n.Steps) > 0 || len(n.Units) > 0 || n.Solo != "" || len(n.Verify) > 0) {
			errs = append(errs, fmt.Errorf("Only tasks can set steps, units, solo or verify."))
		}
		if level == workflow.LevelTask {
			ref := n.Ref
			if n.Workflow == nil {
				errs = append(errs, fmt.Errorf("Task %s has no workflow. Plans assign every role: pick a template or write steps.", ref))
			}
			if reviewTaskTitle.MatchString(n.Title) || n.RoleHint == "reviewer" || n.RoleHint == "ui_reviewer" {
				errs = append(errs, fmt.Errorf("Task %s is a review task. Reviews run inside each task's workflow; remove it and give the reviewed task a reviewed template.", ref))
			}
			if len(n.Steps) > 0 && len(n.Units) > 0 {
				errs = append(errs, fmt.Errorf("Task %s has both steps and units; use one.", ref))
			}
			if len(n.Units) > 8 {
				errs = append(errs, fmt.Errorf("Task %s has %d units (max 8).", ref, len(n.Units)))
			}
			for i, u := range n.Units {
				if strings.TrimSpace(u.Title) == "" || len(u.Steps) == 0 {
					errs = append(errs, fmt.Errorf("Unit %d needs a title and at least one step.", i+1))
				}
			}
			if n.Workflow != nil {
				resolved, err := workflow.Resolve(*n.Workflow, n.TddExempt != nil)
				if err != nil {
					errs = append(errs, fmt.Errorf("Task %s workflow: %w.", ref, err))
				} else {
					role := workflow.RunRole(resolved)
					if n.RoleHint != "" && n.RoleHint != role {
						errs = append(errs, fmt.Errorf("Task %s role_hint %s doesn't match its workflow (%s).", ref, n.RoleHint, role))
					}
					tdd := false
					for _, step := range resolved.Steps {
						for _, gate := range step.Gates {
							if gate == workflow.GateTDD {
								tdd = true
							}
						}
					}
					if tdd {
						if (len(n.Steps) == 0 && len(n.Units) == 0) || len(n.Verify) == 0 {
							errs = append(errs, fmt.Errorf("Task %s needs steps and verify commands (its workflow has a tdd gate).", ref))
						}
						containsTest := func(steps []string) bool {
							for _, step := range steps {
								if strings.Contains(strings.ToLower(step), "test") {
									return true
								}
							}
							return false
						}
						if len(n.Units) == 0 && !containsTest(n.Steps) {
							warnings = append(warnings, fmt.Sprintf("Task %s has no step mentioning a test (its workflow has a tdd gate).", ref))
						}
						for i, u := range n.Units {
							if !containsTest(u.Steps) {
								warnings = append(warnings, fmt.Sprintf("Task %s unit %d has no step mentioning a test (its workflow has a tdd gate).", ref, i+1))
							}
						}
					}
				}
			}
			if splitTDDTitle.MatchString(n.Title) {
				warnings = append(warnings, "Looks like a TDD phase or review step split out as its own task. Fold it into the task's steps.")
			}
			if (len(n.Steps) > 0 || len(n.Units) == 1) && strings.TrimSpace(n.Solo) == "" {
				warnings = append(warnings, fmt.Sprintf("Task %s is a single unit. Batch it with related units (3–5 per task) or say why it stands alone in `solo`.", ref))
			}
			if len(n.Units) > 5 {
				warnings = append(warnings, fmt.Sprintf("Task %s has %d units; split above 5 unless they're same-shape edits.", ref, len(n.Units)))
			}
		} else if n.Type == "story" && len(n.Children) >= 3 {
			all := true
			for _, c := range n.Children {
				if c.Type != "task" || !(len(c.Steps) > 0 || len(c.Units) == 1) {
					all = false
					break
				}
			}
			if all {
				warnings = append(warnings, fmt.Sprintf("Story %s has %d single-unit tasks; they look batchable.", n.Ref, len(n.Children)))
			}
		}
		// Root's inline children are not part of the tree: validateTreeShape,
		// materialize and Tree.Tasks all walk tree.Children and never touch
		// Root.Children, so descending here would lint phantom nodes nothing
		// else sees (and double-report a node listed in both places).
		if level != workflow.LevelRoot {
			for _, c := range n.Children {
				childLevel := workflow.LevelTask
				if c.Type == "story" {
					childLevel = workflow.LevelStory
				}
				visit(c, childLevel)
			}
		}
	}
	visit(tree.Root, workflow.LevelRoot)
	for _, c := range tree.Children {
		level := workflow.LevelTask
		if c.Type == "story" {
			level = workflow.LevelStory
		}
		visit(c, level)
	}
	return errs, warnings
}
